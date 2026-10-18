package observe

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// demandCtl drives one consumer's demand from a test.
type demandCtl struct {
	mu  sync.Mutex
	yes bool
	err error
}

func (d *demandCtl) set(yes bool, err error) { d.mu.Lock(); d.yes, d.err = yes, err; d.mu.Unlock() }
func (d *demandCtl) demand(context.Context) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.yes, d.err
}

// sharedHarness: one shared kind "svc" with consumers gw and adm, each read
// by its own part.
func sharedHarness(t *testing.T) (*harness, *fakeSource, *demandCtl, *demandCtl) {
	t.Helper()
	src := newSource()
	src.put("s1", nil)
	gw, adm := &demandCtl{}, &demandCtl{}
	kind := cmKind("svc", src)
	kind.Consumers, kind.Version = []Consumer{{Name: "gw", Demand: gw.demand}, {Name: "adm", Demand: adm.demand}}, "v1"
	part := func(id, consumer string) PartSpec {
		p := cmPart(id, "svc")
		p.Consumer, p.Ledger = consumer, true
		return p
	}
	h := start(t, Config{ReprobeEvery: time.Second, Kinds: []KindSpec{kind}, Domains: []DomainSpec{
		{ID: "gateway", Parts: []PartSpec{part("routes", "gw")}},
		{ID: "admission", Parts: []PartSpec{part("webhooks", "adm")}},
	}})
	return h, src, gw, adm
}

func covIs(want Coverage, class string) func(Part) bool {
	return func(p Part) bool {
		return p.Coverage == want && len(p.Kinds) == 1 && p.Kinds[0].Coverage == want && p.Kinds[0].Class == class
	}
}

// The union truth table (C3): served when any consumer's demand is proven,
// absent only when every consumer proves none, a gap otherwise - and each
// part sees the kind through its own consumer.
func TestSharedKindConsumerTruthTable(t *testing.T) {
	h, src, gw, adm := sharedHarness(t)
	gw.set(false, nil)
	adm.set(true, nil)
	h.eventually(h.partIs("admission", "webhooks", func(p Part) bool { return live(p) && p.Counts != nil && p.Counts.Total == 1 }), "admission's demand opens the kind")
	h.eventually(h.partIs("gateway", "routes", func(p Part) bool { return p.Coverage == Absent && p.Counts == nil }),
		"no borrowed presence: gateway's own no reads absent while admission holds the kind")
	if p, _ := h.sink.part("admission", "webhooks"); len(p.Kinds[0].Consumers) != 2 {
		t.Fatalf("the demand vector is published: %+v", p.Kinds[0])
	}

	// gateway's demand becomes unknown while admission stays yes: the
	// generation runs on, and only gateway's view changes
	lists, _ := src.counts()
	gw.set(false, errors.New("discovery failed"))
	h.eventually(h.partIs("gateway", "routes", covIs(Unreadable, ClassDemand)), "own unknown reads unread, class demand")
	h.eventually(h.partIs("admission", "webhooks", live), "the other consumer keeps its view")
	if l, _ := src.counts(); l != lists || src.liveWatcher() == nil {
		t.Fatalf("the physical generation is unchanged: lists %d -> %d", lists, l)
	}

	// gateway yes: both see it, one generation
	gw.set(true, nil)
	h.eventually(h.partIs("gateway", "routes", func(p Part) bool { return live(p) && p.Counts != nil && p.Counts.Total == 1 }), "gateway's own yes")
	if l, _ := src.counts(); l != lists {
		t.Fatalf("still one generation: lists %d -> %d", lists, l)
	}

	// gateway unknown, admission no: a gap - admission reads absent (its own
	// no), gateway unread
	gw.set(false, errors.New("discovery failed"))
	adm.set(false, nil)
	h.eventually(h.partIs("admission", "webhooks", func(p Part) bool { return p.Coverage == Absent }), "admission's own no reads absent")
	h.eventually(h.partIs("gateway", "routes", covIs(Unreadable, ClassDemand)), "gateway's own unknown reads unread")

	// every consumer no: the kind stops
	gw.set(false, nil)
	h.eventually(func() bool {
		gp, ok1 := h.sink.part("gateway", "routes")
		ap, ok2 := h.sink.part("admission", "webhooks")
		return ok1 && ok2 && gp.Coverage == Absent && ap.Coverage == Absent && src.liveWatcher() == nil
	}, "absent only when every consumer proves none, and the generation stops")
}

// A part that reads a shared kind through an unknown consumer is a
// programming error.
func TestSharedKindRejectsAnUnknownConsumer(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	src := newSource()
	kind := cmKind("svc", src)
	kind.Consumers, kind.Version = []Consumer{{Name: "gw", Demand: func(context.Context) (bool, error) { return true, nil }}}, "v1"
	p := cmPart("x", "svc")
	p.Consumer = "nobody"
	New(Config{Cluster: "c", Sink: &recSink{}, Kinds: []KindSpec{kind}, Domains: []DomainSpec{{ID: "d", Parts: []PartSpec{p}}}})
}

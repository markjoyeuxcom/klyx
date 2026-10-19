package observe

import (
	"context"
	"testing"
	"time"
)

func TestReviewS3aAbsentConsumerDoesNotBorrowContinuity(t *testing.T) {
	src := newSource()
	own := cmKind("own", src)
	shared := cmKind("shared", src)
	shared.Consumers = []Consumer{{Name: "a", Demand: func(context.Context) (bool, error) { return false, nil }}, {Name: "b", Demand: func(context.Context) (bool, error) { return true, nil }}}
	shared.Version = "v1"
	p := cmPart("p", "own", "shared")
	p.Consumer = "a"
	p.Ledger = true
	o := New(Config{Cluster: "c", Sink: &recSink{}, Kinds: []KindSpec{own, shared}, Domains: []DomainSpec{{ID: "d", Parts: []PartSpec{p}}}})
	o.kinds["own"].gen = &kindGen{id: 1, epoch: 1, model: map[string]any{}, fresh: freshness{cov: Live}}
	ks := o.kinds["shared"]
	ks.gen = &kindGen{id: 2, epoch: 1, version: "v1", model: map[string]any{}, fresh: freshness{cov: Live}}
	ks.demand = map[string]Demand{"a": DemandNo, "b": DemandYes}
	ps := o.partOf(partKey{"d", "p"})
	c1, _, _, cp1 := o.captureLocked(ps, time.Now())
	id1 := partitionIdentity(ps.spec, "own", cp1)
	o.commitLedger(ps, cp1, c1, PartResult{}, 1)
	before := len(o.cfg.Sink.(*recSink).allBatches())
	ks.gen.epoch++
	ks.gen.relisting = true
	c2, _, _, cp2 := o.captureLocked(ps, time.Now())
	id2 := partitionIdentity(ps.spec, "own", cp2)
	if !liveForLedger(id1) || !liveForLedger(id2) {
		t.Fatal("absent dependency is continuous")
	}
	o.commitLedger(ps, cp2, c2, PartResult{}, 2)
	for _, b := range o.cfg.Sink.(*recSink).allBatches()[before:] {
		if b.Kind == BatchLoss {
			t.Fatalf("borrowed continuity causes a spurious loss: %+v", b)
		}
	}
	if !sameIdentity(id1, id2) {
		t.Fatalf("consumer a proves no demand, but b's relist changes its ledger identity: before=%+v after=%+v", id1, id2)
	}
}

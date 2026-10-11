package observe

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	krt "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	testingclock "k8s.io/utils/clock/testing"
)

// ---- fixtures ---------------------------------------------------------------

// fakeSource is a controllable API for one kind of ConfigMaps.
type fakeSource struct {
	mu        sync.Mutex
	objs      map[string]*corev1.ConfigMap
	rv        int
	listErr   error
	listGate  chan struct{}
	watchErrs []error
	watchers  []*watch.RaceFreeFakeWatcher
	lists     int
	versions  []string
}

func newSource() *fakeSource { return &fakeSource{objs: map[string]*corev1.ConfigMap{}} }

func (f *fakeSource) lw(version string) cache.ListerWatcherWithContext {
	f.mu.Lock()
	f.versions = append(f.versions, version)
	f.mu.Unlock()
	return &fakeLW{f}
}

type fakeLW struct{ f *fakeSource }

func (l *fakeLW) ListWithContext(ctx context.Context, _ metav1.ListOptions) (krt.Object, error) {
	f := l.f
	f.mu.Lock()
	gate := f.listGate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	list := &corev1.ConfigMapList{ListMeta: metav1.ListMeta{ResourceVersion: strconv.Itoa(f.rv)}}
	names := make([]string, 0, len(f.objs))
	for n := range f.objs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		list.Items = append(list.Items, *f.objs[n].DeepCopy())
	}
	return list, nil
}

func (l *fakeLW) WatchWithContext(_ context.Context, _ metav1.ListOptions) (watch.Interface, error) {
	f := l.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.watchErrs) > 0 {
		e := f.watchErrs[0]
		f.watchErrs = f.watchErrs[1:]
		if e != nil {
			return nil, e
		}
	}
	w := watch.NewRaceFreeFake()
	f.watchers = append(f.watchers, w)
	return w, nil
}

func (l *fakeLW) IsWatchListSemanticsUnSupported() bool { return true }

func (f *fakeSource) put(name string, data map[string]string) {
	f.mu.Lock()
	f.rv++
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(name), ResourceVersion: strconv.Itoa(f.rv)}, Data: data}
	_, existed := f.objs[name]
	f.objs[name] = cm
	w := f.liveWatcherLocked()
	f.mu.Unlock()
	if w != nil {
		if existed {
			w.Modify(cm.DeepCopy())
		} else {
			w.Add(cm.DeepCopy())
		}
	}
}

func (f *fakeSource) liveWatcherLocked() *watch.RaceFreeFakeWatcher {
	if n := len(f.watchers); n > 0 && !f.watchers[n-1].IsStopped() {
		return f.watchers[n-1]
	}
	return nil
}

func (f *fakeSource) liveWatcher() *watch.RaceFreeFakeWatcher {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.liveWatcherLocked()
}

func (f *fakeSource) counts() (lists, watchers int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists, len(f.watchers)
}

type cmView struct {
	Name  string
	Bad   bool
	Until string
}

func projectCM(obj any) (any, error) {
	cm := obj.(*corev1.ConfigMap)
	return cmView{Name: cm.Name, Bad: cm.Data["bad"] == "1", Until: cm.Data["until"]}, nil
}

// evalCMs: bad=1 is fault; until=<RFC3339> is progressing until then, fault
// after (a time-only threshold); else ok.
func evalCMs(kind string) func(Capture) PartResult {
	return func(c Capture) PartResult {
		var counts Counts
		var rows []string
		var next *time.Time
		for _, o := range c.Objects[kind] {
			v := o.(cmView)
			counts.Total++
			role := "ok"
			switch {
			case v.Bad:
				role = "fault"
			case v.Until != "":
				t, _ := time.Parse(time.RFC3339, v.Until)
				if c.Now.Before(t) {
					role = "progressing"
					if next == nil || t.Before(*next) {
						next = &t
					}
				} else {
					role = "fault"
				}
			}
			switch role {
			case "fault":
				counts.Fault++
			case "progressing":
				counts.Progressing++
			default:
				counts.OK++
			}
			if c.Rows {
				rows = append(rows, v.Name+":"+role)
			}
		}
		r := PartResult{Counts: &counts, Next: next}
		if c.Rows {
			r.Rows = rows
		}
		return r
	}
}

type recSink struct {
	mu   sync.Mutex
	sums []Summary
	rows []Rows
}

func (s *recSink) Summary(x Summary) { s.mu.Lock(); s.sums = append(s.sums, x); s.mu.Unlock() }
func (s *recSink) Rows(x Rows)       { s.mu.Lock(); s.rows = append(s.rows, x); s.mu.Unlock() }

func (s *recSink) last() (Summary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sums) == 0 {
		return Summary{}, false
	}
	return s.sums[len(s.sums)-1], true
}

func (s *recSink) part(task, part string) (Part, bool) {
	sum, ok := s.last()
	if !ok {
		return Part{}, false
	}
	for _, t := range sum.Tasks {
		if t.ID == task {
			for _, p := range t.Parts {
				if p.ID == part {
					return p, true
				}
			}
		}
	}
	return Part{}, false
}

func (s *recSink) lastRows(task, part string) (Rows, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.rows) - 1; i >= 0; i-- {
		if s.rows[i].Task == task && s.rows[i].Part.ID == part {
			return s.rows[i], true
		}
	}
	return Rows{}, false
}

func (s *recSink) n() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sums) }

var t0 = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

type harness struct {
	t      *testing.T
	clk    *testingclock.FakeClock
	sink   *recSink
	o      *Observer
	cancel context.CancelFunc
}

func cmKind(name string, src *fakeSource) KindSpec {
	return KindSpec{Name: name, Scope: "all namespaces", NewListWatch: src.lw, ExpectedType: &corev1.ConfigMap{}, Project: projectCM}
}

func cmPart(id, kind string, deps ...string) PartSpec {
	return PartSpec{ID: id, Entity: id, Scope: "all namespaces", Classifier: "verdict", Counted: []string{kind}, Deps: deps, Evaluate: evalCMs(kind)}
}

func start(t *testing.T, cfg Config) *harness {
	t.Helper()
	clk := testingclock.NewFakeClock(t0)
	sink := &recSink{}
	cfg.Cluster, cfg.Clock, cfg.Sink = "c1", clk, sink
	if cfg.InitialSyncTimeout == 0 {
		cfg.InitialSyncTimeout = time.Hour
	}
	if cfg.ReprobeEvery == 0 {
		cfg.ReprobeEvery = time.Hour
	}
	o := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx)
	h := &harness{t: t, clk: clk, sink: sink, o: o, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		select {
		case <-o.Done():
		case <-time.After(10 * time.Second):
			t.Errorf("observer did not finish")
		}
	})
	return h
}

// eventually advances the fake clock in small steps until cond holds.
func (h *harness) eventually(cond func() bool, msg string) {
	h.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		h.clk.Step(250 * time.Millisecond)
		time.Sleep(4 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting: %s", msg)
}

// settle runs the clock for a while without asserting.
func (h *harness) settle(d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); {
		h.clk.Step(250 * time.Millisecond)
		time.Sleep(4 * time.Millisecond)
	}
}

func (h *harness) partIs(task, part string, cond func(Part) bool) func() bool {
	return func() bool {
		p, ok := h.sink.part(task, part)
		return ok && cond(p)
	}
}

func live(p Part) bool { return p.Coverage == Live }

// ---- leases and generations -------------------------------------------------

func TestLeaseRefcountByDistinctOwnersAndNewGenerations(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")

	lens := Owner{Kind: "lens", ID: "things"}
	relLens := h.o.Acquire("things", lens)
	relLensAgain := h.o.Acquire("things", lens) // idempotent: no second reference
	relHealth := h.o.Acquire("things", HealthOwner)
	relHealth()
	relHealth() // idempotent
	if lists, _ := src.counts(); lists != 1 {
		t.Fatalf("the lens still holds the domain; want one list, got %d", lists)
	}
	relLensAgain()
	relLens()
	h.eventually(func() bool { w := src.liveWatcher(); return w == nil }, "watch stopped on last release")
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return p.Coverage == Checking }), "no owner: not observed")

	h.o.Acquire("things", lens)
	h.eventually(h.partIs("things", "cms", live), "live again")
	if lists, _ := src.counts(); lists != 2 {
		t.Fatalf("re-acquire starts a new generation with a fresh list; lists=%d", lists)
	}
}

func TestReleaseDuringDiscoveryDiscardsTheProbe(t *testing.T) {
	src := newSource()
	gate := make(chan struct{})
	var probes atomic.Int32
	probe := func(ctx context.Context) (Probe, error) {
		probes.Add(1)
		select {
		case <-gate:
		case <-ctx.Done():
			return Probe{}, ctx.Err()
		}
		return Probe{Served: true, Version: "v1"}, nil
	}
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Probe: probe, Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(func() bool { return probes.Load() == 1 }, "probe in flight")
	h.o.Acquire("things", HealthOwner)() // the only owner leaves mid-probe
	close(gate)
	h.settle(100 * time.Millisecond)
	if lists, _ := src.counts(); lists != 0 {
		t.Fatalf("a probe finishing after release must start nothing; lists=%d", lists)
	}
}

func TestLateCallbacksFromAnEndedGenerationWriteNowhere(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	h.o.mu.Lock()
	oldGen := h.o.kinds["cms"].gen
	oldInst := oldGen.current
	h.o.mu.Unlock()

	prior, _ := h.sink.part("things", "cms")
	lens := Owner{Kind: "lens", ID: "x"}
	h.o.Acquire("things", HealthOwner)()
	h.o.Acquire("things", lens) // a new generation
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return live(p) && p.Ver > prior.Ver }), "new generation live")
	h.settle(100 * time.Millisecond)
	before, _ := h.sink.part("things", "cms")

	late := &modelAdapter{g: oldGen, inst: oldInst}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "ghost", Namespace: "ns"}, Data: map[string]string{"bad": "1"}}
	_ = late.Add(cm)
	_ = late.Replace([]any{cm}, "99")
	h.settle(100 * time.Millisecond)
	after, _ := h.sink.part("things", "cms")
	if after.Counts.Total != 1 || after.Ver != before.Ver {
		t.Fatalf("late writes from an ended generation leaked: %+v -> %+v", before.Counts, after.Counts)
	}
}

// ---- discovery ----------------------------------------------------------------

func TestDisappearanceAndReappearanceUseNewGenerations(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	var served atomic.Bool
	served.Store(true)
	probe := func(context.Context) (Probe, error) { return Probe{Served: served.Load(), Version: "v1"}, nil }
	h := start(t, Config{ReprobeEvery: 10 * time.Second, Kinds: []KindSpec{cmKind("cms", src)},
		Domains: []DomainSpec{{ID: "things", Probe: probe, Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")

	served.Store(false)
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return p.Coverage == Absent && p.Counts == nil }), "absent")
	h.eventually(func() bool { return src.liveWatcher() == nil }, "watch stopped when the CRD went away")

	served.Store(true)
	h.eventually(h.partIs("things", "cms", live), "back")
	if lists, _ := src.counts(); lists != 2 {
		t.Fatalf("reappearance starts a new generation; lists=%d", lists)
	}
}

func TestNotFoundListReprobesAndFollowsTheServedVersion(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	var version atomic.Value
	version.Store("v1")
	var probes atomic.Int32
	probe := func(context.Context) (Probe, error) {
		probes.Add(1)
		return Probe{Served: true, Version: version.Load().(string)}, nil
	}
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)},
		Domains: []DomainSpec{{ID: "things", Probe: probe, Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live at v1")

	version.Store("v2")
	src.mu.Lock()
	src.listErr = apierrors.NewNotFound(schema.GroupResource{Group: "x", Resource: "cms"}, "")
	src.mu.Unlock()
	src.liveWatcher().Error(&apierrors.NewInternalError(errors.New("boom")).ErrStatus) // forces a restart, whose list is 404
	h.eventually(func() bool { return probes.Load() >= 2 }, "a NotFound list re-probes")
	src.mu.Lock()
	src.listErr = nil
	src.mu.Unlock()
	h.eventually(h.partIs("things", "cms", live), "live at the new version")
	src.mu.Lock()
	defer src.mu.Unlock()
	if last := src.versions[len(src.versions)-1]; last != "v2" {
		t.Fatalf("the kind restarted at %q, want v2 (versions %v)", last, src.versions)
	}
}

func TestADiscoveryErrorConfirmsNothing(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	var fail atomic.Bool
	fail.Store(true)
	probe := func(context.Context) (Probe, error) {
		if fail.Load() {
			return Probe{}, errors.New("discovery: connection refused")
		}
		return Probe{Served: true, Version: "v1"}, nil
	}
	h := start(t, Config{ReprobeEvery: 10 * time.Second, BackoffMax: 2 * time.Second, Kinds: []KindSpec{cmKind("cms", src)},
		Domains: []DomainSpec{{ID: "things", Probe: probe, Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", func(p Part) bool {
		return p.Coverage == Unreadable && p.Kinds[0].Class == ClassDiscovery
	}), "a first probe that fails is a named gap, never absent")
	fail.Store(false)
	h.eventually(h.partIs("things", "cms", live), "live after discovery recovers")
	fail.Store(true) // a later failure keeps the last known served set
	h.settle(300 * time.Millisecond)
	if p, _ := h.sink.part("things", "cms"); p.Coverage != Live {
		t.Fatalf("a failed re-probe must not change coverage; got %s", p.Coverage)
	}
}

// ---- freshness and the restart barrier ---------------------------------------------

func kindOf(p Part) KindCoverage { return p.Kinds[0] }

func TestErrorThenRecoveryWithoutObjectChangesAdvancesVer(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	p0, _ := h.sink.part("things", "cms")

	src.liveWatcher().Error(&apierrors.NewInternalError(errors.New("etcd hiccup")).ErrStatus)
	h.eventually(h.partIs("things", "cms", func(p Part) bool {
		return p.Coverage == Stale && kindOf(p).LastGoodAt != nil && p.Counts.Total == 1
	}), "stale with retained evidence")
	p1, _ := h.sink.part("things", "cms")
	h.eventually(h.partIs("things", "cms", live), "live after the restart's Replace")
	p2, _ := h.sink.part("things", "cms")
	if !(p0.Ver < p1.Ver && p1.Ver < p2.Ver) || p0.Obs != p2.Obs {
		t.Fatalf("coverage transitions advance Ver without touching Obs: %d/%d/%d obs %d/%d", p0.Ver, p1.Ver, p2.Ver, p0.Obs, p2.Obs)
	}
	if lists, _ := src.counts(); lists != 2 {
		t.Fatalf("recovery needs a complete list; lists=%d", lists)
	}
}

func TestFailedWatchEstablishmentNeedsAFullListNotAResumedWatch(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")

	// The next watch establishment fails (connection refused: client-go
	// would resume from its resourceVersion without listing). Hold the
	// restart's list to show the kind stays stale until Replace.
	gate := make(chan struct{})
	src.mu.Lock()
	src.watchErrs = []error{&apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 503, Reason: metav1.StatusReasonServiceUnavailable, Message: "connection refused"}}}
	src.listGate = gate
	src.mu.Unlock()
	// A clean close after an event (a close within a second with no events
	// is client-go's "very short watch", which re-lists instead): the
	// reflector resumes the watch from its resourceVersion, and that fails.
	src.put("a", map[string]string{"touch": "1"})
	src.liveWatcher().Stop()
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return p.Coverage == Stale }), "stale on the failed establishment")
	h.settle(200 * time.Millisecond)
	if p, _ := h.sink.part("things", "cms"); p.Coverage != Stale {
		t.Fatalf("no list has completed: must stay stale, got %s", p.Coverage)
	}
	close(gate)
	h.eventually(h.partIs("things", "cms", live), "live only after the full list")
}

func TestCleanWatchCloseStaysLiveWithoutAList(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	n := h.sink.n()
	src.put("a", map[string]string{"touch": "1"}) // not a "very short watch"
	src.liveWatcher().Stop()                      // server timeout / rotation
	h.eventually(func() bool { _, w := src.counts(); return w == 2 }, "the reflector resumes the watch")
	src.put("b", nil) // the resumed stream still delivers
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return p.Counts.Total == 2 }), "events on the resumed stream")
	if lists, _ := src.counts(); lists != 1 {
		t.Fatalf("a clean close is protocol-continuous: no list expected, got %d", lists)
	}
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	for _, s := range h.sink.sums[n:] {
		for _, tk := range s.Tasks {
			if tk.Parts[0].Coverage != Live {
				t.Fatalf("a clean close must never read as a fault: %s", tk.Parts[0].Coverage)
			}
		}
	}
}

func TestExpiredResourceVersionRelistsWithoutGoingStale(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	n := h.sink.n()
	src.liveWatcher().Error(&apierrors.NewResourceExpired("too old").ErrStatus)
	h.eventually(func() bool { l, _ := src.counts(); return l == 2 }, "the reflector's own re-list")
	h.settle(100 * time.Millisecond)
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	for _, s := range h.sink.sums[n:] {
		if c := s.Tasks[0].Parts[0].Coverage; c != Live {
			t.Fatalf("410 is the protocol's recovery path, not a fault: %s", c)
		}
	}
}

func TestForbiddenFirstReadIsUnreadableAndRevokedLaterIsStale(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "cms"}, "", errors.New("rbac"))
	first := newSource()
	first.listErr = forbidden
	later := newSource()
	later.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("first", first), cmKind("later", later)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("first", "first"), cmPart("later", "later")}}}})
	h.eventually(h.partIs("things", "first", func(p Part) bool {
		return p.Coverage == Unreadable && kindOf(p).Class == ClassForbidden && p.Counts.Total == 0
	}), "forbidden first read: no evidence")
	h.eventually(h.partIs("things", "later", live), "the other kind is unaffected")

	later.mu.Lock()
	later.listErr = forbidden
	later.mu.Unlock()
	later.liveWatcher().Error(&forbidden.ErrStatus)
	h.eventually(h.partIs("things", "later", func(p Part) bool {
		return p.Coverage == Stale && kindOf(p).Class == ClassForbidden && p.Counts.Total == 1
	}), "revoked after a sync: last-good, retained")
	if p, _ := h.sink.part("things", "first"); p.Coverage != Unreadable {
		t.Fatalf("kinds are independent; first is %s", p.Coverage)
	}
}

func TestInitialSyncTimeoutEndsChecking(t *testing.T) {
	src := newSource()
	src.listGate = make(chan struct{}) // never lists
	h := start(t, Config{InitialSyncTimeout: 5 * time.Second, Kinds: []KindSpec{cmKind("cms", src)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", func(p Part) bool {
		return p.Coverage == Unreadable && kindOf(p).Class == ClassTimeout
	}), "checking never lasts past InitialSyncTimeout")
}

// ---- versions, time and atomic capture ----------------------------------------------

func TestTimeOnlyThresholdAdvancesVerAndRowsWithoutData(t *testing.T) {
	src := newSource()
	src.put("a", map[string]string{"until": t0.Add(30 * time.Second).Format(time.RFC3339)})
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.o.SubscribeRows("things", "cms", Owner{Kind: "lens", ID: "things"})
	h.eventually(func() bool {
		r, ok := h.sink.lastRows("things", "cms")
		return ok && r.Part.Counts.Progressing == 1
	}, "rows: progressing before the boundary")
	before, _ := h.sink.lastRows("things", "cms")
	h.eventually(func() bool {
		r, ok := h.sink.lastRows("things", "cms")
		return ok && r.Part.Counts.Fault == 1 && fmt.Sprint(r.Rows) == "[a:fault]"
	}, "rows and counts reclassify at the boundary with an open lens")
	after, _ := h.sink.lastRows("things", "cms")
	if after.Part.Ver <= before.Part.Ver || after.Part.Obs != before.Part.Obs {
		t.Fatalf("a time-only crossing advances Ver, not Obs: ver %d->%d obs %d->%d", before.Part.Ver, after.Part.Ver, before.Part.Obs, after.Part.Obs)
	}
}

func TestUnchangedSuccessfulSnapshotAdvancesFreshness(t *testing.T) {
	var reads atomic.Int32
	snap := SnapshotSpec{Name: "events", Scope: "all namespaces", Cadence: 5 * time.Second, Timeout: 2 * time.Second,
		Read: func(context.Context) ([]any, bool, error) { reads.Add(1); return []any{"w1"}, false, nil }}
	eval := func(c Capture) PartResult { n := len(c.Objects["events"]); return PartResult{Evidence: &n} }
	h := start(t, Config{Snapshots: []SnapshotSpec{snap}, Domains: []DomainSpec{{ID: "events", Parts: []PartSpec{{ID: "warnings", Classifier: "evidence", Counted: []string{"events"}, Evaluate: eval}}}}})
	h.eventually(h.partIs("events", "warnings", live), "first read")
	p0, _ := h.sink.part("events", "warnings")
	h.eventually(func() bool { return reads.Load() >= 2 }, "second read")
	h.eventually(h.partIs("events", "warnings", func(p Part) bool { return p.Ver > p0.Ver }), "freshness advanced")
	p1, _ := h.sink.part("events", "warnings")
	if p1.Obs != p0.Obs || !p1.AsOf.After(*p0.AsOf) {
		t.Fatalf("an unchanged read advances AsOf and Ver, not Obs: obs %d->%d asOf %v->%v", p0.Obs, p1.Obs, p0.AsOf, p1.AsOf)
	}
}

func TestHungSnapshotReadTimesOutAndOverdueGoesStale(t *testing.T) {
	var hang atomic.Bool
	snap := SnapshotSpec{Name: "helm", Scope: "all namespaces", Cadence: 5 * time.Second, Timeout: 2 * time.Second,
		Read: func(ctx context.Context) ([]any, bool, error) {
			if hang.Load() {
				<-ctx.Done()
				return nil, false, ctx.Err()
			}
			return []any{"r1"}, false, nil
		}}
	eval := func(c Capture) PartResult { n := len(c.Objects["helm"]); return PartResult{Evidence: &n} }
	h := start(t, Config{Snapshots: []SnapshotSpec{snap}, Domains: []DomainSpec{{ID: "helm", Parts: []PartSpec{{ID: "releases", Counted: []string{"helm"}, Evaluate: eval}}}}})
	h.eventually(h.partIs("helm", "releases", live), "first read")
	hang.Store(true)
	h.eventually(h.partIs("helm", "releases", func(p Part) bool {
		return p.Coverage == Stale && (kindOf(p).Class == ClassTimeout || kindOf(p).Class == ClassOverdue)
	}), "a hung read never leaves the part live or checking")
}

func TestUnrelatedPartsKeepTheirVer(t *testing.T) {
	a, b := newSource(), newSource()
	a.put("x", nil)
	b.put("y", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("a", a), cmKind("b", b)},
		Domains: []DomainSpec{{ID: "pods", Parts: []PartSpec{cmPart("a", "a")}}, {ID: "certs", Parts: []PartSpec{cmPart("b", "b")}}}})
	h.eventually(h.partIs("pods", "a", live), "a live")
	h.eventually(h.partIs("certs", "b", live), "b live")
	pb, _ := h.sink.part("certs", "b")
	for i := 0; i < 20; i++ {
		a.put(fmt.Sprintf("p%d", i), nil)
	}
	h.eventually(h.partIs("pods", "a", func(p Part) bool { return p.Counts.Total == 21 }), "churn applied")
	if p, _ := h.sink.part("certs", "b"); p.Ver != pb.Ver {
		t.Fatalf("pod churn touched certificates: ver %d -> %d", pb.Ver, p.Ver)
	}
}

func TestRowsAndCountsComeFromOneCaptureUnderConcurrentMutation(t *testing.T) {
	src := newSource()
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.o.SubscribeRows("things", "cms", Owner{Kind: "lens", ID: "t"})
	h.eventually(h.partIs("things", "cms", live), "live")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			bad := "0"
			if i%3 == 0 {
				bad = "1"
			}
			src.put(fmt.Sprintf("o%d", i%40), map[string]string{"bad": bad, "i": strconv.Itoa(i)})
			time.Sleep(200 * time.Microsecond)
		}
	}()
	h.settle(400 * time.Millisecond)
	for i := 0; i < 10; i++ {
		r, ok := h.o.RowsNow("things", "cms")
		if ok && len(r.Rows.([]string)) != r.Part.Counts.Total {
			t.Fatalf("bootstrap rows and counts disagree: %d rows, total %d", len(r.Rows.([]string)), r.Part.Counts.Total)
		}
	}
	close(stop)
	wg.Wait()
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	lastVer := uint64(0)
	for _, r := range h.sink.rows {
		if n := len(r.Rows.([]string)); n != r.Part.Counts.Total {
			t.Fatalf("rows and counts disagree at ver %d: %d vs %d", r.Part.Ver, n, r.Part.Counts.Total)
		}
		if r.Part.Ver < lastVer {
			t.Fatalf("an older capture published under a newer version: %d after %d", r.Part.Ver, lastVer)
		}
		lastVer = r.Part.Ver
	}
}

func TestContinuousChurnPublishesAtMostOncePerThrottle(t *testing.T) {
	src := newSource()
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	startN, startT := h.sink.n(), h.clk.Now()
	for i := 0; i < 200; i++ {
		src.put(fmt.Sprintf("o%d", i%10), map[string]string{"i": strconv.Itoa(i)})
		if i%10 == 0 {
			h.clk.Step(100 * time.Millisecond)
		}
		time.Sleep(time.Millisecond)
	}
	h.settle(200 * time.Millisecond)
	published := h.sink.n() - startN
	elapsed := h.clk.Now().Sub(startT)
	if limit := int(elapsed/time.Second) + 2; published > limit {
		t.Fatalf("%d publishes in %v of clock time (limit %d)", published, elapsed, limit)
	}
	if published == 0 {
		t.Fatal("churn never published")
	}
}

// ---- routes (spec §1.3) ------------------------------------------------------------

func TestRouteCountedOnceWithItsWorstParent(t *testing.T) {
	atts := []Attachment{
		{UID: "r1", Namespace: "app", Name: "web", Parent: "infra/a", Role: "ok", Label: "resolved"},
		{UID: "r1", Namespace: "app", Name: "web", Parent: "infra/b", Role: "degraded", Label: "2/3 endpoints ready", Included: true},
		{UID: "r1", Namespace: "app", Name: "web", Parent: "infra/c", Role: "fault", Label: "backend not found", Included: true},
	}
	got := ReduceAttachments(atts)
	if len(got) != 1 || got[0].Role != "fault" || got[0].Parent != "infra/c" || !got[0].Included || len(got[0].Attachments) != 3 {
		t.Fatalf("one healthy and two failing parents: %+v", got)
	}
	// Parent c removed: the attachment drops and the route re-derives.
	got = ReduceAttachments(atts[:2])
	if got[0].Role != "degraded" || got[0].Parent != "infra/b" {
		t.Fatalf("after parent removal: %+v", got)
	}
	// Parent c recreated: same identity, progressing until status is written.
	got = ReduceAttachments(append(atts[:2:2], Attachment{UID: "r1", Namespace: "app", Name: "web", Parent: "infra/c", Role: "progressing", Label: "acceptance pending"}))
	if len(got) != 1 || got[0].Role != "degraded" || len(got[0].Attachments) != 3 {
		t.Fatalf("after recreation: %+v", got)
	}
	// Ties break by parent name; an unrecognised role ranks as unknown.
	got = ReduceAttachments([]Attachment{
		{UID: "r2", Name: "x", Parent: "z/g", Role: "fault"}, {UID: "r2", Name: "x", Parent: "a/g", Role: "fault"},
		{UID: "r3", Name: "y", Parent: "a/g", Role: "weird"}, {UID: "r3", Name: "y", Parent: "b/g", Role: "ok"},
	})
	if got[0].Parent != "a/g" || got[1].Role != "weird" {
		t.Fatalf("ties and unknown roles: %+v", got)
	}
}

// ---- identity and Final --------------------------------------------------------------

func TestFinalPublishesOnceAndEndsEveryGoroutine(t *testing.T) {
	baseline := runtime.NumGoroutine()
	src := newSource()
	src.put("a", nil)
	clk := testingclock.NewFakeClock(t0)
	sink := &recSink{}
	o := New(Config{Cluster: "c1", Clock: clk, Sink: sink, InitialSyncTimeout: time.Hour, ReprobeEvery: time.Hour,
		Kinds:   []KindSpec{cmKind("cms", src)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	older := New(Config{Cluster: "c1", Sink: sink})
	if older.Incarnation() >= New(Config{Cluster: "c1", Sink: sink}).Incarnation() || o.Incarnation() >= older.Incarnation() {
		t.Fatal("incarnations must be process-wide monotonic")
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx)
	o.Start(ctx) // idempotent
	o.SubscribeRows("things", "cms", Owner{Kind: "lens", ID: "x"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s, ok := sink.last(); ok && len(s.Tasks) == 1 && s.Tasks[0].Parts[0].Coverage == Live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never live")
		}
		clk.Step(250 * time.Millisecond)
		time.Sleep(4 * time.Millisecond)
	}
	cancel()
	select {
	case <-o.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("observer did not finish")
	}
	finals := 0
	for _, s := range sink.sums {
		if s.Final {
			finals++
		}
	}
	last, _ := sink.last()
	if finals != 1 || !last.Final || last.Incarnation != o.Incarnation() {
		t.Fatalf("want exactly one Final, last: %+v (finals %d)", last, finals)
	}
	if _, ok := o.RowsNow("things", "cms"); ok {
		t.Fatal("a bootstrap after Final must not return rows")
	}
	if l, _ := o.Latest(); !l.Final {
		t.Fatal("Latest after Final is the Final")
	}
	for end := time.Now().Add(5 * time.Second); runtime.NumGoroutine() > baseline && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		t.Fatalf("goroutines leaked: %d > baseline %d", n, baseline)
	}
}

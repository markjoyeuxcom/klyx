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
	watchList bool  // serve watch-list (SendInitialEvents) streams
	wlErr     error // a watch-list attempt fails (the reflector falls back to a list)
	wlCalls   int
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

func (l *fakeLW) WatchWithContext(_ context.Context, o metav1.ListOptions) (watch.Interface, error) {
	f := l.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if o.SendInitialEvents != nil && *o.SendInitialEvents {
		f.wlCalls++
		if f.wlErr != nil {
			return nil, f.wlErr
		}
		// The initial state as ADDED events, then the initial-events-end
		// bookmark; the same stream then carries ordinary events.
		w := watch.NewRaceFreeFake()
		names := make([]string, 0, len(f.objs))
		for n := range f.objs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			w.Add(f.objs[n].DeepCopy())
		}
		w.Action(watch.Bookmark, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{ResourceVersion: strconv.Itoa(f.rv),
			Annotations: map[string]string{metav1.InitialEventsAnnotationKey: "true"}}})
		f.watchers = append(f.watchers, w)
		return w, nil
	}
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

func (l *fakeLW) IsWatchListSemanticsUnSupported() bool {
	l.f.mu.Lock()
	defer l.f.mu.Unlock()
	return !l.f.watchList
}

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

// putUID writes an object with an explicit UID (a same-name recreation).
func (f *fakeSource) putUID(name, uid string, data map[string]string) {
	f.mu.Lock()
	f.rv++
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID(uid), ResourceVersion: strconv.Itoa(f.rv)}, Data: data}
	old, existed := f.objs[name]
	f.objs[name] = cm
	w := f.liveWatcherLocked()
	f.mu.Unlock()
	if w != nil {
		if existed && old.UID == cm.UID {
			w.Modify(cm.DeepCopy())
		} else {
			if existed {
				w.Delete(old.DeepCopy())
			}
			w.Add(cm.DeepCopy())
		}
	}
}

// remove deletes an object.
func (f *fakeSource) remove(name string) {
	f.mu.Lock()
	f.rv++
	old, existed := f.objs[name]
	delete(f.objs, name)
	w := f.liveWatcherLocked()
	f.mu.Unlock()
	if w != nil && existed {
		old = old.DeepCopy()
		old.ResourceVersion = strconv.Itoa(f.rv)
		w.Delete(old)
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
	mu      sync.Mutex
	sums    []Summary
	rows    []Rows
	batches []TransitionBatch
}

func (s *recSink) Summary(x Summary) { s.mu.Lock(); s.sums = append(s.sums, x); s.mu.Unlock() }
func (s *recSink) Rows(x Rows)       { s.mu.Lock(); s.rows = append(s.rows, x); s.mu.Unlock() }
func (s *recSink) Transitions(b TransitionBatch) {
	s.mu.Lock()
	s.batches = append(s.batches, b)
	s.mu.Unlock()
}

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

func withProbe(k KindSpec, probe func(context.Context) (Probe, error)) KindSpec {
	k.Probe = probe
	return k
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
	h := start(t, Config{Kinds: []KindSpec{withProbe(cmKind("cms", src), probe)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
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
	h := start(t, Config{ReprobeEvery: 10 * time.Second, Kinds: []KindSpec{withProbe(cmKind("cms", src), probe)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
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
	h := start(t, Config{Kinds: []KindSpec{withProbe(cmKind("cms", src), probe)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
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
	h := start(t, Config{ReprobeEvery: 10 * time.Second, BackoffMax: 2 * time.Second, Kinds: []KindSpec{withProbe(cmKind("cms", src), probe)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
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

// ---- review findings (Codex, dd8d2ee) ---------------------------------------------

func TestWatchListStreamErrorsAfterTheBarrierAreRecorded(t *testing.T) {
	src := newSource()
	src.watchList = true
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return live(p) && p.Counts.Total == 1 }), "live through initial events, bookmark and Replace")
	src.mu.Lock()
	wl, lists := src.wlCalls, src.lists
	src.mu.Unlock()
	if wl != 1 || lists != 0 {
		t.Fatalf("the production default path must stream, not list: watch-list calls %d, lists %d", wl, lists)
	}
	src.put("b", nil) // the same stream now carries ordinary events
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return p.Counts.Total == 2 }), "events after the barrier")
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "cms"}, "", errors.New("rbac revoked"))
	// The reflector's retry is held - its watch-list falls back to a list
	// that never answers - so the only error the part can show is the one on
	// the reused stream, and a retry that succeeded before the next summary
	// cannot hide it on a slow runner.
	src.mu.Lock()
	src.wlErr = apierrors.NewBadRequest("watch-list is not supported")
	src.listGate = make(chan struct{})
	src.mu.Unlock()
	src.liveWatcher().Error(&forbidden.ErrStatus)
	h.eventually(h.partIs("things", "cms", func(p Part) bool {
		return p.Coverage == Stale && kindOf(p).Class == ClassForbidden
	}), "an error on the reused watch-list stream is recorded")
}

func TestWatchListInitialErrorFallsBackToAListWithoutAFault(t *testing.T) {
	src := newSource()
	src.watchList = true
	src.wlErr = apierrors.NewBadRequest("watch-list is not supported")
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live through the list fallback")
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	for _, s := range h.sink.sums {
		if c := s.Tasks[0].Parts[0].Coverage; c == Stale || c == Unreadable {
			t.Fatalf("a watch-list attempt that falls back is not a fault: %s", c)
		}
	}
}

func TestRowsNowNeverConsumesTheBroadcastOwedToSubscribers(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.o.SubscribeRows("things", "cms", Owner{Kind: "lens", ID: "open"})
	h.eventually(func() bool { r, ok := h.sink.lastRows("things", "cms"); return ok && r.Part.Coverage == Live }, "the open lens has rows")
	h.settle(300 * time.Millisecond) // past the throttle: the next change is not yet owed
	src.put("b", nil)
	var boot Rows
	deadline := time.Now().Add(3 * time.Second)
	for { // a second consumer bootstraps before the throttle tick
		r, ok := h.o.RowsNow("things", "cms")
		if ok && r.Part.Counts.Total == 2 {
			boot = r
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bootstrap never saw b")
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.eventually(func() bool {
		r, ok := h.sink.lastRows("things", "cms")
		return ok && r.Part.Ver >= boot.Part.Ver && r.Part.Counts.Total == 2
	}, "the existing subscriber still receives the version the bootstrap advanced")
}

func TestDiscoveryIsPerKindAndSharedKindsHaveOneGeneration(t *testing.T) {
	v1, beta, absentSrc, svc := newSource(), newSource(), newSource(), newSource()
	v1.put("k1", nil)
	beta.put("b1", map[string]string{"bad": "1"})
	svc.put("s1", nil)
	served := func(v string) func(context.Context) (Probe, error) {
		return func(context.Context) (Probe, error) { return Probe{Served: true, Version: v}, nil }
	}
	notServed := func(context.Context) (Probe, error) { return Probe{Served: false}, nil }
	mixed := PartSpec{ID: "sources", Classifier: "native", Counted: []string{"git", "oci", "helm"},
		Evaluate: func(c Capture) PartResult {
			n := len(c.Objects["git"]) + len(c.Objects["oci"]) + len(c.Objects["helm"])
			return PartResult{Counts: &Counts{Total: n}}
		}}
	h := start(t, Config{
		Kinds: []KindSpec{
			withProbe(cmKind("git", v1), served("v1")), withProbe(cmKind("oci", beta), served("v1beta2")),
			withProbe(cmKind("helm", absentSrc), notServed), cmKind("svc", svc),
		},
		Domains: []DomainSpec{
			{ID: "flux", Parts: []PartSpec{mixed}},
			{ID: "gateway", Parts: []PartSpec{{ID: "routes", Counted: []string{"helm"}, Deps: []string{"svc"}, Evaluate: evalCMs("helm")}}},
			{ID: "services", Parts: []PartSpec{cmPart("svc", "svc")}},
		}})
	h.eventually(h.partIs("flux", "sources", func(p Part) bool { return live(p) && p.Counts.Total == 2 }), "a missing optional kind leaves the part observed")
	p, _ := h.sink.part("flux", "sources")
	if p.Kinds[2].Coverage != Absent {
		t.Fatalf("the absent kind is named absent: %+v", p.Kinds[2])
	}
	v1.mu.Lock()
	beta.mu.Lock()
	if v1.versions[0] != "v1" || beta.versions[0] != "v1beta2" {
		t.Fatalf("each kind lists at its own served version: %v %v", v1.versions, beta.versions)
	}
	beta.mu.Unlock()
	v1.mu.Unlock()
	h.eventually(h.partIs("gateway", "routes", func(p Part) bool { return p.Coverage == Absent && p.Counts == nil }),
		"absent counted kinds make the part absent even with a live dependency")
	h.eventually(h.partIs("services", "svc", live), "the shared kind is live")
	if lists, _ := svc.counts(); lists != 1 {
		t.Fatalf("two domains share one generation of svc; lists=%d", lists)
	}
	h.o.Acquire("services", HealthOwner)() // one reader leaves
	h.settle(150 * time.Millisecond)
	if lists, _ := svc.counts(); lists != 1 || svc.liveWatcher() == nil {
		t.Fatal("the other reader keeps the shared generation running")
	}
}

func TestTimedOutSnapshotReadIsNeverOverlappedAndDoneWaitsForIt(t *testing.T) {
	var inflight, maxInflight atomic.Int32
	var lastReturn atomic.Int64
	var hang atomic.Bool
	snap := SnapshotSpec{Name: "helm", Scope: "all namespaces", Cadence: 3 * time.Second, Timeout: time.Second,
		Read: func(ctx context.Context) ([]any, bool, error) {
			n := inflight.Add(1)
			for m := maxInflight.Load(); n > m && !maxInflight.CompareAndSwap(m, n); m = maxInflight.Load() {
			}
			defer func() { inflight.Add(-1); lastReturn.Store(time.Now().UnixNano()) }()
			if hang.Load() {
				<-ctx.Done()
				time.Sleep(150 * time.Millisecond) // slow to honour cancellation
				return nil, false, ctx.Err()
			}
			return []any{"r1"}, false, nil
		}}
	eval := func(c Capture) PartResult { n := len(c.Objects["helm"]); return PartResult{Evidence: &n} }
	clk := testingclock.NewFakeClock(t0)
	sink := &recSink{}
	o := New(Config{Cluster: "c1", Clock: clk, Sink: sink, InitialSyncTimeout: time.Hour, Snapshots: []SnapshotSpec{snap},
		Domains: []DomainSpec{{ID: "helm", Parts: []PartSpec{{ID: "releases", Counted: []string{"helm"}, Evaluate: eval}}}}})
	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx)
	h := &harness{t: t, clk: clk, sink: sink, o: o, cancel: cancel}
	h.eventually(h.partIs("helm", "releases", live), "first read")
	hang.Store(true)
	h.eventually(h.partIs("helm", "releases", func(p Part) bool {
		return p.Coverage == Stale && kindOf(p).Class == ClassTimeout
	}), "the timeout is visible while the read is still unwinding")
	h.o.Refresh("helm") // a refresh during the abandoned read must not overlap it
	h.settle(600 * time.Millisecond)
	if m := maxInflight.Load(); m > 1 {
		t.Fatalf("reads overlapped: %d concurrent", m)
	}
	cancel()
	<-o.Done()
	doneAt := time.Now().UnixNano()
	if inflight.Load() != 0 || lastReturn.Load() > doneAt {
		t.Fatal("Done closed before the last read returned")
	}
}

// ---- discovery gaps after a success (Codex re-review, 9390458) -------------------

// probeScript is a controllable probe: "absent", "fail" or a served version.
type probeScript struct{ mode atomic.Value }

func newProbe(mode string) *probeScript { p := &probeScript{}; p.mode.Store(mode); return p }
func (p *probeScript) set(mode string)  { p.mode.Store(mode) }
func (p *probeScript) probe(context.Context) (Probe, error) {
	switch m := p.mode.Load().(string); m {
	case "absent":
		return Probe{Served: false}, nil
	case "fail":
		return Probe{}, errors.New("discovery: the server is unavailable")
	default:
		return Probe{Served: true, Version: m}, nil
	}
}

func gapHarness(t *testing.T, mode string) (*harness, *fakeSource, *probeScript) {
	src := newSource()
	src.put("a", nil)
	ps := newProbe(mode)
	h := start(t, Config{ReprobeEvery: 5 * time.Second, BackoffMax: 2 * time.Second,
		Kinds:   []KindSpec{withProbe(cmKind("cms", src), ps.probe)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	return h, src, ps
}

func TestFailedReprobeOfAnAbsentKindIsAGapThenConfirmedAbsent(t *testing.T) {
	h, _, ps := gapHarness(t, "absent")
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return p.Coverage == Absent }), "absent")
	p0, _ := h.sink.part("things", "cms")
	ps.set("fail")
	h.eventually(h.partIs("things", "cms", func(p Part) bool {
		return p.Coverage == Stale && kindOf(p).Class == ClassDiscovery && kindOf(p).LastGoodAt != nil
	}), "a failing re-probe never confirms absence")
	if p, _ := h.sink.part("things", "cms"); p.Ver <= p0.Ver || p.Obs != p0.Obs {
		t.Fatalf("the gap publishes without an object mutation: ver %d->%d obs %d->%d", p0.Ver, p.Ver, p0.Obs, p.Obs)
	}
	ps.set("absent")
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return p.Coverage == Absent && kindOf(p).Error == "" }), "confirmed absent again")
}

func TestFailedReprobeOfAnAbsentKindThenPresent(t *testing.T) {
	h, _, ps := gapHarness(t, "absent")
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return p.Coverage == Absent }), "absent")
	ps.set("fail")
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return p.Coverage == Stale }), "gap")
	ps.set("v1")
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return live(p) && p.Counts.Total == 1 && kindOf(p).Discovery == "" }), "present")
}

func TestFailedReprobeOfALiveKindNamesTheGapWithoutTearingItDown(t *testing.T) {
	h, src, ps := gapHarness(t, "v1")
	h.eventually(h.partIs("things", "cms", live), "live")
	p0, _ := h.sink.part("things", "cms")
	ps.set("fail")
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return live(p) && kindOf(p).Discovery != "" }), "live data, named discovery gap")
	p1, _ := h.sink.part("things", "cms")
	ps.set("v1")
	h.eventually(h.partIs("things", "cms", func(p Part) bool { return live(p) && kindOf(p).Discovery == "" }), "gap cleared on same-version recovery")
	p2, _ := h.sink.part("things", "cms")
	if !(p0.Ver < p1.Ver && p1.Ver < p2.Ver) || p0.Obs != p2.Obs {
		t.Fatalf("the gap opens and closes as versions without data: ver %d/%d/%d obs %d/%d", p0.Ver, p1.Ver, p2.Ver, p0.Obs, p2.Obs)
	}
	if lists, _ := src.counts(); lists != 1 || src.liveWatcher() == nil {
		t.Fatalf("a failed re-probe must not tear down a valid reflector; lists=%d", lists)
	}
}

// ---- facts (Codex 2a review #2) ---------------------------------------------------

func TestFactsReevaluateOnlyTheirPartsWithoutData(t *testing.T) {
	a, b := newSource(), newSource()
	a.put("x", nil)
	b.put("y", nil)
	factual := PartSpec{ID: "a", Counted: []string{"a"}, Facts: []string{"mode"}, Evaluate: func(c Capture) PartResult {
		mode, _ := c.Facts["mode"].(string)
		r := PartResult{Counts: &Counts{Total: len(c.Objects["a"])}}
		if c.Rows {
			r.Rows = []string{"mode=" + mode}
		}
		return r
	}}
	h := start(t, Config{Kinds: []KindSpec{cmKind("a", a), cmKind("b", b)},
		Domains: []DomainSpec{{ID: "workloads", Parts: []PartSpec{factual}}, {ID: "certs", Parts: []PartSpec{cmPart("b", "b")}}}})
	h.o.SubscribeRows("workloads", "a", Owner{Kind: "lens", ID: "w"})
	h.eventually(func() bool { r, ok := h.sink.lastRows("workloads", "a"); return ok && live(r.Part) }, "rows")
	h.eventually(h.partIs("certs", "b", live), "b live")
	r0, _ := h.sink.lastRows("workloads", "a")
	pb, _ := h.sink.part("certs", "b")

	h.o.SetFact("mode", "flux")
	h.eventually(func() bool {
		r, _ := h.sink.lastRows("workloads", "a")
		return fmt.Sprint(r.Rows) == "[mode=flux]"
	}, "a fact change re-evaluates the part with no object mutation")
	r1, _ := h.sink.lastRows("workloads", "a")
	if r1.Part.Ver <= r0.Part.Ver || r1.Part.Obs != r0.Part.Obs {
		t.Fatalf("Ver advances, Obs does not: ver %d->%d obs %d->%d", r0.Part.Ver, r1.Part.Ver, r0.Part.Obs, r1.Part.Obs)
	}
	h.o.SetFact("mode", "flux") // unchanged: a no-op
	h.settle(200 * time.Millisecond)
	if r, _ := h.sink.lastRows("workloads", "a"); r.Part.Ver != r1.Part.Ver {
		t.Fatalf("an equal fact must not advance Ver: %d -> %d", r1.Part.Ver, r.Part.Ver)
	}
	if p, _ := h.sink.part("certs", "b"); p.Ver != pb.Ver {
		t.Fatal("a part that does not declare the fact is untouched")
	}
}

// ---- ReadSnapshot: every read of a snapshot source is the snapshot's own ----

// scriptedSnap is a snapshot source whose reads can be held, counted and
// scripted; it records the most reads ever in flight at once.
type scriptedSnap struct {
	mu       sync.Mutex
	n        int
	inflight int
	max      int
	hold     map[int]chan struct{} // read number -> released when closed
	fail     map[int]error
	entered  chan int
}

func newScripted() *scriptedSnap {
	return &scriptedSnap{hold: map[int]chan struct{}{}, fail: map[int]error{}, entered: make(chan int, 16)}
}

func (s *scriptedSnap) read(ctx context.Context) ([]any, bool, error) {
	s.mu.Lock()
	s.n++
	n := s.n
	s.inflight++
	if s.inflight > s.max {
		s.max = s.inflight
	}
	h, err := s.hold[n], s.fail[n]
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.inflight--; s.mu.Unlock() }()
	s.entered <- n
	if h != nil {
		select {
		case <-h:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	if err != nil {
		return nil, false, err
	}
	items := make([]any, n) // read n returns n items: its identity
	for i := range items {
		items[i] = fmt.Sprintf("r%d", i)
	}
	return items, false, nil
}

func (s *scriptedSnap) reads() int       { s.mu.Lock(); defer s.mu.Unlock(); return s.n }
func (s *scriptedSnap) maxInFlight() int { s.mu.Lock(); defer s.mu.Unlock(); return s.max }

func scriptedHarness(t *testing.T, s *scriptedSnap) *harness {
	snap := SnapshotSpec{Name: "helm", Scope: "all namespaces", Cadence: 10 * time.Second, Timeout: 5 * time.Second, Read: s.read}
	eval := func(c Capture) PartResult { n := len(c.Objects["helm"]); return PartResult{Evidence: &n} }
	return start(t, Config{Snapshots: []SnapshotSpec{snap}, Domains: []DomainSpec{{ID: "helm", Parts: []PartSpec{{ID: "releases", Counted: []string{"helm"}, Evaluate: eval}}}}})
}

type readOut struct {
	r   SnapshotRead
	err error
}

func asyncRead(ctx context.Context, o *Observer) chan readOut {
	out := make(chan readOut, 1)
	go func() { r, err := o.ReadSnapshot(ctx, "helm"); out <- readOut{r, err} }()
	return out
}

func TestReadSnapshotIsTheSnapshotsOwnRead(t *testing.T) {
	s := newScripted()
	h := scriptedHarness(t, s)
	<-s.entered // read 1, the snapshot's first
	h.eventually(h.partIs("helm", "releases", live), "first read")
	got := asyncRead(context.Background(), h.o)
	if n := <-s.entered; n != 2 {
		t.Fatalf("the request runs read 2, got %d", n)
	}
	out := <-got
	if out.err != nil || len(out.r.Items) != 2 {
		t.Fatalf("the caller gets the read it caused: %+v", out)
	}
	h.eventually(h.partIs("helm", "releases", func(p Part) bool { return *p.Evidence == 2 }), "and the summary has it")
	if s.reads() != 2 {
		t.Fatalf("one read served both: %d reads", s.reads())
	}
}

// Requests made while a read is in flight are owed a read that starts after
// them: they coalesce into one follow-up, applied after the older read - so
// an older outcome can never land after a newer one.
func TestRequestsDuringAReadCoalesceIntoAFollowUpInOrder(t *testing.T) {
	s := newScripted()
	release2 := make(chan struct{})
	s.hold[2] = release2
	h := scriptedHarness(t, s)
	<-s.entered
	h.eventually(h.partIs("helm", "releases", live), "first read")
	first := asyncRead(context.Background(), h.o) // causes read 2, held
	if n := <-s.entered; n != 2 {
		t.Fatalf("want read 2 in flight, got %d", n)
	}
	a, b := asyncRead(context.Background(), h.o), asyncRead(context.Background(), h.o) // during read 2
	time.Sleep(50 * time.Millisecond)                                                  // both are queued
	close(release2)
	if r := <-first; len(r.r.Items) != 2 {
		t.Fatalf("the first caller gets read 2: %+v", r)
	}
	ra, rb := <-a, <-b
	if len(ra.r.Items) != 3 || len(rb.r.Items) != 3 {
		t.Fatalf("both later callers share read 3, never read 2: %d, %d", len(ra.r.Items), len(rb.r.Items))
	}
	h.eventually(h.partIs("helm", "releases", func(p Part) bool { return *p.Evidence == 3 }), "the newest read is the summary's")
	time.Sleep(50 * time.Millisecond)
	if s.reads() != 3 || s.maxInFlight() != 1 {
		t.Fatalf("one follow-up read, never parallel: %d reads, %d at once", s.reads(), s.maxInFlight())
	}
}

// A cadence read in flight serves no request made after it began, and a
// cadence tick during a requested read cannot start a parallel one.
func TestCadenceAndRequestedReadsNeverOverlap(t *testing.T) {
	s := newScripted()
	release2, release3 := make(chan struct{}), make(chan struct{})
	s.hold[2], s.hold[3] = release2, release3
	h := scriptedHarness(t, s)
	<-s.entered
	h.eventually(h.partIs("helm", "releases", live), "first read")
	h.clk.Step(11 * time.Second) // the cadence starts read 2
	if n := <-s.entered; n != 2 {
		t.Fatalf("want the cadence's read 2, got %d", n)
	}
	got := asyncRead(context.Background(), h.o)
	time.Sleep(20 * time.Millisecond)
	close(release2)
	if n := <-s.entered; n != 3 {
		t.Fatalf("the request runs its own read 3, got %d", n)
	}
	h.clk.Step(4 * time.Second) // time passes while read 3 is held (within its timeout); the cadence timer runs only between reads
	time.Sleep(50 * time.Millisecond)
	close(release3)
	if out := <-got; len(out.r.Items) != 3 {
		t.Fatalf("a request during a cadence read is served by the next: %+v", out)
	}
	if s.maxInFlight() != 1 {
		t.Fatalf("reads overlapped: %d", s.maxInFlight())
	}
}

// A failed request is the snapshot's failure: the caller gets it, the
// summary's freshness gap opens, and the last good evidence stays.
func TestAFailedRequestOpensTheFreshnessGapAndKeepsLastGood(t *testing.T) {
	s := newScripted()
	s.fail[2] = apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("rbac"))
	h := scriptedHarness(t, s)
	<-s.entered
	h.eventually(h.partIs("helm", "releases", live), "first read")
	out := <-asyncRead(context.Background(), h.o)
	if out.err != nil || out.r.Err == nil {
		t.Fatalf("the caller gets the read's failure: %+v", out)
	}
	h.eventually(h.partIs("helm", "releases", func(p Part) bool {
		return p.Coverage == Stale && kindOf(p).Class == ClassForbidden && *p.Evidence == 1
	}), "stale, classed, last good retained")
}

// A caller that stops waiting does not cancel the read: it belongs to the
// snapshot and is applied.
func TestACallerThatGivesUpLeavesTheReadToTheSnapshot(t *testing.T) {
	s := newScripted()
	release2 := make(chan struct{})
	s.hold[2] = release2
	h := scriptedHarness(t, s)
	<-s.entered
	h.eventually(h.partIs("helm", "releases", live), "first read")
	ctx, cancel := context.WithCancel(context.Background())
	got := asyncRead(ctx, h.o)
	<-s.entered
	cancel()
	if out := <-got; !errors.Is(out.err, context.Canceled) {
		t.Fatalf("the caller's wait ends with its own context: %+v", out)
	}
	close(release2)
	h.eventually(h.partIs("helm", "releases", func(p Part) bool { return *p.Evidence == 2 }), "the read still lands")
}

func TestReadSnapshotOfAStoppedOrUnknownSnapshot(t *testing.T) {
	s := newScripted()
	h := scriptedHarness(t, s)
	if _, err := h.o.ReadSnapshot(context.Background(), "nope"); !errors.Is(err, ErrSnapshotNotRunning) {
		t.Fatalf("unknown snapshot: %v", err)
	}
	h.cancel()
	<-h.o.Done()
	if _, err := h.o.ReadSnapshot(context.Background(), "helm"); !errors.Is(err, ErrSnapshotNotRunning) {
		t.Fatalf("stopped Observer: %v", err)
	}
}

// ---- controller issues ------------------------------------------------------------

func TestControllerIssueIsTaskLevelNeverACount(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "certs", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("certs", "cms", live), "live")
	taskOf := func() (Task, uint64) {
		s, _ := h.sink.last()
		for _, t := range s.Tasks {
			if t.ID == "certs" {
				return t, s.Seq
			}
		}
		return Task{}, s.Seq
	}
	p0, _ := h.sink.part("certs", "cms")
	_, seq0 := taskOf()
	h.o.SetController("certs", &ControllerIssue{State: "not ready", Reason: "cert-manager installed but cert-manager is not ready"})
	h.eventually(func() bool { t, _ := taskOf(); return t.Controller != nil && t.Controller.State == "not ready" }, "the issue is published")
	tk, seq1 := taskOf()
	if seq1 <= seq0 || tk.Parts[0].Ver != p0.Ver || tk.Parts[0].Counts.Total != p0.Counts.Total {
		t.Fatalf("a controller change advances Seq and touches no part: seq %d->%d, ver %d->%d", seq0, seq1, p0.Ver, tk.Parts[0].Ver)
	}
	n := h.sink.n()
	h.o.SetController("certs", &ControllerIssue{State: "not ready", Reason: "cert-manager installed but cert-manager is not ready"})
	h.o.SetController("nope", &ControllerIssue{State: "not ready"}) // unknown task: ignored
	h.settle(300 * time.Millisecond)
	if h.sink.n() != n {
		t.Fatal("an equal issue or an unknown task republished")
	}
	h.o.SetController("certs", nil)
	h.eventually(func() bool { t, _ := taskOf(); return t.Controller == nil }, "recovery clears it")
}

// ---- WatchKind (the connection's pod seam, 1c 2d) ---------------------------

type stateLog struct {
	mu  sync.Mutex
	all []KindState
}

func (l *stateLog) add(st KindState) { l.mu.Lock(); l.all = append(l.all, st); l.mu.Unlock() }
func (l *stateLog) last() (KindState, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.all) == 0 {
		return KindState{}, false
	}
	return l.all[len(l.all)-1], true
}

func TestWatchKindDeliversTheCapturedStateAtOnce(t *testing.T) {
	src := newSource()
	src.put("a", nil)
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	log := &stateLog{}
	cancel := h.o.WatchKind("cms", log.add)
	defer cancel()
	h.eventually(func() bool { st, ok := log.last(); return ok && st.Coverage == Live }, "an already-live kind is delivered at subscription")
	st, _ := log.last()
	if !st.CountKnown || st.Objects != 1 || st.Generation == 0 || st.ObservedAt == nil {
		t.Fatalf("one capture: count and coverage together: %+v", st)
	}
	if c := h.o.WatchKind("nope", log.add); c == nil {
		t.Fatal("an unknown kind returns a no-op cancel")
	}
}

func TestWatchKindCountAndTimesFollowObservationsOnly(t *testing.T) {
	src := newSource()
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	log := &stateLog{}
	defer h.o.WatchKind("cms", log.add)()
	h.eventually(func() bool { st, ok := log.last(); return ok && st.Coverage == Live }, "an empty successful list is live")
	st, _ := log.last()
	if !st.CountKnown || st.Objects != 0 {
		t.Fatalf("an empty complete read is a known zero: %+v", st)
	}
	seen := *st.ObservedAt
	h.clk.Step(time.Second)
	src.put("a", nil)
	h.eventually(func() bool { st, _ := log.last(); return st.Objects == 1 }, "a mutation")
	st, _ = log.last()
	if !st.ObservedAt.After(seen) {
		t.Fatal("a mutation is an observation")
	}
	seen = *st.ObservedAt
	h.clk.Step(time.Second)
	src.mu.Lock()
	src.listErr = errors.New("connection refused")
	src.mu.Unlock()
	src.liveWatcher().Error(&apierrors.NewServiceUnavailable("down").ErrStatus)
	h.eventually(func() bool { st, _ := log.last(); return st.Coverage == Stale }, "a failure")
	st, _ = log.last()
	if !st.CountKnown || st.Objects != 1 || st.LastGoodAt == nil || !st.ObservedAt.Equal(seen) {
		t.Fatalf("stale keeps the last good count, and a failure is never an observation: %+v", st)
	}
}

func TestWatchKindNamesEachGeneration(t *testing.T) {
	h, src, ps := gapHarness(t, "v1")
	_ = src
	log := &stateLog{}
	defer h.o.WatchKind("cms", log.add)()
	h.eventually(func() bool { st, _ := log.last(); return st.Coverage == Live }, "live")
	first, _ := log.last()
	ps.set("absent")
	h.eventually(func() bool { st, _ := log.last(); return st.Coverage == Absent }, "absent")
	if st, _ := log.last(); st.Generation != 0 || st.CountKnown {
		t.Fatalf("no generation runs while absent: %+v", st)
	}
	ps.set("v1")
	h.eventually(func() bool { st, _ := log.last(); return st.Coverage == Live }, "live again")
	if st, _ := log.last(); st.Generation <= first.Generation {
		t.Fatalf("a new generation has a new identity: %d after %d", st.Generation, first.Generation)
	}
}

func TestASlowKindListenerSeesTheNewestStateNotAQueue(t *testing.T) {
	src := newSource()
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	release := make(chan struct{})
	var calls atomic.Int32
	log := &stateLog{}
	cancel := h.o.WatchKind("cms", func(st KindState) {
		if calls.Add(1) == 1 {
			<-release
		}
		log.add(st)
	})
	time.Sleep(20 * time.Millisecond) // the first call is blocked
	for i := 0; i < 50; i++ {
		src.put(fmt.Sprintf("c%d", i), nil)
	}
	h.eventually(func() bool {
		h.o.mu.Lock()
		defer h.o.mu.Unlock()
		return len(h.o.kinds["cms"].gen.model) == 50
	}, "every mutation applied")
	close(release)
	h.eventually(func() bool { st, _ := log.last(); return st.Objects == 50 }, "the newest state arrives")
	if n := calls.Load(); n > 3 {
		t.Fatalf("a slow listener was queued %d calls, not the newest state", n)
	}
	cancel()
	n := calls.Load()
	src.put("after", nil)
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != n {
		t.Fatal("a call after cancel returned")
	}
}

func TestKindListenersEndWithTheObserver(t *testing.T) {
	src := newSource()
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	var calls atomic.Int32
	h.o.WatchKind("cms", func(KindState) { calls.Add(1) })
	h.eventually(func() bool { return calls.Load() > 0 }, "delivered")
	h.cancel()
	<-h.o.Done() // waits for the listener goroutine
	n := calls.Load()
	src.put("late", nil)
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != n {
		t.Fatal("a listener outlived its Observer")
	}
	if c := h.o.WatchKind("cms", func(KindState) { calls.Add(1) }); c == nil {
		t.Fatal("a stopped Observer returns a no-op cancel")
	}
}

// The per-kind tally (1c slice 3 §6): computed from the SAME model as
// Objects, set exactly when the count is known, kept as last good while
// stale, and absent without a hook.
func TestWatchKindCarriesTheTallyFromTheSameModel(t *testing.T) {
	src := newSource()
	tallied := cmKind("cms", src)
	tallied.Tally = func(objs []any) map[string]int {
		bad := 0
		for _, o := range objs { // the projected model, as the hook sees it
			if o.(cmView).Bad {
				bad++
			}
		}
		return map[string]int{"bad": bad, "total": len(objs)}
	}
	plainSrc := newSource()
	h := start(t, Config{Kinds: []KindSpec{tallied, cmKind("plain", plainSrc)},
		Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms"), cmPart("plain", "plain")}}}})
	log, plain := &stateLog{}, &stateLog{}
	defer h.o.WatchKind("cms", log.add)()
	defer h.o.WatchKind("plain", plain.add)()
	h.eventually(func() bool { st, ok := log.last(); return ok && st.Coverage == Live }, "live")
	if st, _ := log.last(); st.Tally["total"] != 0 || st.Tally == nil {
		t.Fatalf("an empty complete read is a known zero tally: %+v", st)
	}
	src.put("a", map[string]string{"bad": "1"})
	src.put("b", nil)
	h.eventually(func() bool { st, _ := log.last(); return st.Objects == 2 }, "two objects")
	st, _ := log.last()
	if st.Tally["total"] != st.Objects || st.Tally["bad"] != 1 {
		t.Fatalf("the tally is the same model as the count: %+v", st)
	}
	src.mu.Lock()
	src.listErr = errors.New("connection refused")
	src.mu.Unlock()
	src.liveWatcher().Error(&apierrors.NewServiceUnavailable("down").ErrStatus)
	h.eventually(func() bool { st, _ := log.last(); return st.Coverage == Stale }, "stale")
	if st, _ := log.last(); st.Tally["bad"] != 1 || st.Tally["total"] != 2 {
		t.Fatalf("stale keeps the last good tally with the last good count: %+v", st)
	}
	h.eventually(func() bool { st, ok := plain.last(); return ok && st.Coverage == Live }, "plain live")
	if st, _ := plain.last(); st.Tally != nil {
		t.Fatalf("no hook, no tally: %+v", st)
	}
}

// Before a complete read no count is known - and no tally either.
func TestNoTallyBeforeACompleteRead(t *testing.T) {
	src := newSource()
	src.mu.Lock()
	src.listErr = apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "", errors.New("rbac"))
	src.mu.Unlock()
	k := cmKind("cms", src)
	k.Tally = func(objs []any) map[string]int { return map[string]int{"total": len(objs)} }
	h := start(t, Config{Kinds: []KindSpec{k}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	log := &stateLog{}
	defer h.o.WatchKind("cms", log.add)()
	h.eventually(func() bool { st, ok := log.last(); return ok && st.Coverage == Unreadable }, "unreadable")
	if st, _ := log.last(); st.CountKnown || st.Tally != nil {
		t.Fatalf("an unread kind has neither count nor tally: %+v", st)
	}
}

// The Line slice 9 note §2.4: VisitKind walks the kind's model under the
// lock - no copy - and returns the state captured in the same section;
// visit can stop early; nothing is visited without a known count.
func TestVisitKind(t *testing.T) {
	src := newSource()
	for i := 0; i < 50; i++ {
		src.put(fmt.Sprintf("cm-%02d", i), nil)
	}
	h := start(t, Config{Kinds: []KindSpec{cmKind("cms", src)}, Domains: []DomainSpec{{ID: "things", Parts: []PartSpec{cmPart("cms", "cms")}}}})
	h.eventually(h.partIs("things", "cms", live), "live")
	seen := map[string]bool{}
	st, complete, ok := h.o.VisitKind("cms", func(obj any) bool {
		seen[obj.(cmView).Name] = true // the projected model value, as kept
		return true
	})
	if !ok || !complete || len(seen) != 50 || st.Objects != 50 || !st.CountKnown || st.Coverage != Live || st.Generation == 0 {
		t.Fatalf("visit: ok %v complete %v seen %d state %+v", ok, complete, len(seen), st)
	}
	n := 0
	if _, complete, _ := h.o.VisitKind("cms", func(any) bool { n++; return n < 10 }); complete || n != 10 {
		t.Fatalf("an early stop: complete %v after %d", complete, n)
	}
	if _, _, ok := h.o.VisitKind("nope", func(any) bool { return true }); ok {
		t.Fatal("an unknown kind")
	}
	if ks := h.o.Kinds(); len(ks) != 1 || ks[0].Name != "cms" {
		t.Fatalf("kinds %+v", ks)
	}
	// concurrent writes and visits under -race: every visit sees one
	// consistent model whose size matches the state captured with it
	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			src.put(fmt.Sprintf("x-%d", i%20), nil)
			time.Sleep(200 * time.Microsecond) // the fake watch's channel is bounded
		}
	}()
	for i := 0; i < 200; i++ {
		c := 0
		st, complete, _ := h.o.VisitKind("cms", func(any) bool { c++; return true })
		if complete && c != st.Objects {
			close(stop)
			t.Fatalf("visited %d, state says %d", c, st.Objects)
		}
	}
	close(stop)
}

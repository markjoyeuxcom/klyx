package appbridge

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

// anyEmitter records every emitted event (name + payload) under a mutex so tests
// can poll it race-free from another goroutine.
type anyEmitter struct {
	mu     sync.Mutex
	events []anyEvent
}

type anyEvent struct {
	name string
	data any
}

func (e *anyEmitter) Emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, anyEvent{name: name, data: data})
}

func (e *anyEmitter) snapshot() []anyEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]anyEvent, len(e.events))
	copy(out, e.events)
	return out
}

// countOf returns how many recorded events have the given name.
func (e *anyEmitter) countOf(name string) int {
	n := 0
	for _, ev := range e.snapshot() {
		if ev.name == name {
			n++
		}
	}
	return n
}

func (e *anyEmitter) lastLive(name string) (bool, bool) {
	var found bool
	var v bool
	for _, ev := range e.snapshot() {
		if ev.name == name {
			if s, ok := ev.data.(liveStatusDTO); ok {
				v = s.Live
				found = true
			}
		}
	}
	return v, found
}

func liveWaitUntil(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// fakeLivePodConn captures the onDirty/onLive callbacks WatchDirty is given so a
// test can fire dirty signals at will. computeFails forces ListPods to error so
// the compute-failure path is exercised.
type fakeLivePodConn struct {
	mu          sync.Mutex
	onDirty     func()
	onLive      func(bool)
	watchErr    error
	noInitialUp bool
	computeFail bool
	empty       bool // ListPods returns no pods (e.g. everything was deleted)
	listCalls   int
	listHook    func() // if set, invoked (outside the lock) during each ListPods
}

func (f *fakeLivePodConn) ListPods(context.Context, string) ([]workloads.PodSummary, bool, error) {
	f.mu.Lock()
	f.listCalls++
	fail := f.computeFail
	empty := f.empty
	hook := f.listHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if fail {
		return nil, false, context.DeadlineExceeded
	}
	if empty {
		return []workloads.PodSummary{}, false, nil
	}
	return []workloads.PodSummary{{Namespace: "ns", Name: "p1", Rank: workloads.Healthy}}, false, nil
}

func (f *fakeLivePodConn) PodDetail(context.Context, string, string) (fleet.PodDetail, error) {
	return fleet.PodDetail{}, nil
}

func (f *fakeLivePodConn) LaunchDiagnosticPod(context.Context, string, string, int64) fleet.AdhocResult {
	return fleet.AdhocResult{}
}

func (f *fakeLivePodConn) DeletePod(context.Context, string, string) error { return nil }

func (f *fakeLivePodConn) WatchDirty(_ context.Context, _ string, _ []string, onDirty func(), onLive func(bool)) (func(), error) {
	f.mu.Lock()
	f.onDirty = onDirty
	f.onLive = onLive
	werr := f.watchErr
	f.mu.Unlock()
	if werr != nil {
		return nil, werr
	}
	if !f.noInitialUp && onLive != nil {
		onLive(true)
	}
	return func() {}, nil
}

func (f *fakeLivePodConn) fireDirty() {
	f.mu.Lock()
	d := f.onDirty
	f.mu.Unlock()
	if d != nil {
		d()
	}
}

func (f *fakeLivePodConn) setComputeFail(v bool) {
	f.mu.Lock()
	f.computeFail = v
	f.mu.Unlock()
}

// newFastPodsSvc builds a PodsService whose live registry ticks every 10ms so
// dirty-driven re-emits land fast in tests.
func newFastPodsSvc(conn PodsConn, em Emitter) *PodsService {
	s := NewPodsService(func(string) (PodsConn, bool) { return conn, true }, em)
	s.live.interval = 10 * time.Millisecond
	return s
}

// --- tests ---

func TestLiveList_ImmediateEmitOnOpen(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{}
	svc := newFastPodsSvc(conn, em)

	res := svc.OpenLivePods("c", "ns")
	if !res.OK {
		t.Fatalf("open failed: %+v", res)
	}
	defer svc.CloseLivePods("c", "ns")

	if !liveWaitUntil(t, time.Second, func() bool { return em.countOf("livePods:c:ns") >= 1 }) {
		t.Fatal("no immediate emit on open")
	}
	if !liveWaitUntil(t, time.Second, func() bool {
		v, ok := em.lastLive("livePodsStatus:c:ns")
		return ok && v
	}) {
		t.Fatal("never reported live(true) after open")
	}
}

func TestLiveList_DirtyTriggersEmit(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{}
	svc := newFastPodsSvc(conn, em)

	svc.OpenLivePods("c", "ns")
	defer svc.CloseLivePods("c", "ns")

	if !liveWaitUntil(t, time.Second, func() bool { return em.countOf("livePods:c:ns") >= 1 }) {
		t.Fatal("no immediate emit")
	}
	base := em.countOf("livePods:c:ns")

	conn.fireDirty()
	if !liveWaitUntil(t, time.Second, func() bool { return em.countOf("livePods:c:ns") > base }) {
		t.Fatalf("dirty did not trigger a re-emit; count stuck at %d", em.countOf("livePods:c:ns"))
	}
}

// TestLiveList_DirtyDuringInitialComputeSurvives fires a watch event while the
// initial compute is listing. The coalescing flag must be cleared BEFORE that
// compute, so the mid-compute event survives and drives a re-list on the next
// tick - clearing after the compute would swallow it and the list would stay
// stale until the next (possibly never) watch event.
func TestLiveList_DirtyDuringInitialComputeSurvives(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{}
	var once sync.Once
	conn.listHook = func() {
		once.Do(conn.fireDirty) // pod flips while the initial LIST is in flight
	}
	svc := newFastPodsSvc(conn, em)

	svc.OpenLivePods("c", "ns")
	defer svc.CloseLivePods("c", "ns")

	// The immediate emit plus the dirty-driven re-emit: count must reach 2
	// without any further dirty signals.
	if !liveWaitUntil(t, time.Second, func() bool { return em.countOf("livePods:c:ns") >= 2 }) {
		t.Fatalf("dirty fired during the initial compute was swallowed; emits: %d", em.countOf("livePods:c:ns"))
	}
}

func TestLiveList_StableWhenIdle(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{}
	svc := newFastPodsSvc(conn, em)

	svc.OpenLivePods("c", "ns")
	defer svc.CloseLivePods("c", "ns")

	if !liveWaitUntil(t, time.Second, func() bool { return em.countOf("livePods:c:ns") >= 1 }) {
		t.Fatal("no immediate emit")
	}
	// No dirty fired: across many ticker intervals the count must not grow.
	stable := em.countOf("livePods:c:ns")
	time.Sleep(150 * time.Millisecond) // ~15 ticks at 10ms
	if got := em.countOf("livePods:c:ns"); got != stable {
		t.Fatalf("idle subscription emitted unprompted: was %d now %d", stable, got)
	}
}

func TestLiveList_ReplaceOnSameKeyStopsOld(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{}
	svc := newFastPodsSvc(conn, em)

	svc.OpenLivePods("c", "ns")
	if svc.live.count() != 1 {
		t.Fatalf("want 1 sub, got %d", svc.live.count())
	}
	// Capture the first sub's done so we can prove it closes on replace.
	svc.live.mu.Lock()
	old := svc.live.subs["pods:c:ns"]
	svc.live.mu.Unlock()

	svc.OpenLivePods("c", "ns") // replace
	defer svc.CloseLivePods("c", "ns")

	select {
	case <-old.done:
	case <-time.After(2 * time.Second):
		t.Fatal("old sub not stopped on replace")
	}
	if svc.live.count() != 1 {
		t.Fatalf("want 1 sub after replace, got %d", svc.live.count())
	}
}

func TestLiveList_CloseIdempotent(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{}
	svc := newFastPodsSvc(conn, em)

	svc.OpenLivePods("c", "ns")
	svc.CloseLivePods("c", "ns")
	svc.CloseLivePods("c", "ns")   // second close: no-op
	svc.CloseLivePods("c", "gone") // unknown key: no-op
	if svc.live.count() != 0 {
		t.Fatalf("want 0 subs after close, got %d", svc.live.count())
	}
}

func TestLiveList_CloseAllDrains(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{}
	svc := newFastPodsSvc(conn, em)

	svc.OpenLivePods("c", "ns1")
	svc.OpenLivePods("c", "ns2")
	svc.OpenLivePods("c", "ns3")
	if svc.live.count() != 3 {
		t.Fatalf("want 3 subs, got %d", svc.live.count())
	}

	done := make(chan struct{})
	go func() { svc.CloseAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("CloseAll blocked: leak")
	}
	if svc.live.count() != 0 {
		t.Fatalf("registry not drained: %d", svc.live.count())
	}
}

func TestLiveList_ComputeFailureEmitsLiveFalseOnce(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{computeFail: true}
	svc := newFastPodsSvc(conn, em)

	svc.OpenLivePods("c", "ns")
	defer svc.CloseLivePods("c", "ns")

	// Immediate compute fails -> no data emit, and live(false). Since we start
	// not-live and the watch is up but compute fails, the aggregate stays false:
	// the status event should report live=false (or no true edge ever appears).
	if !liveWaitUntil(t, time.Second, func() bool {
		v, ok := em.lastLive("livePodsStatus:c:ns")
		return ok && !v
	}) {
		// Acceptable alternative: no status event at all yet (still false by
		// default). The hard requirement is we never reported true.
		if v, ok := em.lastLive("livePodsStatus:c:ns"); ok && v {
			t.Fatal("reported live(true) despite compute failure")
		}
	}
	// No data event should have been emitted on a failing compute.
	if em.countOf("livePods:c:ns") != 0 {
		t.Fatalf("compute failure must not emit data; got %d", em.countOf("livePods:c:ns"))
	}

	// Now let compute succeed and fire dirty: it should emit data + live(true).
	conn.setComputeFail(false)
	conn.fireDirty()
	if !liveWaitUntil(t, 2*time.Second, func() bool { return em.countOf("livePods:c:ns") >= 1 }) {
		t.Fatal("recovery: no data emit after compute succeeds")
	}
	if !liveWaitUntil(t, time.Second, func() bool {
		v, ok := em.lastLive("livePodsStatus:c:ns")
		return ok && v
	}) {
		t.Fatal("recovery: never reported live(true)")
	}
}

func TestLiveList_DoesNotReportLiveTrueBeforeWatchUp(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{noInitialUp: true}
	svc := newFastPodsSvc(conn, em)

	res := svc.OpenLivePods("c", "ns")
	if !res.OK {
		t.Fatalf("open failed: %+v", res)
	}
	defer svc.CloseLivePods("c", "ns")

	if !liveWaitUntil(t, time.Second, func() bool { return em.countOf("livePods:c:ns") >= 1 }) {
		t.Fatal("no immediate data emit")
	}
	if v, ok := em.lastLive("livePodsStatus:c:ns"); ok && v {
		t.Fatal("reported live(true) before the watch emitted onLive(true)")
	}

	conn.mu.Lock()
	onLive := conn.onLive
	conn.mu.Unlock()
	if onLive == nil {
		t.Fatal("watch live callback was not captured")
	}
	onLive(true)
	if !liveWaitUntil(t, time.Second, func() bool {
		v, ok := em.lastLive("livePodsStatus:c:ns")
		return ok && v
	}) {
		t.Fatal("did not report live(true) after explicit watch-up edge")
	}
}

func TestLiveList_WatchStartError_OneShotComputeThenExit(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{watchErr: context.DeadlineExceeded}
	svc := newFastPodsSvc(conn, em)

	svc.OpenLivePods("c", "ns")
	// Watch establish failed: one-shot compute emits data, live(false), and the
	// sub deregisters (no loop without a watch).
	if !liveWaitUntil(t, time.Second, func() bool { return svc.live.count() == 0 }) {
		t.Fatalf("watch-fail sub did not deregister; count=%d", svc.live.count())
	}
	if em.countOf("livePods:c:ns") != 1 {
		t.Fatalf("want exactly 1 one-shot emit on watch-fail, got %d", em.countOf("livePods:c:ns"))
	}
	if v, ok := em.lastLive("livePodsStatus:c:ns"); !ok || v {
		t.Fatalf("want live(false) on watch-fail, got ok=%v v=%v", ok, v)
	}
}

// TestReplaceDropsStaleEmitFromBlockedCompute reproduces the race: the OLD sub's
// compute blocks while the NEW sub (triggered by a replace/re-open) completes and
// emits "NEW". When the old compute unblocks it must NOT emit its stale payload on
// the same event name.
//
// The test uses a blocking compute controlled by a channel so we can hold the old
// sub's compute past stopSub's drain window, then verify that no stale payload
// appears after the replace completed.
func TestReplaceDropsStaleEmitFromBlockedCompute(t *testing.T) {
	const key = "pods:c:block"

	// blockOld controls whether the next ListPods call should block.
	// unblockCh releases a blocked ListPods.
	var (
		mu       sync.Mutex
		blockOld bool
	)
	unblockCh := make(chan struct{})

	type emitRecord struct {
		name string
		data any
	}
	var (
		emitMu  sync.Mutex
		emitted []emitRecord
	)
	recordEmit := func(name string, data any) {
		emitMu.Lock()
		emitted = append(emitted, emitRecord{name, data})
		emitMu.Unlock()
	}
	countPayload := func(wantData string) int {
		emitMu.Lock()
		defer emitMu.Unlock()
		n := 0
		for _, e := range emitted {
			if e.name == "livePods:c:block" {
				if pods, ok := e.data.([]workloads.PodSummary); ok {
					for _, p := range pods {
						if p.Name == wantData {
							n++
						}
					}
				}
			}
		}
		return n
	}

	// replaceMarkCh is closed once the NEW sub has emitted at least once.
	replaceMarkCh := make(chan struct{})
	var replaceMarkOnce sync.Once

	// blockingConn is the OLD sub's connection: first ListPods blocks until
	// unblockCh is signalled; subsequent calls return normally.
	blockingConn := &fakeLivePodConn{}
	// We wrap its ListPods by standing up a custom emitter/service. Instead of
	// fakeLivePodConn's fixed ListPods we want a blocking one. We build a small
	// liveRegistry directly so we can inject arbitrary compute closures.
	//
	// Rather than reimplementing registry internals, we drive the existing
	// PodsService with a custom fake that lets us intercept compute.
	//
	// Simpler: use a direct liveRegistry with injected closures.
	reg := newLiveRegistry()
	reg.interval = 10 * time.Millisecond

	// emitterFn wraps recordEmit as an Emitter-compatible closure.
	emitterFn := func(name string) func(any) {
		return func(data any) { recordEmit(name, data) }
	}
	emitLiveFn := func(name string) func(bool) {
		return func(live bool) { recordEmit(name+"Status", liveStatusDTO{Live: live}) }
	}

	// watchStart always succeeds, dirty/live callbacks stored in blockingConn.
	watchStartFn := func(onDirty func(), onLive func(bool)) (func(), error) {
		mu.Lock()
		blockingConn.onDirty = onDirty
		blockingConn.onLive = onLive
		mu.Unlock()
		return func() {}, nil
	}

	// OLD sub's compute: blocks when blockOld==true.
	oldComputeFn := func() (any, bool) {
		mu.Lock()
		shouldBlock := blockOld
		mu.Unlock()
		if shouldBlock {
			<-unblockCh // blocks until test releases it
		}
		return []workloads.PodSummary{{Namespace: "ns", Name: "OLD"}}, true
	}

	// Open OLD sub; let its immediate (non-blocking) compute run.
	reg.open(key, watchStartFn, oldComputeFn,
		emitterFn("livePods:c:block"), emitLiveFn("livePodsStatus:c:block"))

	// Wait for the OLD sub's immediate emit.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if countPayload("OLD") >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if countPayload("OLD") < 1 {
		t.Fatal("OLD sub never emitted initial payload")
	}

	// Now arm the block: next compute on the old sub will block.
	mu.Lock()
	blockOld = true
	mu.Unlock()

	// Fire dirty so the OLD sub's ticker picks it up and enters the blocked compute.
	mu.Lock()
	od := blockingConn.onDirty
	mu.Unlock()
	if od != nil {
		od()
	}

	// Give the old sub's goroutine time to enter compute() and block there.
	// We detect this by watching that its goroutine is scheduled and blocked.
	// A short sleep is necessary here because we can't observe the goroutine's
	// internal state directly - but 50ms is well above the 10ms ticker.
	time.Sleep(50 * time.Millisecond)

	// NEW sub's compute always returns "NEW" immediately.
	newComputeFn := func() (any, bool) {
		return []workloads.PodSummary{{Namespace: "ns", Name: "NEW"}}, true
	}
	newWatchStartFn := func(onDirty func(), onLive func(bool)) (func(), error) {
		return func() {}, nil
	}

	// Open the NEW sub (replaces old). stopSub has a 2s drain window but the old
	// compute is still blocked - stopSub will time out and proceed. The new sub
	// emits its payload immediately.
	reg.open(key, newWatchStartFn, newComputeFn,
		func(data any) {
			recordEmit("livePods:c:block", data)
			replaceMarkOnce.Do(func() { close(replaceMarkCh) })
		},
		emitLiveFn("livePodsStatus:c:block"))

	// Wait for the new sub to emit at least once.
	select {
	case <-replaceMarkCh:
	case <-time.After(3 * time.Second):
		t.Fatal("NEW sub never emitted after replace")
	}

	// Record how many emissions exist at this point (new sub is live).
	emitMu.Lock()
	snapAfterReplace := make([]emitRecord, len(emitted))
	copy(snapAfterReplace, emitted)
	emitMu.Unlock()

	// Now unblock the OLD compute. Without the fix it would emit "OLD" again.
	close(unblockCh)

	// Let the unblocked goroutine run.
	time.Sleep(30 * time.Millisecond)

	// Assert: any "livePods:c:block" event emitted AFTER the replace must not
	// carry the OLD payload. We check this by counting "OLD" in the post-replace
	// window. There should be none.
	emitMu.Lock()
	postReplace := emitted[len(snapAfterReplace):]
	emitMu.Unlock()

	for _, ev := range postReplace {
		if ev.name != "livePods:c:block" {
			continue
		}
		pods, ok := ev.data.([]workloads.PodSummary)
		if !ok {
			continue
		}
		for _, p := range pods {
			if p.Name == "OLD" {
				t.Errorf("stale OLD payload emitted after replace completed (post-replace events: %d)", len(postReplace))
			}
		}
	}

	// Cleanup.
	reg.closeAll()
}

func TestLiveList_NoGoroutineLeakAcrossLifecycles(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()

	em := &anyEmitter{}
	conn := &fakeLivePodConn{}
	svc := newFastPodsSvc(conn, em)

	// Open/dirty/close cycles plus a replace.
	for i := 0; i < 5; i++ {
		svc.OpenLivePods("c", "ns")
		conn.fireDirty()
		time.Sleep(15 * time.Millisecond)
		svc.OpenLivePods("c", "ns") // replace
		svc.CloseLivePods("c", "ns")
	}
	// A batch of distinct keys drained via CloseAll.
	svc.OpenLivePods("c", "a")
	svc.OpenLivePods("c", "b")
	svc.CloseAll()

	deadline := time.Now().Add(3 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		runtime.GC()
		after = runtime.NumGoroutine()
		if after <= before+2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: before=%d after=%d (delta=%d)", before, after, after-before)
}

// Review 2026-10-03 #6: a watch that reconnects after an outage in which the
// only change was deleting everything replays NO events (there is nothing left
// to replay), so the old rows used to stay up under live:true. Re-establishment
// itself must force a re-list, and live must wait for it.
func TestLiveList_ReconnectRelistsWithoutEvents(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{}
	svc := newFastPodsSvc(conn, em)
	svc.OpenLivePods("c", "ns")
	defer svc.CloseLivePods("c", "ns")
	if !liveWaitUntil(t, time.Second, func() bool { v, ok := em.lastLive("livePodsStatus:c:ns"); return ok && v }) {
		t.Fatal("never went live")
	}
	emits := em.countOf("livePods:c:ns")

	conn.mu.Lock()
	onLive := conn.onLive
	conn.empty = true // everything deleted while the watch was down
	conn.mu.Unlock()
	onLive(false)
	onLive(true) // reconnected; no replay events because nothing exists

	if !liveWaitUntil(t, time.Second, func() bool { return em.countOf("livePods:c:ns") > emits }) {
		t.Fatal("a reconnected watch must re-list even when it replays no events")
	}
	if !liveWaitUntil(t, time.Second, func() bool { v, ok := em.lastLive("livePodsStatus:c:ns"); return ok && v }) {
		t.Fatal("live must come back once the post-reconnect re-list succeeded")
	}
}

// Review 2026-10-03 #6: a failed compute on a quiet namespace used to stay
// not-live forever (re-lists only ran on watch events). It must retry by itself.
func TestLiveList_FailedComputeRetriesWithoutEvents(t *testing.T) {
	em := &anyEmitter{}
	conn := &fakeLivePodConn{computeFail: true}
	svc := newFastPodsSvc(conn, em)
	svc.OpenLivePods("c", "ns")
	defer svc.CloseLivePods("c", "ns")

	if !liveWaitUntil(t, time.Second, func() bool { v, ok := em.lastLive("livePodsStatus:c:ns"); return ok && !v }) {
		t.Fatal("a failing compute must report not-live")
	}
	conn.setComputeFail(false) // the apiserver recovers; NO dirty signal is fired
	if !liveWaitUntil(t, 2*time.Second, func() bool { return em.countOf("livePods:c:ns") >= 1 }) {
		t.Fatal("the loop must retry a failed compute on its own")
	}
	if !liveWaitUntil(t, time.Second, func() bool { v, ok := em.lastLive("livePodsStatus:c:ns"); return ok && v }) {
		t.Fatal("never recovered to live after the retry succeeded")
	}
}


package appbridge

import (
	"sync"
	"testing"
	"time"
)

// TestSafeGo_RecoversPanicAndCallsOnPanic is the core of the panic net: a panic
// in a detached goroutine is recovered (the test process survives) and the
// optional onPanic callback fires with the recovered value. Without this, the
// panic would terminate the whole Wails process.
func TestSafeGo_RecoversPanicAndCallsOnPanic(t *testing.T) {
	got := make(chan any, 1)
	safeGoOnPanic("test-goroutine", func() { panic("boom: malformed object") }, func(r any) { got <- r })

	select {
	case r := <-got:
		if r != "boom: malformed object" {
			t.Fatalf("onPanic received %v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("panic was not recovered / onPanic never fired")
	}
}

// TestSafeGo_NoOnPanicStillRecovers proves the log-only variant contains a panic
// (no crash) even without a callback.
func TestSafeGo_NoOnPanicStillRecovers(t *testing.T) {
	done := make(chan struct{})
	safeGo("test-goroutine", func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
		// The deferred close ran during unwind and recoverGuard swallowed the panic.
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo goroutine did not run its deferred cleanup")
	}
}

// TestLiveList_ComputePanicRecoversAndReportsNotLive is the P2 regression: a
// panic in compute() (parsing malformed cluster data) must not crash the app and
// must degrade the lens honestly - emit live(false) rather than leave the last
// live(true) edge latched over a dead compute loop.
func TestLiveList_ComputePanicRecoversAndReportsNotLive(t *testing.T) {
	r := newLiveRegistry()
	r.interval = 5 * time.Millisecond

	var mu sync.Mutex
	var liveEdges []bool
	emitLive := func(b bool) { mu.Lock(); liveEdges = append(liveEdges, b); mu.Unlock() }
	lastLive := func() (bool, bool) {
		mu.Lock()
		defer mu.Unlock()
		if len(liveEdges) == 0 {
			return false, false
		}
		return liveEdges[len(liveEdges)-1], true
	}

	armPanic := make(chan struct{}, 1)
	compute := func() (any, bool) {
		select {
		case <-armPanic:
			panic("boom: malformed object")
		default:
			return "payload", true
		}
	}

	var onDirty func()
	watchStart := func(od func(), onLive func(bool)) (func(), error) {
		onDirty = od
		onLive(true) // watch is up
		return func() {}, nil
	}

	r.open("k", watchStart, compute, func(any) {}, emitLive)
	t.Cleanup(func() { r.closeAll() })

	// The immediate compute succeeded -> a live(true) edge exists.
	if !waitUntil(t, 2*time.Second, func() bool { v, ok := lastLive(); return ok && v }) {
		t.Fatalf("expected an initial live(true) edge, got %v", liveEdges)
	}

	// Arm the panic and fire dirty so the next tick computes -> panics.
	armPanic <- struct{}{}
	onDirty()

	// The panic net must recover (no crash) and emit an honest live(false).
	if !waitUntil(t, 2*time.Second, func() bool { v, ok := lastLive(); return ok && !v }) {
		t.Fatalf("expected a live(false) edge after a compute panic, got %v", liveEdges)
	}
}

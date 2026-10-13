package appbridge

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// gateConn blocks every log open until released or its context ends, and
// reports each open's context as it starts.
type gateConn struct {
	entered chan context.Context
	release chan struct{}
	pods    []string
	podsCtx chan context.Context // WorkloadPods blocks here when set
}

func newGateConn() *gateConn {
	return &gateConn{entered: make(chan context.Context, 32), release: make(chan struct{})}
}

func (c *gateConn) PodLogStream(ctx context.Context, _, _, _ string, _ bool, _ int64) (io.ReadCloser, error) {
	c.entered <- ctx
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return io.NopCloser(strings.NewReader("")), nil
	}
}

func (c *gateConn) WorkloadPods(ctx context.Context, _, _, _ string) ([]string, error) {
	if c.podsCtx != nil {
		c.podsCtx <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.pods, nil
}

func openAsync(fn func() OpenLogStreamResultDTO) <-chan OpenLogStreamResultDTO {
	out := make(chan OpenLogStreamResultDTO, 1)
	go func() { out <- fn() }()
	return out
}

func waitCtx(t *testing.T, ch <-chan context.Context) context.Context {
	t.Helper()
	select {
	case ctx := <-ch:
		return ctx
	case <-time.After(2 * time.Second):
		t.Fatal("the open never started")
	}
	return nil
}

func waitResult(t *testing.T, ch <-chan OpenLogStreamResultDTO) OpenLogStreamResultDTO {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("the open never returned")
	}
	return OpenLogStreamResultDTO{}
}

// Codex review 2026-10-13 #4: CloseLogStream cancels an open still in
// flight, and the open reports it was closed - no stream survives.
func TestCloseLogStreamCancelsAPendingOpen(t *testing.T) {
	c := newGateConn()
	s := NewLogsService(lookupOf(c), &captureEmitter{})
	res := openAsync(func() OpenLogStreamResultDTO { return s.OpenLogStream("c", "ns", "pod", "ct", false, 1, "pending") })
	ctx := waitCtx(t, c.entered)
	s.CloseLogStream("pending")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("CloseLogStream did not cancel the pending open")
	}
	if r := waitResult(t, res); r.Error == "" || !strings.Contains(r.Error, "closed before it opened") {
		t.Fatalf("a cancelled open must say so: %+v", r)
	}
	s.mu.Lock()
	n := len(s.streams) + len(s.draining) + len(s.order)
	s.mu.Unlock()
	if n != 0 {
		t.Fatal("a cancelled open must leave nothing registered")
	}
}

// A pending open counts against the cap: opening past it evicts (and
// cancels) the oldest, pending ones included.
func TestPendingOpensCountAgainstTheCap(t *testing.T) {
	c := newGateConn()
	s := NewLogsService(lookupOf(c), &captureEmitter{})
	results := make([]<-chan OpenLogStreamResultDTO, 0, maxConcurrentLogStreams+1)
	ctxs := make([]context.Context, 0, maxConcurrentLogStreams+1)
	for i := 0; i <= maxConcurrentLogStreams; i++ {
		id := fmt.Sprintf("s%d", i)
		results = append(results, openAsync(func() OpenLogStreamResultDTO { return s.OpenLogStream("c", "ns", "pod", "ct", false, 1, id) }))
		ctxs = append(ctxs, waitCtx(t, c.entered))
	}
	select {
	case <-ctxs[0].Done():
	case <-time.After(time.Second):
		t.Fatal("the oldest pending open must be evicted at the cap")
	}
	if r := waitResult(t, results[0]); r.Error == "" {
		t.Fatalf("the evicted open must fail: %+v", r)
	}
	s.mu.Lock()
	live := len(s.streams)
	s.mu.Unlock()
	if live != maxConcurrentLogStreams {
		t.Fatalf("want %d reserved streams, got %d", maxConcurrentLogStreams, live)
	}
	close(c.release)
	for _, r := range results[1:] {
		if got := waitResult(t, r); got.Error != "" {
			t.Fatalf("the others open: %+v", got)
		}
	}
	s.CloseAll()
}

// Establishing a stream is bounded: a stalled open times out and says so.
func TestLogOpenTimesOut(t *testing.T) {
	old := logOpenTimeout
	logOpenTimeout = 50 * time.Millisecond
	defer func() { logOpenTimeout = old }()
	c := newGateConn()
	s := NewLogsService(lookupOf(c), &captureEmitter{})
	r := s.OpenLogStream("c", "ns", "pod", "ct", false, 1, "slow")
	if !strings.Contains(r.Error, "timed out opening") {
		t.Fatalf("a stalled open must time out: %+v", r)
	}
	// The id is free again.
	s.mu.Lock()
	_, held := s.streams["slow"]
	s.mu.Unlock()
	if held {
		t.Fatal("a timed-out open must release its id")
	}
}

// The aggregate reserves too: closing it while it lists the workload's pods
// cancels the listing.
func TestCloseCancelsAPendingWorkloadOpen(t *testing.T) {
	c := newGateConn()
	c.podsCtx = make(chan context.Context, 1)
	s := NewLogsService(lookupOf(c), &captureEmitter{})
	res := openAsync(func() OpenLogStreamResultDTO {
		return s.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "", 100, "agg")
	})
	ctx := waitCtx(t, c.podsCtx)
	s.CloseLogStream("agg")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("CloseLogStream did not cancel the pending workload open")
	}
	if r := waitResult(t, res); !strings.Contains(r.Error, "closed before it opened") {
		t.Fatalf("a cancelled aggregate open must say so: %+v", r)
	}
}

// Codex review of cd291bd: once stop reports no timeout, the deadline can
// never cancel the stream afterwards - Stop's own answer decides, with no
// window where the callback is scheduled but has not yet run.
func TestOpenDeadlineNeverCancelsAfterReportingNoTimeout(t *testing.T) {
	old := logOpenTimeout
	logOpenTimeout = time.Nanosecond
	defer func() { logOpenTimeout = old }()
	type trial struct{ cancelled *atomic.Bool }
	var passed []trial
	for i := 0; i < 20000; i++ {
		c := &atomic.Bool{}
		stop := openDeadline(&logStream{cancel: func() { c.Store(true) }})
		if !stop() {
			passed = append(passed, trial{c})
		}
	}
	time.Sleep(20 * time.Millisecond)
	for i, tr := range passed {
		if tr.cancelled.Load() {
			t.Fatalf("trial %d: cancelled after stop reported no timeout", i)
		}
	}
}

package appbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// aggConn is a configurable fake for aggregate tests. WorkloadPods returns a
// fixed sorted pod list. PodLogStream hands back a per-pod ReadCloser built by
// the streamFor closure, letting each test wire pipes, finite readers, or
// open-failures per pod. perPodTail records the tailLines each pod was opened
// with for the split assertion.
type aggConn struct {
	pods      []string
	streamFor func(pod string, tail int64) (io.ReadCloser, error)

	mu          sync.Mutex
	openedTails map[string]int64
	openedPods  []string
}

func (c *aggConn) WorkloadPods(_ context.Context, _, _, _ string) ([]string, error) {
	out := make([]string, len(c.pods))
	copy(out, c.pods)
	sort.Strings(out)
	return out, nil
}

func (c *aggConn) PodLogStream(_ context.Context, _, pod, _ string, _ bool, tail int64) (io.ReadCloser, error) {
	c.mu.Lock()
	if c.openedTails == nil {
		c.openedTails = map[string]int64{}
	}
	c.openedTails[pod] = tail
	c.openedPods = append(c.openedPods, pod)
	c.mu.Unlock()
	return c.streamFor(pod, tail)
}

// aggLookup returns a lookup yielding the same aggConn for any cluster name.
func aggLookup(c *aggConn) func(string) (LogsConn, bool) {
	return func(string) (LogsConn, bool) { return c, true }
}

// --- tests ---

func TestAggregate_TwoPodsInterleaveWithPrefixes(t *testing.T) {
	em := &captureEmitter{}
	conn := &aggConn{
		pods: []string{"web-aaa", "web-bbb"},
		streamFor: func(pod string, _ int64) (io.ReadCloser, error) {
			switch pod {
			case "web-aaa":
				return io.NopCloser(strings.NewReader("a1\na2\n")), nil
			default:
				return io.NopCloser(strings.NewReader("b1\nb2\n")), nil
			}
		},
	}
	svc := NewLogsService(aggLookup(conn), em)

	id := tid()
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, id)
	if res.StreamID == "" || res.Error != "" {
		t.Fatalf("open failed: %+v", res)
	}
	if res.StreamID != id {
		t.Fatalf("the aggregate must stream on the frontend-chosen id %q, got %q", id, res.StreamID)
	}

	if !waitUntil(t, 2*time.Second, em.sawEOF) {
		t.Fatal("never saw EOF chunk")
	}
	lines := em.allLines()
	// All four prefixed log lines must appear (order across pods is nondeterministic).
	want := map[string]bool{"aaa › a1": false, "aaa › a2": false, "bbb › b1": false, "bbb › b2": false}
	for _, l := range lines {
		if _, ok := want[l]; ok {
			want[l] = true
		}
	}
	for l, seen := range want {
		if !seen {
			t.Fatalf("missing prefixed line %q in %v", l, lines)
		}
	}
	// Single registry entry for the aggregate; drains after EOF.
	if !waitUntil(t, 2*time.Second, func() bool { return svc.streamCount() == 0 }) {
		t.Fatalf("registry not drained: %d", svc.streamCount())
	}
}

func TestAggregate_PerPodTailSplit(t *testing.T) {
	em := &captureEmitter{}
	conn := &aggConn{
		pods: []string{"web-1", "web-2", "web-3", "web-4"},
		streamFor: func(string, int64) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		},
	}
	svc := NewLogsService(aggLookup(conn), em)
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 400, tid())
	if res.StreamID == "" {
		t.Fatalf("open failed: %+v", res)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	// 400 / 4 = 100 per pod.
	for pod, tail := range conn.openedTails {
		if tail != 100 {
			t.Fatalf("pod %s opened with tail %d, want 100", pod, tail)
		}
	}
}

func TestAggregate_PerPodTailFloor(t *testing.T) {
	em := &captureEmitter{}
	conn := &aggConn{
		pods: []string{"web-1", "web-2", "web-3", "web-4", "web-5"},
		streamFor: func(string, int64) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		},
	}
	svc := NewLogsService(aggLookup(conn), em)
	// 100 / 5 = 20, below the 50 floor -> each pod gets 50.
	svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for pod, tail := range conn.openedTails {
		if tail != 50 {
			t.Fatalf("pod %s opened with tail %d, want floor 50", pod, tail)
		}
	}
}

func TestPodShort_PrefixStripAndFallback(t *testing.T) {
	cases := []struct {
		workload, pod, want string
	}{
		{"web", "web-7d4b9c6f9-x2x9k", "7d4b9c6f9-x2x9k"},
		{"web", "web", "web"},             // not longer than prefix -> full
		{"web", "other-pod", "other-pod"}, // no prefix -> full
		{"web", "webhook-1", "webhook-1"}, // shares letters but not "web-" prefix
		{"db", "db-0", "0"},               // statefulset ordinal
	}
	for _, c := range cases {
		if got := podShort(c.workload, c.pod); got != c.want {
			t.Fatalf("podShort(%q,%q)=%q want %q", c.workload, c.pod, got, c.want)
		}
	}
}

func TestAggregate_ZeroPods(t *testing.T) {
	em := &captureEmitter{}
	conn := &aggConn{pods: nil, streamFor: func(string, int64) (io.ReadCloser, error) { return nil, nil }}
	svc := NewLogsService(aggLookup(conn), em)
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	if res.Error == "" || res.StreamID != "" {
		t.Fatalf("want error + no stream for zero pods, got %+v", res)
	}
	if !strings.Contains(res.Error, "no pods") {
		t.Fatalf("error should mention no pods: %q", res.Error)
	}
}

// One pod's pipe closes early (pod deleted mid-tail): that reader injects a
// "stream ended" marker and the aggregate continues; closing the other pod's
// pipe then drives the natural EOF.
func TestAggregate_OnePodEndsEarly_AggregateContinues(t *testing.T) {
	em := &captureEmitter{}
	pr1, pw1 := io.Pipe()
	pr2, pw2 := io.Pipe()
	conn := &aggConn{
		pods: []string{"web-aaa", "web-bbb"},
		streamFor: func(pod string, _ int64) (io.ReadCloser, error) {
			if pod == "web-aaa" {
				return pr1, nil
			}
			return pr2, nil
		},
	}
	svc := NewLogsService(aggLookup(conn), em)
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	if res.StreamID == "" {
		t.Fatalf("open failed: %+v", res)
	}

	// Pod aaa emits a line then its stream ends (EOF, clean - pod deleted).
	go func() {
		_, _ = pw1.Write([]byte("a1\n"))
		_ = pw1.Close() // clean EOF for aaa
	}()

	// The aggregate must NOT EOF yet: aaa's "stream ended" marker appears but bbb
	// is still live.
	endedMarker := func() bool {
		for _, l := range em.allLines() {
			if l == "aaa › … stream ended" {
				return true
			}
		}
		return false
	}
	if !waitUntil(t, 2*time.Second, endedMarker) {
		t.Fatal("never saw aaa stream-ended marker")
	}
	if em.sawEOF() {
		t.Fatal("aggregate EOF must not fire while bbb is still live")
	}
	if svc.streamCount() != 1 {
		t.Fatalf("aggregate should still be registered, count=%d", svc.streamCount())
	}

	// Now end bbb -> all readers done -> natural EOF.
	go func() {
		_, _ = pw2.Write([]byte("b1\n"))
		_ = pw2.Close()
	}()
	if !waitUntil(t, 2*time.Second, em.sawEOF) {
		t.Fatal("never saw aggregate EOF after all pods ended")
	}
	if !waitUntil(t, 2*time.Second, func() bool { return svc.streamCount() == 0 }) {
		t.Fatalf("registry not drained: %d", svc.streamCount())
	}
	// Clean EOF: no failed readers.
	for _, ev := range em.snapshot() {
		if ev.chunk.EOF && ev.chunk.Error != "" {
			t.Fatalf("clean end must not carry error: %q", ev.chunk.Error)
		}
	}
}

// Close mid-stream while both pods' pipes block forever: all goroutines must
// exit within the close timeout (leak guard).
func TestAggregate_CloseUnblocksBlockedPods(t *testing.T) {
	em := &captureEmitter{}
	pr1, pw1 := io.Pipe()
	pr2, pw2 := io.Pipe()
	defer pw1.Close()
	defer pw2.Close()
	conn := &aggConn{
		pods: []string{"web-aaa", "web-bbb"},
		streamFor: func(pod string, _ int64) (io.ReadCloser, error) {
			if pod == "web-aaa" {
				return pr1, nil
			}
			return pr2, nil
		},
	}
	svc := NewLogsService(aggLookup(conn), em)
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	if res.StreamID == "" {
		t.Fatalf("open failed: %+v", res)
	}
	if svc.streamCount() != 1 {
		t.Fatalf("want 1 aggregate, got %d", svc.streamCount())
	}

	done := make(chan struct{})
	go func() { svc.CloseLogStream(res.StreamID); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseLogStream blocked > 2s: goroutine leak")
	}
	if !waitUntil(t, 2*time.Second, func() bool { return svc.streamCount() == 0 }) {
		t.Fatalf("registry not drained after close: %d", svc.streamCount())
	}
}

func TestAggregate_CapAtTenPodsWithMarker(t *testing.T) {
	em := &captureEmitter{}
	pods := make([]string, 12)
	for i := 0; i < 12; i++ {
		pods[i] = fmt.Sprintf("web-%02d", i)
	}
	conn := &aggConn{
		pods: pods,
		streamFor: func(string, int64) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("")), nil
		},
	}
	svc := NewLogsService(aggLookup(conn), em)
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 1000, tid())
	if res.StreamID == "" {
		t.Fatalf("open failed: %+v", res)
	}
	if !waitUntil(t, 2*time.Second, em.sawEOF) {
		t.Fatal("never saw EOF")
	}
	// Exactly 10 pods opened.
	conn.mu.Lock()
	opened := len(conn.openedPods)
	conn.mu.Unlock()
	if opened != maxAggregatePods {
		t.Fatalf("want %d pods opened, got %d", maxAggregatePods, opened)
	}
	// Marker line announces truncation.
	found := false
	for _, l := range em.allLines() {
		if l == fmt.Sprintf("… showing %d of %d pods", maxAggregatePods, 12) {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing truncation marker in %v", em.allLines())
	}
}

func TestAggregate_OnePodFailsToOpen_MarkerAndContinue(t *testing.T) {
	em := &captureEmitter{}
	conn := &aggConn{
		pods: []string{"web-aaa", "web-bbb"},
		streamFor: func(pod string, _ int64) (io.ReadCloser, error) {
			if pod == "web-aaa" {
				return nil, errors.New("forbidden")
			}
			return io.NopCloser(strings.NewReader("b1\n")), nil
		},
	}
	svc := NewLogsService(aggLookup(conn), em)
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	if res.StreamID == "" || res.Error != "" {
		t.Fatalf("want stream despite one open-failure: %+v", res)
	}
	if !waitUntil(t, 2*time.Second, em.sawEOF) {
		t.Fatal("never saw EOF")
	}
	lines := em.allLines()
	var sawFailMarker, sawBLine bool
	for _, l := range lines {
		if strings.Contains(l, "aaa › … failed to open") {
			sawFailMarker = true
		}
		if l == "bbb › b1" {
			sawBLine = true
		}
	}
	if !sawFailMarker {
		t.Fatalf("missing open-failure marker in %v", lines)
	}
	if !sawBLine {
		t.Fatalf("surviving pod's line missing in %v", lines)
	}
}

func TestAggregate_AllPodsFailToOpen_Error(t *testing.T) {
	em := &captureEmitter{}
	conn := &aggConn{
		pods: []string{"web-aaa", "web-bbb"},
		streamFor: func(string, int64) (io.ReadCloser, error) {
			return nil, errors.New("forbidden")
		},
	}
	svc := NewLogsService(aggLookup(conn), em)
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	if res.Error == "" || res.StreamID != "" {
		t.Fatalf("want error + no stream when all pods fail, got %+v", res)
	}
	if svc.streamCount() != 0 {
		t.Fatalf("no stream should be registered, got %d", svc.streamCount())
	}
}

// A reader that yields one line then a non-EOF error must surface in the
// aggregate's terminal chunk as "N of M pod streams failed".
func TestAggregate_FailedReaderCountInEOF(t *testing.T) {
	em := &captureEmitter{}
	conn := &aggConn{
		pods: []string{"web-aaa", "web-bbb"},
		streamFor: func(pod string, _ int64) (io.ReadCloser, error) {
			if pod == "web-aaa" {
				return &errReader{}, nil // one line then "boom"
			}
			return io.NopCloser(strings.NewReader("b1\n")), nil
		},
	}
	svc := NewLogsService(aggLookup(conn), em)
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	if res.StreamID == "" {
		t.Fatalf("open failed: %+v", res)
	}
	if !waitUntil(t, 2*time.Second, em.sawEOF) {
		t.Fatal("never saw EOF")
	}
	var eofErr string
	for _, ev := range em.snapshot() {
		if ev.chunk.EOF {
			eofErr = ev.chunk.Error
		}
	}
	if !strings.Contains(eofErr, "1 of 2 pod streams failed") {
		t.Fatalf("EOF error should report failed count, got %q", eofErr)
	}
}

func TestAggregate_CountsAsOneStream(t *testing.T) {
	em := &captureEmitter{}
	pr1, pw1 := io.Pipe()
	pr2, pw2 := io.Pipe()
	defer pw1.Close()
	defer pw2.Close()
	conn := &aggConn{
		pods: []string{"web-aaa", "web-bbb"},
		streamFor: func(pod string, _ int64) (io.ReadCloser, error) {
			if pod == "web-aaa" {
				return pr1, nil
			}
			return pr2, nil
		},
	}
	svc := NewLogsService(aggLookup(conn), em)
	res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	if res.StreamID == "" {
		t.Fatalf("open failed: %+v", res)
	}
	// Two apiserver streams open, but ONE registry entry.
	if got := svc.streamCount(); got != 1 {
		t.Fatalf("aggregate must count as 1 stream, got %d", got)
	}
	svc.CloseAll()
}

func TestAggregate_NoGoroutineLeak(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()

	em := &captureEmitter{}
	var mu sync.Mutex
	var pipes []*io.PipeWriter

	// Blocking-pipe aggregates cancelled via CloseAll.
	blockSvc := NewLogsService(func(string) (LogsConn, bool) {
		return &aggConn{
			pods: []string{"web-aaa", "web-bbb", "web-ccc"},
			streamFor: func(string, int64) (io.ReadCloser, error) {
				pr, pw := io.Pipe()
				mu.Lock()
				pipes = append(pipes, pw)
				mu.Unlock()
				return pr, nil
			},
		}, true
	}, em)
	for i := 0; i < 3; i++ {
		blockSvc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	}

	// Clean-EOF aggregate drains on its own.
	cleanSvc := NewLogsService(func(string) (LogsConn, bool) {
		return &aggConn{
			pods: []string{"web-aaa", "web-bbb"},
			streamFor: func(string, int64) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("x\ny\n")), nil
			},
		}, true
	}, em)
	for i := 0; i < 3; i++ {
		cleanSvc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	}
	waitUntil(t, 3*time.Second, func() bool { return cleanSvc.streamCount() == 0 })

	blockSvc.CloseAll()
	mu.Lock()
	for _, pw := range pipes {
		pw.Close()
	}
	mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		runtime.GC()
		after = runtime.NumGoroutine()
		if after <= before+3 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: before=%d after=%d (delta=%d)", before, after, after-before)
}

// TestAggregate_EvictedByOpenLogStream_DrainsAndClosesDone is the regression for
// the P1 log-stream leak: when a single-pod OpenLogStream evicts an aggregate as
// the oldest stream, the aggregate is moved to s.draining. Its batcher's
// deregister() must fall back to draining (like finish()) so the entry is removed
// and its done channel closed. Before the fix, deregister() only looked in
// s.streams, so an evicted aggregate leaked its draining entry for the process
// lifetime and CloseAll blocked the full closeWaitTimeout on its never-closing
// done channel.
func TestAggregate_EvictedByOpenLogStream_DrainsAndClosesDone(t *testing.T) {
	em := &captureEmitter{}
	var mu sync.Mutex
	var pipes []*io.PipeWriter
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, pw := range pipes {
			pw.Close()
		}
	}()

	// Blocking pipes so nothing self-terminates; only eviction/CloseAll ends a
	// stream. The one aggConn serves both the aggregate open (WorkloadPods +
	// PodLogStream) and the single-pod opens (PodLogStream).
	conn := &aggConn{
		pods: []string{"web-aaa"},
		streamFor: func(string, int64) (io.ReadCloser, error) {
			pr, pw := io.Pipe()
			mu.Lock()
			pipes = append(pipes, pw)
			mu.Unlock()
			return pr, nil
		},
	}
	svc := NewLogsService(aggLookup(conn), em)

	// Open the aggregate first so it is the oldest registry entry.
	aggRes := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, tid())
	if aggRes.StreamID == "" || aggRes.Error != "" {
		t.Fatalf("aggregate open failed: %+v", aggRes)
	}

	// Fill to the cap with single-pod streams; the last open evicts the aggregate.
	for i := 0; i < maxConcurrentLogStreams; i++ {
		if res := svc.OpenLogStream("c", "ns", fmt.Sprintf("pod-%d", i), "app", false, 100, tid()); res.StreamID == "" {
			t.Fatalf("single open %d failed: %+v", i, res)
		}
	}

	// The aggregate must be gone from the live registry (evicted) ...
	if !waitUntil(t, 2*time.Second, func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		_, live := svc.streams[aggRes.StreamID]
		return !live
	}) {
		t.Fatal("aggregate was never evicted")
	}
	// ... and its draining entry must be cleaned up by deregister() (the fix).
	// Before the fix this leaked forever.
	if !waitUntil(t, 2*time.Second, func() bool { return svc.drainingCount() == 0 }) {
		t.Fatalf("evicted aggregate leaked a draining entry: %d left", svc.drainingCount())
	}

	// And CloseAll must not stall on a never-closing done channel.
	done := make(chan struct{})
	go func() { svc.CloseAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("CloseAll blocked: evicted-aggregate done never closed")
	}
	if svc.streamCount() != 0 {
		t.Fatalf("registry not drained after CloseAll: %d", svc.streamCount())
	}
}

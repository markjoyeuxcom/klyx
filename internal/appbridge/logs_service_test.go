package appbridge

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureEmitter records every emitted event under a mutex so tests can poll it
// from a different goroutine race-free.
type captureEmitter struct {
	mu     sync.Mutex
	events []capturedEvent
}

type capturedEvent struct {
	name  string
	chunk LogChunkDTO
}

func (e *captureEmitter) Emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	chunk, _ := data.(LogChunkDTO)
	e.events = append(e.events, capturedEvent{name: name, chunk: chunk})
}

func (e *captureEmitter) snapshot() []capturedEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]capturedEvent, len(e.events))
	copy(out, e.events)
	return out
}

func (e *captureEmitter) sawEOF() bool {
	for _, ev := range e.snapshot() {
		if ev.chunk.EOF {
			return true
		}
	}
	return false
}

func (e *captureEmitter) allLines() []string {
	var out []string
	for _, ev := range e.snapshot() {
		out = append(out, ev.chunk.Lines...)
	}
	return out
}

// --- fake conns ---

// stringConn returns a finite reader; the stream ends on EOF.
type stringConn struct{ body string }

func (c *stringConn) PodLogStream(_ context.Context, _, _, _ string, _ bool, _ int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(c.body)), nil
}
func (c *stringConn) WorkloadPods(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}

// pipeConn hands back the read end of an io.Pipe whose write end never advances,
// so a Read blocks until the ReadCloser is closed. Closing it makes Read return
// io.ErrClosedPipe - the unblock path under test.
type pipeConn struct{ r *io.PipeReader }

func (c *pipeConn) PodLogStream(_ context.Context, _, _, _ string, _ bool, _ int64) (io.ReadCloser, error) {
	return c.r, nil
}
func (c *pipeConn) WorkloadPods(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}

// errConn returns a reader that yields one line then a non-EOF error.
type errConn struct{}

type errReader struct {
	done bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		n := copy(p, []byte("line-before-error\n"))
		return n, nil
	}
	return 0, errors.New("boom")
}
func (r *errReader) Close() error { return nil }

func (c *errConn) PodLogStream(_ context.Context, _, _, _ string, _ bool, _ int64) (io.ReadCloser, error) {
	return &errReader{}, nil
}
func (c *errConn) WorkloadPods(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}

// missConn never used directly; cluster-miss uses a lookup returning false.

func lookupOf(conn LogsConn) func(string) (LogsConn, bool) {
	return func(string) (LogsConn, bool) { return conn, true }
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func (s *LogsService) streamCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams)
}

func (s *LogsService) drainingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.draining)
}

// --- tests ---

func TestOpenLogStream_ClusterMiss(t *testing.T) {
	em := &captureEmitter{}
	svc := NewLogsService(func(string) (LogsConn, bool) { return nil, false }, em)
	res := svc.OpenLogStream("nope", "ns", "pod", "app", false, 100)
	if res.Error == "" {
		t.Fatal("want error on cluster miss")
	}
	if res.StreamID != "" {
		t.Fatalf("want empty streamID, got %q", res.StreamID)
	}
}

func TestOpenLogStream_OpenError(t *testing.T) {
	em := &captureEmitter{}
	svc := NewLogsService(lookupOf(openErrConn{}), em)
	res := svc.OpenLogStream("c", "ns", "pod", "app", false, 100)
	if res.Error == "" || res.StreamID != "" {
		t.Fatalf("want error+no id, got %+v", res)
	}
}

type openErrConn struct{}

func (openErrConn) PodLogStream(_ context.Context, _, _, _ string, _ bool, _ int64) (io.ReadCloser, error) {
	return nil, errors.New("forbidden")
}
func (openErrConn) WorkloadPods(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}

func TestOpenLogStream_BatchesLinesThenEOF(t *testing.T) {
	em := &captureEmitter{}
	body := strings.Repeat("hello\n", 5) + "world\n"
	svc := NewLogsService(lookupOf(&stringConn{body: body}), em)

	res := svc.OpenLogStream("c", "ns", "pod", "app", false, 100)
	if res.StreamID == "" || res.Error != "" {
		t.Fatalf("open failed: %+v", res)
	}

	if !waitUntil(t, 2*time.Second, em.sawEOF) {
		t.Fatal("never saw EOF chunk")
	}
	lines := em.allLines()
	if len(lines) != 6 {
		t.Fatalf("want 6 lines, got %d: %v", len(lines), lines)
	}
	if lines[5] != "world" {
		t.Fatalf("last line mismatch: %q", lines[5])
	}
	// Registry must drain after EOF.
	if !waitUntil(t, 2*time.Second, func() bool { return svc.streamCount() == 0 }) {
		t.Fatalf("registry not drained: %d streams left", svc.streamCount())
	}
}

// LEAK TEST: close mid-stream on a blocking reader. The reader goroutine must
// exit (registry drained + done closed) within 2s, proving the close-the-rc
// unblock path works on a Read that never returns on its own.
func TestCloseLogStream_UnblocksBlockedRead(t *testing.T) {
	em := &captureEmitter{}
	pr, pw := io.Pipe()
	defer pw.Close()
	svc := NewLogsService(lookupOf(&pipeConn{r: pr}), em)

	res := svc.OpenLogStream("c", "ns", "pod", "app", false, 100)
	if res.StreamID == "" {
		t.Fatalf("open failed: %+v", res)
	}
	// Reader is now blocked inside scanner.Scan -> pr.Read with no writer.
	if svc.streamCount() != 1 {
		t.Fatalf("want 1 active stream, got %d", svc.streamCount())
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

func TestCapEviction_OldestClosed(t *testing.T) {
	em := &captureEmitter{}
	// Use blocking pipes so no stream self-terminates; only eviction closes them.
	var pipes []*io.PipeWriter
	defer func() {
		for _, pw := range pipes {
			pw.Close()
		}
	}()
	svc := NewLogsService(func(string) (LogsConn, bool) {
		pr, pw := io.Pipe()
		pipes = append(pipes, pw)
		return &pipeConn{r: pr}, true
	}, em)

	ids := make([]string, 0, 9)
	for i := 0; i < 9; i++ {
		res := svc.OpenLogStream("c", "ns", "pod", "app", false, 100)
		if res.StreamID == "" {
			t.Fatalf("open %d failed: %+v", i, res)
		}
		ids = append(ids, res.StreamID)
	}

	// The 9th open evicts the oldest (ids[0]); active streams settle at the cap.
	if !waitUntil(t, 2*time.Second, func() bool { return svc.streamCount() == maxConcurrentLogStreams }) {
		t.Fatalf("want %d streams after eviction, got %d", maxConcurrentLogStreams, svc.streamCount())
	}
	// The oldest is gone from the registry; closing it again is a no-op.
	svc.mu.Lock()
	_, stillThere := svc.streams[ids[0]]
	svc.mu.Unlock()
	if stillThere {
		t.Fatal("oldest stream should have been evicted")
	}
}

func TestCloseLogStream_Idempotent(t *testing.T) {
	em := &captureEmitter{}
	pr, pw := io.Pipe()
	defer pw.Close()
	svc := NewLogsService(lookupOf(&pipeConn{r: pr}), em)
	res := svc.OpenLogStream("c", "ns", "pod", "app", false, 100)

	svc.CloseLogStream(res.StreamID)
	svc.CloseLogStream(res.StreamID) // second close: no-op, must not panic
	svc.CloseLogStream("does-not-exist")
}

func TestRead_ErrorPath_FinalChunkHasError(t *testing.T) {
	em := &captureEmitter{}
	svc := NewLogsService(lookupOf(&errConn{}), em)
	res := svc.OpenLogStream("c", "ns", "pod", "app", false, 100)
	if res.StreamID == "" {
		t.Fatalf("open failed: %+v", res)
	}

	if !waitUntil(t, 2*time.Second, em.sawEOF) {
		t.Fatal("never saw terminal chunk")
	}
	// Find the EOF chunk and assert it carries the error.
	var eof *LogChunkDTO
	for _, ev := range em.snapshot() {
		if ev.chunk.EOF {
			c := ev.chunk
			eof = &c
		}
	}
	if eof == nil {
		t.Fatal("no EOF chunk")
	}
	if !eof.EOF {
		t.Fatal("terminal chunk must have EOF true")
	}
	if !strings.Contains(eof.Error, "boom") {
		t.Fatalf("terminal chunk should carry error, got %q", eof.Error)
	}
}

func TestCloseAll_DrainsEveryStream(t *testing.T) {
	em := &captureEmitter{}
	var pipes []*io.PipeWriter
	defer func() {
		for _, pw := range pipes {
			pw.Close()
		}
	}()
	svc := NewLogsService(func(string) (LogsConn, bool) {
		pr, pw := io.Pipe()
		pipes = append(pipes, pw)
		return &pipeConn{r: pr}, true
	}, em)

	for i := 0; i < 5; i++ {
		svc.OpenLogStream("c", "ns", "pod", "app", false, 100)
	}
	if svc.streamCount() != 5 {
		t.Fatalf("want 5 streams, got %d", svc.streamCount())
	}

	done := make(chan struct{})
	go func() { svc.CloseAll(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("CloseAll blocked: leak")
	}
	if svc.streamCount() != 0 {
		t.Fatalf("registry not drained after CloseAll: %d", svc.streamCount())
	}
}

// TestCapHoldsUnderBurst is the C1 regression lock: opening cap+10 streams
// sequentially must leave the registry at or below the cap IMMEDIATELY (no
// eventual wait). The synchronous eviction in OpenLogStream ensures this.
func TestCapHoldsUnderBurst(t *testing.T) {
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

	svc := NewLogsService(func(string) (LogsConn, bool) {
		pr, pw := io.Pipe()
		mu.Lock()
		pipes = append(pipes, pw)
		mu.Unlock()
		return &pipeConn{r: pr}, true
	}, em)

	burst := maxConcurrentLogStreams + 10
	for i := 0; i < burst; i++ {
		res := svc.OpenLogStream("c", "ns", "pod", "app", false, 100)
		if res.StreamID == "" {
			t.Fatalf("open %d failed: %+v", i, res)
		}
		// Immediate assertion - no wait allowed.
		if got := svc.streamCount(); got > maxConcurrentLogStreams {
			t.Fatalf("burst open %d: streamCount=%d > cap=%d (synchronous eviction failed)", i, got, maxConcurrentLogStreams)
		}
	}
}

// TestNoGoroutineLeakAcrossLifecycles verifies that all goroutines (reader,
// supervisor, feeder) exit cleanly across three scenarios: clean EOF, cancel-
// while-blocked (pipe that never writes), and over-cap eviction. After
// CloseAll the goroutine count must return within a small tolerance of the
// pre-test baseline.
func TestNoGoroutineLeakAcrossLifecycles(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()

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

	// Clean-EOF streams: body finishes immediately, reader exits on its own.
	cleanSvc := NewLogsService(lookupOf(&stringConn{body: "line1\nline2\n"}), em)
	for i := 0; i < 3; i++ {
		cleanSvc.OpenLogStream("c", "ns", "pod", "app", false, 100)
	}

	// Cancel-while-blocked streams: pipes that never write, cancelled via CloseAll.
	blockSvc := NewLogsService(func(string) (LogsConn, bool) {
		pr, pw := io.Pipe()
		mu.Lock()
		pipes = append(pipes, pw)
		mu.Unlock()
		return &pipeConn{r: pr}, true
	}, em)
	for i := 0; i < 3; i++ {
		blockSvc.OpenLogStream("c", "ns", "pod", "app", false, 100)
	}

	// Over-cap burst: opens cap+4 streams to exercise synchronous eviction paths.
	burstSvc := NewLogsService(func(string) (LogsConn, bool) {
		pr, pw := io.Pipe()
		mu.Lock()
		pipes = append(pipes, pw)
		mu.Unlock()
		return &pipeConn{r: pr}, true
	}, em)
	for i := 0; i < maxConcurrentLogStreams+4; i++ {
		burstSvc.OpenLogStream("c", "ns", "pod", "app", false, 100)
	}

	// Wait for clean-EOF streams to drain on their own.
	waitUntil(t, 3*time.Second, func() bool { return cleanSvc.streamCount() == 0 })

	// Cancel remaining live streams.
	blockSvc.CloseAll()
	burstSvc.CloseAll()

	// Goroutine count must settle back to baseline (±3 tolerance for runtime noise).
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

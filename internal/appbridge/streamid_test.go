package appbridge

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testStreamSeq atomic.Uint64

// tid is a fresh frontend-style stream id for a test open.
func tid() string { return fmt.Sprintf("test-%d", testStreamSeq.Add(1)) }

func TestValidStreamID(t *testing.T) {
	for _, ok := range []string{"logs-3f2a", "drain:9b1e-77", "a.b_c"} {
		if err := validStreamID(ok); err != nil {
			t.Errorf("%q must be valid: %v", ok, err)
		}
	}
	long := make([]byte, maxStreamIDLen+1)
	for i := range long {
		long[i] = 'a'
	}
	for _, bad := range []string{"", "a b", "a/b", "x#1", string(long)} {
		if validStreamID(bad) == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// freshPipeConn hands out a NEW blocking pipe per log open (a shared reader
// would let one test stream's Close end another).
type freshPipeConn struct {
	mu sync.Mutex
	ws []*io.PipeWriter
}

func (c *freshPipeConn) PodLogStream(context.Context, string, string, string, bool, int64) (io.ReadCloser, error) {
	r, w := io.Pipe()
	c.mu.Lock()
	c.ws = append(c.ws, w)
	c.mu.Unlock()
	return r, nil
}

func (c *freshPipeConn) WorkloadPods(context.Context, string, string, string) ([]string, error) {
	return []string{"web-1"}, nil
}

// Review 2026-10-03 #8: every chunk of a finite stream - including the ones
// emitted before OpenLogStream returns - is on the id the frontend chose and
// subscribed to before opening.
func TestOpenLogStream_EmitsOnlyOnTheChosenID(t *testing.T) {
	em := &captureEmitter{}
	svc := NewLogsService(lookupOf(&stringConn{body: "first\nsecond\n"}), em)
	id := tid()
	if res := svc.OpenLogStream("c", "ns", "pod", "app", true, 100, id); res.StreamID != id || res.Error != "" {
		t.Fatalf("open: %+v", res)
	}
	if !waitUntil(t, 2*time.Second, em.sawEOF) {
		t.Fatal("never saw EOF")
	}
	for _, ev := range em.snapshot() {
		if ev.name != "podlogs:"+id {
			t.Fatalf("emitted on %q, not the chosen id %q", ev.name, id)
		}
	}
	if got := em.allLines(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("lines: %v", got)
	}
}

func TestStreamOpens_RefuseInvalidIDBeforeTouchingTheCluster(t *testing.T) {
	looked := false
	svc := NewLogsService(func(string) (LogsConn, bool) { looked = true; return &stringConn{}, true }, &captureEmitter{})
	for _, res := range []OpenLogStreamResultDTO{
		svc.OpenLogStream("c", "ns", "pod", "app", false, 100, ""),
		svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, "bad id"),
	} {
		if res.Error == "" || res.StreamID != "" {
			t.Fatalf("an invalid id must be refused: %+v", res)
		}
	}
	if looked {
		t.Fatal("an invalid id must be refused before any cluster call")
	}
	drains, _ := newNodeOpsTestSvc(&fakeNodeOpsConn{})
	if r := drains.StartDrain("test", "node-1", "uid-node-1", DrainOptionsDTO{}, "x/y"); r.Error == "" {
		t.Fatalf("StartDrain must refuse an invalid id: %+v", r)
	}
}

// Two streams on one event name would interleave; an in-use id is refused and
// the live stream is untouched.
func TestStreamOpens_RefuseAnInUseID(t *testing.T) {
	conn := &freshPipeConn{}
	svc := NewLogsService(lookupOf(conn), &captureEmitter{})
	defer svc.CloseAll()
	id := tid()
	if res := svc.OpenLogStream("c", "ns", "pod", "app", false, 100, id); res.Error != "" {
		t.Fatalf("open: %+v", res)
	}
	if res := svc.OpenLogStream("c", "ns", "pod", "app", false, 100, id); res.Error == "" {
		t.Fatal("a second single-pod open on a live id must be refused")
	}
	if res := svc.OpenWorkloadLogStream("c", "ns", "Deployment", "web", "app", 100, id); res.Error == "" {
		t.Fatal("an aggregate open on a live id must be refused")
	}
	if n := svc.streamCount(); n != 1 {
		t.Fatalf("the live stream must be untouched, %d registered", n)
	}
}

func drainTerminal(t *testing.T, em *fakeEmitterWithData, id string) LogChunkDTO {
	t.Helper()
	var final LogChunkDTO
	if !waitUntil(t, 5*time.Second, func() bool {
		for _, ev := range em.events_() {
			if c, ok := ev.data.(LogChunkDTO); ok && ev.name == "nodedrain:"+id && c.EOF {
				final = c
				return true
			}
		}
		return false
	}) {
		t.Fatal("no terminal drain chunk")
	}
	return final
}

// Review 2026-10-03 (H): a cancelled drain killed kubectl part-way but was
// reported as a clean finish; it is now Cancelled and says the node stays
// cordoned. A completed drain stays a clean end.
func TestDrain_CancelIsReportedDistinctly(t *testing.T) {
	conn := &fakeNodeOpsConn{cmdFunc: func(string) (*exec.Cmd, error) { return exec.Command("sh", "-c", "sleep 60"), nil }}
	svc, em := newNodeOpsTestSvc(conn)
	id := tid()
	if r := svc.StartDrain("test", "node-cancel", "uid-node-cancel", DrainOptionsDTO{}, id); r.Error != "" {
		t.Fatalf("start: %+v", r)
	}
	if r := svc.StartDrain("test", "node-cancel", "uid-node-cancel", DrainOptionsDTO{}, id); r.Error == "" {
		t.Fatal("a second drain on a live id must be refused")
	}
	svc.CancelDrain(id)
	final := drainTerminal(t, em, id)
	if !final.Cancelled || !strings.Contains(final.Error, "node-cancel stays cordoned") {
		t.Fatalf("a cancelled drain must be reported as cancelled, node cordoned: %+v", final)
	}

	ok := &fakeNodeOpsConn{cmdFunc: func(string) (*exec.Cmd, error) { return exec.Command("sh", "-c", "echo evicted"), nil }}
	svc2, em2 := newNodeOpsTestSvc(ok)
	id2 := tid()
	if r := svc2.StartDrain("test", "node-ok", "uid-node-ok", DrainOptionsDTO{}, id2); r.Error != "" {
		t.Fatalf("start: %+v", r)
	}
	if final := drainTerminal(t, em2, id2); final.Cancelled || final.Error != "" {
		t.Fatalf("a completed drain is a clean end: %+v", final)
	}
}

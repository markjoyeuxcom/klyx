package appbridge

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleet"
)

// fakeNodeOpsConn is a test double for NodeOpsConn.
type fakeNodeOpsConn struct {
	mu          sync.Mutex
	cordonCalls []cordonCall
	cmdFunc     func(nodeName string) (*exec.Cmd, error)
	lastOpts    fleet.DrainOptions
}

type cordonCall struct {
	node   string
	cordon bool
}

func (f *fakeNodeOpsConn) SetCordon(_ context.Context, node string, cordon bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cordonCalls = append(f.cordonCalls, cordonCall{node: node, cordon: cordon})
	return nil
}

func (f *fakeNodeOpsConn) DrainNodeCmd(nodeName string, opts fleet.DrainOptions) (*exec.Cmd, error) {
	f.mu.Lock()
	f.lastOpts = opts
	f.mu.Unlock()
	if f.cmdFunc != nil {
		return f.cmdFunc(nodeName)
	}
	return exec.Command("sh", "-c", "echo draining; echo done"), nil
}

type fakeEmitterWithData struct {
	mu     sync.Mutex
	events []emittedEvent
}

type emittedEvent struct {
	name string
	data interface{}
}

func (f *fakeEmitterWithData) Emit(name string, data any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, emittedEvent{name: name, data: data})
}

func (f *fakeEmitterWithData) events_() []emittedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]emittedEvent, len(f.events))
	copy(out, f.events)
	return out
}

func newNodeOpsTestSvc(conn NodeOpsConn) (*NodeOpsService, *fakeEmitterWithData) {
	em := &fakeEmitterWithData{}
	svc := NewNodeOpsService(func(cluster string) (NodeOpsConn, bool) {
		if cluster == "test" {
			return conn, true
		}
		return nil, false
	}, em)
	return svc, em
}

func TestNodeOps_Cordon(t *testing.T) {
	conn := &fakeNodeOpsConn{}
	svc, _ := newNodeOpsTestSvc(conn)

	r := svc.Cordon("test", "node-1", true)
	if !r.OK {
		t.Fatalf("expected OK, got error: %s", r.Error)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.cordonCalls) != 1 || !conn.cordonCalls[0].cordon || conn.cordonCalls[0].node != "node-1" {
		t.Errorf("unexpected cordon calls: %+v", conn.cordonCalls)
	}
}

func TestNodeOps_Uncordon(t *testing.T) {
	conn := &fakeNodeOpsConn{}
	svc, _ := newNodeOpsTestSvc(conn)

	r := svc.Cordon("test", "node-2", false)
	if !r.OK {
		t.Fatalf("expected OK, got error: %s", r.Error)
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.cordonCalls) != 1 || conn.cordonCalls[0].cordon || conn.cordonCalls[0].node != "node-2" {
		t.Errorf("unexpected cordon calls: %+v", conn.cordonCalls)
	}
}

func TestNodeOps_Cordon_ClusterMiss(t *testing.T) {
	conn := &fakeNodeOpsConn{}
	svc, _ := newNodeOpsTestSvc(conn)
	r := svc.Cordon("missing-cluster", "node-1", true)
	if r.OK {
		t.Error("expected error for missing cluster")
	}
	if r.Error == "" {
		t.Error("expected non-empty error")
	}
}

func TestNodeOps_StartDrain_LinesAppend(t *testing.T) {
	conn := &fakeNodeOpsConn{
		cmdFunc: func(_ string) (*exec.Cmd, error) {
			return exec.Command("sh", "-c", "echo line1; echo line2"), nil
		},
	}
	svc, em := newNodeOpsTestSvc(conn)

	result := svc.StartDrain("test", "node-drain", DrainOptionsDTO{}, tid())
	if result.Error != "" {
		t.Fatalf("StartDrain error: %s", result.Error)
	}
	if result.StreamID == "" {
		t.Fatal("expected non-empty streamID")
	}

	// Wait for EOF chunk with timeout.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs := em.events_()
		for _, ev := range evs {
			if ev.name == "nodedrain:"+result.StreamID {
				chunk, ok := ev.data.(LogChunkDTO)
				if ok && chunk.EOF {
					// Found the terminal chunk. Also check lines appeared.
					allLines := []string{}
					for _, e := range evs {
						if e.name == "nodedrain:"+result.StreamID {
							c, ok := e.data.(LogChunkDTO)
							if ok {
								allLines = append(allLines, c.Lines...)
							}
						}
					}
					found1, found2 := false, false
					for _, l := range allLines {
						if l == "line1" {
							found1 = true
						}
						if l == "line2" {
							found2 = true
						}
					}
					if !found1 || !found2 {
						t.Errorf("expected line1 and line2 in output, got: %v", allLines)
					}
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for drain EOF chunk")
}

func TestNodeOps_CancelDrain_KillsProcess(t *testing.T) {
	conn := &fakeNodeOpsConn{
		cmdFunc: func(_ string) (*exec.Cmd, error) {
			return exec.Command("sh", "-c", "sleep 60"), nil
		},
	}
	svc, _ := newNodeOpsTestSvc(conn)

	result := svc.StartDrain("test", "node-cancel", DrainOptionsDTO{}, tid())
	if result.Error != "" {
		t.Fatalf("StartDrain error: %s", result.Error)
	}

	// Cancel should return quickly (the process is sleeping).
	done := make(chan struct{})
	go func() {
		svc.CancelDrain(result.StreamID)
		close(done)
	}()
	select {
	case <-done:
		// ok
	case <-time.After(3 * time.Second):
		t.Fatal("CancelDrain did not return within 3s")
	}
}

func TestNodeOps_StartDrain_ClusterMiss(t *testing.T) {
	conn := &fakeNodeOpsConn{}
	svc, _ := newNodeOpsTestSvc(conn)
	r := svc.StartDrain("missing", "node-1", DrainOptionsDTO{}, tid())
	if r.Error == "" {
		t.Error("expected error for missing cluster")
	}
	if r.StreamID != "" {
		t.Error("expected empty streamID on error")
	}
}

func TestNodeOps_StartDrain_ThreadsOptions(t *testing.T) {
	conn := &fakeNodeOpsConn{}
	svc, _ := newNodeOpsTestSvc(conn)
	r := svc.StartDrain("test", "node-1", DrainOptionsDTO{IgnoreDaemonSets: true, DeleteEmptyDirData: true, TimeoutSeconds: 60}, tid())
	if r.Error != "" {
		t.Fatalf("StartDrain: %s", r.Error)
	}
	conn.mu.Lock()
	got := conn.lastOpts
	conn.mu.Unlock()
	if !got.IgnoreDaemonSets || !got.DeleteEmptyDirData || got.Force || got.TimeoutSeconds != 60 {
		t.Fatalf("options not threaded: %+v", got)
	}
	svc.CancelAll()
}

func TestNodeOps_StartDrain_RefusesAtCap(t *testing.T) {
	conn := &fakeNodeOpsConn{
		cmdFunc: func(_ string) (*exec.Cmd, error) {
			return exec.Command("sh", "-c", "sleep 60"), nil
		},
	}
	svc, em := newNodeOpsTestSvc(conn)
	defer svc.CancelAll()

	running := make([]string, 0, maxConcurrentDrains)
	for i := 0; i < maxConcurrentDrains; i++ {
		r := svc.StartDrain("test", "node-cap", DrainOptionsDTO{}, tid())
		if r.Error != "" {
			t.Fatalf("StartDrain %d error: %s", i, r.Error)
		}
		running = append(running, r.StreamID)
	}

	// At cap: the next drain must be refused, not evict a running one.
	r := svc.StartDrain("test", "node-over-cap", DrainOptionsDTO{}, tid())
	if r.Error == "" {
		t.Fatal("expected refusal at cap, got a streamID")
	}
	if r.StreamID != "" {
		t.Errorf("expected empty streamID on refusal, got %q", r.StreamID)
	}

	// The running drains must be untouched: no terminal EOF chunk emitted.
	for _, ev := range em.events_() {
		for _, id := range running {
			if ev.name == "nodedrain:"+id {
				if c, ok := ev.data.(LogChunkDTO); ok && c.EOF {
					t.Errorf("running drain %s was terminated by the refused start", id)
				}
			}
		}
	}

	// Cancelling one frees a slot.
	svc.CancelDrain(running[0])
	r = svc.StartDrain("test", "node-after-cancel", DrainOptionsDTO{}, tid())
	if r.Error != "" {
		t.Fatalf("StartDrain after cancel error: %s", r.Error)
	}
}

package appbridge

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/fleet"
)

// NodeOpsConn is the per-cluster surface NodeOpsService needs.
type NodeOpsConn interface {
	SetCordon(ctx context.Context, nodeName string, cordon bool) error
	DrainNodeCmd(nodeName string, opts fleet.DrainOptions) (*exec.Cmd, error)
}

// DrainOptionsDTO carries the user-composed drain flags from the drain
// composer dialog. Zero value = the safe drain (see fleet.DrainOptions).
type DrainOptionsDTO struct {
	IgnoreDaemonSets   bool `json:"ignoreDaemonSets"`
	DeleteEmptyDirData bool `json:"deleteEmptyDirData"`
	Force              bool `json:"force"`
	TimeoutSeconds     int  `json:"timeoutSeconds"`
}

// drainStream is one live drain process.
type drainStream struct {
	cancel context.CancelFunc // cancels the supervisor goroutine
	cmd    *exec.Cmd          // the running kubectl drain process
	done   chan struct{}      // closed when the reader goroutine has fully exited
	node   string             // the node being drained (for the cancel report)
}

const (
	// maxConcurrentDrains caps simultaneous drain processes. A drain holds a
	// process open for up to 120s; more than 2 at once is unusual.
	maxConcurrentDrains = 2
)

// NodeOpsService is bound to JS and handles cordon/uncordon and drain.
//
// Drain lifecycle mirrors LogsService: supervisor goroutine kills the process
// on context cancel; reader goroutine owns the pipe, batches lines to the
// "nodedrain:<streamID>" event, and emits a terminal EOF chunk on exit.
type NodeOpsService struct {
	lookup func(string) (NodeOpsConn, bool)
	em     Emitter

	mu     sync.Mutex
	drains map[string]*drainStream
	order  []string // insertion order, for oldest-first eviction
}

// NewNodeOpsService wires the cluster lookup and event emitter.
func NewNodeOpsService(lookup func(string) (NodeOpsConn, bool), em Emitter) *NodeOpsService {
	return &NodeOpsService{
		lookup: lookup,
		em:     em,
		drains: map[string]*drainStream{},
	}
}

// Cordon cordons (unschedulable=true) or uncordons (unschedulable=false) a node.
func (s *NodeOpsService) Cordon(cluster, node string, cordon bool) ActionResultDTO {
	if err := checkProtected(cluster); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return ActionResultDTO{Error: "cluster not connected: " + cluster}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	if err := conn.SetCordon(ctx, node, cordon); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// drainPipe wraps a PipeReader; its Close also closes the write end so the
// supervisor kill + pipe close truly unblocks the reader in all paths.
type drainPipe struct {
	*io.PipeReader
	pw *io.PipeWriter
}

func (p *drainPipe) Close() error {
	_ = p.pw.Close()
	return p.PipeReader.Close()
}

// StartDrain starts a kubectl drain process for the given node on the
// frontend-chosen streamID (validStreamID). The frontend subscribes to
// "nodedrain:<streamID>" for LogChunkDTO events BEFORE calling, so no output is
// emitted before a listener exists (review 2026-10-03 #8). The terminal chunk
// has EOF=true; Error is set if kubectl exited non-zero, and a cancelled drain
// is reported as Cancelled with an Error saying the node stays cordoned - never
// as a clean finish (review 2026-10-03, H).
//
// Cap: if maxConcurrentDrains drains are already running, StartDrain refuses.
// A drain is a user-initiated destructive operation mid-flight - evicting one
// would leave its node cordoned and half-drained while reporting a clean
// stream end (same policy as forwards: never silently kill user-managed work).
func (s *NodeOpsService) StartDrain(cluster, node string, opts DrainOptionsDTO, streamID string) OpenLogStreamResultDTO {
	if err := validStreamID(streamID); err != nil {
		return OpenLogStreamResultDTO{Error: err.Error()}
	}
	if err := checkProtected(cluster); err != nil {
		return OpenLogStreamResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return OpenLogStreamResultDTO{Error: "cluster not connected: " + cluster}
	}

	cmd, err := conn.DrainNodeCmd(node, fleet.DrainOptions{
		IgnoreDaemonSets:   opts.IgnoreDaemonSets,
		DeleteEmptyDirData: opts.DeleteEmptyDirData,
		Force:              opts.Force,
		TimeoutSeconds:     opts.TimeoutSeconds,
	})
	if err != nil {
		return OpenLogStreamResultDTO{Error: err.Error()}
	}

	// Combine stdout+stderr into a single io.Pipe. The write end is owned by the
	// subprocess via cmd.Stdout/Stderr; the read end is owned by the reader goroutine.
	// The supervisor closes the read end (via drainPipe.Close) on cancel, which
	// also closes the write end, so the scanner unblocks in all paths.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	rc := &drainPipe{PipeReader: pr, pw: pw}

	ctx, cancel := context.WithCancel(context.Background())
	ds := &drainStream{cancel: cancel, cmd: cmd, done: make(chan struct{}), node: node}

	// Reserve the slot before starting the process: the cap check and the add
	// happen under one lock, and a registered stream can never miss CancelAll.
	s.mu.Lock()
	if s.drains[streamID] != nil {
		s.mu.Unlock()
		cancel()
		_ = rc.Close()
		return OpenLogStreamResultDTO{Error: fmt.Sprintf("stream id %q is already in use", streamID)}
	}
	if len(s.order) >= maxConcurrentDrains {
		s.mu.Unlock()
		cancel()
		_ = rc.Close()
		return OpenLogStreamResultDTO{Error: fmt.Sprintf("too many active drains (max %d); wait for one to finish or cancel it first", maxConcurrentDrains)}
	}
	s.drains[streamID] = ds
	s.order = append(s.order, streamID)
	s.mu.Unlock()

	if err := cmd.Start(); err != nil {
		s.deregister(streamID)
		cancel()
		_ = rc.Close()
		return OpenLogStreamResultDTO{Error: "start drain: " + err.Error()}
	}

	// Close the write end after the process exits. This is the normal EOF path;
	// the supervisor close handles the cancel path. Both are idempotent.
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = pw.Close()
		close(waitDone)
	}()

	// Supervisor: kill process and close the pipe on cancel so the reader
	// goroutine unblocks. rc.Close is idempotent with the wait goroutine close.
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = rc.Close()
	}()

	go s.readDrain(ctx, streamID, rc, waitDone)

	return OpenLogStreamResultDTO{StreamID: streamID}
}

// CancelAll cancels every running drain (app shutdown: child kubectl
// processes must not outlive the app). Idempotent.
func (s *NodeOpsService) CancelAll() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.drains))
	for id := range s.drains {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.CancelDrain(id)
	}
}

// CancelDrain kills the running drain process and waits briefly for the reader
// to exit. Idempotent: unknown or already-finished ids are no-ops.
func (s *NodeOpsService) CancelDrain(streamID string) {
	s.mu.Lock()
	ds := s.drains[streamID]
	s.mu.Unlock()
	if ds == nil {
		return
	}
	ds.cancel()
	select {
	case <-ds.done:
	case <-time.After(closeWaitTimeout):
	}
}

// readDrain is the single reader goroutine for one drain stream. It mirrors
// the batched-emit discipline of LogsService.read exactly.
func (s *NodeOpsService) readDrain(ctx context.Context, streamID string, rc io.ReadCloser, waitDone <-chan struct{}) {
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, logScanInitial), logScanMax)

	ticker := time.NewTicker(logBatchInterval)
	defer ticker.Stop()

	var batch []string
	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.em.Emit("nodedrain:"+streamID, LogChunkDTO{Lines: batch})
		batch = nil
	}

	lines := make(chan string)
	go func() {
		defer close(lines)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			flush()
			s.finishDrain(streamID, rc, s.cancelledChunk(streamID))
			return
		case <-ticker.C:
			flush()
		case line, ok := <-lines:
			if !ok {
				flush()
				// Wait for cmd.Wait() to complete so ProcessState is populated.
				<-waitDone
				// A cancel kills the process and closes the pipe, so this path can
				// win the race against ctx.Done: it is still a cancel, not a
				// "exit status -1" failure or a clean end.
				if ctx.Err() != nil {
					s.finishDrain(streamID, rc, s.cancelledChunk(streamID))
					return
				}
				s.finishDrain(streamID, rc, LogChunkDTO{Lines: []string{}, EOF: true, Error: s.drainExitMsg(streamID)})
				return
			}
			batch = append(batch, line)
			if len(batch) >= logBatchMaxLines {
				flush()
			}
		}
	}
}

// drainExitMsg returns a non-empty string if the process for this streamID
// exited with a non-zero status. It reads ProcessState under the stream's cmd.
func (s *NodeOpsService) drainExitMsg(streamID string) string {
	s.mu.Lock()
	ds := s.drains[streamID]
	s.mu.Unlock()
	if ds == nil {
		return ""
	}
	if ps := ds.cmd.ProcessState; ps != nil && !ps.Success() {
		return fmt.Sprintf("exit status %d", ps.ExitCode())
	}
	return ""
}

// deregister removes a stream from the registry (map + order) and returns it.
// Safe to call for ids that are already gone.
func (s *NodeOpsService) deregister(streamID string) *drainStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	ds := s.drains[streamID]
	delete(s.drains, streamID)
	for i, id := range s.order {
		if id == streamID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return ds
}

// cancelledChunk is the terminal chunk of a cancelled drain: kubectl was
// killed part-way, so the node stays cordoned with some pods possibly still on
// it. Reporting that as a clean end read a half-drained node as finished
// (review 2026-10-03, H).
func (s *NodeOpsService) cancelledChunk(streamID string) LogChunkDTO {
	node := "the node"
	s.mu.Lock()
	if ds := s.drains[streamID]; ds != nil && ds.node != "" {
		node = ds.node
	}
	s.mu.Unlock()
	return LogChunkDTO{
		Lines:     []string{},
		EOF:       true,
		Cancelled: true,
		Error:     fmt.Sprintf("drain cancelled - %s stays cordoned and may be partly drained; uncordon it if you are not retrying", node),
	}
}

func (s *NodeOpsService) finishDrain(streamID string, rc io.ReadCloser, final LogChunkDTO) {
	s.em.Emit("nodedrain:"+streamID, final)
	_ = rc.Close()

	if ds := s.deregister(streamID); ds != nil {
		ds.cancel() // idempotent; release context
		close(ds.done)
	}
}

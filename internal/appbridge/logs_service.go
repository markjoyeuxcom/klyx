package appbridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// LogsConn is the per-cluster surface LogsService needs. The lookup closure
// bridges to the real fleet.Conn; the interface keeps fleet out of appbridge.
type LogsConn interface {
	PodLogStream(ctx context.Context, namespace, pod, container string, previous bool, tailLines int64) (io.ReadCloser, error)
	WorkloadPods(ctx context.Context, kind, namespace, name string) ([]string, error)
}

const (
	// maxConcurrentLogStreams caps live tails. A daily driver follows a handful
	// of logs, never dozens; at cap the OLDEST stream is evicted on a new open.
	maxConcurrentLogStreams = 8

	// logBatchInterval / logBatchMaxLines bound emission: flush every interval
	// OR when the batch fills, whichever first. Never per-line - event flooding
	// kills the webview.
	logBatchInterval = 150 * time.Millisecond
	logBatchMaxLines = 200

	// logScanInitial / logScanMax size the scanner buffer. Default 64KB token
	// cap would kill the stream on a long line (e.g. a stack trace); allow up
	// to 1MB per line.
	logScanInitial = 64 * 1024
	logScanMax     = 1024 * 1024

	// closeWaitTimeout bounds CloseLogStream's wait on the reader so the UI
	// thread is never blocked indefinitely.
	closeWaitTimeout = 2 * time.Second
)

// logStream is one live tail. cancel triggers the supervisor (which closes the
// ReadCloser to unblock a stuck Read); done closes when the reader goroutine has
// fully exited (emitted final chunk, deregistered).
type logStream struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// LogsService streams pod logs to the frontend over Wails events. Event name
// contract: each open emits LogChunkDTO payloads on "podlogs:"+streamID.
//
// Lifecycle / leak-safety: every reader goroutine has exactly one exit, reached
// on any of {ctx cancel, scanner EOF, scanner error}. On exit it emits a final
// EOF chunk, closes the ReadCloser, deregisters itself, and closes done. ctx
// cancel alone does NOT unblock a Read once the HTTP response has started, so a
// per-stream supervisor goroutine closes the ReadCloser on ctx.Done - that is
// the documented unblock mechanism.
type LogsService struct {
	lookup func(string) (LogsConn, bool)
	em     Emitter

	seq atomic.Uint64

	mu       sync.Mutex
	streams  map[string]*logStream
	draining map[string]*logStream // evicted at cap; reader still exiting, finish() closes done
	order    []string              // insertion order, for oldest-first eviction
}

// NewLogsService wires the cluster lookup and the event emitter. It mirrors the
// other push services (e.g. GitOpsService), which receive the Emitter so they
// can push live updates to the frontend.
func NewLogsService(lookup func(string) (LogsConn, bool), em Emitter) *LogsService {
	return &LogsService{
		lookup:   lookup,
		em:       em,
		streams:  map[string]*logStream{},
		draining: map[string]*logStream{},
	}
}

// evictToCapLocked evicts the oldest stream(s) until there is room to register
// one more under maxConcurrentLogStreams. The caller MUST hold s.mu. Each
// evicted stream is MOVED to s.draining (not merely deleted) so its reader's
// finish()/deregister() can still find it and close its done channel; otherwise
// a CloseAll racing an eviction would wait the full closeWaitTimeout on a stream
// whose reader had already exited, and the draining entry would leak for the
// process lifetime. Single-pod (OpenLogStream) and aggregate
// (OpenWorkloadLogStream) opens both call this, so the two eviction paths can
// never drift - a drift that previously dropped an aggregate-evicted stream on
// the floor with its done never closed.
func (s *LogsService) evictToCapLocked() {
	for len(s.order) >= maxConcurrentLogStreams {
		oldest := s.order[0]
		s.order = s.order[1:]
		if ev := s.streams[oldest]; ev != nil {
			delete(s.streams, oldest)
			s.draining[oldest] = ev
			ev.cancel() // supervisor closes its rc; its reader exits via finish()/deregister()
		}
	}
}

// OpenLogStream opens a live tail for one container and returns its streamID.
// The frontend subscribes to "podlogs:"+streamID. tailLines<=0 defaults
// downstream (fleet clamps to 500, caps at 5000). previous tails the prior
// container instance's (static) logs. Cluster miss or open error returns an
// Error and no stream.
func (s *LogsService) OpenLogStream(cluster, namespace, pod, container string, previous bool, tailLines int) OpenLogStreamResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return OpenLogStreamResultDTO{Error: "cluster not connected"}
	}

	// Long-lived: cancellation is the lifecycle, not a timeout.
	ctx, cancel := context.WithCancel(context.Background())
	rc, err := conn.PodLogStream(ctx, namespace, pod, container, previous, int64(tailLines))
	if err != nil {
		cancel()
		return OpenLogStreamResultDTO{Error: err.Error()}
	}

	streamID := fmt.Sprintf("%s/%s/%s/%s#%d", cluster, namespace, pod, container, s.seq.Add(1))
	st := &logStream{cancel: cancel, done: make(chan struct{})}

	s.mu.Lock()
	// Cap: synchronously evict the oldest stream(s) before registering the new
	// one, so concurrent or rapid sequential opens can never blow past
	// maxConcurrentLogStreams - the invariant holds at registration time, not
	// eventually.
	s.evictToCapLocked()
	s.streams[streamID] = st
	s.order = append(s.order, streamID)
	s.mu.Unlock()

	// Supervisor: the ONLY thing that unblocks a stuck Read. ctx is cancelled by
	// CloseLogStream, CloseAll, or eviction; closing rc makes a blocked Read
	// return, so the reader loop terminates.
	go func() {
		<-ctx.Done()
		_ = rc.Close()
	}()

	go s.read(ctx, streamID, rc)

	return OpenLogStreamResultDTO{StreamID: streamID}
}

// read is the single reader goroutine for one stream. It batches lines and
// flushes on a ticker or at the batch cap, then on any exit emits a final EOF
// chunk, closes the reader, and deregisters + closes done. Because the final
// emit happens here (the same goroutine that owns the stream) and deregistration
// happens immediately after, no other goroutine can emit on this stream's event
// after it is gone - the frontend may have unsubscribed, but the one trailing
// EOF chunk is emitted before deregistration and is harmless either way.
func (s *LogsService) read(ctx context.Context, streamID string, rc io.ReadCloser) {
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, logScanInitial), logScanMax)

	ticker := time.NewTicker(logBatchInterval)
	defer ticker.Stop()

	var batch []string
	flush := func() {
		if len(batch) == 0 {
			return
		}
		s.em.Emit("podlogs:"+streamID, LogChunkDTO{Lines: batch})
		batch = nil
	}

	// Scanner runs in its own goroutine so the reader loop can select on the
	// ticker and ctx without blocking inside scanner.Scan. The supervisor closes
	// rc on ctx cancel, which makes Scan return false and closes lines.
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

	var scanErr error
	for {
		select {
		case <-ctx.Done():
			flush()
			s.finish(streamID, rc, "")
			return
		case <-ticker.C:
			flush()
		case line, ok := <-lines:
			if !ok {
				// Scanner ended: EOF or error. Drain the partial batch, then the
				// terminal chunk carries any non-cancel error.
				flush()
				if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
					scanErr = err
				}
				msg := ""
				if scanErr != nil {
					msg = scanErr.Error()
				}
				s.finish(streamID, rc, msg)
				return
			}
			batch = append(batch, line)
			if len(batch) >= logBatchMaxLines {
				flush()
			}
		}
	}
}

// finish emits the terminal EOF chunk, closes the reader, deregisters the
// stream, and closes done. Closing rc here is idempotent with the supervisor's
// close (both call Close; the second is a no-op).
func (s *LogsService) finish(streamID string, rc io.ReadCloser, errMsg string) {
	s.em.Emit("podlogs:"+streamID, LogChunkDTO{Lines: []string{}, EOF: true, Error: errMsg})
	_ = rc.Close()

	s.mu.Lock()
	// An evicted stream lives in draining (moved there by OpenLogStream's cap
	// eviction); a live one in streams. Either way the entry is removed here
	// and its done closed below, so CloseAll/CloseLogStream waits end promptly.
	st := s.streams[streamID]
	delete(s.streams, streamID)
	if st == nil {
		st = s.draining[streamID]
	}
	delete(s.draining, streamID)
	for i, id := range s.order {
		if id == streamID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.mu.Unlock()

	if st != nil {
		st.cancel() // release the context (supervisor exits); idempotent
		close(st.done)
	}
}

// CloseLogStream cancels a stream and waits briefly for its reader to exit.
// Idempotent: an unknown or already-closed id is a no-op. Never blocks the UI
// thread longer than closeWaitTimeout.
func (s *LogsService) CloseLogStream(streamID string) {
	s.mu.Lock()
	st := s.streams[streamID]
	s.mu.Unlock()
	if st == nil {
		return
	}
	st.cancel()
	select {
	case <-st.done:
	case <-time.After(closeWaitTimeout):
	}
}

// CloseAll cancels every live stream and waits briefly for each to exit. Called
// on app shutdown or when the frontend unmounts the logs view.
func (s *LogsService) CloseAll() {
	s.mu.Lock()
	sts := make([]*logStream, 0, len(s.streams)+len(s.draining))
	for _, st := range s.streams {
		sts = append(sts, st)
	}
	for _, st := range s.draining {
		sts = append(sts, st)
	}
	s.mu.Unlock()

	for _, st := range sts {
		st.cancel()
	}
	for _, st := range sts {
		select {
		case <-st.done:
		case <-time.After(closeWaitTimeout):
		}
	}
}

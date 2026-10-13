package appbridge

import (
	"bufio"
	"fmt"
	"strings"
	"sync"
	"time"
)

// maxAggregatePods caps how many pods one aggregate stream fans across. An
// aggregate holds N apiserver log streams open at once (one per pod) but counts
// as ONE entry against maxConcurrentLogStreams - so the worst case is
// maxConcurrentLogStreams * maxAggregatePods concurrent apiserver streams
// (8 * 10 = 80). That ceiling is deliberate: a daily driver tails a handful of
// workloads, and a 10-replica cap keeps the prefixed stream legible while
// bounding apiserver pressure. Beyond the cap the first 10 (sorted) pods are
// tailed and a marker line announces the truncation.
const maxAggregatePods = 10

// OpenWorkloadLogStream opens ONE aggregate live tail fanning across every pod
// of a workload (Deployment/StatefulSet/DaemonSet). Each line is prefixed
// "<pod-short> › " where pod-short strips the workload-name prefix from the pod
// name (falling back to the full name when it is not a prefix). All pods feed a
// single batched event channel ("podlogs:"+streamID), reusing the LogChunkDTO
// contract so the frontend LogsPane works unchanged.
//
// container may be "" (each pod's default/single container - GetLogs accepts "").
// The aggregate is registered as ONE stream against maxConcurrentLogStreams,
// even though it internally holds up to maxAggregatePods apiserver streams.
//
// Failure honesty:
//   - zero pods -> Error, no stream.
//   - a pod that fails to OPEN at start is skipped with an injected marker line;
//     the aggregate proceeds if >=1 pod opened, else returns Error.
//   - a pod stream dying mid-tail (pod deleted) injects a "… stream ended" marker
//     and the aggregate continues; the natural EOF arrives once ALL readers end.
//   - the terminal EOF chunk carries an Error listing the failed-reader count if
//     any pod reader errored.
func (s *LogsService) OpenWorkloadLogStream(cluster, namespace, kind, name, container string, tailLines int, streamID string) OpenLogStreamResultDTO {
	if err := validStreamID(streamID); err != nil {
		return OpenLogStreamResultDTO{Error: err.Error()}
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return OpenLogStreamResultDTO{Error: "cluster not connected"}
	}

	// Reserved before any network I/O, like OpenLogStream: the pending open
	// counts as ONE stream against the cap and a close cancels it.
	st, ctx, err := s.reserve(streamID)
	if err != nil {
		return OpenLogStreamResultDTO{Error: err.Error()}
	}
	stop := openDeadline(st)
	fail := func(timedOut bool, err error) OpenLogStreamResultDTO {
		msg := openFailure(ctx, timedOut, err) // before release cancels ctx
		s.release(streamID, st)
		return OpenLogStreamResultDTO{Error: msg}
	}

	pods, err := conn.WorkloadPods(ctx, kind, namespace, name)
	if err != nil {
		return fail(stop(), err)
	}
	if len(pods) == 0 {
		return fail(stop(), fmt.Errorf("no pods for %s %s/%s", kind, namespace, name))
	}

	// Cap the fan-out. Pods are already sorted by WorkloadPods; take the first N.
	var prelude []string
	totalPods := len(pods)
	if totalPods > maxAggregatePods {
		pods = pods[:maxAggregatePods]
		prelude = append(prelude, fmt.Sprintf("… showing %d of %d pods", maxAggregatePods, totalPods))
	}

	// Per-pod tail split: the caller's tailLines budget is divided across pods so
	// the combined backlog stays near the request, with a floor of 50 lines/pod so
	// each replica still shows recent context. Honest note: this is a split, not a
	// per-pod tailLines - a 500-line request across 5 pods tails 100 lines each.
	perPodTail := tailLines / len(pods)
	if perPodTail < 50 {
		perPodTail = 50
	}

	// Open every pod stream up front so a total-failure returns synchronously
	// (no stream registered). Each opened reader carries its prefix + ReadCloser.
	type podReader struct {
		short string
		rc    interface {
			Read([]byte) (int, error)
			Close() error
		}
	}
	var readers []podReader
	for _, pod := range pods {
		short := podShort(name, pod)
		if ctx.Err() != nil {
			break // closed, evicted or timed out while opening
		}
		rc, perr := conn.PodLogStream(ctx, namespace, pod, container, false, int64(perPodTail))
		if perr != nil {
			prelude = append(prelude, fmt.Sprintf("%s › … failed to open: %s", short, perr.Error()))
			continue
		}
		readers = append(readers, podReader{short: short, rc: rc})
	}
	timedOut := stop()
	if timedOut || ctx.Err() != nil || len(readers) == 0 {
		for _, pr := range readers {
			_ = pr.rc.Close()
		}
		if timedOut || ctx.Err() != nil {
			return fail(timedOut, nil)
		}
		return fail(false, fmt.Errorf("all pod streams failed to open for %s %s/%s", kind, namespace, name))
	}

	// lines is the single shared fan-in channel. closed=cap len(readers) is not
	// required; readers select on ctx.Done so a blocked send never leaks.
	lines := make(chan string)

	// failedReaders counts pod readers that ended on a non-cancel error, under a
	// mutex because every reader goroutine may touch it.
	var failMu sync.Mutex
	failedReaders := 0

	// Per-pod reader goroutines. Each scans its rc and forwards prefixed lines
	// into the shared channel. On EOF/error it injects a terminal marker for that
	// pod and signals the batcher via the WaitGroup; a pod dying early NEVER kills
	// the aggregate - the batcher closes only when ALL readers have ended.
	var wg sync.WaitGroup
	for _, pr := range readers {
		pr := pr
		// Supervisor per pod: ctx cancel closes the ReadCloser to unblock a stuck
		// Read (cancel alone does not unblock an in-flight HTTP body read).
		go func() {
			<-ctx.Done()
			_ = pr.rc.Close()
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(pr.rc)
			scanner.Buffer(make([]byte, logScanInitial), logScanMax)

			send := func(line string) bool {
				select {
				case lines <- line:
					return true
				case <-ctx.Done():
					return false
				}
			}

			for scanner.Scan() {
				if !send(pr.short + " › " + scanner.Text()) {
					_ = pr.rc.Close()
					return
				}
			}
			_ = pr.rc.Close()

			// Scanner ended: EOF (pod stream closed) or error. On a non-cancel error
			// count it and surface it; either way inject a per-pod "stream ended"
			// marker so the operator sees the replica drop without killing the agg.
			if err := scanner.Err(); err != nil && ctx.Err() == nil {
				failMu.Lock()
				failedReaders++
				failMu.Unlock()
			}
			// Best-effort marker; if ctx is gone the batcher is draining and the
			// send falls through on ctx.Done.
			send(pr.short + " › … stream ended")
		}()
	}

	// allDone closes once every reader goroutine has returned, which lets the
	// batcher distinguish "all pods naturally ended" (emit EOF) from "ctx
	// cancelled" (also emit EOF, after a drain).
	allDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(allDone)
	}()

	// Single batcher goroutine: mirrors LogsService.read's 150ms/200-line batch
	// discipline, draining the shared lines channel into chunks. It owns the
	// terminal EOF emit + deregistration (finishAggregate), so no emit races a
	// concurrent close. Mirror rather than reuse: read() couples scanner+batcher
	// to one rc, and extracting a shared batcher would force read() to split that
	// proven single-pod path. The constants are shared.
	go func() {
		ticker := time.NewTicker(logBatchInterval)
		defer ticker.Stop()

		var batch []string
		batch = append(batch, prelude...) // marker lines lead the stream
		flush := func() {
			if len(batch) == 0 {
				return
			}
			s.em.Emit("podlogs:"+streamID, LogChunkDTO{Lines: batch})
			batch = nil
		}

		for {
			select {
			case <-ctx.Done():
				// Cancelled (CloseLogStream / CloseAll / eviction). Drain anything
				// already buffered in lines without blocking, flush, emit the terminal
				// chunk, then deregister + close done. Readers exit via their per-pod
				// supervisors on cancel; we do not wait on them here so the UI thread's
				// close is bounded by closeWaitTimeout, not by reader drain.
				for {
					select {
					case line := <-lines:
						batch = append(batch, line)
					default:
						flush()
						s.emitAggregateEOF(streamID, &failMu, &failedReaders, len(readers))
						s.deregister(streamID)
						return
					}
				}
			case <-allDone:
				// Every pod reader ended on its own (no cancel). Drain remaining
				// buffered lines, flush, emit EOF.
				for {
					select {
					case line := <-lines:
						batch = append(batch, line)
						if len(batch) >= logBatchMaxLines {
							flush()
						}
					default:
						flush()
						s.emitAggregateEOF(streamID, &failMu, &failedReaders, len(readers))
						s.deregister(streamID)
						return
					}
				}
			case <-ticker.C:
				flush()
			case line := <-lines:
				batch = append(batch, line)
				if len(batch) >= logBatchMaxLines {
					flush()
				}
			}
		}
	}()

	return OpenLogStreamResultDTO{StreamID: streamID}
}

// emitAggregateEOF emits the single terminal EOF chunk for the aggregate. If any
// pod reader ended on a non-cancel error, the Error message reports the count,
// e.g. "2 of 5 pod streams failed".
func (s *LogsService) emitAggregateEOF(streamID string, failMu *sync.Mutex, failed *int, total int) {
	failMu.Lock()
	n := *failed
	failMu.Unlock()
	msg := ""
	if n > 0 {
		msg = fmt.Sprintf("%d of %d pod streams failed", n, total)
	}
	s.em.Emit("podlogs:"+streamID, LogChunkDTO{Lines: []string{}, EOF: true, Error: msg})
}

// deregister removes the aggregate from the registry and closes its done
// channel. Tolerant of a prior synchronous eviction (the entry may already be
// gone), mirroring finish() in logs_service.go.
func (s *LogsService) deregister(streamID string) {
	s.mu.Lock()
	// Mirror finish(): a live aggregate lives in streams, one already evicted by a
	// later open lives in draining. Fall back to draining so the entry is removed
	// and its done closed either way - otherwise an evicted aggregate leaks its
	// draining entry and never closes done, stalling CloseAll for the full
	// closeWaitTimeout per leak.
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
		st.cancel() // release ctx (supervisors exit); idempotent
		select {
		case <-st.done:
		default:
			close(st.done)
		}
	}
}

// podShort derives the display prefix: the pod name minus the workload-name
// prefix (and the separating '-'), e.g. workload "web", pod
// "web-7d4b9c6f9-x2x9k" -> "7d4b9c6f9-x2x9k". When the pod name is not prefixed
// by the workload name, the full pod name is returned.
func podShort(workload, pod string) string {
	prefix := workload + "-"
	if strings.HasPrefix(pod, prefix) && len(pod) > len(prefix) {
		return pod[len(prefix):]
	}
	return pod
}

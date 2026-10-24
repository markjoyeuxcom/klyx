//go:build eval

package appbridge

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/evalrun/record"
	"github.com/moomora/klyx/internal/observe"
)

// The AI eval's delivered-evidence record (eval design §5), in the eval
// build only. A hook reports to the record the runner set for this run; the
// record ignores every other conversation. Nothing here changes what a turn
// reads, sends or labels.
type evalHook struct {
	rec    atomic.Pointer[record.Recorder]
	mu     sync.Mutex
	staged map[string][]string // a snapshot document's read IDs, until it is sent
}

// SetEvalRecorder points the eval's record at one run; nil clears it.
func SetEvalRecorder(s *ChatService, rec *record.Recorder) { s.eval.rec.Store(rec) }

func (r *turnRunner) evalRec() *record.Recorder { return r.s.eval.rec.Load() }

func (r *turnRunner) evalMode(mode string) {
	if rec := r.evalRec(); rec != nil {
		rec.Mode(r.c.id, mode)
	}
}

// evalRead is one read as Klyx produced it: the tool, its arguments, and
// the final envelope (after projection, scrubbing and the result's cap).
func (r *turnRunner) evalRead(tool string, args json.RawMessage, res ChatToolResult) {
	if rec := r.evalRec(); rec != nil {
		failed := res.Read.Outcome == "failed" || res.Read.Outcome == "cancelled"
		rec.Read(r.c.id, tool, args, res.Read.ID, failed, res.JSON)
	}
}

// evalSent is a tools request: sent (a response came back) or refused
// locally for its size (nothing was sent).
func (r *turnRunner) evalSent(msgs []ai.ConvMessage, sent bool) {
	rec := r.evalRec()
	if rec == nil {
		return
	}
	ids := carriedReads(r.ex.seedData, msgs)
	if sent {
		rec.Sent(r.c.id, ids)
	} else {
		rec.Refused(r.c.id, ids)
	}
}

// evalSnapshotDoc is a snapshot document as filled: its sections' reads
// are staged until it is sent, and those the fill left out are omitted.
func (r *turnRunner) evalSnapshotDoc(sections, dropped []string) {
	rec := r.evalRec()
	if rec == nil {
		return
	}
	left := map[string]bool{}
	for _, id := range dropped {
		left[id] = true
	}
	var kept []string
	for _, s := range sections {
		if id := readIDOf(s); id != "" && !left[id] {
			kept = append(kept, id)
		}
	}
	r.s.eval.mu.Lock()
	if r.s.eval.staged == nil {
		r.s.eval.staged = map[string][]string{}
	}
	r.s.eval.staged[r.c.id] = kept
	r.s.eval.mu.Unlock()
	rec.Omitted(r.c.id, dropped)
}

// evalSnapshotSent is the snapshot document's request, answered.
func (r *turnRunner) evalSnapshotSent() {
	rec := r.evalRec()
	if rec == nil {
		return
	}
	r.s.eval.mu.Lock()
	kept := r.s.eval.staged[r.c.id]
	delete(r.s.eval.staged, r.c.id)
	r.s.eval.mu.Unlock()
	rec.Sent(r.c.id, kept)
}

func (r *turnRunner) evalToolsReport(accepted bool) {
	if rec := r.evalRec(); rec != nil {
		rec.ToolsReport(r.c.id, accepted)
	}
}

func (r *turnRunner) evalSnapshotReport(found, parsed bool) {
	if rec := r.evalRec(); rec != nil {
		rec.SnapshotReport(r.c.id, found, parsed)
	}
}

// carriedReads are the read IDs a tools request carries: the current
// exchange's seeded object, which its first message always holds, and each
// tool result Klyx built.
func carriedReads(seed string, msgs []ai.ConvMessage) []string {
	var ids []string
	if id := readIDOf(seed); id != "" {
		ids = append(ids, id)
	}
	for _, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		for _, res := range m.Results {
			if id := readIDOf(res.Content); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func readIDOf(envelope string) string {
	var e struct {
		ReadID string `json:"readId"`
	}
	if json.Unmarshal([]byte(envelope), &e) != nil {
		return ""
	}
	return e.ReadID
}

// EvalAnchorReady is the eval's readiness check (eval design §2): the
// anchor's row in Klyx's Observer, in a live capture evaluated after the
// plant check. Otherwise it says what it saw, never why.
func EvalAnchorReady(ctx context.Context, conn ChatConn, group, resource, uid string, since time.Time) (bool, string) {
	tp, ok := chatParts[group+"/"+resource]
	if !ok {
		return false, "Klyx's Observer has no part for " + resource
	}
	rows, ok := conn.HealthRowsNow(ctx, tp[0], tp[1])
	switch {
	case !ok:
		return false, "no Observer capture: none yet, or the Observer ended"
	case rows.Part.Coverage != observe.Live:
		return false, "the Observer's " + tp[1] + " part is " + string(rows.Part.Coverage)
	}
	if _, found := findRow(rows.Rows, uid); !found {
		return false, "no row for the anchor"
	}
	if !rows.EvaluatedAt.After(since) {
		return false, "the anchor's row was evaluated before the plant check"
	}
	return true, ""
}

package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/line"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/obslog"
)

// The Line slice 3 note §2.1, §2.3 and §5: the tap's slots and the line's
// own predicate history, without an Observer.

func tapRowsOf(cluster string, inc, ver uint64) observe.Rows {
	return observe.Rows{Cluster: cluster, Incarnation: inc, Task: healthsrc.TaskFlux, Part: observe.Part{ID: "reconciliations", Ver: ver}}
}

func TestTapOverwriteIsAHistoryGap(t *testing.T) {
	tp := newLineTap()
	tp.OnRows(tapRowsOf("c1", 1, 1))
	tp.OnRows(tapRowsOf("c1", 1, 1)) // the same version again: not a flip
	d := tp.drain()["c1"]
	if d.overwrite["flux/reconciliations"] || d.rows["flux/reconciliations"].Part.Ver != 1 {
		t.Fatalf("same version: %+v", d)
	}
	tp.OnRows(tapRowsOf("c1", 1, 2))
	tp.OnRows(tapRowsOf("c1", 1, 3)) // v2 was never consumed
	d = tp.drain()["c1"]
	if !d.overwrite["flux/reconciliations"] || d.rows["flux/reconciliations"].Part.Ver != 3 {
		t.Fatalf("an unconsumed distinct version was replaced: %+v", d)
	}
	if d := tp.drain()["c1"]; len(d.rows) != 0 || d.overwrite["flux/reconciliations"] {
		t.Fatalf("drained: %+v", d)
	}
	tp.OnRows(tapRowsOf("c1", 0, 9)) // an older incarnation
	if d := tp.drain()["c1"]; len(d.rows) != 0 {
		t.Fatal("an older incarnation's rows are ignored")
	}
	tp.OnRows(observe.Rows{Cluster: "c1", Incarnation: 1, Task: healthsrc.TaskPods, Part: observe.Part{ID: "pods"}})
	if d := tp.drain()["c1"]; len(d.rows) != 0 {
		t.Fatal("parts the line does not read are not recorded")
	}
}

func batchOn(kind string, inc, ver uint64, reason string) observe.TransitionBatch {
	b := observe.TransitionBatch{Cluster: "c1", Incarnation: inc, Task: healthsrc.TaskFlux, Part: "sources", Source: "gitrepositories", Kind: kind, PartVer: ver}
	if reason != "" {
		b.Loss = &observe.Loss{Reason: reason}
	}
	return b
}

// A burst of batches between two drains, then quiet: the worker sees the
// last state, the PartVer of the batch that set it and the history-gap bit
// (revision 4).
func TestTapPartitionKeepsTheLatestStateThroughABurst(t *testing.T) {
	pr := partitionRef{healthsrc.TaskFlux, "sources", "gitrepositories"}
	cases := []struct {
		name  string
		burst []observe.TransitionBatch
		state string
		ver   uint64
	}{
		{"ends in a loss", []observe.TransitionBatch{batchOn(observe.BatchAfterGap, 1, 4, ""), batchOn(observe.BatchLoss, 1, 5, observe.LossCoverage)}, partitionLost, 5},
		{"ends in an after-gap", []observe.TransitionBatch{batchOn(observe.BatchLoss, 1, 4, observe.LossRelist), batchOn(observe.BatchAfterGap, 1, 5, "")}, partitionAvailable, 5},
		{"ends in a baseline (cap recovery)", []observe.TransitionBatch{batchOn(observe.BatchLoss, 1, 4, observe.LossCapped), batchOn(observe.BatchBaseline, 1, 6, "")}, partitionAvailable, 6},
		{"ends absent (uninstalled)", []observe.TransitionBatch{batchOn(observe.BatchLoss, 1, 4, observe.LossAbsent)}, partitionAbsent, 4},
	}
	for _, c := range cases {
		tp := newLineTap()
		for _, b := range c.burst {
			tp.OnTransitions(b)
		}
		p := tp.drain()["c1"].partitions[pr]
		if p.state != c.state || p.ver != c.ver || !p.gap {
			t.Errorf("%s: %+v", c.name, p)
		}
		// Quiet: the state stays, and the gap bit was consumed.
		if p := tp.drain()["c1"].partitions[pr]; p.state != c.state || p.gap {
			t.Errorf("%s, quiet: %+v", c.name, p)
		}
	}
	// Two source kinds recover independently.
	tp := newLineTap()
	tp.OnTransitions(batchOn(observe.BatchLoss, 1, 4, observe.LossCoverage))
	bk := batchOn(observe.BatchLoss, 1, 4, observe.LossCoverage)
	bk.Source = "buckets"
	tp.OnTransitions(bk)
	tp.OnTransitions(batchOn(observe.BatchAfterGap, 1, 5, ""))
	d := tp.drain()["c1"]
	if d.partitions[pr].state != partitionAvailable || d.partitions[partitionRef{healthsrc.TaskFlux, "sources", "buckets"}].state != partitionLost {
		t.Fatalf("independent partitions: %+v", d.partitions)
	}
	// Incarnations: an older one is ignored, a newer one resets.
	tp.OnTransitions(batchOn(observe.BatchAfterGap, 0, 9, ""))
	tp.OnTransitions(batchOn(observe.BatchLoss, 2, 1, observe.LossCoverage))
	if p := tp.drain()["c1"].partitions[pr]; p.inc != 2 || p.state != partitionLost {
		t.Fatalf("incarnation fence: %+v", p)
	}
}

// ---- the line's own predicate history (note §5) ----------------------------------

var gitKey = line.Key{Kind: line.KeyGit, Repo: "github.com/acme/fleet", RefKind: "branch", RefName: "main"}

var h0 = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

func at0(min int) time.Time { return h0.Add(time.Duration(min) * time.Minute) }

const u1 = "line/unit/u1#0"

func failing(v bool) map[string]string {
	if v {
		return map[string]string{u1: predFault}
	}
	return map[string]string{u1: predOK}
}

func unitsAt(h *lineHistory, fault bool, min int, readable, broken bool) {
	h.commit("c1", 1, linePartUnits, unitsKustomizations, failing(fault), histCommit{readable: readable, broken: broken, at: at0(min)})
}

func heldAt(h *lineHistory, held bool, ref string, min int) {
	h.reference("c1", 1, ref, at0(min))
	v := map[string]string{keyHeld: predOK}
	if held {
		v[keyHeld] = predHeld
	}
	h.commit("c1", 1, linePartHeld, lineTask, v, histCommit{readable: true, at: at0(min)})
}

func TestLineHistoryObservedFailureAndFirstSight(t *testing.T) {
	h := newLineHistory(obslog.New())
	unitsAt(h, true, 0, true, false)
	if p := h.proof("c1", u1); p == nil || p.Basis != obslog.ProofUnknown || !p.FirstSeen.Equal(at0(0)) {
		t.Fatalf("first sight: start unknown: %+v", p)
	}
	h = newLineHistory(obslog.New())
	unitsAt(h, false, 0, true, false)
	unitsAt(h, true, 5, true, false)
	if p := h.proof("c1", u1); p == nil || p.Basis != obslog.ProofObserved || !p.Since.Equal(at0(5)) || !p.NotBefore.Equal(at0(0)) {
		t.Fatalf("observed: began in (09:00, 09:05]: %+v", p)
	}
}

// Review of 0af176c: an unchanged commit never advances the evidence
// watermark, so a later change keeps the true lower bound.
func TestLineHistoryQuietCommitsKeepTheWatermark(t *testing.T) {
	h := newLineHistory(obslog.New())
	unitsAt(h, false, 0, true, false)
	unitsAt(h, false, 7, true, false) // nothing changed
	unitsAt(h, true, 3, true, false)  // a capture from 09:03, delivered later
	if p := h.proof("c1", u1); p == nil || p.NotBefore == nil || !p.NotBefore.Equal(at0(0)) {
		t.Fatalf("the lower bound is the last change's capture, 09:00: %+v", p)
	}
}

func TestLineHistoryBreaksReBaselineAtOnce(t *testing.T) {
	h := newLineHistory(obslog.New())
	unitsAt(h, false, 0, true, false)
	unitsAt(h, true, 5, true, false) // failing since ~09:05 (observed)
	unitsAt(h, true, 9, true, true)  // a break: 09:05 is not kept
	if p := h.proof("c1", u1); p == nil || p.Basis != obslog.ProofUnknown || p.Since != nil || !p.AfterGap {
		t.Fatalf("after a break: start unknown, at once: %+v", p)
	}
	h = newLineHistory(obslog.New())
	unitsAt(h, false, 0, true, false)
	unitsAt(h, true, 5, true, false)
	unitsAt(h, true, 6, false, false) // unreadable: frozen
	unitsAt(h, true, 8, true, false)
	if p := h.proof("c1", u1); p.Basis != obslog.ProofUnknown {
		t.Fatalf("after an unreadable spell: %+v", p)
	}
}

func TestLineHistoryHeldEndsOnAReferenceChange(t *testing.T) {
	h := newLineHistory(obslog.New())
	heldAt(h, false, "a", 0)
	heldAt(h, true, "a", 2)
	if p := h.proof("c1", keyHeld); p == nil || p.Basis != obslog.ProofObserved || !p.Since.Equal(at0(2)) {
		t.Fatalf("held: %+v", p)
	}
	heldAt(h, true, "b", 4) // R changed
	if p := h.proof("c1", keyHeld); p == nil || p.Basis != obslog.ProofUnknown || !p.FirstSeen.Equal(at0(4)) {
		t.Fatalf("a held proof ends with its R: %+v", p)
	}
}

// Review of bd385e7: the station state is a line predicate too, tied to the
// reference like held.
func TestLineHistoryStateEndsOnAReferenceChange(t *testing.T) {
	h := newLineHistory(obslog.New())
	state := func(v, ref string, min int) {
		h.reference("c1", 1, ref, at0(min))
		h.commit("c1", 1, linePartState, lineTask, map[string]string{keyState: v}, histCommit{readable: true, at: at0(min)})
	}
	state(line.StateMatches, "a", 0)
	state(line.StateDiffers, "a", 3)
	if p := h.proof("c1", keyState); p == nil || p.Basis != obslog.ProofObserved || !p.Since.Equal(at0(3)) {
		t.Fatalf("state change observed: %+v", p)
	}
	state(line.StateDiffers, "b", 5)
	if p := h.proof("c1", keyState); p == nil || p.Basis != obslog.ProofUnknown {
		t.Fatalf("a reference change re-baselines the state: %+v", p)
	}
}

// Review of bd385e7: evidence captured before a partition's last change but
// delivered after it shows that change came from superseded inputs.
func TestLineHistoryOutOfOrderEvidenceBreaks(t *testing.T) {
	h := newLineHistory(obslog.New())
	unitsAt(h, false, 0, true, false)
	unitsAt(h, true, 5, true, false)
	late := at0(3)
	h.commit("c1", 1, linePartUnits, unitsKustomizations, failing(true), histCommit{readable: true, at: at0(5), trigger: &late})
	if p := h.proof("c1", u1); p == nil || p.Basis != obslog.ProofUnknown || !p.AfterGap {
		t.Fatalf("a 09:03 capture after the 09:05 change re-baselines: %+v", p)
	}
	// Two parts captured at the same instant have no order between them.
	h = newLineHistory(obslog.New())
	unitsAt(h, false, 0, true, false)
	t5 := at0(5)
	h.commit("c1", 1, linePartUnits, unitsKustomizations, failing(true), histCommit{readable: true, at: t5, trigger: &t5, part: "flux/sources"})
	h.commit("c1", 1, linePartUnits, unitsKustomizations, failing(true), histCommit{readable: true, at: t5, trigger: &t5, part: "flux/reconciliations"})
	if p := h.proof("c1", u1); p == nil || p.Basis != obslog.ProofUnknown {
		t.Fatalf("simultaneous captures of two parts re-baseline: %+v", p)
	}
}

func TestLineHistoryIsFencedByIncarnation(t *testing.T) {
	log := obslog.New()
	h := newLineHistory(log)
	unitsAt(h, false, 0, true, false)
	unitsAt(h, true, 5, true, false)
	log.Apply(observe.TransitionBatch{Cluster: "c1", Incarnation: 2, Task: "pods", Part: "pods", Source: "pods", Kind: observe.BatchBaseline, At: at0(6)})
	if p := h.proof("c1", u1); p != nil {
		t.Fatalf("a newer incarnation ends the old line proofs: %+v", p)
	}
	h.commit("c1", 2, linePartUnits, unitsKustomizations, failing(true), histCommit{readable: true, at: at0(7)})
	if p := h.proof("c1", u1); p == nil || p.Basis != obslog.ProofUnknown {
		t.Fatalf("a new incarnation re-baselines: %+v", p)
	}
}

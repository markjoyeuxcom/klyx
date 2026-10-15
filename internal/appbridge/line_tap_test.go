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
// last state, its recovery PartVer and the history-gap bit (revision 4).
func TestTapPartitionKeepsTheLatestStateThroughABurst(t *testing.T) {
	pr := partitionRef{healthsrc.TaskFlux, "sources", "gitrepositories"}
	cases := []struct {
		name  string
		burst []observe.TransitionBatch
		state string
		ver   uint64
	}{
		{"ends in a loss", []observe.TransitionBatch{batchOn(observe.BatchAfterGap, 1, 4, ""), batchOn(observe.BatchLoss, 1, 5, observe.LossCoverage)}, partitionLost, 0},
		{"ends in an after-gap", []observe.TransitionBatch{batchOn(observe.BatchLoss, 1, 4, observe.LossRelist), batchOn(observe.BatchAfterGap, 1, 5, "")}, partitionAvailable, 5},
		{"ends in a baseline (cap recovery)", []observe.TransitionBatch{batchOn(observe.BatchLoss, 1, 4, observe.LossCapped), batchOn(observe.BatchBaseline, 1, 6, "")}, partitionAvailable, 6},
		{"ends absent (uninstalled)", []observe.TransitionBatch{batchOn(observe.BatchLoss, 1, 4, observe.LossAbsent)}, partitionAbsent, 0},
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

func lineWith(failing, held bool, r string) line.Line {
	return line.Line{Key: &gitKey, R: r, Stations: []line.StationLine{{
		Placed: line.Placed{Station: line.Station{Cluster: "c1"}}, Read: line.ReadOK, State: line.StateDiffers, Held: held,
		Units: []line.Unit{{UID: "u1", Key: gitKey, Failing: failing}},
	}}}
}

func historyHarness() (*lineHistory, map[string]*lineStation, *obslog.Log) {
	log := obslog.New()
	return newLineHistory(log), map[string]*lineStation{"c1": {inc: 1, usable: true}}, log
}

var h0 = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

func at0(min int) time.Time { return h0.Add(time.Duration(min) * time.Minute) }

func TestLineHistoryObservedFailureAndFirstSight(t *testing.T) {
	h, st, _ := historyHarness()
	if p := h.commit(lineWith(true, false, "a"), st, at0(0)); p["c1"].failing == nil || p["c1"].failing.Basis != obslog.ProofUnknown || !p["c1"].failing.FirstSeen.Equal(at0(0)) {
		t.Fatalf("first sight: start unknown: %+v", p["c1"].failing)
	}
	h, st, _ = historyHarness()
	h.commit(lineWith(false, false, "a"), st, at0(0))
	p := h.commit(lineWith(true, false, "a"), st, at0(5))
	f := p["c1"].failing
	if f == nil || f.Basis != obslog.ProofObserved || !f.Since.Equal(at0(5)) || !f.NotBefore.Equal(at0(0)) {
		t.Fatalf("observed: began in (09:00, 09:05]: %+v", f)
	}
}

// A role's own proof is never borrowed: the failing predicate is dated from
// when the line saw it, whatever the object's role did.
func TestLineHistoryBreaksReBaselineAtOnce(t *testing.T) {
	h, st, _ := historyHarness()
	h.commit(lineWith(false, false, "a"), st, at0(0))
	h.commit(lineWith(true, false, "a"), st, at0(5)) // failing since ~09:05 (observed)
	// v2 (recovered) and v3 (failing) arrived while the worker was busy: v2
	// was overwritten unseen, so 09:05 is not kept.
	st["c1"].histGap = true
	p := h.commit(lineWith(true, false, "a"), st, at0(9))
	if f := p["c1"].failing; f == nil || f.Basis != obslog.ProofUnknown || f.Since != nil || !f.AfterGap {
		t.Fatalf("after a break: start unknown, at once: %+v", f)
	}
	// An unreadable station freezes; reading again re-baselines.
	h, st, _ = historyHarness()
	h.commit(lineWith(false, false, "a"), st, at0(0))
	h.commit(lineWith(true, false, "a"), st, at0(5))
	l := lineWith(true, false, "a")
	l.Stations[0].Read = line.ReadStale
	h.commit(l, st, at0(6))
	if p := h.commit(lineWith(true, false, "a"), st, at0(8)); p["c1"].failing.Basis != obslog.ProofUnknown {
		t.Fatalf("after an unreadable spell: %+v", p["c1"].failing)
	}
}

func TestLineHistoryHeldEndsOnAReferenceChange(t *testing.T) {
	h, st, _ := historyHarness()
	h.commit(lineWith(true, false, "a"), st, at0(0))
	p := h.commit(lineWith(true, true, "a"), st, at0(2)) // held under R=a, observed
	if hp := p["c1"].held; hp == nil || hp.Basis != obslog.ProofObserved || !hp.Since.Equal(at0(2)) {
		t.Fatalf("held: %+v", hp)
	}
	p = h.commit(lineWith(true, true, "b"), st, at0(4)) // R changed: the reference changed
	if hp := p["c1"].held; hp == nil || hp.Basis != obslog.ProofUnknown || !hp.FirstSeen.Equal(at0(4)) {
		t.Fatalf("a held proof ends with its R: %+v", hp)
	}
	if f := p["c1"].failing; f == nil || f.Basis != obslog.ProofUnknown || !f.FirstSeen.Equal(at0(0)) {
		t.Fatalf("the failing predicate is not tied to R: %+v", f)
	}
}

func TestLineHistoryIsFencedByIncarnation(t *testing.T) {
	h, st, log := historyHarness()
	h.commit(lineWith(false, false, "a"), st, at0(0))
	h.commit(lineWith(true, false, "a"), st, at0(5))
	log.Apply(observe.TransitionBatch{Cluster: "c1", Incarnation: 2, Task: "pods", Part: "pods", Source: "pods", Kind: observe.BatchBaseline, At: at0(6)})
	if p := h.proof("c1", unitPredKey(line.Unit{UID: "u1"})); p != nil {
		t.Fatalf("a newer incarnation ends the old line proofs: %+v", p)
	}
	st["c1"].inc = 2
	if p := h.commit(lineWith(true, false, "a"), st, at0(7)); p["c1"].failing.Basis != obslog.ProofUnknown {
		t.Fatalf("a new incarnation re-baselines: %+v", p["c1"].failing)
	}
}

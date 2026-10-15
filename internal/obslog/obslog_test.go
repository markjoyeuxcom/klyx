package obslog

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

// newLog is a log on a fixture clock at t0: expiry never depends on the
// calendar. Tests that test expiry move the clock themselves.
func newLog() *Log {
	l := New()
	l.now = func() time.Time { return t0 }
	return l
}

func ptr(t time.Time) *time.Time { return &t }

func batch(kind string, min int, items ...observe.Transition) observe.TransitionBatch {
	return observe.TransitionBatch{Cluster: "c1", Incarnation: 1, Task: "flux", Part: "sources", Source: "gitrepositories",
		Mode: observe.ModeWatch, Kind: kind, At: at(min), Items: items}
}

func item(key, from, to string) observe.Transition {
	return observe.Transition{Key: key, Name: key, From: from, To: to}
}

func proof(t *testing.T, l *Log, key string) Proof {
	t.Helper()
	p, ok, _ := l.ProofOf("c1", key)
	if !ok {
		t.Fatalf("no proof for %s", key)
	}
	return p
}

func noProof(t *testing.T, l *Log, key string) {
	t.Helper()
	if p, ok, _ := l.ProofOf("c1", key); ok {
		t.Fatalf("unexpected proof for %s: %+v", key, p)
	}
}

func TestBaselineProvesOnlyRuleStarts(t *testing.T) {
	l := newLog()
	ruled := item("a", observe.StateAbsent, "fault")
	ruled.Since = ptr(at(-90))
	l.Apply(batch(observe.BatchBaseline, 0, ruled, item("b", observe.StateAbsent, "fault"), item("c", observe.StateAbsent, "ok")))
	if p := proof(t, l, "a"); p.Basis != ProofRule || !p.Since.Equal(at(-90)) {
		t.Fatalf("a: %+v", p)
	}
	if p := proof(t, l, "b"); p.Basis != ProofUnknown || p.Since != nil || !p.FirstSeen.Equal(at(0)) {
		t.Fatalf("an observed-only baseline never proves a start: %+v", p)
	}
	noProof(t, l, "c")
	if es := l.Recent("c1", 10); len(es) != 1 || es[0].Kind != observe.BatchBaseline || es[0].Count != 3 || es[0].Faults != 2 {
		t.Fatalf("a baseline is one entry: %+v", es)
	}
}

func TestObservedSampledAndDerivedProofs(t *testing.T) {
	l := newLog()
	l.Apply(batch(observe.BatchBaseline, 0, item("a", observe.StateAbsent, "ok")))
	ch := batch(observe.BatchChanges, 5, item("a", "ok", "fault"))
	ch.PrevAt = ptr(at(4))
	l.Apply(ch)
	if p := proof(t, l, "a"); p.Basis != ProofObserved || !p.NotBefore.Equal(at(4)) || !p.Since.Equal(at(5)) {
		t.Fatalf("observed: began in (prevAt, at]: %+v", p)
	}

	s := batch(observe.BatchChanges, 10, item("r", "ok", "fault"))
	s.Mode, s.PrevReadAt, s.ReadAt = observe.ModeSampled, ptr(at(0)), ptr(at(5))
	l.Apply(s)
	if p := proof(t, l, "r"); p.Basis != ProofSampled || !p.NotBefore.Equal(at(0)) || !p.Since.Equal(at(5)) {
		t.Fatalf("sampled: between the reads: %+v", p)
	}

	d := item("q", "progressing", "degraded")
	d.Derived, d.Boundary = true, ptr(at(11))
	db := batch(observe.BatchChanges, 13, d)
	db.Mode, db.PrevReadAt, db.ReadAt = observe.ModeSampled, ptr(at(10)), ptr(at(10))
	l.Apply(db)
	if p := proof(t, l, "q"); p.Basis != ProofDerived || p.Since != nil || !p.Boundary.Equal(at(11)) || !p.ReadAt.Equal(at(10)) {
		t.Fatalf("derived: the boundary and its read, start unknown - never between 12:10 and 12:10: %+v", p)
	}
}

func TestRestatedUpdatesTheRuleStart(t *testing.T) {
	l := newLog()
	a := item("a", observe.StateAbsent, "fault")
	a.Since = ptr(at(-60))
	l.Apply(batch(observe.BatchBaseline, 0, a))
	r := item("a", "fault", "fault")
	r.Restated, r.Since = true, ptr(at(3))
	l.Apply(batch(observe.BatchChanges, 4, r))
	if p := proof(t, l, "a"); p.Basis != ProofRule || !p.Since.Equal(at(3)) {
		t.Fatalf("restated: %+v", p)
	}
	r.Since = nil
	l.Apply(batch(observe.BatchChanges, 5, r))
	if p := proof(t, l, "a"); p.Basis != ProofUnknown || p.Since != nil {
		t.Fatalf("a vanished Since downgrades: %+v", p)
	}
}

func TestLossSuspendsAndAfterGapDowngradesObservedProofs(t *testing.T) {
	l := newLog()
	ruled := item("r", observe.StateAbsent, "fault")
	ruled.Since = ptr(at(-60))
	l.Apply(batch(observe.BatchBaseline, 0, ruled, item("o", observe.StateAbsent, "ok")))
	ch := batch(observe.BatchChanges, 5, item("o", "ok", "fault"))
	ch.PrevAt = ptr(at(4))
	l.Apply(ch)
	loss := batch(observe.BatchLoss, 6)
	loss.Loss = &observe.Loss{Reason: observe.LossCoverage, Coverage: observe.Stale}
	l.Apply(loss)
	if p := proof(t, l, "o"); !p.Suspended {
		t.Fatalf("a loss suspends: %+v", p)
	}
	if ps := l.Partitions("c1", "flux", "sources"); len(ps) != 1 || ps[0].Live || ps[0].Loss.Reason != observe.LossCoverage {
		t.Fatalf("partition: %+v", ps)
	}
	l.Apply(batch(observe.BatchAfterGap, 9))
	if p := proof(t, l, "o"); p.Basis != ProofUnknown || p.Since != nil || !p.AfterGap || p.Suspended {
		t.Fatalf("an observed start is not proven across a gap: %+v", p)
	}
	if p := proof(t, l, "r"); p.Basis != ProofRule || !p.Since.Equal(at(-60)) {
		t.Fatalf("a rule start survives the gap: %+v", p)
	}
}

func TestGoneOkAndUnevaluatedCloseProofs(t *testing.T) {
	l := newLog()
	l.Apply(batch(observe.BatchBaseline, 0, item("a", observe.StateAbsent, "fault"), item("b", observe.StateAbsent, "fault"), item("c", observe.StateAbsent, "fault")))
	l.Apply(batch(observe.BatchChanges, 1, item("a", "fault", observe.StateGone), item("b", "fault", "ok"), item("c", "fault", observe.StateUnevaluated)))
	noProof(t, l, "a")
	noProof(t, l, "b")
	noProof(t, l, "c")
}

func TestCapAndAbsentLossesDropAndABaselineReplaces(t *testing.T) {
	l := newLog()
	l.Apply(batch(observe.BatchBaseline, 0, item("old", observe.StateAbsent, "fault")))
	capped := batch(observe.BatchLoss, 1)
	capped.Loss = &observe.Loss{Reason: observe.LossCapped}
	l.Apply(capped)
	noProof(t, l, "old")
	// Back under the cap without the old fault: the replacement holds only
	// what it lists.
	b := batch(observe.BatchBaseline, 2, item("new", observe.StateAbsent, "fault"))
	b.AfterCap = true
	l.Apply(b)
	noProof(t, l, "old")
	proof(t, l, "new")
	// A replacement also ends proofs that were never dropped.
	l.Apply(batch(observe.BatchBaseline, 3, item("other", observe.StateAbsent, "ok")))
	noProof(t, l, "new")
}

func TestIncarnationsAndEnd(t *testing.T) {
	l := newLog()
	l.Apply(batch(observe.BatchBaseline, 0, item("a", observe.StateAbsent, "fault")))
	newer := batch(observe.BatchBaseline, 1, item("b", observe.StateAbsent, "fault"))
	newer.Incarnation = 2
	l.Apply(newer)
	noProof(t, l, "a") // superseded
	proof(t, l, "b")
	late := batch(observe.BatchChanges, 2, item("z", "ok", "fault"))
	late.Incarnation = 1 // an old Observer completing after its replacement
	l.Apply(late)
	noProof(t, l, "z")
	if n := len(l.Recent("c1", 0)); n != 2 {
		t.Fatalf("entries of both incarnations are kept: %d", n)
	}
	end := batch(observe.BatchEnd, 3)
	end.Incarnation = 2
	l.Apply(end)
	noProof(t, l, "b")
	after := batch(observe.BatchChanges, 4, item("y", "ok", "fault"))
	after.Incarnation = 2
	l.Apply(after)
	noProof(t, l, "y")
}

func TestEntryAndProofBounds(t *testing.T) {
	l := newLog()
	l.maxEntries, l.maxProofs = 3, 2
	l.Apply(batch(observe.BatchBaseline, 0))
	for i := 1; i <= 4; i++ {
		ch := batch(observe.BatchChanges, i, item(string(rune('a'+i)), "ok", "fault"))
		ch.PrevAt = ptr(at(i - 1))
		l.Apply(ch)
	}
	if es := l.Recent("c1", 0); len(es) != 3 || es[0].Key != "e" {
		t.Fatalf("entries trimmed oldest first: %+v", es)
	}
	if _, ok, capped := l.ProofOf("c1", "e"); ok || !capped {
		t.Fatalf("a proof past the cap is marked capped, not kept")
	}
	now := at(24*60 + 10)
	l.now = func() time.Time { return now }
	l.Apply(batch(observe.BatchChanges, 24*60+10, item("z", "ok", "ok")))
	if oldest, _ := l.OldestEntry("c1"); oldest.Before(at(10)) {
		t.Fatalf("entries older than 24h are trimmed: oldest %v", oldest)
	}
	// A quiet log expires on reads, against the clock, with no new batch.
	now = at(3 * 24 * 60)
	if es := l.Recent("c1", 0); len(es) != 0 {
		t.Fatalf("a quiet log keeps expired entries: %+v", es)
	}
}

// Review of a0c5cf9: an ended cluster terminally marks its partitions and,
// once its history expires, keeps only the incarnation fence.
func TestEndMarksPartitionsAndExpiresToAFence(t *testing.T) {
	l := newLog()
	now := at(0)
	l.now = func() time.Time { return now }
	l.Apply(batch(observe.BatchBaseline, 0, item("a", observe.StateAbsent, "fault")))
	l.Apply(batch(observe.BatchEnd, 1))
	ps := l.Partitions("c1", "flux", "sources")
	if len(ps) != 1 || ps[0].Live || ps[0].Loss == nil || ps[0].Loss.Reason != LossEnded {
		t.Fatalf("ended partitions: %+v", ps)
	}
	now = at(25 * 60)
	if es := l.Recent("c1", 0); len(es) != 0 || len(l.Partitions("c1", "flux", "sources")) != 0 {
		t.Fatal("an expired ended cluster keeps nothing but its fence")
	}
	late := batch(observe.BatchChanges, 2, item("z", "ok", "fault"))
	l.Apply(late) // same incarnation, after end: still rejected
	noProof(t, l, "z")
}

// Review of a0c5cf9: an after-gap is recorded even with no changed items, so
// the history keeps when coverage returned.
func TestAfterGapIsAlwaysLogged(t *testing.T) {
	l := newLog()
	l.Apply(batch(observe.BatchBaseline, 0))
	loss := batch(observe.BatchLoss, 1)
	loss.Loss = &observe.Loss{Reason: observe.LossCoverage}
	l.Apply(loss)
	gap := batch(observe.BatchAfterGap, 3)
	gap.PrevAt = ptr(at(1))
	l.Apply(gap)
	if es := l.Recent("c1", 1); len(es) != 1 || es[0].Kind != observe.BatchAfterGap || !es[0].PrevAt.Equal(at(1)) {
		t.Fatalf("after-gap marker: %+v", es)
	}
}

// Review of 8b0b822: once proofs and markers are both at the cap, a further
// non-ok key still reads capped (its partition's coverage is incomplete),
// and a replacement clears that disclosure.
func TestMarkerExhaustionStillDisclosesTheCap(t *testing.T) {
	l := newLog()
	l.maxProofs = 1
	l.Apply(batch(observe.BatchBaseline, 0, item("a", observe.StateAbsent, "fault"), item("b", observe.StateAbsent, "fault"), item("c", observe.StateAbsent, "fault")))
	n := 0
	for _, k := range []string{"a", "b", "c"} {
		if _, ok, capped := l.ProofOf("c1", k); ok {
			n++
		} else if !capped {
			t.Fatalf("%s: neither proved nor disclosed as capped", k)
		}
	}
	if n != 1 {
		t.Fatalf("one proof kept under maxProofs=1, got %d", n)
	}
	l.Apply(batch(observe.BatchBaseline, 1))
	if _, _, capped := l.ProofOf("c1", "c"); capped {
		t.Fatal("a replacement clears the overflow disclosure")
	}
}

// Review of 8b0b822: expiry follows the log's clock, never the calendar -
// fixtures far older than 24h by the wall clock still behave.
func TestFixturesNeverExpireByTheCalendar(t *testing.T) {
	l := newLog()
	old := t0.Add(-72 * time.Hour)
	l.now = func() time.Time { return old }
	b := batch(observe.BatchBaseline, 0, item("a", observe.StateAbsent, "fault"))
	b.At = old
	l.Apply(b)
	if es := l.Recent("c1", 0); len(es) != 1 {
		t.Fatalf("an old fixture expired by the calendar: %+v", es)
	}
}

package fluxop

import (
	"sort"
	"sync"
	"time"
)

// Resume is a resumed object's evidence state (S1a §4, revisions 3 and 4).
type Resume int

const (
	// Current: no resume is under observation, so the conditions are
	// current - Klyx never saw the object disabled, or the resume is over.
	Current Resume = iota
	// Awaiting: resumed, with no evidence of an evaluation since.
	Awaiting
	// Evaluating: evidence of a run, Ready still in progress.
	Evaluating
	// Unproven: Klyx let go of this object's baseline while it was out of
	// its reads (MaxBaselines), so a resume since cannot be told from an
	// evaluated one. Evidence ends it as it ends Awaiting, judged against
	// the state first seen again (Codex's S1a implementation review,
	// finding 7).
	Unproven
)

// Tracker keeps, per object UID, the resume baseline: the evaluation
// fingerprint of the last state Klyx observed with reconciliation disabled.
// One per observer (per cluster); safe for concurrent use.
//
// A baseline ends only on evidence, or when a complete capture of its kind
// proves the object gone (Prune). Age never ends one: an object the budget
// left out is absent from captures without having departed. Memory is
// bounded instead: past MaxBaselines the least recently captured become
// lost markers (the UID alone), which make the object Unproven, never
// Current, when it is captured again.
//
// Past MaxLost the oldest markers are dropped, and their kind becomes
// saturated (Codex's S1a implementation review 2, finding 1): forgetting a
// UID never authorizes Current. While a kind is saturated, any of its
// objects without a baseline reads Unproven on first sight, and gets an
// unproven baseline; one proven since is remembered. A complete capture in
// which every present object is tracked or proven ends the saturation: a
// UID absent from it has departed, and one seen later is new.
type Tracker struct {
	mu        sync.Mutex
	base      map[string]baseline
	lost      map[string]lostMark
	saturated map[string]bool
	proven    map[string]map[string]bool // kind -> UIDs proven current while saturated
}

type baseline struct {
	kind     string
	at       time.Time // the baseline state's capture
	seen     time.Time // the last capture that held the object
	fp       [32]byte
	hasReady bool
	exported string
	unproven bool // rebuilt from a lost marker: no disabled state behind it
}

type lostMark struct {
	kind string
	seen time.Time
}

// The tracker's bounds. Past MaxLost the oldest lost markers are dropped
// and their kind saturates; past MaxBaselines proven UIDs, a saturated
// kind forgets them, and they read Unproven again until evidence: both
// fail closed.
const (
	MaxBaselines = 1024
	MaxLost      = 4096
)

// NewTracker returns an empty tracker: a Klyx that starts after a resume
// has no baseline, and treats the object as current.
func NewTracker() *Tracker {
	return &Tracker{base: map[string]baseline{}, lost: map[string]lostMark{}, saturated: map[string]bool{}, proven: map[string]map[string]bool{}}
}

// Observe classifies one object's state, captured at at.
//
//   - Disabled: the state becomes the baseline (a later observation of a
//     still-disabled object replaces it).
//   - Enabled without a baseline: Current.
//   - Enabled with one: evidence is a fingerprint unlike the baseline's,
//     unless Ready's reason is SkippedDueToSchedule or
//     ReconciliationDisabled, which are never evidence whenever they
//     arrive; for a provider, a changed lastExportedRevision is evidence
//     too. Evidence with Ready in progress is Evaluating; evidence with a
//     final Ready ends the resume, and the object is Current from then.
//
// States are compared, not events, so a watch gap loses nothing. No time is
// compared with another; at only orders this tracker's own captures.
func (t *Tracker) Observe(at time.Time, o *Object, c Common, exported string) Resume {
	t.mu.Lock()
	defer t.mu.Unlock()
	fp, has := o.Fingerprint()
	b, had := t.base[c.UID]
	if c.Disabled {
		if !had || !at.Before(b.at) {
			t.base[c.UID] = baseline{kind: c.Kind, at: at, seen: at, fp: fp, hasReady: has, exported: exported}
			delete(t.lost, c.UID)
		}
		return Current
	}
	if !had {
		_, lost := t.lost[c.UID]
		if lost || (t.saturated[c.Kind] && !t.proven[c.Kind][c.UID]) {
			delete(t.lost, c.UID)
			t.base[c.UID] = baseline{kind: c.Kind, at: at, seen: at, fp: fp, hasReady: has, exported: exported, unproven: true}
			return Unproven
		}
		return Current
	}
	waiting := Awaiting
	if b.unproven {
		waiting = Unproven
	}
	if at.After(b.seen) {
		b.seen = at
		t.base[c.UID] = b
	}
	if at.Before(b.at) {
		return waiting // a capture older than the baseline proves nothing
	}
	evidence := false
	if c.Reason != ReasonSkippedSchedule && c.Reason != ReasonDisabled && (has != b.hasReady || fp != b.fp) {
		evidence = true
	}
	if exported != "" && exported != b.exported {
		evidence = true
	}
	switch {
	case !evidence:
		return waiting
	case inProgress(c):
		return Evaluating
	}
	delete(t.base, c.UID)
	if t.saturated[c.Kind] {
		if t.proven[c.Kind] == nil {
			t.proven[c.Kind] = map[string]bool{}
		}
		t.proven[c.Kind][c.UID] = true
	}
	return Current
}

// inProgress: Ready says a run is under way.
func inProgress(c Common) bool {
	return c.Ready == Reconciling || (c.Ready == Unknown && c.Reason == ReasonProgressing)
}

// Prune ends what a capture of kind proves: when the capture is complete
// (live, nothing left out), a baseline or lost marker whose UID it does not
// hold is a departed object. An incomplete capture proves nothing by
// absence. Then the bounds apply.
func (t *Tracker) Prune(kind string, present map[string]bool, complete bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if complete {
		for uid, b := range t.base {
			if b.kind == kind && !present[uid] {
				delete(t.base, uid)
			}
		}
		for uid, m := range t.lost {
			if m.kind == kind && !present[uid] {
				delete(t.lost, uid)
			}
		}
		for uid := range t.proven[kind] {
			if !present[uid] {
				delete(t.proven[kind], uid)
			}
		}
		if t.saturated[kind] && t.trackedLocked(kind, present) {
			delete(t.saturated, kind)
			delete(t.proven, kind)
		}
	}
	if over := len(t.base) - MaxBaselines; over > 0 {
		for _, uid := range oldest(t.base, func(b baseline) time.Time { return b.seen }, over) {
			t.lost[uid] = lostMark{kind: t.base[uid].kind, seen: t.base[uid].seen}
			delete(t.base, uid)
		}
	}
	if over := len(t.lost) - MaxLost; over > 0 {
		for _, uid := range oldest(t.lost, func(m lostMark) time.Time { return m.seen }, over) {
			t.saturated[t.lost[uid].kind] = true
			delete(t.lost, uid)
		}
	}
	for k, uids := range t.proven {
		if len(uids) > MaxBaselines {
			delete(t.proven, k)
		}
	}
}

// trackedLocked: every present object of kind has a baseline or was proven
// current while the kind was saturated.
func (t *Tracker) trackedLocked(kind string, present map[string]bool) bool {
	for uid := range present {
		if _, ok := t.base[uid]; !ok && !t.proven[kind][uid] {
			return false
		}
	}
	return true
}

// Saturated reports whether kind has forgotten lost markers.
func (t *Tracker) Saturated(kind string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.saturated[kind]
}

// oldest is the n least recently seen keys of m.
func oldest[V any](m map[string]V, seen func(V) time.Time, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := seen(m[keys[i]]), seen(m[keys[j]])
		if !a.Equal(b) {
			return a.Before(b)
		}
		return keys[i] < keys[j]
	})
	return keys[:n]
}

// Len is the number of baselines held; Lost the number of lost markers.
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.base)
}

func (t *Tracker) Lost() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.lost)
}

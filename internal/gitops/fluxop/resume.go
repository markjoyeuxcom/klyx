package fluxop

import (
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
)

// Tracker keeps, per object UID, the resume baseline: the evaluation
// fingerprint of the last state Klyx observed with reconciliation disabled.
// One per observer (per cluster); safe for concurrent use.
type Tracker struct {
	mu   sync.Mutex
	base map[string]baseline
}

type baseline struct {
	at       time.Time
	fp       [32]byte
	hasReady bool
	exported string
}

// NewTracker returns an empty tracker: a Klyx that starts after a resume
// has no baseline, and treats the object as current.
func NewTracker() *Tracker { return &Tracker{base: map[string]baseline{}} }

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
			t.base[c.UID] = baseline{at: at, fp: fp, hasReady: has, exported: exported}
		}
		return Current
	}
	if !had {
		return Current
	}
	if at.Before(b.at) {
		return Awaiting // a capture older than the baseline proves nothing
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
		return Awaiting
	case inProgress(c):
		return Evaluating
	}
	delete(t.base, c.UID)
	return Current
}

// inProgress: Ready says a run is under way.
func inProgress(c Common) bool {
	return c.Ready == Reconciling || (c.Ready == Unknown && c.Reason == ReasonProgressing)
}

// Retain drops the baselines of objects no longer observed. Call it only
// with a complete read: a kind the budget truncated holds objects it did
// not retain.
func (t *Tracker) Retain(uids map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for uid := range t.base {
		if !uids[uid] {
			delete(t.base, uid)
		}
	}
}

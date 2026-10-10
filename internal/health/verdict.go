// Package health is Klyx's single classification of "is this object OK?".
// Every lamp, tone, attention count and verdict word in the UI derives from
// the Verdict an evaluator here returns; no view computes health locally.
//
// The package is pure: it takes typed API objects (or plain inputs) and a
// "now", never a client. Spec of record:
// docs/superpowers/specs/2026-10-10-klyx-redesign-health-model.md.
//
// Time convention: an allowance is "within" while elapsed < allowance. At the
// instant elapsed == allowance the verdict flips, and that instant is what
// ReclassifyAt reports.
package health

import "time"

// Kind is the closed verdict set shared by every domain.
type Kind int

// Declared in triage order (worst first), so Kind doubles as the sort key.
const (
	Fault       Kind = iota // failing now; needs an operator
	Degraded                // partially serving, overdue or drifting
	Unknown                 // cannot be determined
	Progressing             // moving toward desired state within its allowance
	Paused                  // intentionally stopped
	OK                      // healthy
)

// String is the pinned lowercase API value.
func (k Kind) String() string {
	switch k {
	case Fault:
		return "fault"
	case Degraded:
		return "degraded"
	case Unknown:
		return "unknown"
	case Progressing:
		return "progressing"
	case Paused:
		return "paused"
	default:
		return "ok"
	}
}

// Attention reports whether the verdict counts toward "N attention".
func (k Kind) Attention() bool { return k == Fault || k == Degraded }

// Verdict is one object's classification.
type Verdict struct {
	Kind Kind
	// Label is the actionable, tool-native headline ("ImagePullBackOff",
	// "Exited 1", "NotReady"). The same string heads list rows and inspectors.
	Label string
	// Reason is a stable machine code for the cause.
	Reason string
	// Message is the full diagnostic text; never truncated here.
	Message string
	// Native is the underlying native state, shown secondarily
	// ("phase Pending · Ready=False").
	Native string
	// Note carries history that must never headline ("restarted 4m ago").
	Note string
	// Rule is the stable ID of the rule that fired (explain/debug payload).
	Rule string
	// Rank orders verdicts of the same Kind (higher = worse), e.g. a
	// CrashLoopBackOff outranks an Unschedulable pod.
	Rank int
	// ReclassifyAt is the next time-threshold crossing that could change this
	// verdict with no object change; nil when none is pending.
	ReclassifyAt *time.Time
}

// Thresholds. These are product heuristics, stated as such wherever a
// verdict they produce is shown.
const (
	// PodStartGrace bounds how long a Pending pod (or a Running pod that has
	// not yet reported readiness) reads as progressing.
	PodStartGrace = 120 * time.Second
	// ReadyGrace bounds how long a pod whose Ready condition turned False
	// reads as progressing, timed from that transition.
	ReadyGrace = 60 * time.Second
	// StuckTerminating is allowed past a pod's deletion deadline
	// (metadata.deletionTimestamp, which already includes the grace period).
	StuckTerminating = 5 * time.Minute
	// RolloutStallGrace is allowed past a Deployment's own progress deadline
	// before a rollout the controller still calls progressing reads stalled
	// (covers a controller that never marks ProgressDeadlineExceeded).
	RolloutStallGrace = 10 * time.Minute
	// RecentTermination is how long a container termination is worth a note.
	RecentTermination = time.Hour
)

// within reports whether elapsed since anchor is still inside allow, and the
// instant it stops being so. An anchor in the future (clock skew) counts as
// elapsed zero.
func within(anchor time.Time, allow time.Duration, now time.Time) (bool, time.Time) {
	deadline := anchor.Add(allow)
	return now.Before(deadline), deadline
}

func at(t time.Time) *time.Time { return &t }

package health

import (
	"time"

	"github.com/moomora/klyx/internal/scrub"
)

// NativeEval is a relocated lens rule's verdict for one object (1c addendum
// §2). A native lens has two rules that disagree on purpose: an inclusion
// predicate (what its attention filter and header count) and a presentation
// role. They are kept separate and verbatim until each domain's own
// decision table is reviewed - e.g. an Issuing certificate is Included with
// the progressing Role.
type NativeEval struct {
	Included bool
	Role     Kind
	Label    string
	// Next is the time boundary the rule depends on, if any (an expiry, a
	// refresh deadline, a launch deadline); nil when none is pending.
	Next *time.Time
	// Since is when the current role began, only when the rule can prove it:
	// the role is a function of inputs that have not changed since, and every
	// change of those inputs is timestamped (a condition read alone, a field
	// that never changes, or a time derived from fields). An overlay that can
	// end without a timestamp - an unobserved spec, a reconcile, a suspension
	// - makes the start unprovable, and Since stays nil: the age comes from
	// Klyx's own observation instead (The Line §8.1).
	Since *time.Time
	// Boundary is the instant a time-only threshold flipped this object's
	// role (a pending deadline, a launch deadline, an overdue refresh, an
	// expiry), set while the role is the crossed one: evidence for a derived
	// change, never the role's start (The Line slice 2b §1.1).
	Boundary *time.Time
	// Cond is the condition the role was read from, with its own transition
	// time: qualified evidence ("Ready=False since 09:00, condition time"),
	// never the role's start. Nil when no condition decided the role or its
	// time is absent.
	Cond *CondSince
	// Reason is the rule's own reason and message for a non-ok role,
	// "reason: message", capped by CapReason; empty when ok or unknown.
	Reason string
}

// CondSince is one condition's own transition: it has had Status since At.
type CondSince struct {
	Type, Status string
	At           time.Time
}

// CondAt returns the condition evidence, nil when at is zero (an absent or
// invalid lastTransitionTime proves nothing).
func CondAt(condType, status string, at time.Time) *CondSince {
	if at.IsZero() {
		return nil
	}
	return &CondSince{Type: condType, Status: status, At: at}
}

// ReasonMax caps NativeEval.Reason (and the Ref that carries it).
const ReasonMax = 200

// CapReason joins a condition's reason and message, scrubs credential shapes
// from the whole text, then caps it at ReasonMax runes with an ellipsis. The
// scrub runs first, so a secret crossing the cap is replaced whole, never
// left as a readable prefix (The Line §8.1).
func CapReason(reason, message string) string {
	s := reason
	switch {
	case s == "":
		s = message
	case message != "":
		s = reason + ": " + message
	}
	s, _ = scrub.Credentials(s)
	if r := []rune(s); len(r) > ReasonMax {
		return string(r[:ReasonMax-1]) + "…"
	}
	return s
}

// At returns t as a NativeEval boundary.
func At(t time.Time) *time.Time { return &t }

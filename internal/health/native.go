package health

import "time"

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
	// Since is when the current role began, only when the rule can back it
	// with a timestamp that matches the role (a condition's
	// lastTransitionTime, or a time derived from fields); nil otherwise, so
	// the age comes from Klyx's own observation instead (The Line §8.1).
	Since *time.Time
	// Reason is the rule's own reason and message for a non-ok role,
	// "reason: message", capped by CapReason; empty when ok or unknown.
	Reason string
}

// ReasonMax caps NativeEval.Reason (and the Ref that carries it).
const ReasonMax = 200

// CapReason joins a condition's reason and message and caps the result at
// ReasonMax runes, marking a cut with an ellipsis.
func CapReason(reason, message string) string {
	s := reason
	switch {
	case s == "":
		s = message
	case message != "":
		s = reason + ": " + message
	}
	if r := []rune(s); len(r) > ReasonMax {
		return string(r[:ReasonMax-1]) + "…"
	}
	return s
}

// At returns t as a NativeEval boundary.
func At(t time.Time) *time.Time { return &t }

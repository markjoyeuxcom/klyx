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
}

// At returns t as a NativeEval boundary.
func At(t time.Time) *time.Time { return &t }

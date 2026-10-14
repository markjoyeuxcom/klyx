package certmanager

import (
	"time"

	"github.com/moomora/klyx/internal/health"
)

// Expired: a known NotAfter at or before now, at the second granularity the
// lens has always used (CertsView.expired).
func Expired(c Certificate, now time.Time) bool {
	return !c.NotAfter.IsZero() && c.NotAfter.Unix() <= now.Unix()
}

// CertificateRule is the Certificates lens rule, relocated verbatim
// (CertsView.needsAttention / certHealth): Included = not Ready or expired;
// the role puts absolute expiry first, then an unobserved spec (Syncing…).
// Next is the expiry boundary while the certificate has not expired.
func CertificateRule(c Certificate, now time.Time) health.NativeEval {
	expired := Expired(c, now)
	e := health.NativeEval{Included: c.Ready != Ready || expired}
	switch {
	case expired:
		e.Role, e.Label = health.Fault, "Expired"
	case c.Stale:
		e.Role, e.Label = health.Progressing, "Syncing…"
	case c.Ready == Failed:
		e.Role, e.Label = health.Fault, "Failed"
	case c.Ready == Issuing:
		e.Role, e.Label = health.Progressing, "Issuing"
	case c.Ready == Ready:
		e.Role, e.Label = health.OK, "Ready"
	case c.Ready == Unknown || c.Ready == "":
		e.Role, e.Label = health.Unknown, "Unknown"
	default:
		e.Role, e.Label = health.Degraded, string(c.Ready)
	}
	if !expired && !c.NotAfter.IsZero() {
		e.Next = health.At(time.Unix(c.NotAfter.Unix(), 0))
	}
	certificateDated(&e, c, expired)
	return e
}

// certificateDated fills Since, Cond and Reason (The Line §8.1); Reason
// never depends on a time. Expiry is derived: the role began at notAfter, and
// it overrides every other role. A role read from the Ready condition
// carries Ready's own time as Cond. Since is set only for a fault whose start
// is provable:
//   - generation 1: the spec never changed, so no unobserved spec (Syncing...)
//     can have overlaid the fault and ended without changing Ready - every
//     condition write was for generation 1;
//   - an Issuing condition is present: every Issuing change since then is
//     timestamped (a removal and re-add would reset its time), so an issuance
//     in flight cannot have come and gone unseen;
//   - an expiry in between is a fault too, so it never interrupts the role.
//
// The fault then began at the later of Ready's and Issuing's times. Unknown
// and Ready=True stay undated: an expiry can overlay them and end without a
// timestamp (a replaced Secret moves notAfter), and a successful renewal
// removes Issuing without one.
func certificateDated(e *health.NativeEval, c Certificate, expired bool) {
	if expired {
		at := time.Unix(c.NotAfter.Unix(), 0).UTC()
		e.Since = &at
		e.Reason = health.CapReason("Expired", "notAfter "+at.UTC().Format(time.RFC3339))
		return
	}
	if e.Role == health.Fault || e.Role == health.Degraded {
		e.Reason = health.CapReason(c.Reason, c.Message)
	}
	if e.Role == health.Progressing {
		return
	}
	e.Cond = health.CondAt("Ready", certReadyStatus(c.Ready), c.ReadySince)
	if e.Role != health.Fault || c.Generation != 1 || c.ReadySince.IsZero() || c.IssuingChanged.IsZero() {
		return
	}
	since := c.ReadySince
	if c.IssuingChanged.After(since) {
		since = c.IssuingChanged
	}
	e.Since = &since
}

// certReadyStatus is the Ready condition status a non-issuing state was read
// from.
func certReadyStatus(r ReadyState) string {
	switch r {
	case Ready:
		return "True"
	case Failed:
		return "False"
	default:
		return "Unknown"
	}
}

// IssuerRule is the issuers view rule, relocated verbatim (issuerHealth; the
// header counts ready !== Ready).
func IssuerRule(i Issuer) health.NativeEval {
	e := health.NativeEval{Included: i.Ready != Ready}
	switch {
	case i.Ready == Ready:
		e.Role, e.Label = health.OK, "Ready"
	case i.Ready == Failed:
		e.Role, e.Label = health.Fault, "Failed"
	case i.Ready == Unknown || i.Ready == "":
		e.Role, e.Label = health.Unknown, "Unknown"
	default:
		e.Role, e.Label = health.Degraded, string(i.Ready)
	}
	// Every issuer role is a function of the Ready condition alone (no
	// overlay), so Ready's lastTransitionTime is the role's start (The Line
	// §8.1).
	if !i.ReadySince.IsZero() {
		since := i.ReadySince
		e.Since = &since
	}
	e.Cond = health.CondAt("Ready", certReadyStatus(i.Ready), i.ReadySince)
	if e.Role != health.OK && e.Role != health.Unknown {
		e.Reason = health.CapReason(i.Reason, i.Message)
	}
	return e
}

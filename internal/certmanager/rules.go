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

// certificateDated fills Since and Reason (The Line §8.1). Expiry is derived:
// the role began at notAfter. A role read from the Ready condition (failed,
// degraded, unknown) began at Ready's lastTransitionTime, or at the Issuing
// condition's if later - a failed issuance flips Issuing to False while Ready
// stays False - so the later time never overstates the age. An unobserved
// spec or an issuance in flight is not what either time records. Ready=True
// keeps Since nil too: a successful renewal removes the Issuing condition
// without a timestamp, so Ready's time would overstate how long the
// certificate has read Ready rather than Issuing.
func certificateDated(e *health.NativeEval, c Certificate, expired bool) {
	switch {
	case expired:
		at := time.Unix(c.NotAfter.Unix(), 0)
		e.Since = &at
		e.Reason = health.CapReason("Expired", "notAfter "+at.UTC().Format(time.RFC3339))
		return
	case e.Role == health.Progressing || e.Role == health.OK || c.ReadySince.IsZero():
		return
	}
	since := c.ReadySince
	if c.IssuingChanged.After(since) {
		since = c.IssuingChanged
	}
	e.Since = &since
	if e.Role != health.Unknown {
		e.Reason = health.CapReason(c.Reason, c.Message)
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
	// Every issuer role is read from the Ready condition alone, so Ready's
	// lastTransitionTime dates it (The Line §8.1).
	if !i.ReadySince.IsZero() {
		since := i.ReadySince
		e.Since = &since
	}
	if e.Role != health.OK && e.Role != health.Unknown {
		e.Reason = health.CapReason(i.Reason, i.Message)
	}
	return e
}

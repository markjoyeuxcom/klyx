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
	return e
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
	return e
}

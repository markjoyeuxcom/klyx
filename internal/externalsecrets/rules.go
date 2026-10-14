package externalsecrets

import (
	"fmt"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// ExternalSecretRule is the External Secrets lens rule, relocated verbatim
// (ExternalSecretsView.needsAttention / secretHealth): Included = not Synced
// or stale (refresh overdue). Next is the moment a periodically refreshed
// secret goes stale (refreshTime + 2x interval) while it has not.
func ExternalSecretRule(e ExternalSecret) health.NativeEval {
	ev := health.NativeEval{Included: e.State != Synced || e.Stale}
	switch {
	case e.State == Failed:
		ev.Role, ev.Label = health.Fault, "Failed"
	case e.State == Synced && e.Stale:
		ev.Role, ev.Label = health.Degraded, "Stale"
	case e.State == Synced:
		ev.Role, ev.Label = health.OK, "Synced"
	case e.State == Unknown || e.State == "":
		ev.Role, ev.Label = health.Unknown, "Unknown"
	default:
		ev.Role, ev.Label = health.Degraded, string(e.State)
	}
	if !e.Stale && e.RefreshesPeriodically() && !e.RefreshTime.IsZero() {
		ev.Next = health.At(e.StaleAt())
	}
	secretDated(&ev, e)
	return ev
}

// secretDated fills Since and Reason (The Line §8.1). Stale is derived: the
// role began at the first stale instant (or when Ready last turned True, if
// later). The other roles are read from the Ready condition, so its
// lastTransitionTime dates them - except a periodically refreshed Synced
// secret: it may have been stale in between, and a refresh that ended that
// leaves no time but refreshTime, so Ready's time would overstate the age.
func secretDated(ev *health.NativeEval, e ExternalSecret) {
	if e.State == Synced && e.Stale {
		since := e.StaleAt()
		if e.ReadySince.After(since) {
			since = e.ReadySince
		}
		ev.Since = &since
		ev.Reason = health.CapReason("", fmt.Sprintf("refresh overdue: last refresh %s, interval %s",
			e.RefreshTime.UTC().Format(time.RFC3339), e.RefreshInterval))
		return
	}
	if e.ReadySince.IsZero() || (ev.Role == health.OK && e.RefreshesPeriodically()) {
		return
	}
	since := e.ReadySince
	ev.Since = &since
	if ev.Role != health.OK && ev.Role != health.Unknown {
		ev.Reason = health.CapReason(e.Reason, e.Message)
	}
}

// StoreRule is the stores view rule, relocated verbatim (storeHealth; the
// header counts state !== Synced).
func StoreRule(s Store) health.NativeEval {
	ev := health.NativeEval{Included: s.State != Synced}
	switch {
	case s.State == Synced:
		ev.Role, ev.Label = health.OK, "Ready"
	case s.State == Failed:
		ev.Role, ev.Label = health.Fault, "Failed"
	case s.State == Unknown || s.State == "":
		ev.Role, ev.Label = health.Unknown, "Unknown"
	default:
		ev.Role, ev.Label = health.Degraded, string(s.State)
	}
	// Every store role is read from the Ready condition alone, so Ready's
	// lastTransitionTime dates it (The Line §8.1).
	if !s.ReadySince.IsZero() {
		since := s.ReadySince
		ev.Since = &since
	}
	if ev.Role != health.OK && ev.Role != health.Unknown {
		ev.Reason = health.CapReason(s.Reason, s.Message)
	}
	return ev
}

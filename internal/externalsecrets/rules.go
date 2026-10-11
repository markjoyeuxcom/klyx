package externalsecrets

import (
	"time"

	"github.com/moomora/klyx/internal/health"
)

// ExternalSecretRule is the External Secrets lens rule, relocated verbatim
// (ExternalSecretsView.needsAttention / secretHealth): Included = not Synced
// or stale (refresh overdue). Next is the moment a synced secret goes stale
// (refreshTime + 2x interval) while it has not.
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
	if !e.Stale && e.RefreshInterval > 0 && !e.RefreshTime.IsZero() {
		// Stale is now - refreshTime > 2x interval: the first instant past it.
		ev.Next = health.At(e.RefreshTime.Add(staleGraceFactor*e.RefreshInterval + time.Nanosecond))
	}
	return ev
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
	return ev
}

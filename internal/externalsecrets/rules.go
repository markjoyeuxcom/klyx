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

// secretDated fills Since, Cond and Reason (The Line §8.1); Reason never
// depends on a time.
//   - Failed and unknown are functions of the Ready condition alone, so
//     Ready's time is the role's start.
//   - Stale is Ready=True AND overdue, derived from refreshTime and the
//     CURRENT interval and policy: it began at the first stale instant or
//     when Ready last turned True, whichever is later - so only when that
//     spec never changed (generation 1: an interval shortened just now made
//     the secret stale now, not at the recomputed instant) and Ready's time is
//     known (lastTransitionTime is optional in ESO's API).
//   - Synced began at Ready's time only when it cannot have been stale in
//     between: a non-periodic secret whose spec never changed. A periodic one
//     may have been stale, and the refresh that ended it leaves no time but
//     refreshTime; a policy switched from periodic ended a stale role then.
//
// A role read from Ready carries Ready's own time as Cond either way.
func secretDated(ev *health.NativeEval, e ExternalSecret) {
	if e.State == Synced && e.Stale {
		ev.Reason = health.CapReason("", fmt.Sprintf("refresh overdue: last refresh %s, interval %s",
			e.RefreshTime.UTC().Format(time.RFC3339), e.RefreshInterval))
		if e.Generation == 1 && !e.ReadySince.IsZero() {
			since := e.StaleAt()
			if e.ReadySince.After(since) {
				since = e.ReadySince
			}
			ev.Since = &since
		}
		return
	}
	if ev.Role == health.Fault || ev.Role == health.Degraded {
		ev.Reason = health.CapReason(e.Reason, e.Message)
	}
	ev.Cond = health.CondAt("Ready", syncStatus(e.State), e.ReadySince)
	if e.ReadySince.IsZero() || (ev.Role == health.OK && (e.RefreshesPeriodically() || e.Generation != 1)) {
		return
	}
	since := e.ReadySince
	ev.Since = &since
}

// syncStatus is the Ready condition status a sync state was read from.
func syncStatus(s SyncState) string {
	switch s {
	case Synced:
		return "True"
	case Failed:
		return "False"
	default:
		return "Unknown"
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
	// Every store role is a function of the Ready condition alone (no
	// overlay), so Ready's lastTransitionTime is the role's start (The Line
	// §8.1).
	if !s.ReadySince.IsZero() {
		since := s.ReadySince
		ev.Since = &since
	}
	ev.Cond = health.CondAt("Ready", syncStatus(s.State), s.ReadySince)
	if ev.Role != health.OK && ev.Role != health.Unknown {
		ev.Reason = health.CapReason(s.Reason, s.Message)
	}
	return ev
}

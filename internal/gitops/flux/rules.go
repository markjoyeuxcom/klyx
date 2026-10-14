package flux

import (
	"time"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/kmeta"
)

// fluxRole is the Flux lens's presentation role, relocated verbatim
// (GitOps.tsx fluxHealth): Suspended = paused (intended, never broken); an
// unobserved spec = progressing "Syncing…"; Failed = fault; Reconciling, or
// Ready=Unknown while Flux reports a reconcile in flight, = progressing; any
// other Unknown = unknown; other not-Ready = degraded; Ready = ok.
func fluxRole(ready ReadyState, suspended, stale bool, reason string) (health.Kind, string) {
	switch {
	case suspended:
		return health.Paused, "Suspended"
	case stale:
		return health.Progressing, "Syncing…"
	case ready == Failed:
		return health.Fault, "Failed"
	case ready == Reconciling:
		return health.Progressing, "Reconciling"
	case ready == Unknown:
		switch reason {
		case "Progressing", "ProgressingWithRetry", "Reconciling":
			return health.Progressing, "Progressing"
		}
		return health.Unknown, "Unknown"
	case ready != Ready:
		if ready == "" {
			return health.Degraded, "Not ready"
		}
		return health.Degraded, string(ready)
	default:
		return health.OK, "Ready"
	}
}

// ReconciliationRule is the Flux lens rule for Kustomizations and
// HelmReleases, relocated verbatim (GitOps.tsx needsAttention / fluxHealth):
// Included = not suspended and not Ready - the legacy count, which still
// includes progressing (Suspended is an intended pause, never attention).
func ReconciliationRule(r Resource) health.NativeEval {
	role, label := fluxRole(r.Ready, r.Suspended, r.Stale, r.Reason)
	e := health.NativeEval{Included: !r.Suspended && r.Ready != Ready, Role: role, Label: label}
	// A suspension or an unobserved spec overlays Ready and can end without
	// changing it, so Ready's time dates the role only when no such overlay
	// can have followed it.
	readyBacked(&e, r.Suspended, r.Stale, r.Ready, r.ReadySince, r.Reason, r.Message,
		kmeta.Undisturbed(r.Generation, r.Writes, r.ReadySince))
	return e
}

// readyBacked fills Since and Reason when the role comes from the Ready
// condition itself (fault, degraded, unknown, ok): Ready's lastTransitionTime
// then dates the role, when undisturbed says no overlay since it can have
// interrupted the role. A pause, an unobserved spec or a reconcile in flight
// is not what Ready's time records, so those keep Since nil (The Line §8.1).
// Reason does not depend on the time.
func readyBacked(e *health.NativeEval, suspended, stale bool, ready ReadyState, since time.Time, reason, message string, undisturbed bool) {
	if suspended || stale || ready == Reconciling || e.Role == health.Progressing {
		return
	}
	if !since.IsZero() && undisturbed {
		t := since
		e.Since = &t
	}
	if e.Role != health.OK && e.Role != health.Unknown {
		e.Reason = health.CapReason(reason, message)
	}
}

// SourceRule is the Sources view rule, relocated verbatim
// (sourceNeedsAttention / fluxHealth). Sources carry no generation-lag flag.
func SourceRule(s Source) health.NativeEval {
	role, label := fluxRole(s.Ready, s.Suspended, false, s.Reason)
	e := health.NativeEval{Included: !s.Suspended && s.Ready != Ready, Role: role, Label: label}
	// A source's rule has no generation overlay, but a suspension (a spec
	// write) still overlays Ready: the spec must be settled since Ready's time.
	readyBacked(&e, s.Suspended, false, s.Ready, s.ReadySince, s.Reason, s.Message,
		kmeta.SpecSettled(s.Generation, s.Writes, s.ReadySince))
	return e
}

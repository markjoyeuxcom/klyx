package helmcli

import (
	"time"

	"github.com/moomora/klyx/internal/health"
)

// PendingDeadline is Klyx's attention heuristic for an in-flight release:
// twice helm's default 5m --timeout. Past it a release "appears stuck".
// Elapsed time is not proof - a snapshot shows no configured timeout or
// process evidence - so the wording never claims the operation stopped.
const PendingDeadline = 600 * time.Second

// inFlight are the states a helm operation holds while it runs; helm refuses
// new operations on the release until it leaves them.
var inFlight = map[string]bool{"pending-install": true, "pending-upgrade": true, "pending-rollback": true, "uninstalling": true}

// historical states never describe a live release, so they never fault.
var historical = map[string]bool{"superseded": true, "uninstalled": true}

// InFlight reports whether a release status is an operation in progress.
func InFlight(status string) bool { return inFlight[status] }

// ReleaseRule is the Helm lens rule, relocated verbatim (helmHealth.ts
// releaseHealth and HelmView needsAttention): Included = fault or degraded,
// i.e. failed or appears stuck. The deadline is whole seconds, as the lens
// counted it: stuck once now - updated > 600s, so Next is updated + 601s.
func ReleaseRule(r Release, now time.Time) health.NativeEval {
	eval := func(role health.Kind, label string) health.NativeEval {
		return health.NativeEval{Included: role == health.Fault || role == health.Degraded, Role: role, Label: label}
	}
	switch {
	case r.Status == "deployed":
		return eval(health.OK, "deployed")
	case r.Status == "failed":
		return eval(health.Fault, "failed")
	case inFlight[r.Status]:
		deadline := int64(PendingDeadline / time.Second)
		if r.UpdatedUnix > 0 && now.Unix()-r.UpdatedUnix > deadline {
			return eval(health.Degraded, r.Status+", appears stuck")
		}
		e := eval(health.Progressing, r.Status)
		if r.UpdatedUnix > 0 {
			next := time.Unix(r.UpdatedUnix+deadline+1, 0)
			e.Next = &next
		}
		return e
	case historical[r.Status]:
		return eval(health.Unknown, r.Status+" (historical)")
	case r.Status == "":
		return eval(health.Unknown, "unknown")
	default:
		return eval(health.Unknown, r.Status)
	}
}

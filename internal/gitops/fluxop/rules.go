package fluxop

import (
	"github.com/moomora/klyx/internal/health"
)

// Labels the rules use.
const (
	LabelSuspended  = "Suspended"
	LabelAwaiting   = "Resumed · awaiting evaluation"
	LabelSchedule   = "Resumed · awaiting its schedule"
	LabelUnproven   = "Awaiting evaluation"
	LabelEvaluating = "Resumed · evaluating"
	LabelSyncing    = "Syncing…"
)

// Rule is the verdict for a FluxInstance, ResourceSet or provider (S1a §4).
// Suspension comes first; a resumed object without evidence is checking and
// not counted; ReconciliationDisabled is never a verdict; a spec Ready has
// not yet answered for is Syncing; otherwise Ready decides, as the Flux lens
// reads it.
func Rule(c Common, r Resume) health.NativeEval {
	switch {
	case c.Disabled:
		return health.NativeEval{Role: health.Paused, Label: LabelSuspended}
	case r == Awaiting:
		return health.NativeEval{Role: health.Unknown, Label: LabelAwaiting}
	case c.Reason == ReasonDisabled:
		return health.NativeEval{Role: health.Unknown, Label: LabelUnproven}
	case r == Evaluating:
		return health.NativeEval{Included: true, Role: health.Progressing, Label: LabelEvaluating}
	case c.ReadyGeneration > 0 && c.Generation > c.ReadyGeneration:
		// Ready answers for an older generation: the spec is not yet
		// evaluated. A Ready for the current generation is never masked.
		return health.NativeEval{Included: true, Role: health.Progressing, Label: LabelSyncing}
	}
	e := health.NativeEval{Included: c.Ready != Ready}
	switch c.Ready {
	case Ready:
		e.Role, e.Label = health.OK, "Ready"
		e.Cond = health.CondAt("Ready", "True", c.ReadySince)
	case Failed:
		e.Role, e.Label = health.Fault, "Failed"
		if c.Stalled {
			e.Label = "Stalled"
		}
		e.Cond = health.CondAt("Ready", "False", c.ReadySince)
		e.Reason = health.CapReason(c.Reason, c.Message)
	case Reconciling:
		e.Role, e.Label = health.Progressing, "Reconciling"
	case Unknown:
		switch c.Reason {
		case ReasonProgressing, "ProgressingWithRetry", "Reconciling":
			e.Role, e.Label = health.Progressing, "Progressing"
		default:
			e.Role, e.Label = health.Unknown, "Unknown"
		}
	default:
		e.Role, e.Label = health.Unknown, "Not reported"
	}
	return e
}

// ProviderRule is Rule, with a scheduled provider's wait named: it is not
// evaluated until its window opens, or "Fetch now" forces a run.
func ProviderRule(p Provider, r Resume) health.NativeEval {
	e := Rule(p.Common, r)
	if r == Awaiting && p.Scheduled() {
		e.Label = LabelSchedule
	}
	return e
}

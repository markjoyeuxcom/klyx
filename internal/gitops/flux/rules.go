package flux

import "github.com/moomora/klyx/internal/health"

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
	return health.NativeEval{Included: !r.Suspended && r.Ready != Ready, Role: role, Label: label}
}

// SourceRule is the Sources view rule, relocated verbatim
// (sourceNeedsAttention / fluxHealth). Sources carry no generation-lag flag.
func SourceRule(s Source) health.NativeEval {
	role, label := fluxRole(s.Ready, s.Suspended, false, s.Reason)
	return health.NativeEval{Included: !s.Suspended && s.Ready != Ready, Role: role, Label: label}
}

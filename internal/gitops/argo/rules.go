package argo

import "github.com/moomora/klyx/internal/health"

// AppRule is the Argo CD lens rule for Applications, relocated verbatim:
// Included is the ratified backend rule (Broken: not Synced or unhealthy;
// Progressing and Suspended are not broken); the role is ArgoView.argoHealth,
// in Argo's own vocabulary - Degraded/Missing = fault, Suspended = paused,
// OutOfSync = degraded (drift), Progressing = progressing, Healthy+Synced =
// ok, anything else = unknown.
//
// Since stays nil for now. Argo does expose times (health.lastTransitionTime,
// condition times), but only a timestamp that matches the rule may date a
// role: a health transition never dates an OutOfSync role, and "ok" needs both
// health and sync. Ages for Argo come from Klyx's observation until a
// rule-matched time is wired (The Line §8.1).
func AppRule(a App) health.NativeEval {
	e := health.NativeEval{Included: a.Broken()}
	switch {
	case a.HealthStatus == "Degraded" || a.HealthStatus == "Missing":
		e.Role, e.Label = health.Fault, a.HealthStatus
	case a.HealthStatus == "Suspended":
		e.Role, e.Label = health.Paused, "Suspended"
	case a.SyncStatus == "OutOfSync":
		e.Role, e.Label = health.Degraded, "OutOfSync"
	case a.HealthStatus == "Progressing":
		e.Role, e.Label = health.Progressing, "Progressing"
	case a.HealthStatus == "Healthy" && a.SyncStatus == "Synced":
		e.Role, e.Label = health.OK, "Healthy"
	default:
		e.Role, e.Label = health.Unknown, a.HealthStatus
		if e.Label == "" {
			e.Label = "Unknown"
		}
	}
	return e
}

// AppSetRule is the ApplicationSets view rule, relocated verbatim: Included =
// not healthy (appSetConditionBroken over its conditions); the row lamp reads
// healthy = ok, otherwise degraded "needs attention".
func AppSetRule(s ApplicationSet) health.NativeEval {
	if s.Healthy {
		return health.NativeEval{Role: health.OK, Label: "healthy"}
	}
	return health.NativeEval{Included: true, Role: health.Degraded, Label: "needs attention"}
}

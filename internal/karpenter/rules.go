package karpenter

import (
	"time"

	"github.com/moomora/klyx/internal/health"
)

// LaunchDeadline is Karpenter's own registration TTL, used as Klyx's
// "appears stuck" heuristic (KarpenterView LAUNCH_DEADLINE_S). Elapsed time
// is not proof.
const LaunchDeadline = 900 * time.Second

// ClaimRule is the NodeClaims lens rule, relocated verbatim (claimHealth;
// the header counts fault + degraded). Next is the launch deadline while a
// claim is still launching or initializing within it.
func ClaimRule(c NodeClaim) health.NativeEval {
	var e health.NativeEval
	switch {
	case c.Ready == "ready":
		e.Role, e.Label = health.OK, "ready"
	case c.Ready == "not-ready":
		e.Role, e.Label = health.Fault, "not ready"
		if c.FailingStatus == "False" && c.FailingType != "" {
			e.Label = "failed at " + c.FailingType
		}
	default:
		phase := "launching"
		if c.NodeName != "" {
			phase = "initializing"
		}
		if c.AgeSeconds > int(LaunchDeadline/time.Second) {
			e.Role, e.Label = health.Degraded, phase+", appears stuck"
		} else {
			e.Role, e.Label = health.Progressing, phase
			if !c.Created.IsZero() {
				// AgeSeconds is whole seconds: stuck from created + deadline + 1s.
				e.Next = health.At(c.Created.Add(LaunchDeadline + time.Second))
			}
		}
	}
	e.Included = e.Role == health.Fault || e.Role == health.Degraded
	return e
}

// PoolRule is the NodePools lens rule, relocated verbatim (poolHealth; the
// header counts fault).
func PoolRule(p NodePool) health.NativeEval {
	var e health.NativeEval
	switch p.Ready {
	case "ready":
		e.Role, e.Label = health.OK, "ready"
	case "not-ready":
		e.Role, e.Label = health.Fault, "not ready"
	default:
		e.Role, e.Label = health.Unknown, "unknown"
	}
	e.Included = e.Role == health.Fault
	return e
}

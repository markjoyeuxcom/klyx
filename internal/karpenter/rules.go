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
			if !c.Created.IsZero() {
				e.Boundary = health.At(c.Created.Add(LaunchDeadline + time.Second))
			}
		} else {
			e.Role, e.Label = health.Progressing, phase
			if !c.Created.IsZero() {
				// AgeSeconds is whole seconds: stuck from created + deadline + 1s.
				e.Next = health.At(c.Created.Add(LaunchDeadline + time.Second))
			}
		}
	}
	e.Included = e.Role == health.Fault || e.Role == health.Degraded
	claimDated(&e, c)
	return e
}

// claimDated fills Since, Cond and Reason (The Line §8.1). Ready and
// not-ready are functions of the Ready condition alone (a NodeClaim has no
// spec overlay), so its lastTransitionTime is the role's start, and Ready's
// time is also the condition evidence. "Appears stuck" is derived: it began
// at the launch deadline (creation never changes), or when Ready last changed
// if that is later. Launching and initializing keep Since nil.
func claimDated(e *health.NativeEval, c NodeClaim) {
	switch e.Role {
	case health.OK, health.Fault:
		if !c.ReadySince.IsZero() {
			since := c.ReadySince
			e.Since = &since
		}
		status := "False"
		if e.Role == health.OK {
			status = "True"
		}
		e.Cond = health.CondAt("Ready", status, c.ReadySince)
	case health.Degraded:
		if c.Created.IsZero() {
			break
		}
		since := c.Created.Add(LaunchDeadline + time.Second)
		if c.ReadySince.After(since) {
			since = c.ReadySince
		}
		e.Since = &since
	}
	// The reason is Klyx's fixed wording, never the native message (S10 §8):
	// a launch failure's text can carry a provider's identifiers.
	switch {
	case e.Role != health.Fault && e.Role != health.Degraded:
	case c.FailingType != "":
		e.Reason = health.CapReason("", Phrase(&Cond{Type: c.FailingType, Status: c.FailingStatus,
			Reason: c.FailingReason, ReasonWithheld: c.FailingReasonWithheld}))
	default:
		e.Reason = health.CapReason("", "not ready "+LaunchDeadline.String()+" after creation")
	}
}

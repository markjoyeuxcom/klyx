package healthsrc

import (
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/observe"
)

// Controller issue states (1c spec §1: a task-level field, never a part
// count; a task can be lit by its controller alone).
const (
	ControllerNotReady = "not ready" // the detector found the controller not ready
	ControllerUnknown  = "unknown"   // its health could not be read
)

// ControllerTasks are the tasks whose controller health the capability
// detector reports. Each always has an entry in ControllerIssues, so a
// recovery clears a previous issue.
var ControllerTasks = []string{TaskFlux, TaskArgo, TaskCerts, TaskESO, TaskKarpenter, TaskGateway}

// ControllerIssues maps the capability tiers onto per-task controller
// issues (nil when healthy or not installed), in the detector's own words.
// Reloader has no task of its own and is not mapped.
func ControllerIssues(caps capability.Set) map[string]*observe.ControllerIssue {
	issue := func(state, reason string) *observe.ControllerIssue {
		return &observe.ControllerIssue{State: state, Reason: reason}
	}
	out := make(map[string]*observe.ControllerIssue, len(ControllerTasks))
	for _, t := range ControllerTasks {
		out[t] = nil
	}
	if r := caps.GitOps.FluxIssue(); r != "" {
		out[TaskFlux] = issue(ControllerNotReady, r)
	}
	if r := caps.GitOps.ArgoIssue(); r != "" {
		out[TaskArgo] = issue(ControllerNotReady, r)
	}
	if caps.CertManager.Present && !caps.CertManager.Healthy {
		out[TaskCerts] = issue(ControllerNotReady, caps.CertManager.Reason)
	}
	if caps.ExternalSecrets.Present && !caps.ExternalSecrets.Healthy {
		out[TaskESO] = issue(ControllerNotReady, caps.ExternalSecrets.Reason)
	}
	if caps.Karpenter.Present && !caps.Karpenter.Healthy {
		out[TaskKarpenter] = issue(ControllerNotReady, caps.Karpenter.Reason)
	}
	// The network tier degrades only on the Gateway API data plane (no
	// GatewayClass accepted, or the classes unreadable).
	if caps.Network.GatewayAPIVersion != "" && caps.Network.Tier == capability.Degraded {
		state := ControllerNotReady
		if caps.Network.GatewayClassesUnread {
			state = ControllerUnknown
		}
		out[TaskGateway] = issue(state, caps.Network.Reason)
	}
	return out
}

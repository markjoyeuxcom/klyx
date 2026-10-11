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
	// Not ready only from a controller workload actually read; a workload
	// unreadable or not found under its conventional name is unknown.
	stateOf := func(verified bool) string {
		if verified {
			return ControllerNotReady
		}
		return ControllerUnknown
	}
	out := make(map[string]*observe.ControllerIssue, len(ControllerTasks))
	for _, t := range ControllerTasks {
		out[t] = nil
	}
	if r := caps.GitOps.FluxIssue(); r != "" {
		out[TaskFlux] = issue(stateOf(caps.GitOps.Flux.Verified), r)
	}
	if r := caps.GitOps.ArgoIssue(); r != "" {
		out[TaskArgo] = issue(stateOf(caps.GitOps.Argo.Verified), r)
	}
	if caps.CertManager.Present && !caps.CertManager.Healthy {
		out[TaskCerts] = issue(stateOf(caps.CertManager.Verified), caps.CertManager.Reason)
	}
	if caps.ExternalSecrets.Present && !caps.ExternalSecrets.Healthy {
		out[TaskESO] = issue(stateOf(caps.ExternalSecrets.Verified), caps.ExternalSecrets.Reason)
	}
	if caps.Karpenter.Present && !caps.Karpenter.Healthy {
		out[TaskKarpenter] = issue(stateOf(caps.Karpenter.Verified), caps.Karpenter.Reason)
	}
	// The network tier degrades only on the Gateway API data plane: every
	// GatewayClass rejected (not ready), or the classes unreadable or their
	// acceptance pending or unreported (unknown).
	if caps.Network.GatewayAPIVersion != "" && caps.Network.Tier == capability.Degraded {
		out[TaskGateway] = issue(stateOf(!caps.Network.GatewayUnverified), caps.Network.Reason)
	}
	return out
}

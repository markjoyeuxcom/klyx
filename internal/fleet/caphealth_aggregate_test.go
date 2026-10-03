package fleet

import (
	"testing"

	"github.com/moomora/klyx/internal/capability"
)

// Review 2026-10-03 F: the cluster's Degraded overlay aggregates every
// degraded capability. A healthy GitOps recompute used to transition
// Degraded -> Synced even while the Network capability was still degraded.
func TestApplyGitOpsHealth_DoesNotClearAnotherCapabilitysDegraded(t *testing.T) {
	c := &ClusterConn{state: Degraded, reason: "flux controller not ready"}
	c.caps.Network = capability.NetworkCapability{Base: capability.Base{Tier: capability.Degraded, Reason: "no GatewayClass accepted"}}

	c.applyGitOpsHealth(capability.GitOpsCapability{Base: capability.Base{Tier: capability.Healthy}})
	if c.state != Degraded || c.reason != "no GatewayClass accepted" {
		t.Fatalf("still degraded by network: got state=%v reason=%q", c.state, c.reason)
	}
	if c.caps.GitOps.Tier != capability.Healthy {
		t.Fatal("the GitOps capability itself is still updated")
	}

	// With nothing else degraded, GitOps recovery clears the overlay.
	c2 := &ClusterConn{state: Degraded, reason: "flux controller not ready"}
	c2.applyGitOpsHealth(capability.GitOpsCapability{Base: capability.Base{Tier: capability.Healthy}})
	if c2.state != Synced {
		t.Fatalf("want Synced, got %v", c2.state)
	}
}

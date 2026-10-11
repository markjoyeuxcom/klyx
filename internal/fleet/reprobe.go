package fleet

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/capability"
)

// capReprobeEvery is the capability re-probe cadence: controller issues
// (cert-manager, External Secrets, Karpenter, the Gateway API data plane)
// stay current instead of frozen at connect (1c spec §1). Test seam.
var capReprobeEvery = 5 * time.Minute

// reprobeDetect runs one probe. Test seam.
var reprobeDetect = func(ctx context.Context, c *ClusterConn) (capability.Set, error) {
	return c.detector.DetectErr(ctx)
}

// reprobeLoop re-runs capability detection on a cadence after the first
// sync. A failed probe confirms nothing: the last tiers stand.
func (c *ClusterConn) reprobeLoop(ctx context.Context) {
	if c.detector == nil {
		return
	}
	t := time.NewTicker(capReprobeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		set, err := reprobeDetect(ctx, c)
		if err != nil || ctx.Err() != nil {
			continue
		}
		c.applyReprobe(set)
	}
}

// applyReprobe takes a re-probe's tiers. GitOps is not taken: its tier is
// live through the controller-workload watch (startCapHealth), which owns
// it. The network tier drives the same Degraded overlay edges as GitOps
// health: a degraded data plane overlays a Synced cluster, and recovery
// lifts the overlay only when GitOps is not degraded too - the cluster then
// stays Degraded for GitOps's reason (the review 2026-10-03 F rule).
func (c *ClusterConn) applyReprobe(set capability.Set) {
	defer c.syncHealth() // after the unlock below
	c.mu.Lock()
	defer c.mu.Unlock()
	c.caps.CertManager = set.CertManager
	c.caps.ExternalSecrets = set.ExternalSecrets
	c.caps.Karpenter = set.Karpenter
	c.caps.Reloader = set.Reloader
	c.caps.Network = set.Network
	switch {
	case set.Network.Tier == capability.Degraded:
		if next, ok := Transition(c.state, EvCapUnhealthy); ok {
			c.state, c.reason = next, capabilityReason(c.caps)
		} else if c.state == Degraded {
			c.reason = capabilityReason(c.caps)
		}
	case c.caps.GitOps.Tier == capability.Degraded:
		if c.state == Degraded {
			c.reason = c.caps.GitOps.Reason
		}
	default:
		if next, ok := Transition(c.state, EvCapHealthy); ok {
			c.state, c.reason = next, ""
		}
	}
}

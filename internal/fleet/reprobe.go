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

// reprobeVersion reads the API server's GitVersion. Test seam.
var reprobeVersion = func(ctx context.Context, c *ClusterConn) (string, error) {
	if c.typed == nil {
		return "", nil
	}
	vi, err := c.typed.Discovery().ServerVersion()
	if err != nil || vi == nil {
		return "", err
	}
	return vi.GitVersion, nil
}

// reprobeLoop re-runs capability detection on a cadence after the first
// sync. A failed probe confirms nothing: the last tiers stand, except
// time-bounded evidence past its bound (expireReprobe).
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
		switch {
		case ctx.Err() != nil:
		case err != nil:
			c.expireReprobe()
		default:
			// coverage S11b §2.1: the server's version is re-read too, so the
			// pool rules follow an upgrade; a failed read keeps the last one
			if v, verr := reprobeVersion(ctx, c); verr == nil && v != "" {
				c.mu.Lock()
				c.snapVersion = v
				c.mu.Unlock()
			}
			c.applyReprobe(set)
		}
	}
}

// reprobeNow is the re-probe's clock.
func (c *ClusterConn) reprobeNow() time.Time {
	if c.clk != nil {
		return c.clk.Now()
	}
	return time.Now()
}

// expireReprobe is a failed re-probe: no tier is taken, but NAP activity
// past its one-hour window expires (coverage S10 §5), and the controller
// task and the facts are published when it does - a discovery outage never
// keeps "managed" alive.
func (c *ClusterConn) expireReprobe() {
	c.mu.Lock()
	prev := c.caps.Karpenter
	c.caps.Karpenter = prev.Expired(c.reprobeNow())
	changed := c.caps.Karpenter.NAP.State != prev.NAP.State
	c.mu.Unlock()
	if changed {
		c.syncHealth()
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
	// coverage S10 §5: a NAP read that failed keeps the last activity
	// until it leaves the window
	c.caps.Karpenter = set.Karpenter.Refreshed(c.caps.Karpenter, c.reprobeNow())
	c.caps.Reloader = set.Reloader
	c.caps.Network = set.Network
	c.caps.Admission = set.Admission
	c.caps.LoadBalancing = set.LoadBalancing
	// Velero's servers are re-read too (found during S4: the re-probe had
	// never refreshed them, so a server going not ready after connect was
	// never seen)
	c.caps.Velero = set.Velero
	c.caps.KEDA = set.KEDA // coverage S9: the operators, their versions, the APIService
	c.caps.DRA = set.DRA   // coverage S11b: served, and any DRA object
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

package fleet

import (
	"context"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/hubble"
)

// HubbleFlowStats returns the cluster's Hubble flow/drop triage readout from
// its Prometheus metrics. Capability-gated on HasHubble (honest "not detected",
// never an empty panel) and on the M7 metrics capability; reuses the cached
// metrics transport exactly like RouteMetrics. On-demand; no watch.
func (c *ClusterConn) HubbleFlowStats(ctx context.Context) (hubble.FlowStats, hubble.Status) {
	clk := c.clk
	if clk == nil { // defensive: manual struct construction in tests
		clk = clock.Real{}
	}
	now := clk.Now()

	c.mu.RLock()
	hasHubble := c.caps.Network.HasHubble
	c.mu.RUnlock()
	if !hasHubble {
		return hubble.FlowStats{}, hubble.Status{Available: false, Message: "Hubble not detected on this cluster"}
	}

	mr := c.readMetrics(ctx, now)
	cap := mr.cap

	if !cap.Available {
		msg := "metrics unavailable"
		if cap.Reason != "" {
			msg += ": " + cap.Reason
		}
		return hubble.FlowStats{}, hubble.Status{Available: false, Message: msg}
	}

	src := hubble.NewSource(mr.client)
	stats, st, err := src.QueryFlowStats(ctx)
	if err != nil {
		return hubble.FlowStats{}, hubble.Status{Available: false, Message: "hubble metrics query failed: " + err.Error()}
	}
	if c.credentialMoved(mr.rev) {
		return hubble.FlowStats{}, hubble.Status{Available: false, Message: "metrics unavailable: " + reasonCredentialChanged}
	}
	st.UpdatedAt = now // fleet stamps freshness on a produced result
	return stats, st
}

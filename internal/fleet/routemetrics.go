package fleet

import (
	"context"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/routemetrics"
)

// RouteMetrics returns per-route traffic metrics for the given route keys
// ("<ns>/<name>") plus an Envoy-route-series status (distinct from the M7-a
// Prometheus capability). On-demand; reuses the cached metrics transport.
// It snapshots the resolved transport+capability under metricsMu then releases
// the lock before querying — unlike ClusterMetrics, route queries are not
// serialized under the mutex (a stale snapshot self-corrects on the next poll).
func (c *ClusterConn) RouteMetrics(ctx context.Context, routeKeys []string) (map[string]routemetrics.RouteMetrics, routemetrics.Status) {
	clk := c.clk
	if clk == nil { // defensive: manual struct construction in tests
		clk = clock.Real{}
	}
	now := clk.Now()

	if len(routeKeys) == 0 {
		return map[string]routemetrics.RouteMetrics{}, routemetrics.Status{Available: true, UpdatedAt: now}
	}

	c.mu.RLock()
	hasEnvoy := c.caps.Network.HasEnvoyProxy
	c.mu.RUnlock()
	if !hasEnvoy {
		return nil, routemetrics.Status{Available: false, Message: "Envoy Gateway not detected"}
	}

	mr := c.readMetrics(ctx, now)
	cap := mr.cap

	if !cap.Available {
		msg := "metrics unavailable"
		if cap.Reason != "" {
			msg += ": " + cap.Reason
		}
		return nil, routemetrics.Status{Available: false, Message: msg}
	}

	src := routemetrics.NewEnvoyClusterSource(mr.client)
	out, st, err := src.QueryRouteMetrics(ctx, routeKeys)
	if err != nil {
		return nil, routemetrics.Status{Available: false, Message: "route metrics query failed: " + err.Error()}
	}
	if c.credentialMoved(mr.rev) {
		return nil, routemetrics.Status{Available: false, Message: "metrics unavailable: " + reasonCredentialChanged}
	}
	st.UpdatedAt = now // fleet stamps freshness on a produced result
	return out, st
}

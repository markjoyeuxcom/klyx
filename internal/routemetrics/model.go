// Package routemetrics builds per-route traffic metrics (rps/p50/p99/err) for
// the network topology from a metrics source. EnvoyClusterSource is the only
// implementation; Cilium/Hubble can implement Source later.
package routemetrics

import (
	"context"
	"time"
)

// RouteMetrics is the per-route traffic readout. A nil pointer means "no usable
// value" (no series, or not meaningful e.g. latency/err at zero traffic) and
// renders "—"; it is never a fabricated 0. ErrRate is a FRACTION in [0,1].
type RouteMetrics struct {
	RPS     *float64
	P50     *float64 // milliseconds
	P99     *float64 // milliseconds
	ErrRate *float64 // fraction 0..1
}

// Status reports whether route metrics are usable for a topology, separate from
// the M7-a Prometheus capability. Message is the unavailable reason when
// Available is false, OR an informational note when Available is true (e.g.
// "no route series matched this topology").
type Status struct {
	Available bool
	Message   string
	UpdatedAt time.Time
	// NoEndpoint: no metrics endpoint is configured or discovered (slice
	// 8d-2): the one case worded "configure a metrics endpoint".
	NoEndpoint bool
}

// Source produces per-route metrics for a set of route keys ("<ns>/<name>").
type Source interface {
	QueryRouteMetrics(ctx context.Context, routeKeys []string) (map[string]RouteMetrics, Status, error)
}

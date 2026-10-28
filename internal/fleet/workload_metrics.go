package fleet

import (
	"context"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/workloads"
)

// WorkloadMetrics returns live per-workload cpu/memory usage keyed by the stable
// "<Kind>/<Namespace>/<Name>", plus a status. Self-contained (mirrors RouteMetrics):
// it re-lists workloads+pods to reuse Assemble's pod->workload join, then enriches
// with Prometheus usage. Usage only -- requests/limits already ship with ListWorkloads.
func (c *ClusterConn) WorkloadMetrics(ctx context.Context, namespace string) (map[string]workloads.Usage, workloads.UsageStatus) {
	clk := c.clk
	if clk == nil { // defensive: manual struct construction in tests
		clk = clock.Real{}
	}
	now := clk.Now()

	ws, _, _, err := c.ListWorkloads(ctx, namespace)
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "workload list failed: " + err.Error()}
	}

	mr := c.readMetrics(ctx, now)
	cap := mr.cap

	if !cap.Available {
		msg := "metrics unavailable"
		if cap.Reason != "" {
			msg += ": " + cap.Reason
		}
		return nil, workloads.UsageStatus{Available: false, Message: msg}
	}

	cl := mr.client
	ns := metrics.Namespace{Name: namespace}
	cpuByPod, err := queryByPod(ctx, cl, metrics.PodCPU.With(ns))
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "cpu usage query failed: " + err.Error()}
	}
	memByPod, err := queryByPod(ctx, cl, metrics.PodMem.With(ns))
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "memory usage query failed: " + err.Error()}
	}

	if c.credentialMoved(mr.rev) {
		return nil, workloads.UsageStatus{Available: false, Message: "metrics unavailable: " + reasonCredentialChanged}
	}
	return workloads.AggregateUsage(ws, cpuByPod, memByPod), workloads.UsageStatus{Available: true, UpdatedAt: now}
}

// queryByPod runs a `sum by (namespace,pod)` instant query and reduces it to
// "<namespace>/<pod>" -> value. Samples missing either label are dropped (no
// ambiguous key). NaN/Inf are already skipped by InstantVector.
func queryByPod(ctx context.Context, cl *metrics.Client, q metrics.Query) (map[string]float64, error) {
	samples, err := cl.InstantVector(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(samples))
	for _, s := range samples {
		ns, pod := s.Labels["namespace"], s.Labels["pod"]
		if ns == "" || pod == "" {
			continue
		}
		out[ns+"/"+pod] = s.Value
	}
	return out, nil
}

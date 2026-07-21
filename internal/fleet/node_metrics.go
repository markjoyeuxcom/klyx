package fleet

import (
	"context"
	"fmt"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/workloads"
)

// Per-container usage for one pod: the same confirmed cAdvisor series the
// workload aggregate uses, reduced by container instead of pod. %s/%s are the
// namespace and pod matchers.
const (
	ctCPUQuery = `sum by (container) (rate(container_cpu_usage_seconds_total{namespace=%q,pod=%q,container!="",container!="POD"}[5m]))`
	ctMemQuery = `sum by (container) (container_memory_working_set_bytes{namespace=%q,pod=%q,container!="",container!="POD"})`
)

// NodeUtilization returns per-node pod-attributed live usage: the cluster-wide
// per-pod usage queries joined to each pod's assigned node. This deliberately
// reuses the CONFIRMED per-pod series rather than assuming a node label scheme
// (node_exporter and per-scrape relabeling vary); the trade-off - node OS and
// kubelet overhead are not included - is stated in the lens caption. A capped
// pod list means possible under-count, so the status says so instead of
// presenting the sum as complete.
func (c *ClusterConn) NodeUtilization(ctx context.Context) ([]workloads.NodeUsage, workloads.UsageStatus) {
	clk := c.clk
	if clk == nil { // defensive: manual struct construction in tests
		clk = clock.Real{}
	}
	now := clk.Now()

	pods, truncated, err := c.ListPods(ctx, "")
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "pod list failed: " + err.Error()}
	}

	c.metricsMu.Lock()
	c.ensureMetricsLocked(ctx, false, now)
	cap := c.metricsState.cap
	transport := c.metricsState.transport
	c.metricsMu.Unlock()

	if !cap.Available {
		msg := "metrics unavailable"
		if cap.Reason != "" {
			msg += ": " + cap.Reason
		}
		return nil, workloads.UsageStatus{Available: false, Message: msg}
	}

	cl := metrics.NewClient(transport)
	cpuByPod, err := queryByPod(ctx, cl, fmt.Sprintf(wlCPUQuery, ""))
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "cpu usage query failed: " + err.Error()}
	}
	memByPod, err := queryByPod(ctx, cl, fmt.Sprintf(wlMemQuery, ""))
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "memory usage query failed: " + err.Error()}
	}

	st := workloads.UsageStatus{Available: true, UpdatedAt: now}
	if truncated {
		st.Message = "pod list capped - node usage may under-count"
	}
	return workloads.AggregateNodeUsage(pods, cpuByPod, memByPod), st
}

// PodContainerUsage returns one pod's live per-container cpu/mem usage - the
// input the pod inspector's rightsizing delta needs (usage vs the container's
// declared requests/limits). Keyed by container name; a container with no
// sample is simply absent (nil = no data at the caller).
func (c *ClusterConn) PodContainerUsage(ctx context.Context, namespace, pod string) (map[string]workloads.Usage, workloads.UsageStatus) {
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	now := clk.Now()

	c.metricsMu.Lock()
	c.ensureMetricsLocked(ctx, false, now)
	cap := c.metricsState.cap
	transport := c.metricsState.transport
	c.metricsMu.Unlock()

	if !cap.Available {
		msg := "metrics unavailable"
		if cap.Reason != "" {
			msg += ": " + cap.Reason
		}
		return nil, workloads.UsageStatus{Available: false, Message: msg}
	}

	cl := metrics.NewClient(transport)
	cpuByCt, err := queryByLabel(ctx, cl, fmt.Sprintf(ctCPUQuery, namespace, pod), "container")
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "cpu usage query failed: " + err.Error()}
	}
	memByCt, err := queryByLabel(ctx, cl, fmt.Sprintf(ctMemQuery, namespace, pod), "container")
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "memory usage query failed: " + err.Error()}
	}

	out := make(map[string]workloads.Usage, len(cpuByCt)+len(memByCt))
	for name, v := range cpuByCt {
		u := out[name]
		cv := v
		u.CPU = &cv
		out[name] = u
	}
	for name, v := range memByCt {
		u := out[name]
		mv := v
		u.Mem = &mv
		out[name] = u
	}
	return out, workloads.UsageStatus{Available: true, UpdatedAt: now}
}

// queryByLabel runs an instant query and reduces it to label-value -> sample.
// Samples missing the label are dropped (no ambiguous key).
func queryByLabel(ctx context.Context, cl *metrics.Client, promql, label string) (map[string]float64, error) {
	samples, err := cl.InstantVector(ctx, promql)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(samples))
	for _, s := range samples {
		k := s.Labels[label]
		if k == "" {
			continue
		}
		out[k] = s.Value
	}
	return out, nil
}

package fleet

import (
	"context"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/workloads"
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

	mr := c.readMetrics(ctx, now)
	defer mr.release()
	cap := mr.cap

	if !cap.Available {
		msg := "metrics unavailable"
		if cap.Reason != "" {
			msg += ": " + cap.Reason
		}
		return nil, workloads.UsageStatus{Available: false, Message: msg}
	}

	cl := mr.client
	cpuByPod, err := queryByPod(ctx, cl, metrics.PodCPU.With(metrics.Namespace{}))
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "cpu usage query failed: " + err.Error()}
	}
	memByPod, err := queryByPod(ctx, cl, metrics.PodMem.With(metrics.Namespace{}))
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "memory usage query failed: " + err.Error()}
	}

	if why := mr.unsettled(); why != "" {
		return nil, workloads.UsageStatus{Available: false, Message: "metrics unavailable: " + why}
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

	mr := c.readMetrics(ctx, now)
	defer mr.release()
	cap := mr.cap

	if !cap.Available {
		msg := "metrics unavailable"
		if cap.Reason != "" {
			msg += ": " + cap.Reason
		}
		return nil, workloads.UsageStatus{Available: false, Message: msg}
	}

	cl := mr.client
	cpuByCt, err := queryByLabel(ctx, cl, metrics.ContainerCPU.With(metrics.Pod{Namespace: namespace, Name: pod}), "container")
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "cpu usage query failed: " + err.Error()}
	}
	memByCt, err := queryByLabel(ctx, cl, metrics.ContainerMem.With(metrics.Pod{Namespace: namespace, Name: pod}), "container")
	if err != nil {
		return nil, workloads.UsageStatus{Available: false, Message: "memory usage query failed: " + err.Error()}
	}

	if why := mr.unsettled(); why != "" {
		return nil, workloads.UsageStatus{Available: false, Message: "metrics unavailable: " + why}
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
func queryByLabel(ctx context.Context, cl *metrics.Client, q metrics.Query, label string) (map[string]float64, error) {
	samples, err := cl.InstantVector(ctx, q)
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

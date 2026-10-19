package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/metrics"
)

// Sparkline window: 30 minutes at 60s resolution. Small enough that a matrix
// response stays kilobytes, long enough to show a deploy or a leak taking off.
const (
	sparklineWindow = 30 * time.Minute
	sparklineStep   = 60 * time.Second
)

// SparklineSet is a pair of cpu/mem range series. CPU is cores (workload) or
// a 0-1 fraction (cluster); Mem is bytes (workload) or a 0-1 fraction
// (cluster) — the same units as the matching instant readouts. Empty slices
// mean "no data in the window", distinct from an error.
type SparklineSet struct {
	CPU []metrics.Point
	Mem []metrics.Point
}

// WorkloadSparklines returns 30m cpu/mem series for one workload, summed over
// the pods CURRENTLY backing it (selector join via WorkloadPods). Pods that
// churned out of the workload during the window drop out of the series — the
// sparkline describes what the present pods did, not a historical ownership
// reconstruction. Errors cover both "metrics unavailable" and query failure.
func (c *ClusterConn) WorkloadSparklines(ctx context.Context, kind, namespace, name string) (SparklineSet, error) {
	pods, err := c.WorkloadPods(ctx, kind, namespace, name)
	if err != nil {
		return SparklineSet{}, fmt.Errorf("resolve workload pods: %w", err)
	}
	if len(pods) == 0 {
		return SparklineSet{CPU: []metrics.Point{}, Mem: []metrics.Point{}}, nil
	}

	sel := metrics.Equals("namespace", namespace) + "," + metrics.MatchAnyOf("pod", "", pods, "") + `,container!="",container!="POD"`
	cpuQ := fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{%s}[5m]))`, sel)
	memQ := fmt.Sprintf(`sum(container_memory_working_set_bytes{%s})`, sel)

	return c.sparklines(ctx, cpuQ, memQ)
}

// ClusterSparklines returns 30m cluster-wide cpu/mem utilisation fractions,
// the range twins of the instant cpuQuery/memQuery powering the Overview
// readout.
func (c *ClusterConn) ClusterSparklines(ctx context.Context) (SparklineSet, error) {
	return c.sparklines(ctx, cpuQuery, memQuery)
}

// sparklines resolves the metrics transport (same lazy probe + cache as
// every other metrics read) and queries the pair through it; a pair read
// across a credential change is dropped (C5 §7).
func (c *ClusterConn) sparklines(ctx context.Context, cpuQ, memQ string) (SparklineSet, error) {
	clk := c.clk
	if clk == nil { // defensive: manual struct construction in tests
		clk = clock.Real{}
	}
	now := clk.Now()

	mr := c.readMetrics(ctx, now)
	cap, transport := mr.cap, mr.transport

	if !cap.Available {
		msg := "metrics unavailable"
		if cap.Reason != "" {
			msg += ": " + cap.Reason
		}
		return SparklineSet{}, fmt.Errorf("%s", msg)
	}
	set, err := querySparklines(ctx, metrics.NewClient(transport), now, cpuQ, memQ)
	if err == nil && c.credentialMoved(mr.rev) {
		return SparklineSet{}, errors.New("metrics unavailable: " + reasonCredentialChanged)
	}
	return set, err
}

func querySparklines(ctx context.Context, cl *metrics.Client, now time.Time, cpuQ, memQ string) (SparklineSet, error) {
	start := now.Add(-sparklineWindow)
	cpu, err := cl.RangeSeries(ctx, cpuQ, start, now, sparklineStep)
	if err != nil {
		return SparklineSet{}, fmt.Errorf("cpu range query failed: %w", err)
	}
	mem, err := cl.RangeSeries(ctx, memQ, start, now, sparklineStep)
	if err != nil {
		return SparklineSet{}, fmt.Errorf("memory range query failed: %w", err)
	}
	return SparklineSet{CPU: cpu, Mem: mem}, nil
}

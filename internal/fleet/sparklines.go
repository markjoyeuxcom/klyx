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
	// Sentence and Lineage are the read's source (Settings S3b §3.2,
	// §3.4); NoData says both series came back empty under its scope
	Sentence string
	Lineage  string
	NoData   string
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

	set := metrics.PodSet{Namespace: namespace, Pods: pods}
	return c.sparklines(ctx, metrics.WorkloadCPU.With(set), metrics.WorkloadMem.With(set))
}

// ClusterSparklines returns 30m cluster-wide cpu/mem utilisation fractions,
// the range twins of the instant cluster queries powering the Overview
// readout.
func (c *ClusterConn) ClusterSparklines(ctx context.Context) (SparklineSet, error) {
	return c.sparklines(ctx, metrics.ClusterCPU.With(metrics.None{}), metrics.ClusterMem.With(metrics.None{}))
}

// sparklines resolves the metrics transport (same lazy probe + cache as
// every other metrics read) and queries the pair through it; a pair read
// across a credential change is dropped (C5 §7).
func (c *ClusterConn) sparklines(ctx context.Context, cpuQ, memQ metrics.Query) (SparklineSet, error) {
	clk := c.clk
	if clk == nil { // defensive: manual struct construction in tests
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
		return SparklineSet{}, fmt.Errorf("%s", msg)
	}
	set, series, err := querySparklines(ctx, mr.client, now, cpuQ, memQ)
	if why := mr.unsettled(); why != "" {
		return SparklineSet{}, errors.New("metrics unavailable: " + why)
	}
	if err == nil {
		set.Sentence, set.Lineage = cap.Sentence, cap.Lineage
		if series == 0 {
			set.NoData = mr.noData()
		}
	}
	return set, err
}

// querySparklines is the pair and how many series they held: zero is an
// answer with no series at all, told apart from gaps (S3b §3.3).
func querySparklines(ctx context.Context, cl *metrics.Client, now time.Time, cpuQ, memQ metrics.Query) (SparklineSet, int, error) {
	start := now.Add(-sparklineWindow)
	cpu, nc, err := cl.RangeSeriesSeen(ctx, cpuQ, start, now, sparklineStep)
	if err != nil {
		return SparklineSet{}, 0, fmt.Errorf("cpu range query failed: %w", err)
	}
	mem, nm, err := cl.RangeSeriesSeen(ctx, memQ, start, now, sparklineStep)
	if err != nil {
		return SparklineSet{}, 0, fmt.Errorf("memory range query failed: %w", err)
	}
	return SparklineSet{CPU: cpu, Mem: mem}, nc + nm, nil
}

package appbridge

import (
	"context"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/workloads"
)

const metricsTimeout = 30 * time.Second

// MetricsConn is the per-cluster read surface MetricsService needs (lookup-seam
// pattern; cf. CRDService/GatewayService). fleet.ClusterConn satisfies it.
type MetricsConn interface {
	ClusterMetrics(ctx context.Context, forceReprobe bool) (metrics.ClusterMetrics, metrics.MetricsCapability)
	WorkloadSparklines(ctx context.Context, kind, namespace, name string) (fleet.SparklineSet, error)
	ClusterSparklines(ctx context.Context) (fleet.SparklineSet, error)
	NodeUtilization(ctx context.Context) ([]workloads.NodeUsage, workloads.UsageStatus)
	PodContainerUsage(ctx context.Context, namespace, pod string) (map[string]workloads.Usage, workloads.UsageStatus)
}

// MetricsService is bound to JS. On-demand only; no push loop.
type MetricsService struct {
	lookup func(string) (MetricsConn, bool)
}

func NewMetricsService(lookup func(string) (MetricsConn, bool)) *MetricsService {
	return &MetricsService{lookup: lookup}
}

// GetClusterMetrics returns the cluster's metrics + connection status.
// forceReprobe re-resolves and re-probes (the manual-refresh escape hatch).
func (s *MetricsService) GetClusterMetrics(cluster string, forceReprobe bool) MetricsDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return MetricsDTO{Mode: string(metrics.ModeUnavailable), Reason: "cluster not connected"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)
	defer cancel()
	cm, cap := conn.ClusterMetrics(ctx, forceReprobe)
	return MetricsDTO{
		Available:   cap.Available,
		Mode:        string(cap.Mode),
		Source:      cap.Source,
		Warning:     cap.Warning,
		Reason:      cap.Reason,
		CPUFraction: cm.CPUFraction,
		MemFraction: cm.MemFraction,
		Sentence:    cap.Sentence,
		Lineage:     cap.Lineage,
		NoData:      cm.NoData,
	}
}

// GetWorkloadSparklines returns 30m cpu/mem range series for one workload
// (summed over its current pods). Available=false carries the reason; the
// series are never fabricated.
func (s *MetricsService) GetWorkloadSparklines(cluster, namespace, kind, name string) SparklinesDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return SparklinesDTO{Message: "cluster not connected"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)
	defer cancel()
	set, err := conn.WorkloadSparklines(ctx, kind, namespace, name)
	if err != nil {
		return SparklinesDTO{Message: err.Error()}
	}
	return sparklinesDTO(set)
}

// GetClusterSparklines returns 30m cluster cpu/mem utilisation fractions —
// the range twins of GetClusterMetrics' instant readout.
func (s *MetricsService) GetClusterSparklines(cluster string) SparklinesDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return SparklinesDTO{Message: "cluster not connected"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)
	defer cancel()
	set, err := conn.ClusterSparklines(ctx)
	if err != nil {
		return SparklinesDTO{Message: err.Error()}
	}
	return sparklinesDTO(set)
}

func sparklinesDTO(set fleet.SparklineSet) SparklinesDTO {
	return SparklinesDTO{Available: true, CPU: pointsDTO(set.CPU), Mem: pointsDTO(set.Mem), Sentence: set.Sentence, Lineage: set.Lineage, NoData: set.NoData}
}

func pointsDTO(ps []metrics.Point) []PointDTO {
	out := make([]PointDTO, len(ps))
	for i, p := range ps {
		out[i] = PointDTO{T: p.Unix, V: p.Value}
	}
	return out
}

// GetNodeUtilization returns per-node pod-attributed live usage for the nodes
// lens. Available=true with a Message means usable-but-caveated (the pod-list
// cap under-count) - the view renders it as a caption, never discards data.
func (s *MetricsService) GetNodeUtilization(cluster string) NodeUtilizationResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return NodeUtilizationResultDTO{Items: []NodeUsageDTO{}, Message: "cluster not connected"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)
	defer cancel()
	usages, st := conn.NodeUtilization(ctx)
	out := NodeUtilizationResultDTO{Available: st.Available, Message: st.Message, Items: make([]NodeUsageDTO, 0, len(usages)), Sentence: st.Sentence, Lineage: st.Lineage, NoData: st.NoData}
	if !st.UpdatedAt.IsZero() {
		out.UpdatedAtUnix = st.UpdatedAt.Unix()
	}
	for _, u := range usages {
		out.Items = append(out.Items, NodeUsageDTO{Node: u.Node, CPU: u.CPU, Mem: u.Mem, SampledPods: u.SampledPods})
	}
	return out
}

// GetPodContainerUsage returns one pod's live per-container usage - the pod
// inspector joins it to each container's declared requests/limits for the
// rightsizing readout. Sorted by container name for a stable render.
func (s *MetricsService) GetPodContainerUsage(cluster, namespace, pod string) PodUsageResultDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return PodUsageResultDTO{Items: []ContainerUsageDTO{}, Message: "cluster not connected"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsTimeout)
	defer cancel()
	byName, st := conn.PodContainerUsage(ctx, namespace, pod)
	out := PodUsageResultDTO{Available: st.Available, Message: st.Message, Items: make([]ContainerUsageDTO, 0, len(byName)), Sentence: st.Sentence, Lineage: st.Lineage, NoData: st.NoData}
	if !st.UpdatedAt.IsZero() {
		out.UpdatedAtUnix = st.UpdatedAt.Unix()
	}
	for name, u := range byName {
		out.Items = append(out.Items, ContainerUsageDTO{Container: name, CPU: u.CPU, Mem: u.Mem})
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Container < out.Items[j].Container })
	return out
}

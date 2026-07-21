package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/workloads"
)

type fakeMetricsConn struct {
	cm        metrics.ClusterMetrics
	cap       metrics.MetricsCapability
	spark     fleet.SparklineSet
	sparkErr  error
	nodeUsage []workloads.NodeUsage
	podUsage  map[string]workloads.Usage
	usageSt   workloads.UsageStatus
}

func (f fakeMetricsConn) ClusterMetrics(context.Context, bool) (metrics.ClusterMetrics, metrics.MetricsCapability) {
	return f.cm, f.cap
}

func (f fakeMetricsConn) WorkloadSparklines(context.Context, string, string, string) (fleet.SparklineSet, error) {
	return f.spark, f.sparkErr
}

func (f fakeMetricsConn) ClusterSparklines(context.Context) (fleet.SparklineSet, error) {
	return f.spark, f.sparkErr
}

func (f fakeMetricsConn) NodeUtilization(context.Context) ([]workloads.NodeUsage, workloads.UsageStatus) {
	return f.nodeUsage, f.usageSt
}

func (f fakeMetricsConn) PodContainerUsage(context.Context, string, string) (map[string]workloads.Usage, workloads.UsageStatus) {
	return f.podUsage, f.usageSt
}

func TestGetClusterMetrics(t *testing.T) {
	t.Run("cluster miss → unavailable", func(t *testing.T) {
		s := NewMetricsService(func(string) (MetricsConn, bool) { return nil, false })
		dto := s.GetClusterMetrics("nope", false)
		if dto.Available || dto.Mode != string(metrics.ModeUnavailable) {
			t.Fatalf("got %+v", dto)
		}
	})
	t.Run("available with fractions", func(t *testing.T) {
		cpu, mem := 0.38, 0.61
		conn := fakeMetricsConn{
			cm:  metrics.ClusterMetrics{CPUFraction: &cpu, MemFraction: &mem},
			cap: metrics.MetricsCapability{Available: true, Mode: metrics.ModeDiscovered, Source: "monitoring/prometheus-operated:9090"},
		}
		s := NewMetricsService(func(string) (MetricsConn, bool) { return conn, true })
		dto := s.GetClusterMetrics("c", false)
		if !dto.Available || dto.CPUFraction == nil || *dto.CPUFraction != 0.38 || dto.Source == "" {
			t.Fatalf("got %+v", dto)
		}
	})
	t.Run("available but nil fractions stay nil", func(t *testing.T) {
		conn := fakeMetricsConn{cap: metrics.MetricsCapability{Available: true, Mode: metrics.ModeExplicitEndpoint}}
		s := NewMetricsService(func(string) (MetricsConn, bool) { return conn, true })
		dto := s.GetClusterMetrics("c", false)
		if dto.CPUFraction != nil || dto.MemFraction != nil {
			t.Fatal("nil fractions must round-trip as nil")
		}
	})
}

func TestGetWorkloadSparklines(t *testing.T) {
	t.Run("cluster miss → unavailable with message", func(t *testing.T) {
		s := NewMetricsService(func(string) (MetricsConn, bool) { return nil, false })
		dto := s.GetWorkloadSparklines("nope", "ns", "Deployment", "api")
		if dto.Available || dto.Message != "cluster not connected" {
			t.Fatalf("got %+v", dto)
		}
	})
	t.Run("error → unavailable with reason", func(t *testing.T) {
		conn := fakeMetricsConn{sparkErr: errors.New("metrics unavailable: no source")}
		s := NewMetricsService(func(string) (MetricsConn, bool) { return conn, true })
		dto := s.GetWorkloadSparklines("c", "ns", "Deployment", "api")
		if dto.Available || dto.Message != "metrics unavailable: no source" {
			t.Fatalf("got %+v", dto)
		}
	})
	t.Run("series map through with timestamps", func(t *testing.T) {
		conn := fakeMetricsConn{spark: fleet.SparklineSet{
			CPU: []metrics.Point{{Unix: 100, Value: 0.5}, {Unix: 220, Value: 0.7}}, // gap at 160 preserved
			Mem: []metrics.Point{},
		}}
		s := NewMetricsService(func(string) (MetricsConn, bool) { return conn, true })
		dto := s.GetWorkloadSparklines("c", "ns", "Deployment", "api")
		if !dto.Available || len(dto.CPU) != 2 || dto.CPU[1] != (PointDTO{T: 220, V: 0.7}) {
			t.Fatalf("got %+v", dto)
		}
		if dto.Mem == nil || len(dto.Mem) != 0 {
			t.Fatalf("empty series must stay empty (non-nil), got %+v", dto.Mem)
		}
	})
}

func TestGetClusterSparklines(t *testing.T) {
	conn := fakeMetricsConn{spark: fleet.SparklineSet{
		CPU: []metrics.Point{{Unix: 1, Value: 0.4}},
		Mem: []metrics.Point{{Unix: 1, Value: 0.6}},
	}}
	s := NewMetricsService(func(string) (MetricsConn, bool) { return conn, true })
	dto := s.GetClusterSparklines("c")
	if !dto.Available || len(dto.CPU) != 1 || len(dto.Mem) != 1 || dto.Mem[0].V != 0.6 {
		t.Fatalf("got %+v", dto)
	}
}

func TestGetNodeUtilization(t *testing.T) {
	cpu, mem := 0.75, 4096.0
	conn := fakeMetricsConn{
		nodeUsage: []workloads.NodeUsage{{Node: "node-1", CPU: &cpu, Mem: &mem, SampledPods: 3}},
		usageSt:   workloads.UsageStatus{Available: true, Message: "pod list capped - node usage may under-count", UpdatedAt: time.Unix(1700, 0)},
	}
	s := NewMetricsService(func(string) (MetricsConn, bool) { return conn, true })
	dto := s.GetNodeUtilization("c")
	if !dto.Available || len(dto.Items) != 1 || dto.UpdatedAtUnix != 1700 {
		t.Fatalf("dto: %+v", dto)
	}
	it := dto.Items[0]
	if it.Node != "node-1" || it.CPU == nil || *it.CPU != 0.75 || it.Mem == nil || *it.Mem != 4096 || it.SampledPods != 3 {
		t.Fatalf("item: %+v", it)
	}
	// Available=true + Message is the under-count caveat, and must survive the remap.
	if dto.Message == "" {
		t.Fatal("the cap caveat must reach the UI")
	}

	miss := NewMetricsService(func(string) (MetricsConn, bool) { return nil, false }).GetNodeUtilization("nope")
	if miss.Available || miss.Message == "" || miss.Items == nil {
		t.Fatalf("cluster miss must be honest and non-nil: %+v", miss)
	}
}

func TestGetPodContainerUsage(t *testing.T) {
	cpu := 0.02
	conn := fakeMetricsConn{
		podUsage: map[string]workloads.Usage{
			"sidecar": {CPU: &cpu},
			"app":     {},
		},
		usageSt: workloads.UsageStatus{Available: true, UpdatedAt: time.Unix(1800, 0)},
	}
	s := NewMetricsService(func(string) (MetricsConn, bool) { return conn, true })
	dto := s.GetPodContainerUsage("c", "ns", "pod")
	if !dto.Available || len(dto.Items) != 2 {
		t.Fatalf("dto: %+v", dto)
	}
	// Sorted by container name for a stable render.
	if dto.Items[0].Container != "app" || dto.Items[1].Container != "sidecar" {
		t.Fatalf("order: %+v", dto.Items)
	}
	if dto.Items[1].CPU == nil || *dto.Items[1].CPU != 0.02 || dto.Items[1].Mem != nil {
		t.Fatalf("sidecar: %+v", dto.Items[1])
	}
}

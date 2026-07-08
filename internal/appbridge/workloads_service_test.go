package appbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/moomora/klyx/internal/workloads"
)

type fakeWLConn struct {
	wl              []workloads.Workload
	flux            bool
	truncated       bool
	rolloutErr      error
	lastRestartKind string
	scaleErr        error
	lastScaleKind   string
	lastScaleN      int32
}

func (f *fakeWLConn) ListWorkloads(context.Context, string) ([]workloads.Workload, bool, bool, error) {
	return f.wl, f.flux, f.truncated, nil
}

func (f *fakeWLConn) WorkloadMetrics(context.Context, string) (map[string]workloads.Usage, workloads.UsageStatus) {
	cpu := 0.3
	return map[string]workloads.Usage{"Deployment/ns/api": {CPU: &cpu}}, workloads.UsageStatus{Available: true}
}

func (f *fakeWLConn) RolloutRestart(_ context.Context, kind, _, _ string) error {
	f.lastRestartKind = kind
	return f.rolloutErr
}

func (f *fakeWLConn) ScaleWorkload(_ context.Context, kind, _, _ string, replicas int32) error {
	f.lastScaleKind = kind
	f.lastScaleN = replicas
	return f.scaleErr
}

func (f *fakeWLConn) WatchDirty(context.Context, string, []string, func(), func(bool)) (func(), error) {
	return func() {}, nil
}

func TestListWorkloadsDTO(t *testing.T) {
	t.Run("cluster miss -> empty non-nil", func(t *testing.T) {
		s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return nil, false }, nil)
		dto := s.ListWorkloads("nope", "")
		if dto.Workloads == nil || dto.Namespaces == nil {
			t.Fatal("slices must be non-nil")
		}
	})
	t.Run("maps + namespaces on all-load + rank string + flux", func(t *testing.T) {
		conn := &fakeWLConn{flux: true, wl: []workloads.Workload{
			{Kind: "Deployment", Namespace: "b", Name: "x", Desired: 1, Ready: 0, Rank: workloads.Unhealthy, Reason: "CrashLoopBackOff",
				GitOps: &workloads.Owner{Kind: "Kustomization", Namespace: "flux-system", Name: "x"},
				Pods:   []workloads.Pod{{Name: "x-1", Ready: false, Restarts: 5, Reason: "CrashLoopBackOff", Node: "n1", AgeSeconds: 30}}},
			{Kind: "DaemonSet", Namespace: "a", Name: "y", Desired: 3, Ready: 3, Rank: workloads.Healthy},
		}}
		s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true }, nil)

		all := s.ListWorkloads("c", "")
		if !all.FluxPresent {
			t.Fatal("want FluxPresent true")
		}
		if len(all.Workloads) != 2 || all.Workloads[0].Rank != "unhealthy" {
			t.Fatalf("workloads: %+v", all.Workloads)
		}
		if all.Workloads[0].GitOps == nil || all.Workloads[0].GitOps.Name != "x" {
			t.Fatalf("owner: %+v", all.Workloads[0].GitOps)
		}
		if len(all.Workloads[0].Pods) != 1 || all.Workloads[0].Pods[0].AgeSeconds != 30 {
			t.Fatalf("pods: %+v", all.Workloads[0].Pods)
		}
		// Namespaces populated (sorted distinct) ONLY on all-load.
		if len(all.Namespaces) != 2 || all.Namespaces[0] != "a" || all.Namespaces[1] != "b" {
			t.Fatalf("namespaces: %+v", all.Namespaces)
		}
		scoped := s.ListWorkloads("c", "b")
		if len(scoped.Namespaces) != 0 {
			t.Fatalf("scoped namespaces should be empty, got %+v", scoped.Namespaces)
		}
	})
	t.Run("truncated flag propagates", func(t *testing.T) {
		conn := &fakeWLConn{truncated: true}
		s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true }, nil)
		if dto := s.ListWorkloads("c", ""); !dto.Truncated {
			t.Fatalf("truncated must propagate, got %+v", dto)
		}
		conn.truncated = false
		if dto := s.ListWorkloads("c", ""); dto.Truncated {
			t.Fatal("non-truncated list must not set Truncated")
		}
	})
}

func TestGetWorkloadMetricsDTO(t *testing.T) {
	s := NewWorkloadsService(func(name string) (WorkloadsConn, bool) {
		if name == "c" {
			return &fakeWLConn{}, true
		}
		return nil, false
	}, nil)

	t.Run("cluster miss returns non-nil empty + unavailable", func(t *testing.T) {
		r := s.GetWorkloadMetrics("nope", "")
		if r.Usage == nil || len(r.Usage) != 0 || r.Status.Available {
			t.Fatalf("got %+v", r)
		}
	})

	t.Run("maps usage by workload key", func(t *testing.T) {
		r := s.GetWorkloadMetrics("c", "")
		if !r.Status.Available {
			t.Fatalf("status: %+v", r.Status)
		}
		u, ok := r.Usage["Deployment/ns/api"]
		if !ok || u.CPUUsage == nil || *u.CPUUsage != 0.3 {
			t.Fatalf("usage: %+v", r.Usage)
		}
		if u.MemUsage != nil {
			t.Fatalf("mem should be nil, got %v", *u.MemUsage)
		}
	})
}

// --- RolloutRestart tests ---

func TestWorkloadsService_RolloutRestart_ClusterMiss(t *testing.T) {
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return nil, false }, nil)
	r := s.RolloutRestart("ghost", "Deployment", "ns", "api")
	if r.OK || r.Error == "" {
		t.Fatalf("want failure for unknown cluster, got %+v", r)
	}
	if r.Error != "cluster not connected: ghost" {
		t.Errorf("error message: %q", r.Error)
	}
}

func TestWorkloadsService_RolloutRestart_Success(t *testing.T) {
	conn := &fakeWLConn{}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true }, nil)
	r := s.RolloutRestart("c", "Deployment", "default", "api")
	if !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
	if conn.lastRestartKind != "Deployment" {
		t.Errorf("kind: got %q, want Deployment", conn.lastRestartKind)
	}
}

func TestWorkloadsService_RolloutRestart_ErrorSurfaced(t *testing.T) {
	conn := &fakeWLConn{rolloutErr: errors.New("unsupported kind \"Job\"")}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true }, nil)
	r := s.RolloutRestart("c", "Job", "default", "migrate")
	if r.OK || r.Error == "" {
		t.Fatalf("want failure surfaced, got %+v", r)
	}
}

// --- ScaleWorkload tests ---

func TestWorkloadsService_ScaleWorkload_ClusterMiss(t *testing.T) {
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return nil, false }, nil)
	r := s.ScaleWorkload("ghost", "Deployment", "ns", "api", 3)
	if r.OK || r.Error == "" {
		t.Fatalf("want failure for unknown cluster, got %+v", r)
	}
	if r.Error != "cluster not connected: ghost" {
		t.Errorf("error message: %q", r.Error)
	}
}

func TestWorkloadsService_ScaleWorkload_Success(t *testing.T) {
	conn := &fakeWLConn{}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true }, nil)
	r := s.ScaleWorkload("c", "Deployment", "prod", "api", 5)
	if !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
	if conn.lastScaleKind != "Deployment" {
		t.Errorf("kind: got %q, want Deployment", conn.lastScaleKind)
	}
	if conn.lastScaleN != 5 {
		t.Errorf("replicas: got %d, want 5", conn.lastScaleN)
	}
}

func TestWorkloadsService_ScaleWorkload_StatefulSet(t *testing.T) {
	conn := &fakeWLConn{}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true }, nil)
	r := s.ScaleWorkload("c", "StatefulSet", "db", "pg", 3)
	if !r.OK || r.Error != "" {
		t.Fatalf("want OK for StatefulSet, got %+v", r)
	}
	if conn.lastScaleKind != "StatefulSet" || conn.lastScaleN != 3 {
		t.Errorf("got kind=%q replicas=%d", conn.lastScaleKind, conn.lastScaleN)
	}
}

func TestWorkloadsService_ScaleWorkload_ErrorSurfaced(t *testing.T) {
	conn := &fakeWLConn{scaleErr: errors.New("unsupported kind \"DaemonSet\"")}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true }, nil)
	r := s.ScaleWorkload("c", "DaemonSet", "kube-system", "cilium", 1)
	if r.OK || r.Error == "" {
		t.Fatalf("want failure surfaced, got %+v", r)
	}
}

func TestWorkloadsService_ScaleWorkload_NegativeBoundsRejected(t *testing.T) {
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return &fakeWLConn{}, true }, nil)
	r := s.ScaleWorkload("c", "Deployment", "ns", "api", -1)
	if r.OK || r.Error == "" {
		t.Fatalf("want bounds error, got %+v", r)
	}
}

func TestWorkloadsService_ScaleWorkload_UpperBoundsRejected(t *testing.T) {
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return &fakeWLConn{}, true }, nil)
	r := s.ScaleWorkload("c", "Deployment", "ns", "api", maxScaleReplicas+1)
	if r.OK || r.Error == "" {
		t.Fatalf("want bounds error for replicas > %d, got %+v", maxScaleReplicas, r)
	}
}

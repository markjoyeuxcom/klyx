package appbridge

import (
	"context"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

type fakeWLConn struct {
	rolloutErr      error
	lastRestartKind string
	scaleErr        error
	lastScaleKind   string
	lastScaleN      int32
	compareInfo     fleet.WorkloadCompareInfo
	compareErr      error
	history         []workloads.RolloutRevision
	historyUID      types.UID
	historyErr      error
	undoErr         error
	lastUndoRev     int64
	lastExpect      *fleet.RevisionExpect
	lastPause       *bool
	lastUID         string // the armed UID the last verb carried
}

func (f *fakeWLConn) RolloutHistory(context.Context, string, string, string) ([]workloads.RolloutRevision, types.UID, error) {
	return f.history, f.historyUID, f.historyErr
}

func (f *fakeWLConn) RolloutUndo(_ context.Context, _, _, _, uid string, revision int64, expect *fleet.RevisionExpect) error {
	f.lastUndoRev, f.lastUID, f.lastExpect = revision, uid, expect
	return f.undoErr
}

func (f *fakeWLConn) RolloutPause(_ context.Context, _, _, uid string, pause bool) error {
	f.lastPause, f.lastUID = &pause, uid
	return nil
}

func (f *fakeWLConn) WorkloadCompare(context.Context, string, string, string) (fleet.WorkloadCompareInfo, error) {
	return f.compareInfo, f.compareErr
}

func (f *fakeWLConn) WorkloadMetrics(context.Context, string) (map[string]workloads.Usage, workloads.UsageStatus) {
	cpu := 0.3
	return map[string]workloads.Usage{"Deployment/ns/api": {CPU: &cpu}}, workloads.UsageStatus{Available: true}
}

func (f *fakeWLConn) RolloutRestart(ctx context.Context, kind, _, _, uid string) error {
	f.lastRestartKind, f.lastUID = kind, uid
	if f.rolloutErr == nil {
		fleet.NoteReceipt(ctx, fleet.Receipt{UID: uid, Generation: 7, Token: "t-1", PriorGeneration: 6})
	}
	return f.rolloutErr
}

func (f *fakeWLConn) ScaleWorkload(_ context.Context, kind, _, _, uid string, replicas int32) error {
	f.lastScaleKind, f.lastUID = kind, uid
	f.lastScaleN = replicas
	return f.scaleErr
}

func (f *fakeWLConn) WatchDirty(context.Context, string, []string, func(), func(bool)) (func(), error) {
	return func() {}, nil
}

// The workload converter (the rows transport uses it): rank string, GitOps
// owner and the nested pods.
func TestToWorkloadDTO(t *testing.T) {
	w := toWorkloadDTO(workloads.Workload{Kind: "Deployment", Namespace: "b", Name: "x", Desired: 1, Ready: 0, Rank: workloads.Unhealthy, Reason: "CrashLoopBackOff",
		GitOps: &workloads.Owner{Kind: "Kustomization", Namespace: "flux-system", Name: "x"},
		Pods:   []workloads.Pod{{Name: "x-1", Ready: false, Restarts: 5, Reason: "CrashLoopBackOff", Node: "n1", AgeSeconds: 30}}})
	if w.Rank != "unhealthy" {
		t.Fatalf("rank: %+v", w)
	}
	if w.GitOps == nil || w.GitOps.Name != "x" {
		t.Fatalf("owner: %+v", w.GitOps)
	}
	if len(w.Pods) != 1 || w.Pods[0].AgeSeconds != 30 {
		t.Fatalf("pods: %+v", w.Pods)
	}
}

func TestWorkloadCompareMapsToDTO(t *testing.T) {
	conn := &fakeWLConn{compareInfo: fleet.WorkloadCompareInfo{
		Found: true, Kind: "Deployment", Namespace: "shop", Name: "cart",
		DesiredReplicas: 3, ReadyReplicas: 3, SpecHash: "abc123def456",
		Containers: []fleet.CompareContainer{{Name: "app", Image: "shop/cart:v1", Resources: "req 100m/128Mi"}},
		OwnerKind:  "Kustomization", OwnerName: "apps", OwnerRevision: "main@sha1:abc",
	}}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true })

	dto := s.WorkloadCompare("c", "Deployment", "shop", "cart")
	if !dto.Found || dto.SpecHash != "abc123def456" || dto.OwnerRevision != "main@sha1:abc" {
		t.Fatalf("facts not mapped: %+v", dto)
	}
	if len(dto.Containers) != 1 || dto.Containers[0].Image != "shop/cart:v1" {
		t.Fatalf("containers not mapped: %+v", dto.Containers)
	}

	// A degraded lookup carries its error - never mistaken for absence.
	conn.compareErr = errors.New("forbidden")
	if d := s.WorkloadCompare("c", "Deployment", "shop", "cart"); d.Found || d.Error == "" {
		t.Fatalf("error must fold into DTO: %+v", d)
	}
	// Cluster miss too.
	miss := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return nil, false })
	if d := miss.WorkloadCompare("nope", "Deployment", "shop", "cart"); d.Error == "" {
		t.Fatalf("cluster miss must carry an error: %+v", d)
	}
}

func TestGetWorkloadMetricsDTO(t *testing.T) {
	s := NewWorkloadsService(func(name string) (WorkloadsConn, bool) {
		if name == "c" {
			return &fakeWLConn{}, true
		}
		return nil, false
	})

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
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return nil, false })
	r := s.RolloutRestart("ghost", "Deployment", "ns", "api", "uid-api")
	if r.OK || r.Error == "" {
		t.Fatalf("want failure for unknown cluster, got %+v", r)
	}
	if r.Error != "cluster not connected: ghost" {
		t.Errorf("error message: %q", r.Error)
	}
}

func TestWorkloadsService_RolloutRestart_Success(t *testing.T) {
	conn := &fakeWLConn{}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true })
	r := s.RolloutRestart("c", "Deployment", "default", "api", "uid-api")
	if !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
	if conn.lastRestartKind != "Deployment" {
		t.Errorf("kind: got %q, want Deployment", conn.lastRestartKind)
	}
}

func TestWorkloadsService_RolloutRestart_ErrorSurfaced(t *testing.T) {
	conn := &fakeWLConn{rolloutErr: errors.New("unsupported kind \"Job\"")}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true })
	r := s.RolloutRestart("c", "Job", "default", "migrate", "uid-migrate")
	if r.OK || r.Error == "" {
		t.Fatalf("want failure surfaced, got %+v", r)
	}
}

// --- ScaleWorkload tests ---

func TestWorkloadsService_ScaleWorkload_ClusterMiss(t *testing.T) {
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return nil, false })
	r := s.ScaleWorkload("ghost", "Deployment", "ns", "api", "uid-api", 3)
	if r.OK || r.Error == "" {
		t.Fatalf("want failure for unknown cluster, got %+v", r)
	}
	if r.Error != "cluster not connected: ghost" {
		t.Errorf("error message: %q", r.Error)
	}
}

func TestWorkloadsService_ScaleWorkload_Success(t *testing.T) {
	conn := &fakeWLConn{}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true })
	r := s.ScaleWorkload("c", "Deployment", "prod", "api", "uid-api", 5)
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
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true })
	r := s.ScaleWorkload("c", "StatefulSet", "db", "pg", "uid-pg", 3)
	if !r.OK || r.Error != "" {
		t.Fatalf("want OK for StatefulSet, got %+v", r)
	}
	if conn.lastScaleKind != "StatefulSet" || conn.lastScaleN != 3 {
		t.Errorf("got kind=%q replicas=%d", conn.lastScaleKind, conn.lastScaleN)
	}
}

func TestWorkloadsService_ScaleWorkload_ErrorSurfaced(t *testing.T) {
	conn := &fakeWLConn{scaleErr: errors.New("unsupported kind \"DaemonSet\"")}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true })
	r := s.ScaleWorkload("c", "DaemonSet", "kube-system", "cilium", "uid-cilium", 1)
	if r.OK || r.Error == "" {
		t.Fatalf("want failure surfaced, got %+v", r)
	}
}

func TestWorkloadsService_ScaleWorkload_NegativeBoundsRejected(t *testing.T) {
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return &fakeWLConn{}, true })
	r := s.ScaleWorkload("c", "Deployment", "ns", "api", "uid-api", -1)
	if r.OK || r.Error == "" {
		t.Fatalf("want bounds error, got %+v", r)
	}
}

func TestWorkloadsService_ScaleWorkload_UpperBoundsRejected(t *testing.T) {
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return &fakeWLConn{}, true })
	r := s.ScaleWorkload("c", "Deployment", "ns", "api", "uid-api", maxScaleReplicas+1)
	if r.OK || r.Error == "" {
		t.Fatalf("want bounds error for replicas > %d, got %+v", maxScaleReplicas, r)
	}
}

func wlSvc(conn *fakeWLConn) *WorkloadsService {
	return NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true })
}

func TestRolloutHistoryMapsToDTO(t *testing.T) {
	conn := &fakeWLConn{historyUID: "uid-cart", history: []workloads.RolloutRevision{
		{Revision: 3, AgeSeconds: 600, Images: []string{"shop/cart:v3"}, Current: true},
		{Revision: 2, AgeSeconds: 3600, Images: nil, ChangeCause: "hotfix"},
	}}
	svc := wlSvc(conn)
	svc.now = func() time.Time { return time.Unix(5000, 0) }
	dto := svc.RolloutHistory("c", "Deployment", "shop", "cart")
	if dto.Error != "" || len(dto.Revisions) != 2 {
		t.Fatalf("dto: %+v", dto)
	}
	// the history names the object it was read from, and when (§5b)
	if dto.UID != "uid-cart" || dto.AsOfUnix != 5000 {
		t.Fatalf("history identity: uid=%q asOf=%d", dto.UID, dto.AsOfUnix)
	}
	if !dto.Revisions[0].Current || dto.Revisions[0].Images[0] != "shop/cart:v3" {
		t.Fatalf("head: %+v", dto.Revisions[0])
	}
	// nil images must serialize as [], never null.
	if dto.Revisions[1].Images == nil || dto.Revisions[1].ChangeCause != "hotfix" {
		t.Fatalf("second: %+v", dto.Revisions[1])
	}

	conn.historyErr = errors.New("boom")
	if dto := wlSvc(conn).RolloutHistory("c", "Deployment", "shop", "cart"); dto.Error != "boom" || dto.Revisions == nil {
		t.Fatalf("error fold-in: %+v", dto)
	}
}

func TestRolloutUndoGatedByProtectGuard(t *testing.T) {
	prev := activeProtectGuard.Load()
	t.Cleanup(func() { activeProtectGuard.Store(prev) })
	SetProtectGuard(NewProtectGuard(func(c string) bool { return c == "prd" }, time.Now))

	conn := &fakeWLConn{}
	svc := wlSvc(conn)

	// Unarmed on a protected cluster: refused before the conn is touched.
	if r := svc.RolloutUndo("prd", "Deployment", "shop", "cart", "uid-cart", 2, "", ""); r.OK || r.Error == "" {
		t.Fatalf("unarmed undo on prd must refuse: %+v", r)
	}
	if conn.lastUndoRev != 0 {
		t.Fatal("refused undo must never reach the conn")
	}

	// Armed: the one-shot grant admits exactly one undo.
	if err := activeProtectGuard.Load().Arm("prd", "prd"); err != nil {
		t.Fatal(err)
	}
	if r := svc.RolloutUndo("prd", "Deployment", "shop", "cart", "uid-cart", 2, "", ""); !r.OK {
		t.Fatalf("armed undo: %+v", r)
	}
	if conn.lastUndoRev != 2 {
		t.Fatalf("revision passthrough: %d", conn.lastUndoRev)
	}
	// The grant was consumed - a second undo without re-arming refuses.
	if r := svc.RolloutUndo("prd", "Deployment", "shop", "cart", "uid-cart", 1, "", ""); r.OK {
		t.Fatalf("grant must be one-shot: %+v", r)
	}
}

func TestRolloutPausePassesThroughAndGates(t *testing.T) {
	prev := activeProtectGuard.Load()
	t.Cleanup(func() { activeProtectGuard.Store(prev) })
	SetProtectGuard(NewProtectGuard(func(c string) bool { return c == "prd" }, time.Now))

	conn := &fakeWLConn{}
	svc := wlSvc(conn)
	if r := svc.RolloutPause("dev", "shop", "cart", "uid-cart", true); !r.OK || conn.lastPause == nil || !*conn.lastPause {
		t.Fatalf("pause on unprotected: %+v %+v", r, conn.lastPause)
	}
	if r := svc.RolloutPause("dev", "shop", "cart", "uid-cart", false); !r.OK || *conn.lastPause {
		t.Fatalf("resume: %+v", r)
	}
	if r := svc.RolloutPause("prd", "shop", "cart", "uid-cart", true); r.OK {
		t.Fatalf("unarmed pause on prd must refuse: %+v", r)
	}
}

func TestWorkloadDTOCarriesPaused(t *testing.T) {
	dto := toWorkloadDTO(workloads.Workload{Kind: "Deployment", Namespace: "x", Name: "held", Paused: true})
	if !dto.Paused {
		t.Fatalf("paused must carry: %+v", dto)
	}
}

// Every workload verb passes the UID it was armed against to the cluster
// (design §5b): the fleet layer writes only that object.
func TestWorkloadsService_VerbsCarryTheArmedUID(t *testing.T) {
	conn := &fakeWLConn{}
	s := NewWorkloadsService(func(string) (WorkloadsConn, bool) { return conn, true })
	for name, run := range map[string]func() ActionResultDTO{
		"restart": func() ActionResultDTO { return s.RolloutRestart("c", "Deployment", "ns", "api", "uid-armed") },
		"scale":   func() ActionResultDTO { return s.ScaleWorkload("c", "Deployment", "ns", "api", "uid-armed", 2) },
		"undo":    func() ActionResultDTO { return s.RolloutUndo("c", "Deployment", "ns", "api", "uid-armed", 1, "", "") },
		"pause":   func() ActionResultDTO { return s.RolloutPause("c", "ns", "api", "uid-armed", true) },
	} {
		conn.lastUID = ""
		if r := run(); !r.OK || conn.lastUID != "uid-armed" {
			t.Fatalf("%s: the armed UID must reach the cluster: %+v uid=%q", name, r, conn.lastUID)
		}
	}
}

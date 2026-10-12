package appbridge

import (
	"context"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

// fakePodConn satisfies PodsConn without importing the real fleet.ClusterConn.
type fakePodConn struct {
	detail    fleet.PodDetail
	detErr    error
	deleteErr error
	deleted   []string // records "<namespace>/<name>" for assertion
	launched  string   // records "<name>|<image>" for assertion
	launchTTL int64
}

func (f *fakePodConn) LaunchDiagnosticPod(_ context.Context, name, image string, ttl int64) fleet.AdhocResult {
	f.launched = name + "|" + image
	f.launchTTL = ttl
	return fleet.AdhocResult{Kind: "Pod", Namespace: fleet.ScratchNamespace, Name: name, OK: true}
}

func (f *fakePodConn) PodDetail(_ context.Context, _, _ string) (fleet.PodDetail, error) {
	return f.detail, f.detErr
}

func (f *fakePodConn) DeletePod(_ context.Context, namespace, name, uid string) error {
	f.deleted = append(f.deleted, namespace+"/"+name+"@"+uid)
	return f.deleteErr
}

func (f *fakePodConn) WatchDirty(context.Context, string, []string, func(), func(bool)) (func(), error) {
	return func() {}, nil
}

// --- converter tests ---

// The pod summary converter (the UID-fenced detail uses it).
func TestToPodSummaryDTO_MappingAndRankString(t *testing.T) {
	pods := []workloads.PodSummary{
		{
			Namespace: "b", Name: "crash", Ready: false,
			Phase: "Running", Reason: "CrashLoopBackOff",
			Rank: workloads.Unhealthy, Restarts: 5,
			Node: "n1", IP: "10.0.0.1",
			OwnerKind: "ReplicaSet", OwnerName: "crash-rs",
			AgeSeconds: 60,
			Containers: []workloads.ContainerSummary{
				{Name: "app", Image: "app:1", Ready: false, Restarts: 5, State: "waiting:CrashLoopBackOff",
					LastReason: "OOMKilled", LastExitCode: 137, LastFinishedUnix: 1_700_000_000},
			},
		},
		{
			Namespace: "a", Name: "web", Ready: true,
			Phase: "Running", Rank: workloads.Healthy,
			Node: "n2", IP: "10.0.0.2", AgeSeconds: 120,
			Containers: []workloads.ContainerSummary{
				{Name: "web", Image: "nginx:1", Ready: true, State: "running"},
			},
		},
	}
	all := struct{ Pods []PodSummaryDTO }{Pods: []PodSummaryDTO{toPodSummaryDTO(pods[0]), toPodSummaryDTO(pods[1])}}
	// Rank as string.
	if all.Pods[0].Rank != "unhealthy" {
		t.Errorf("rank[0]: got %q, want unhealthy", all.Pods[0].Rank)
	}
	if all.Pods[1].Rank != "healthy" {
		t.Errorf("rank[1]: got %q, want healthy", all.Pods[1].Rank)
	}
	// Field mapping.
	p := all.Pods[0]
	if p.Name != "crash" || p.Namespace != "b" || p.Restarts != 5 {
		t.Errorf("fields: %+v", p)
	}
	if p.OwnerKind != "ReplicaSet" || p.OwnerName != "crash-rs" {
		t.Errorf("owner: %+v", p)
	}
	if len(p.Containers) != 1 || p.Containers[0].State != "waiting:CrashLoopBackOff" {
		t.Errorf("containers: %+v", p.Containers)
	}
	if c := p.Containers[0]; c.LastReason != "OOMKilled" || c.LastExitCode != 137 || c.LastFinishedUnix != 1_700_000_000 {
		t.Errorf("last termination not mapped: %+v", c)
	}
}

// --- GetPodDetail tests ---

func TestPodsService_GetPodDetail_ClusterMiss_NonNilEmpties(t *testing.T) {
	svc := NewPodsService(func(string) (PodsConn, bool) { return nil, false })
	d := svc.GetPodDetail("nope", "ns", "pod")
	if d.Labels == nil || d.Annotations == nil || d.Conditions == nil || d.Events == nil {
		t.Fatal("collections must be non-nil on cluster miss")
	}
}

func TestPodsService_GetPodDetail_Mapping(t *testing.T) {
	last := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	detail := fleet.PodDetail{
		Summary: workloads.PodSummary{
			Namespace: "ns", Name: "web", Ready: true,
			Phase: "Running", Rank: workloads.Healthy,
			Node: "n1", IP: "10.0.0.1", AgeSeconds: 30,
			Containers: []workloads.ContainerSummary{{Name: "web", Image: "nginx:1", Ready: true, State: "running"}},
		},
		Labels:         map[string]string{"env": "prod"},
		Annotations:    map[string]string{"owner": "platform"},
		Conditions:     []crd.Condition{{Type: "Ready", Status: "True", Reason: "OK", Message: "ready"}},
		Events:         []crd.Event{{Type: "Normal", Reason: "Started", Message: "started", Count: 1, Last: last}},
		YAML:           "kind: Pod\n",
		QoSClass:       "Burstable",
		ServiceAccount: "web-sa",
	}
	conn := &fakePodConn{detail: detail}
	svc := NewPodsService(func(string) (PodsConn, bool) { return conn, true })

	d := svc.GetPodDetail("c", "ns", "web")
	if d.Summary.Name != "web" || d.Summary.Rank != "healthy" {
		t.Errorf("summary: %+v", d.Summary)
	}
	if d.Labels["env"] != "prod" {
		t.Errorf("labels: %+v", d.Labels)
	}
	if d.Annotations["owner"] != "platform" {
		t.Errorf("annotations: %+v", d.Annotations)
	}
	if len(d.Conditions) != 1 || d.Conditions[0].Type != "Ready" {
		t.Errorf("conditions: %+v", d.Conditions)
	}
	if len(d.Events) != 1 || d.Events[0].Reason != "Started" || d.Events[0].LastSeen != "2026-06-09T12:00:00Z" {
		t.Errorf("events: %+v", d.Events)
	}
	if d.YAML != "kind: Pod\n" {
		t.Errorf("yaml: %q", d.YAML)
	}
	if d.QosClass != "Burstable" || d.ServiceAccount != "web-sa" {
		t.Errorf("qosClass/sa: %q %q", d.QosClass, d.ServiceAccount)
	}
}

func TestPodsService_GetPodDetail_ErrorReturnsEmpties(t *testing.T) {
	conn := &fakePodConn{detErr: context.DeadlineExceeded}
	svc := NewPodsService(func(string) (PodsConn, bool) { return conn, true })
	d := svc.GetPodDetail("c", "ns", "missing")
	if d.Labels == nil || d.Annotations == nil || d.Conditions == nil || d.Events == nil {
		t.Fatal("collections must be non-nil on error")
	}
	if d.Summary.Name != "" {
		t.Errorf("summary should be zero on error, got %+v", d.Summary)
	}
	if d.Error == "" {
		t.Error("a failed detail read must carry Error")
	}
}

// --- DeletePod tests ---

func TestPodsService_DeletePod_ClusterMiss(t *testing.T) {
	svc := NewPodsService(func(string) (PodsConn, bool) { return nil, false })
	r := svc.DeletePod("ghost", "ns", "pod", "uid-pod")
	if r.OK || r.Error == "" {
		t.Fatalf("want failure for unknown cluster, got %+v", r)
	}
	if r.Error != "cluster not connected: ghost" {
		t.Errorf("error message: %q", r.Error)
	}
}

func TestPodsService_DeletePod_Success(t *testing.T) {
	conn := &fakePodConn{}
	svc := NewPodsService(func(string) (PodsConn, bool) { return conn, true })
	r := svc.DeletePod("c", "default", "web-xyz", "uid-web-xyz")
	if !r.OK || r.Error != "" {
		t.Fatalf("want OK, got %+v", r)
	}
	if len(conn.deleted) != 1 || conn.deleted[0] != "default/web-xyz@uid-web-xyz" {
		t.Errorf("deleted records: %v", conn.deleted)
	}
}

func TestPodsService_DeletePod_ErrorSurfaced(t *testing.T) {
	conn := &fakePodConn{deleteErr: context.DeadlineExceeded}
	svc := NewPodsService(func(string) (PodsConn, bool) { return conn, true })
	r := svc.DeletePod("c", "ns", "pod", "uid-pod")
	if r.OK || r.Error == "" {
		t.Fatalf("want failure surfaced, got %+v", r)
	}
}

// A pod row names the pod object and its owner object: the lens arms a
// delete with the pod UID and an owner restart with the owner UID (§5b).
func TestToPodSummaryDTO_CarriesPodAndOwnerIdentity(t *testing.T) {
	p := toPodSummaryDTO(workloads.PodSummary{Namespace: "prod", Name: "web-abc", UID: "uid-pod", OwnerKind: "Deployment", OwnerName: "web", OwnerUID: "uid-web"})
	if p.UID != "uid-pod" || p.OwnerUID != "uid-web" {
		t.Fatalf("pod row identity: uid=%q ownerUid=%q", p.UID, p.OwnerUID)
	}
}

// The detail names the object it read and when (design §5b): the lens shows
// a detail only for the selected row's UID.
func TestPodsService_GetPodDetail_CarriesIdentityAndAsOf(t *testing.T) {
	conn := &fakePodConn{detail: fleet.PodDetail{Summary: workloads.PodSummary{Namespace: "ns", Name: "web-0", UID: "uid-web-0"}}}
	svc := NewPodsService(func(string) (PodsConn, bool) { return conn, true })
	svc.now = func() time.Time { return time.Unix(7000, 0) }
	d := svc.GetPodDetail("c", "ns", "web-0")
	if d.Summary.UID != "uid-web-0" || d.AsOfUnix != 7000 {
		t.Fatalf("detail identity/as-of: uid=%q asOf=%d", d.Summary.UID, d.AsOfUnix)
	}
}

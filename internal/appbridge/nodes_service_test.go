package appbridge

import (
	"context"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

// fakeNodeConn satisfies NodesConn without importing the real fleet.ClusterConn.
type fakeNodeConn struct {
	nodes  []workloads.NodeSummary
	detail fleet.NodeDetail
	detErr error
}

func (f *fakeNodeConn) ListNodes(_ context.Context) ([]workloads.NodeSummary, error) {
	return f.nodes, nil
}

func (f *fakeNodeConn) NodeHeadroom(context.Context, string, string) (fleet.HeadroomReport, error) {
	return fleet.HeadroomReport{}, nil
}

func (f *fakeNodeConn) NodeDetail(_ context.Context, _ string) (fleet.NodeDetail, error) {
	return f.detail, f.detErr
}

// --- ListNodes tests ---

func TestNodesService_ClusterMiss_NonNilEmpties(t *testing.T) {
	svc := NewNodesService(func(string) (NodesConn, bool) { return nil, false })
	dto := svc.ListNodes("nope")
	if dto.Nodes == nil {
		t.Fatal("Nodes slice must be non-nil on cluster miss")
	}
	if len(dto.Nodes) != 0 {
		t.Fatalf("want empty nodes, got %d", len(dto.Nodes))
	}
}

func TestNodesService_ListNodes_Mapping(t *testing.T) {
	nodes := []workloads.NodeSummary{
		{
			Name:           "node-1",
			Roles:          []string{"control-plane"},
			Ready:          false,
			Unschedulable:  false,
			Problems:       []string{"NotReady"},
			Version:        "v1.30.0",
			OS:             "linux",
			Arch:           "amd64",
			TaintCount:     1,
			CPUCapacity:    4.0,
			CPUAllocatable: 3.8,
			MemCapacity:    8 * 1e9,
			MemAllocatable: 7 * 1e9,
			PodCapacity:    110,
			AgeSeconds:     3600,
		},
		{
			Name:       "node-2",
			Ready:      true,
			AgeSeconds: 7200,
		},
	}
	conn := &fakeNodeConn{nodes: nodes}
	svc := NewNodesService(func(string) (NodesConn, bool) { return conn, true })

	dto := svc.ListNodes("c")
	if len(dto.Nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(dto.Nodes))
	}
	n := dto.Nodes[0]
	if n.Name != "node-1" {
		t.Errorf("Name: %q", n.Name)
	}
	if len(n.Roles) != 1 || n.Roles[0] != "control-plane" {
		t.Errorf("Roles: %v", n.Roles)
	}
	if n.Ready {
		t.Error("Ready should be false")
	}
	if len(n.Problems) != 1 || n.Problems[0] != "NotReady" {
		t.Errorf("Problems: %v", n.Problems)
	}
	if n.TaintCount != 1 {
		t.Errorf("TaintCount: %d", n.TaintCount)
	}
	if n.PodCapacity != 110 {
		t.Errorf("PodCapacity: %d", n.PodCapacity)
	}
}

func TestNodesService_ListNodes_NonNilRolesAndProblems(t *testing.T) {
	// NodeSummary with nil slices → DTOs must be non-nil.
	nodes := []workloads.NodeSummary{
		{Name: "worker", Ready: true}, // nil Roles, nil Problems
	}
	conn := &fakeNodeConn{nodes: nodes}
	svc := NewNodesService(func(string) (NodesConn, bool) { return conn, true })

	dto := svc.ListNodes("c")
	n := dto.Nodes[0]
	if n.Roles == nil {
		t.Error("Roles must be non-nil")
	}
	if n.Problems == nil {
		t.Error("Problems must be non-nil")
	}
}

// --- GetNodeDetail tests ---

func TestNodesService_GetNodeDetail_ClusterMiss_NonNilEmpties(t *testing.T) {
	svc := NewNodesService(func(string) (NodesConn, bool) { return nil, false })
	d := svc.GetNodeDetail("nope", "node-1")
	if d.Labels == nil || d.Annotations == nil || d.Taints == nil || d.Conditions == nil || d.Events == nil || d.PodsOnNode == nil {
		t.Fatal("all collections must be non-nil on cluster miss")
	}
}

func TestNodesService_GetNodeDetail_Mapping(t *testing.T) {
	last := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	detail := fleet.NodeDetail{
		Summary: workloads.NodeSummary{
			Name:    "node-1",
			Ready:   true,
			Version: "v1.30.0",
		},
		Labels:      map[string]string{"region": "westeurope"},
		Annotations: map[string]string{"owner": "platform"},
		Taints: []fleet.NodeTaint{
			{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"},
		},
		Conditions: []crd.Condition{
			{Type: "Ready", Status: "True", Reason: "KubeletReady", Message: "kubelet is posting ready status"},
		},
		Events: []crd.Event{
			{Type: "Normal", Reason: "Starting", Message: "started", Count: 1, Last: last},
		},
		YAML: "apiVersion: v1\nkind: Node\n",
		PodsOnNode: []fleet.PodOnNode{
			{Namespace: "default", Name: "web-abc", Phase: "Running"},
		},
	}
	conn := &fakeNodeConn{detail: detail}
	svc := NewNodesService(func(string) (NodesConn, bool) { return conn, true })

	d := svc.GetNodeDetail("c", "node-1")
	if d.Summary.Name != "node-1" || !d.Summary.Ready {
		t.Errorf("summary: %+v", d.Summary)
	}
	if d.Labels["region"] != "westeurope" {
		t.Errorf("labels: %+v", d.Labels)
	}
	if d.Annotations["owner"] != "platform" {
		t.Errorf("annotations: %+v", d.Annotations)
	}
	if len(d.Taints) != 1 || d.Taints[0].Key != "dedicated" || d.Taints[0].Effect != "NoSchedule" {
		t.Errorf("taints: %+v", d.Taints)
	}
	if len(d.Conditions) != 1 || d.Conditions[0].Type != "Ready" {
		t.Errorf("conditions: %+v", d.Conditions)
	}
	if len(d.Events) != 1 || d.Events[0].Reason != "Starting" || d.Events[0].LastSeen != "2026-06-09T12:00:00Z" {
		t.Errorf("events: %+v", d.Events)
	}
	if d.YAML != "apiVersion: v1\nkind: Node\n" {
		t.Errorf("yaml: %q", d.YAML)
	}
	if len(d.PodsOnNode) != 1 || d.PodsOnNode[0].Name != "web-abc" {
		t.Errorf("podsOnNode: %+v", d.PodsOnNode)
	}
}

func TestNodesService_GetNodeDetail_ErrorReturnsEmpties(t *testing.T) {
	conn := &fakeNodeConn{detErr: context.DeadlineExceeded}
	svc := NewNodesService(func(string) (NodesConn, bool) { return conn, true })
	d := svc.GetNodeDetail("c", "missing")
	if d.Labels == nil || d.Annotations == nil || d.Taints == nil || d.Conditions == nil || d.Events == nil || d.PodsOnNode == nil {
		t.Fatal("collections must be non-nil on error")
	}
	if d.Summary.Name != "" {
		t.Errorf("summary should be zero on error, got %+v", d.Summary)
	}
}

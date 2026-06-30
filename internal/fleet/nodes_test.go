package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
)

func makeFakeNode(name string, ready bool) *corev1.Node {
	condStatus := corev1.ConditionTrue
	if !ready {
		condStatus = corev1.ConditionFalse
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			UID:               types.UID("uid-node-" + name),
			CreationTimestamp: metav1.NewTime(time.Unix(100_000, 0)),
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: condStatus},
			},
		},
	}
}

func TestListNodes_OrderProblemFirst(t *testing.T) {
	now := time.Unix(200_000, 0)
	notReady := makeFakeNode("b-sick", false)
	healthy := makeFakeNode("a-healthy", true)

	cs := fake.NewSimpleClientset(healthy, notReady)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(now)}

	out, err := c.ListNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(out))
	}
	if out[0].Ready {
		t.Errorf("out[0] should be problem (not-ready) node")
	}
	if out[0].Name != "b-sick" {
		t.Errorf("out[0].Name: got %q, want b-sick", out[0].Name)
	}
	if out[1].Name != "a-healthy" {
		t.Errorf("out[1].Name: got %q, want a-healthy", out[1].Name)
	}
}

func TestListNodes_NotReadyFirst(t *testing.T) {
	now := time.Unix(200_000, 0)
	notReady := makeFakeNode("z-notready", false)
	healthy := makeFakeNode("a-healthy", true)

	cs := fake.NewSimpleClientset(healthy, notReady)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(now)}

	out, err := c.ListNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Ready {
		t.Error("not-ready node should sort first")
	}
}

func TestNodeDetail_BasicFields(t *testing.T) {
	now := time.Unix(200_000, 0)
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "node-1",
			UID:               "uid-node-1",
			Labels:            map[string]string{"node-role.kubernetes.io/control-plane": ""},
			Annotations:       map[string]string{"owner": "platform"},
			CreationTimestamp: metav1.NewTime(time.Unix(100_000, 0)),
		},
		Spec: corev1.NodeSpec{
			Unschedulable: true,
			Taints: []corev1.Taint{
				{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule},
			},
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion:  "v1.30.0",
				OperatingSystem: "linux",
				Architecture:    "amd64",
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("8Gi"),
			},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("3800m"),
				corev1.ResourceMemory: resource.MustParse("7Gi"),
			},
		},
	}

	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "node-1.ev1", Namespace: ""},
		InvolvedObject: corev1.ObjectReference{UID: "uid-node-1"},
		Type:           "Warning",
		Reason:         "NodeNotReady",
		Message:        "node not ready",
		Count:          1,
		LastTimestamp:  metav1.NewTime(now),
	}

	cs := fake.NewSimpleClientset(n, ev)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(now)}

	d, err := c.NodeDetail(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("NodeDetail: %v", err)
	}

	// Summary fields.
	if d.Summary.Name != "node-1" {
		t.Errorf("Summary.Name: %q", d.Summary.Name)
	}
	if !d.Summary.Ready {
		t.Error("Summary.Ready should be true")
	}
	if !d.Summary.Unschedulable {
		t.Error("Summary.Unschedulable should be true")
	}
	if len(d.Summary.Roles) == 0 || d.Summary.Roles[0] != "control-plane" {
		t.Errorf("Summary.Roles: %v", d.Summary.Roles)
	}

	// Labels.
	if d.Labels == nil {
		t.Fatal("Labels must be non-nil")
	}
	if _, ok := d.Labels["node-role.kubernetes.io/control-plane"]; !ok {
		t.Error("expected role label in Labels map")
	}
	if d.Annotations["owner"] != "platform" {
		t.Errorf("annotations: %+v", d.Annotations)
	}

	// Taints.
	if len(d.Taints) != 1 || d.Taints[0].Key != "node-role.kubernetes.io/control-plane" {
		t.Errorf("Taints: %+v", d.Taints)
	}

	// Conditions.
	found := false
	for _, cond := range d.Conditions {
		if cond.Type == "Ready" && cond.Status == "True" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Ready=True condition, got: %+v", d.Conditions)
	}

	// Events via instanceEvents.
	if len(d.Events) != 1 || d.Events[0].Reason != "NodeNotReady" {
		t.Errorf("Events: %+v", d.Events)
	}

	// YAML.
	if !strings.Contains(d.YAML, "kind: Node") {
		t.Errorf("YAML missing 'kind: Node': %s", d.YAML)
	}
	if strings.Contains(d.YAML, "managedFields") {
		t.Errorf("YAML must not contain managedFields")
	}
}

func TestNodeDetail_PodsOnNode(t *testing.T) {
	now := time.Unix(200_000, 0)
	n := makeFakeNode("node-x", true)
	pod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web-abc", CreationTimestamp: metav1.NewTime(now)},
		Spec:       corev1.PodSpec{NodeName: "node-x", Containers: []corev1.Container{{Name: "c", Image: "i:1"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	pod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "agent-1", CreationTimestamp: metav1.NewTime(now)},
		Spec:       corev1.PodSpec{NodeName: "node-x", Containers: []corev1.Container{{Name: "c", Image: "i:1"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	otherPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "other-xyz", CreationTimestamp: metav1.NewTime(now)},
		Spec:       corev1.PodSpec{NodeName: "other-node", Containers: []corev1.Container{{Name: "c", Image: "i:1"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}

	cs := fake.NewSimpleClientset(n, pod1, pod2, otherPod)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(now)}

	d, err := c.NodeDetail(context.Background(), "node-x")
	if err != nil {
		t.Fatalf("NodeDetail: %v", err)
	}

	// fake clientset doesn't support field selectors — we get all pods; just verify the call succeeds.
	// In production the field selector filters server-side. Here we check that PodsOnNode is non-nil.
	if d.PodsOnNode == nil {
		t.Error("PodsOnNode must be non-nil")
	}
}

func TestNodeDetail_NotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := &ClusterConn{typed: cs, clk: clock.Real{}}
	_, err := c.NodeDetail(context.Background(), "missing")
	if err == nil {
		t.Fatal("want error for missing node, got nil")
	}
}

func TestNodeDetail_EmptyLabelsNonNil(t *testing.T) {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bare",
			UID:  "uid-bare",
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
	cs := fake.NewSimpleClientset(n)
	c := &ClusterConn{typed: cs, clk: clock.Real{}}
	d, err := c.NodeDetail(context.Background(), "bare")
	if err != nil {
		t.Fatalf("NodeDetail: %v", err)
	}
	if d.Labels == nil {
		t.Error("Labels must be non-nil even when node has no labels")
	}
	if d.Annotations == nil {
		t.Error("Annotations must be non-nil even when node has no annotations")
	}
}

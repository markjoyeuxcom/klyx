package workloads

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var testNow = time.Unix(200_000, 0)

func makeNode(name string, ready bool, labels map[string]string) *corev1.Node {
	condStatus := corev1.ConditionTrue
	if !ready {
		condStatus = corev1.ConditionFalse
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Labels:            labels,
			CreationTimestamp: metav1.NewTime(testNow.Add(-time.Hour)),
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: condStatus},
			},
		},
	}
}

func TestSummarizeNodes_OrderProblemFirst(t *testing.T) {
	notReady := makeNode("b-notready", false, nil)
	healthy := makeNode("a-healthy", true, nil)

	out := SummarizeNodes([]corev1.Node{*healthy, *notReady}, testNow)
	if len(out) != 2 {
		t.Fatalf("want 2, got %d", len(out))
	}
	if out[0].Name != "b-notready" {
		t.Errorf("out[0] should be problem node, got %q", out[0].Name)
	}
	if out[1].Name != "a-healthy" {
		t.Errorf("out[1] should be healthy, got %q", out[1].Name)
	}
}

func TestSummarizeNodes_PressureBeforeCordoned(t *testing.T) {
	cordoned := makeNode("z-cordoned", true, nil)
	cordoned.Spec.Unschedulable = true

	pressure := makeNode("a-pressure", true, nil)
	pressure.Status.Conditions = append(pressure.Status.Conditions, corev1.NodeCondition{
		Type:   corev1.NodeMemoryPressure,
		Status: corev1.ConditionTrue,
	})

	healthy := makeNode("m-healthy", true, nil)

	out := SummarizeNodes([]corev1.Node{*cordoned, *healthy, *pressure}, testNow)
	if out[0].Name != "a-pressure" {
		t.Errorf("pressure should be first, got %q", out[0].Name)
	}
	if out[1].Name != "z-cordoned" {
		t.Errorf("cordoned should be second, got %q", out[1].Name)
	}
	if out[2].Name != "m-healthy" {
		t.Errorf("healthy should be last, got %q", out[2].Name)
	}
}

func TestSummarizeNodes_PressureDetection(t *testing.T) {
	n := makeNode("node", true, nil)
	n.Status.Conditions = append(n.Status.Conditions,
		corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
		corev1.NodeCondition{Type: corev1.NodePIDPressure, Status: corev1.ConditionTrue},
		corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse}, // false = no pressure
	)

	out := SummarizeNodes([]corev1.Node{*n}, testNow)
	s := out[0]

	hasDP := false
	hasPID := false
	hasMem := false
	for _, p := range s.Problems {
		switch p {
		case "DiskPressure":
			hasDP = true
		case "PIDPressure":
			hasPID = true
		case "MemoryPressure":
			hasMem = true
		}
	}
	if !hasDP {
		t.Error("DiskPressure should be in Problems")
	}
	if !hasPID {
		t.Error("PIDPressure should be in Problems")
	}
	if hasMem {
		t.Error("MemoryPressure=False must NOT be in Problems")
	}
}

func TestSummarizeNodes_CordonFlag(t *testing.T) {
	n := makeNode("node", true, nil)
	n.Spec.Unschedulable = true

	out := SummarizeNodes([]corev1.Node{*n}, testNow)
	if !out[0].Unschedulable {
		t.Error("Unschedulable should be true for cordoned node")
	}
}

func TestSummarizeNodes_RoleLabelParsing(t *testing.T) {
	n := makeNode("node", true, map[string]string{
		"node-role.kubernetes.io/control-plane": "",
		"node-role.kubernetes.io/master":        "",
		"some-other-label":                      "value",
	})

	out := SummarizeNodes([]corev1.Node{*n}, testNow)
	roles := out[0].Roles
	if len(roles) != 2 {
		t.Fatalf("want 2 roles, got %v", roles)
	}
	// sorted
	if roles[0] != "control-plane" || roles[1] != "master" {
		t.Errorf("roles not sorted: %v", roles)
	}
}

func TestSummarizeNodes_EmptyRoles(t *testing.T) {
	n := makeNode("worker", true, map[string]string{"kubernetes.io/hostname": "worker"})
	out := SummarizeNodes([]corev1.Node{*n}, testNow)
	if len(out[0].Roles) != 0 {
		t.Errorf("want empty roles, got %v", out[0].Roles)
	}
}

func TestSummarizeNodes_Quantities(t *testing.T) {
	n := makeNode("node", true, nil)
	n.Status.Capacity = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("8"),
		corev1.ResourceMemory: resource.MustParse("16Gi"),
		corev1.ResourcePods:   resource.MustParse("110"),
	}
	n.Status.Allocatable = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("7800m"),
		corev1.ResourceMemory: resource.MustParse("15Gi"),
	}

	out := SummarizeNodes([]corev1.Node{*n}, testNow)
	s := out[0]

	if s.CPUCapacity < 7.9 || s.CPUCapacity > 8.1 {
		t.Errorf("CPUCapacity: got %f, want ~8", s.CPUCapacity)
	}
	if s.CPUAllocatable < 7.7 || s.CPUAllocatable > 7.9 {
		t.Errorf("CPUAllocatable: got %f, want ~7.8", s.CPUAllocatable)
	}
	if s.PodCapacity != 110 {
		t.Errorf("PodCapacity: got %d, want 110", s.PodCapacity)
	}
	gi := float64(1 << 30)
	if s.MemCapacity < 15.9*gi || s.MemCapacity > 16.1*gi {
		t.Errorf("MemCapacity: got %f, want ~16Gi", s.MemCapacity)
	}
}

func TestSummarizeNodes_TaintCount(t *testing.T) {
	n := makeNode("node", true, nil)
	n.Spec.Taints = []corev1.Taint{
		{Key: "node.kubernetes.io/not-ready", Effect: corev1.TaintEffectNoExecute},
		{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
	}

	out := SummarizeNodes([]corev1.Node{*n}, testNow)
	if out[0].TaintCount != 2 {
		t.Errorf("TaintCount: got %d, want 2", out[0].TaintCount)
	}
}

func TestSummarizeNodes_NotReadyProblemString(t *testing.T) {
	n := makeNode("node", false, nil)
	out := SummarizeNodes([]corev1.Node{*n}, testNow)
	if len(out[0].Problems) == 0 || out[0].Problems[0] != "NotReady" {
		t.Errorf("Problems should contain NotReady, got %v", out[0].Problems)
	}
	if out[0].Ready {
		t.Error("Ready should be false")
	}
}

// A summary carries its creation time beside the age at its now, so a live
// view ticks the age instead of freezing it between pushes (1c slice 3 §3).
func TestSummarizeNodes_CarriesTheCreationAnchor(t *testing.T) {
	s := SummarizeNodes([]corev1.Node{*makeNode("n1", true, nil)}, testNow)[0]
	if !s.Created.Equal(testNow.Add(-time.Hour)) || s.AgeSeconds != 3600 {
		t.Fatalf("anchor: created=%v age=%d", s.Created, s.AgeSeconds)
	}
}

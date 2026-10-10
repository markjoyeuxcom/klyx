package health

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func node(unschedulable bool, conds ...corev1.NodeCondition) *corev1.Node {
	n := &corev1.Node{}
	n.Spec.Unschedulable = unschedulable
	n.Status.Conditions = conds
	return n
}

func nc(t corev1.NodeConditionType, s corev1.ConditionStatus) corev1.NodeCondition {
	return corev1.NodeCondition{Type: t, Status: s}
}

func TestNodeTable(t *testing.T) {
	cases := []struct {
		name  string
		n     *corev1.Node
		kind  Kind
		label string
	}{
		{"ready", node(false, nc(corev1.NodeReady, corev1.ConditionTrue)), OK, "Ready"},
		{"ready missing is unknown, not NotReady", node(false), Unknown, "Ready not reported"},
		{"ready unknown", node(false, nc(corev1.NodeReady, corev1.ConditionUnknown)), Unknown, "Kubelet not reporting"},
		{"not ready", node(false, nc(corev1.NodeReady, corev1.ConditionFalse)), Fault, "NotReady"},
		{"cordoned NotReady stays a fault", node(true, nc(corev1.NodeReady, corev1.ConditionFalse)), Fault, "NotReady"},
		{"pressure", node(false, nc(corev1.NodeReady, corev1.ConditionTrue), nc(corev1.NodeDiskPressure, corev1.ConditionTrue)), Degraded, "DiskPressure"},
		{"cordoned pressured node stays degraded", node(true, nc(corev1.NodeReady, corev1.ConditionTrue), nc(corev1.NodeMemoryPressure, corev1.ConditionTrue)), Degraded, "MemoryPressure"},
		{"cordoned", node(true, nc(corev1.NodeReady, corev1.ConditionTrue)), Paused, "Cordoned"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Node(c.n)
			if v.Kind != c.kind || v.Label != c.label {
				t.Fatalf("got %s %q, want %s %q", v.Kind, v.Label, c.kind, c.label)
			}
		})
	}
	// Unknown readiness keeps known pressure as evidence.
	v := Node(node(false, nc(corev1.NodeReady, corev1.ConditionUnknown), nc(corev1.NodePIDPressure, corev1.ConditionTrue)))
	if v.Message != "PIDPressure" {
		t.Fatalf("pressure evidence lost: %q", v.Message)
	}
}

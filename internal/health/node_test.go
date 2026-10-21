package health

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

// Coverage S11a §3: an extended resource whose allocatable reads below its
// capacity. The numbers are the finding; the cause is an interpretation.
func TestNodeResourcesNotAllocatable(t *testing.T) {
	with := func(n *corev1.Node, capacity, allocatable map[string]string) *corev1.Node {
		n.Status.Capacity, n.Status.Allocatable = corev1.ResourceList{}, corev1.ResourceList{}
		for k, v := range capacity {
			n.Status.Capacity[corev1.ResourceName(k)] = resource.MustParse(v)
		}
		for k, v := range allocatable {
			n.Status.Allocatable[corev1.ResourceName(k)] = resource.MustParse(v)
		}
		return n
	}
	ready := func() *corev1.Node { return node(false, nc(corev1.NodeReady, corev1.ConditionTrue)) }
	gap := map[string]string{"nvidia.com/gpu": "8"}
	less := map[string]string{"nvidia.com/gpu": "6"}

	v := Node(with(ready(), gap, less))
	if v.Kind != Degraded || v.Label != "Resources not allocatable" || v.Rule != "node.resources-not-allocatable" ||
		!strings.HasPrefix(v.Message, "nvidia.com/gpu: 6 of 8 allocatable. For a device-plugin resource") ||
		!strings.Contains(v.Message, "The node does not say which.") || !strings.HasSuffix(v.Native, " · nvidia.com/gpu allocatable 6, capacity 8") {
		t.Fatalf("the reading: %+v", v)
	}
	// a manual capacity raise before the kubelet's sync reads the same,
	// with the same neutral cause
	if v := Node(with(ready(), map[string]string{"example.com/dongle": "16"}, map[string]string{"example.com/dongle": "8"})); v.Kind != Degraded || !strings.Contains(v.Message, "manually advertised") {
		t.Fatalf("a manual raise: %+v", v)
	}
	// with pressure: one Degraded, both named
	p := with(node(false, nc(corev1.NodeReady, corev1.ConditionTrue), nc(corev1.NodeMemoryPressure, corev1.ConditionTrue)), gap, less)
	if v := Node(p); v.Kind != Degraded || v.Label != "MemoryPressure · resources not allocatable" || v.Rule != "node.pressure" || !strings.Contains(v.Message, "6 of 8") {
		t.Fatalf("with pressure: %+v", v)
	}
	// cordoned: degraded beats paused; NotReady and unknown Ready stay ahead
	if v := Node(with(node(true, nc(corev1.NodeReady, corev1.ConditionTrue)), gap, less)); v.Kind != Degraded {
		t.Fatalf("cordoned: %+v", v)
	}
	if v := Node(with(node(false, nc(corev1.NodeReady, corev1.ConditionFalse)), gap, less)); v.Kind != Fault {
		t.Fatalf("NotReady ahead: %+v", v)
	}
	if v := Node(with(node(false, nc(corev1.NodeReady, corev1.ConditionUnknown)), gap, less)); v.Kind != Unknown {
		t.Fatalf("unknown Ready ahead: %+v", v)
	}
	// 0 advertised and allocatable not yet reported are no reading
	for _, c := range []struct{ capacity, allocatable map[string]string }{
		{map[string]string{"nvidia.com/gpu": "0"}, map[string]string{"nvidia.com/gpu": "0"}},
		{map[string]string{"example.com/new": "4"}, map[string]string{}},
	} {
		if v := Node(with(ready(), c.capacity, c.allocatable)); v.Kind != OK {
			t.Fatalf("no reading: %+v", v)
		}
	}
	// more than four named: "and N more"
	cap6, alloc6 := map[string]string{}, map[string]string{}
	for _, r := range []string{"a", "b", "c", "d", "e", "f"} {
		cap6["vendor.example/"+r], alloc6["vendor.example/"+r] = "2", "1"
	}
	if v := Node(with(ready(), cap6, alloc6)); !strings.Contains(v.Message, "vendor.example/d: 1 of 2 allocatable and 2 more.") || strings.Contains(v.Message, "vendor.example/e") {
		t.Fatalf("four named: %q", v.Message)
	}
}

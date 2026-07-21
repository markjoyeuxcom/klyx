package workloads

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAggregateNodeUsage(t *testing.T) {
	pods := []PodSummary{
		{Namespace: "a", Name: "p1", Node: "node-1"},
		{Namespace: "a", Name: "p2", Node: "node-1"},
		{Namespace: "b", Name: "p3", Node: "node-2"},
		{Namespace: "b", Name: "p4", Node: ""},       // unscheduled: skipped
		{Namespace: "b", Name: "p5", Node: "node-3"}, // no samples: node-3 absent
	}
	cpu := map[string]float64{"a/p1": 0.1, "a/p2": 0.2, "b/p3": 0.5, "b/p4": 9.9}
	mem := map[string]float64{"a/p1": 100, "b/p3": 200}

	out := AggregateNodeUsage(pods, cpu, mem)
	if len(out) != 2 {
		t.Fatalf("want node-1 + node-2 only, got %+v", out)
	}
	n1 := out[0]
	if n1.Node != "node-1" || n1.CPU == nil || *n1.CPU != 0.30000000000000004 && *n1.CPU != 0.3 {
		t.Fatalf("node-1 cpu: %+v", n1)
	}
	if n1.Mem == nil || *n1.Mem != 100 || n1.SampledPods != 2 {
		t.Fatalf("node-1: %+v", n1)
	}
	n2 := out[1]
	if n2.Node != "node-2" || *n2.CPU != 0.5 || *n2.Mem != 200 || n2.SampledPods != 1 {
		t.Fatalf("node-2: %+v", n2)
	}
}

func TestAggregateNodeUsage_MemOnlySamplesLeaveCPUNil(t *testing.T) {
	pods := []PodSummary{{Namespace: "a", Name: "p1", Node: "n"}}
	out := AggregateNodeUsage(pods, nil, map[string]float64{"a/p1": 42})
	if len(out) != 1 || out[0].CPU != nil || out[0].Mem == nil || *out[0].Mem != 42 {
		t.Fatalf("a missing cpu sample must stay nil, never zero: %+v", out)
	}
}

func TestContainerSummaryCarriesDeclaredResources(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "p"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{
				Name: "app",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			},
			{Name: "bare"}, // no resources declared
		}},
	}
	s := SummarizePods([]corev1.Pod{*pod}, metav1.Now().Time)[0]
	app := s.Containers[0]
	if app.CPURequest == nil || *app.CPURequest != 0.1 {
		t.Fatalf("cpu request: %+v", app.CPURequest)
	}
	if app.MemRequest == nil || *app.MemRequest != 128*1048576 {
		t.Fatalf("mem request: %+v", app.MemRequest)
	}
	if app.MemLimit == nil || *app.MemLimit != 256*1048576 {
		t.Fatalf("mem limit: %+v", app.MemLimit)
	}
	if app.CPULimit != nil {
		t.Fatalf("undeclared cpu limit must stay nil, got %v", *app.CPULimit)
	}
	bare := s.Containers[1]
	if bare.CPURequest != nil || bare.MemRequest != nil || bare.CPULimit != nil || bare.MemLimit != nil {
		t.Fatalf("bare container must keep all nil: %+v", bare)
	}
}

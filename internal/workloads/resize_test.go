package workloads

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Implementation review 1 (S6): the evidence revision changes with every
// status-only change the inspector's resize and debug blocks show, and
// with nothing else.
func TestEvidenceRev(t *testing.T) {
	base := func() *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Generation: 2},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ObservedGeneration: 1, PodIP: "10.0.0.1",
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue},
					{Type: corev1.PodResizeInProgress, Status: corev1.ConditionTrue, ObservedGeneration: 2}},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 0,
					AllocatedResources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
					Resources:          &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}}},
				EphemeralContainerStatuses: []corev1.ContainerStatus{{Name: "dbg", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}},
			}}
	}
	rev := EvidenceRev(base())
	if len(rev) != 16 || rev != EvidenceRev(base()) {
		t.Fatalf("bounded and deterministic: %q", rev)
	}
	changes := map[string]func(*corev1.Pod){
		"acknowledged":   func(p *corev1.Pod) { p.Status.ObservedGeneration = 2 },
		"resize cleared": func(p *corev1.Pod) { p.Status.Conditions = p.Status.Conditions[:1] },
		"actuated": func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("1500m")
		},
		"allocated": func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].AllocatedResources[corev1.ResourceCPU] = resource.MustParse("3")
		},
		"pod-level": func(p *corev1.Pod) {
			p.Status.AllocatedResources = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}
		},
		"debug exited": func(p *corev1.Pod) {
			p.Status.EphemeralContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 3}}
		},
		"debug added": func(p *corev1.Pod) {
			p.Status.EphemeralContainerStatuses = append(p.Status.EphemeralContainerStatuses, corev1.ContainerStatus{Name: "dbg2"})
		},
		"resize message": func(p *corev1.Pod) { p.Status.Conditions[1].Message = "runtime refused" },
	}
	for name, change := range changes {
		p := base()
		change(p)
		if EvidenceRev(p) == rev {
			t.Errorf("%s: the revision did not change", name)
		}
	}
	same := map[string]func(*corev1.Pod){
		"pod IP":   func(p *corev1.Pod) { p.Status.PodIP = "10.0.0.2" },
		"restarts": func(p *corev1.Pod) { p.Status.ContainerStatuses[0].RestartCount = 4 },
		"ready":    func(p *corev1.Pod) { p.Status.Conditions[0].Status = corev1.ConditionFalse },
	}
	for name, change := range same {
		p := base()
		change(p)
		if EvidenceRev(p) != rev {
			t.Errorf("%s is not resize or debug evidence: the revision changed", name)
		}
	}
}

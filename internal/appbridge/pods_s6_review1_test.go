package appbridge

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/workloads"
)

// Implementation review 1 (S6): a status-only change to the detail's
// evidence (an actuation, a debug container exiting) reaches the row, so
// the selected pod's detail is read again.
func TestReview1StatusOnlyEvidenceReachesTheRow(t *testing.T) {
	mk := func() corev1.Pod {
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns", UID: "u", Generation: 2},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ObservedGeneration: 2,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}, {Type: corev1.PodResizeInProgress, Status: corev1.ConditionTrue, ObservedGeneration: 2}},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					Resources: &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}}}},
				EphemeralContainerStatuses: []corev1.ContainerStatus{{Name: "dbg", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}}},
			}}
	}
	row := func(p corev1.Pod) []byte {
		b, _ := json.Marshal(toPodRowDTO(workloads.SummarizePodsModel([]corev1.Pod{p}, time.Unix(1, 0), workloads.SchedModelFor(37))[0]))
		return b
	}
	before := mk()
	actuated := mk()
	actuated.Status.ContainerStatuses[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("1500m")
	exited := mk()
	exited.Status.EphemeralContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 3, Reason: "Error"}}
	for name, after := range map[string]corev1.Pod{"actuated": actuated, "debug exited": exited} {
		if bytes.Equal(row(before), row(after)) {
			t.Errorf("%s: the row carries no signal", name)
		}
	}
}

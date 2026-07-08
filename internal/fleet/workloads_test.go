package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestListWorkloads(t *testing.T) {
	reps := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api"},
		Spec:       appsv1.DeploymentSpec{Replicas: &reps, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api-x", Labels: map[string]string{"app": "api"}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			{RestartCount: 4, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}},
	}
	cs := fake.NewSimpleClientset(dep, pod)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}
	c.caps = capability.Set{GitOps: capability.GitOpsCapability{Flux: capability.FluxInfo{Present: true}}}

	out, fluxPresent, _, err := c.ListWorkloads(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !fluxPresent {
		t.Fatal("want fluxPresent true")
	}
	if len(out) != 1 || out[0].Name != "api" || out[0].Rank.String() != "unhealthy" || out[0].Reason != "ImagePullBackOff" || out[0].Restarts != 4 {
		t.Fatalf("got %+v", out)
	}
}

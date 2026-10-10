package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/health"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

// A ReplicaSet list the user may not read must not fail the workloads lens:
// Deployment health degrades to "Unverified" instead of guessing ownership.
func TestListWorkloadsReplicaSetsForbiddenIsNonFatal(t *testing.T) {
	reps := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api", UID: "dep-uid", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &reps, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1},
	}
	cs := fake.NewSimpleClientset(dep)
	cs.PrependReactor("list", "replicasets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "replicasets"}, "", errors.New("rbac"))
	})
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}

	out, _, _, err := c.ListWorkloads(context.Background(), "")
	if err != nil {
		t.Fatalf("forbidden ReplicaSets must be non-fatal, got %v", err)
	}
	if len(out) != 1 || out[0].Health.Kind != health.Unknown || out[0].Health.Label != "Unverified" {
		t.Fatalf("got %+v", out)
	}
}

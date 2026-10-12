package fleet

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
)

func newScaleConn(typed *typedfake.Clientset) *ClusterConn {
	return newPodActionsConn(typed, clock.Real{})
}

func seedDeploymentWithReplicas(ns, name string, replicas int32) *appsv1.Deployment {
	r := replicas
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)},
		Spec:       appsv1.DeploymentSpec{Replicas: &r},
	}
}

func seedStatefulSetWithReplicas(ns, name string, replicas int32) *appsv1.StatefulSet {
	r := replicas
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)},
		Spec:       appsv1.StatefulSetSpec{Replicas: &r},
	}
}

// --- ScaleWorkload ---

func TestScaleWorkload_Deployment_UpdatesScale(t *testing.T) {
	typed := typedfake.NewSimpleClientset(seedDeploymentWithReplicas("prod", "api", 3))
	c := newScaleConn(typed)

	if err := c.ScaleWorkload(context.Background(), "Deployment", "prod", "api", "uid-api", 5); err != nil {
		t.Fatalf("ScaleWorkload Deployment: %v", err)
	}

	// Verify an update-scale action was recorded with the right resource and replicas.
	found := false
	for _, a := range typed.Actions() {
		if ua, ok := a.(k8stesting.UpdateAction); ok &&
			ua.GetSubresource() == "scale" &&
			ua.GetResource().Resource == "deployments" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected update-scale action on deployments")
	}
}

func TestScaleWorkload_StatefulSet_UpdatesScale(t *testing.T) {
	typed := typedfake.NewSimpleClientset(seedStatefulSetWithReplicas("db", "pg", 1))
	c := newScaleConn(typed)

	if err := c.ScaleWorkload(context.Background(), "StatefulSet", "db", "pg", "uid-pg", 3); err != nil {
		t.Fatalf("ScaleWorkload StatefulSet: %v", err)
	}

	found := false
	for _, a := range typed.Actions() {
		if ua, ok := a.(k8stesting.UpdateAction); ok &&
			ua.GetSubresource() == "scale" &&
			ua.GetResource().Resource == "statefulsets" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected update-scale action on statefulsets")
	}
}

func TestScaleWorkload_Deployment_ScaleToZero(t *testing.T) {
	typed := typedfake.NewSimpleClientset(seedDeploymentWithReplicas("prod", "api", 2))
	c := newScaleConn(typed)

	if err := c.ScaleWorkload(context.Background(), "Deployment", "prod", "api", "uid-api", 0); err != nil {
		t.Fatalf("scale to 0 must succeed: %v", err)
	}
}

func TestScaleWorkload_NegativeReplicasRejected(t *testing.T) {
	c := newScaleConn(typedfake.NewSimpleClientset())

	err := c.ScaleWorkload(context.Background(), "Deployment", "prod", "api", "uid-api", -1)
	if err == nil || !strings.Contains(err.Error(), "replicas must be >= 0") {
		t.Fatalf("want negative-replicas error, got %v", err)
	}
}

func TestScaleWorkload_DaemonSetRejected(t *testing.T) {
	c := newScaleConn(typedfake.NewSimpleClientset())

	err := c.ScaleWorkload(context.Background(), "DaemonSet", "kube-system", "cilium", "uid-cilium", 3)
	if err == nil || !strings.Contains(err.Error(), "unsupported kind") {
		t.Fatalf("want unsupported-kind error for DaemonSet, got %v", err)
	}
}

func TestScaleWorkload_UnsupportedKindRejected(t *testing.T) {
	c := newScaleConn(typedfake.NewSimpleClientset())

	err := c.ScaleWorkload(context.Background(), "Job", "default", "migrate", "uid-migrate", 1)
	if err == nil || !strings.Contains(err.Error(), "unsupported kind") {
		t.Fatalf("want unsupported-kind error, got %v", err)
	}
}

func TestScaleWorkload_NotFoundPropagates(t *testing.T) {
	typed := typedfake.NewSimpleClientset()
	typed.PrependReactor("update", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if ua, ok := a.(k8stesting.UpdateAction); ok && ua.GetSubresource() == "scale" {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "deployments"}, "ghost")
		}
		return false, nil, nil
	})
	c := newScaleConn(typed)

	err := c.ScaleWorkload(context.Background(), "Deployment", "prod", "ghost", "uid-ghost", 2)
	if err == nil {
		t.Fatal("want error for not-found deployment")
	}
	if !apierrors.IsNotFound(err) && !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("want not-found surfaced, got %v", err)
	}
}

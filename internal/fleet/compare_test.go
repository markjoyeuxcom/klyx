package fleet

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func compareDeploy(replicas int32, image string, fluxOwned bool) *appsv1.Deployment {
	labels := map[string]string{}
	if fluxOwned {
		labels["kustomize.toolkit.fluxcd.io/name"] = "apps"
		labels["kustomize.toolkit.fluxcd.io/namespace"] = "flux-system"
	}
	cpu := resource.MustParse("100m")
	mem := resource.MustParse("128Mi")
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "cart", Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  "app",
				Image: image,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: mem},
				},
			}}}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: replicas},
	}
}

func TestWorkloadCompare_FactsAndRevision(t *testing.T) {
	typed := k8sfake.NewSimpleClientset(compareDeploy(3, "shop/cart:v1.2.3", true))
	ksGVR := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{ksGVR: "KustomizationList"})
	ks := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"namespace": "flux-system", "name": "apps"},
		"status":   map[string]interface{}{"lastAppliedRevision": "main@sha1:abc123"},
	}}
	if _, err := dyn.Resource(ksGVR).Namespace("flux-system").Create(context.Background(), ks, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	info, err := c.WorkloadCompare(context.Background(), "Deployment", "shop", "cart")
	if err != nil || !info.Found {
		t.Fatalf("want found, got %+v (%v)", info, err)
	}
	if info.DesiredReplicas != 3 || info.ReadyReplicas != 3 {
		t.Fatalf("replicas: %+v", info)
	}
	if len(info.Containers) != 1 || info.Containers[0].Image != "shop/cart:v1.2.3" {
		t.Fatalf("containers: %+v", info.Containers)
	}
	if !strings.Contains(info.Containers[0].Resources, "req 100m/128Mi") {
		t.Fatalf("resources compact: %q", info.Containers[0].Resources)
	}
	if info.SpecHash == "" || len(info.SpecHash) != 12 {
		t.Fatalf("spec hash: %q", info.SpecHash)
	}
	if info.OwnerKind != "Kustomization" || info.OwnerName != "apps" {
		t.Fatalf("owner: %+v", info)
	}
	if info.OwnerRevision != "main@sha1:abc123" {
		t.Fatalf("the resolved Flux revision is the point of the compare: %+v", info)
	}
}

func TestWorkloadCompare_HashDetectsSpecDrift(t *testing.T) {
	a := k8sfake.NewSimpleClientset(compareDeploy(3, "shop/cart:v1", false))
	b := k8sfake.NewSimpleClientset(compareDeploy(3, "shop/cart:v2", false))
	ca := NewClusterConn("a", a, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	cb := NewClusterConn("b", b, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	ia, _ := ca.WorkloadCompare(context.Background(), "Deployment", "shop", "cart")
	ib, _ := cb.WorkloadCompare(context.Background(), "Deployment", "shop", "cart")
	if ia.SpecHash == ib.SpecHash {
		t.Fatal("differing templates must hash differently")
	}
	// Identical templates hash identically across conns.
	b2 := k8sfake.NewSimpleClientset(compareDeploy(3, "shop/cart:v1", false))
	cb2 := NewClusterConn("b2", b2, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	ib2, _ := cb2.WorkloadCompare(context.Background(), "Deployment", "shop", "cart")
	if ia.SpecHash != ib2.SpecHash {
		t.Fatal("identical templates must hash identically")
	}
}

func TestWorkloadCompare_NotFoundIsHonestNotError(t *testing.T) {
	c := NewClusterConn("x", k8sfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	info, err := c.WorkloadCompare(context.Background(), "Deployment", "shop", "cart")
	if err != nil {
		t.Fatalf("not-found must not error: %v", err)
	}
	if info.Found {
		t.Fatalf("want Found=false, got %+v", info)
	}
	// An unsupported kind IS an error.
	if _, err := c.WorkloadCompare(context.Background(), "CronJob", "shop", "cart"); err == nil {
		t.Fatal("unsupported kind must error")
	}
}

package fleet

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func i32(v int32) *int32 { return &v }

func kustomizeDeploy(avail int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "kustomize-controller", Namespace: "flux-system"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(1)},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: avail},
	}
}

func argoSts(ready int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "argocd-application-controller", Namespace: "argocd"},
		Spec:       appsv1.StatefulSetSpec{Replicas: i32(1)},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: ready},
	}
}

// TestCapHealthFluxArgoCoexistence exercises the dual-tool path: both informers
// must be assigned before either factory starts (a handler recompute reads both
// fields), and either tool's controller going down must degrade the tier.
func TestCapHealthFluxArgoCoexistence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		kustomizeDeploy(1),
		argoSts(1),
	)
	typed.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "kustomize.toolkit.fluxcd.io/v1"},
		{GroupVersion: "argoproj.io/v1alpha1"},
	}

	mscheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(mscheme)
	mclient := metadatafake.NewSimpleMetadataClient(mscheme, podMeta("p1", "default"))

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, mclient, nil, det, clock.Real{}, config.MetricsConfig{})
	c.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		s := c.Snapshot()
		return s.State == Synced && s.Capabilities.GitOps.Tier == capability.Healthy &&
			s.Capabilities.GitOps.Flux.Present && s.Capabilities.GitOps.Argo.Present
	})

	// Argo's controller going down must degrade the tier even with Flux healthy.
	if _, err := typed.AppsV1().StatefulSets("argocd").Update(ctx, argoSts(0), metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update argo to 0: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		s := c.Snapshot()
		return s.Capabilities.GitOps.Tier == capability.Degraded
	})

	if _, err := typed.AppsV1().StatefulSets("argocd").Update(ctx, argoSts(1), metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update argo to 1: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		s := c.Snapshot()
		return s.Capabilities.GitOps.Tier == capability.Healthy
	})
}

func TestCapHealthReactsToControllerHealth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		kustomizeDeploy(1),
	)
	typed.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "kustomize.toolkit.fluxcd.io/v1"},
	}

	mscheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(mscheme)
	mclient := metadatafake.NewSimpleMetadataClient(mscheme, podMeta("p1", "default"))

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, mclient, nil, det, clock.Real{}, config.MetricsConfig{})
	c.Start(ctx)

	waitFor(t, 3*time.Second, func() bool {
		s := c.Snapshot()
		return s.State == Synced && s.Capabilities.GitOps.Tier == capability.Healthy
	})

	if _, err := typed.AppsV1().Deployments("flux-system").Update(ctx, kustomizeDeploy(0), metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update to 0: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		s := c.Snapshot()
		return s.State == Degraded && s.Capabilities.GitOps.Tier == capability.Degraded && s.Reason != ""
	})

	if _, err := typed.AppsV1().Deployments("flux-system").Update(ctx, kustomizeDeploy(1), metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update to 1: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		s := c.Snapshot()
		return s.State == Synced && s.Capabilities.GitOps.Tier == capability.Healthy && s.Reason == ""
	})
}

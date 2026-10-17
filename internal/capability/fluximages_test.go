package capability

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func imageDep(ns, component string, ready bool) *appsv1.Deployment {
	one := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: component, Labels: map[string]string{"app.kubernetes.io/component": component}},
		Spec: appsv1.DeploymentSpec{Replicas: &one}}
	if ready {
		d.Status.AvailableReplicas = 1
	}
	return d
}

// Coverage S1b §3: the image controllers by label, not ready or absent
// while their CRDs are served a failure, unreadable a gap.
func TestImageControllers(t *testing.T) {
	served := map[string]bool{fluxImageGroup: true}
	detect := func(cs *fake.Clientset) ImageControllers {
		return (&Detector{cs: cs}).detectImageControllers(context.Background(), served, "flux-system")
	}
	ok := detect(fake.NewSimpleClientset(imageDep("flux-system", imageReflector, true), imageDep("flux-system", imageAutomation, true)))
	if ok.Issue() != "" {
		t.Fatalf("ready: %q", ok.Issue())
	}
	down := detect(fake.NewSimpleClientset(imageDep("flux-system", imageReflector, false)))
	if f, g := down.Problems(); len(f) != 2 || len(g) != 0 || !strings.Contains(f[0], "image-reflector-controller is not ready") || !strings.Contains(f[1], "no image-automation-controller in flux-system") {
		t.Fatalf("not ready and absent: %v %v", f, g)
	}
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
	if f, g := detect(cs).Problems(); len(f) != 0 || len(g) != 2 {
		t.Fatalf("unreadable is a gap: %v %v", f, g)
	}
	if none := (&Detector{cs: fake.NewSimpleClientset()}).detectImageControllers(context.Background(), map[string]bool{}, "flux-system"); none.Issue() != "" {
		t.Fatal("not served: nothing")
	}
	g := GitOpsCapability{Flux: FluxInfo{Present: true, Healthy: true, Verified: true, Images: down}}
	if !strings.Contains(g.FluxIssue(), "image-reflector-controller is not ready") {
		t.Fatalf("the Flux task names it: %q", g.FluxIssue())
	}
	if tier, _ := gitOpsTier(g.Flux, ArgoInfo{}); tier == Healthy {
		t.Fatal("a failing image controller degrades the tier")
	}
}

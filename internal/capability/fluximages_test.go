package capability

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func imageDep(ns, name, component string, ready bool) *appsv1.Deployment {
	one := int32(1)
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app.kubernetes.io/component": component}},
		Spec: appsv1.DeploymentSpec{Replicas: &one}}
	if ready {
		d.Status.AvailableReplicas = 1
	}
	return d
}

// imageCluster is a clientset whose discovery serves image.toolkit.fluxcd.io/v1
// with the given resources.
func imageCluster(resources []string, objs ...runtime.Object) *fake.Clientset {
	cs := fake.NewSimpleClientset(objs...)
	rl := &metav1.APIResourceList{GroupVersion: fluxImageGroup + "/v1"}
	for _, r := range resources {
		rl.APIResources = append(rl.APIResources, metav1.APIResource{Name: r})
	}
	cs.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{rl}
	return cs
}

var imageServed = map[string]bool{fluxImageGroup: true, fluxImageGroup + "/v1": true}
var allImageKinds = []string{"imagerepositories", "imagepolicies", "imageupdateautomations"}

func detectImages(cs *fake.Clientset) ImageControllers {
	return (&Detector{cs: cs}).detectImageControllers(context.Background(), imageServed, "flux-system")
}

// Coverage S1b §3: the image controllers by label; not ready or absent while
// their resources are served is a failure, unreadable a gap.
func TestImageControllers(t *testing.T) {
	ok := detectImages(imageCluster(allImageKinds, imageDep("flux-system", "r", imageReflector, true), imageDep("flux-system", "a", imageAutomation, true)))
	if ok.Issue() != "" {
		t.Fatalf("ready: %q", ok.Issue())
	}
	down := detectImages(imageCluster(allImageKinds, imageDep("flux-system", "r", imageReflector, false)))
	if f, g := down.Problems(); len(f) != 2 || len(g) != 0 || !strings.Contains(f[0], "flux-system/r is not ready") || !strings.Contains(f[1], "no image-automation-controller in flux-system") {
		t.Fatalf("not ready and absent: %v %v", f, g)
	}
	cs := imageCluster(allImageKinds)
	cs.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
	if f, g := detectImages(cs).Problems(); len(f) != 0 || len(g) != 2 {
		t.Fatalf("unreadable is a gap: %v %v", f, g)
	}
	if none := (&Detector{cs: fake.NewSimpleClientset()}).detectImageControllers(context.Background(), map[string]bool{}, "flux-system"); none.Issue() != "" {
		t.Fatal("not served: nothing")
	}
	g := GitOpsCapability{Flux: FluxInfo{Present: true, Healthy: true, Verified: true, Images: down}}
	if !strings.Contains(g.FluxIssue(), "flux-system/r is not ready") {
		t.Fatalf("the Flux task names it: %q", g.FluxIssue())
	}
	if tier, _ := gitOpsTier(g.Flux, ArgoInfo{}); tier == Healthy {
		t.Fatal("a failing image controller degrades the tier")
	}
}

// Codex's S1b implementation review, finding 2: each controller is needed
// only when its own resources are served.
func TestImageControllersPerResource(t *testing.T) {
	reflectorOnly := detectImages(imageCluster([]string{"imagerepositories", "imagepolicies"}, imageDep("flux-system", "r", imageReflector, true)))
	if reflectorOnly.Issue() != "" || reflectorOnly.Automation.Needed {
		t.Fatalf("a reflector-only installation: %q", reflectorOnly.Issue())
	}
	cs := fake.NewSimpleClientset()
	cs.Discovery().(*fakediscovery.FakeDiscovery).Resources = nil // the version's resources cannot be read
	if f, g := detectImages(cs).Problems(); len(f) != 0 || len(g) != 2 || !strings.Contains(g[0], "could not be read") {
		t.Fatalf("unreadable discovery is a gap: %v %v", f, g)
	}
	// review 2, finding 2: v1 proves all three resources, v1beta2 is
	// advertised but unreadable, and the reflector is down - the failure is
	// still read and reported
	both := imageCluster(allImageKinds, imageDep("flux-system", "r", imageReflector, false), imageDep("flux-system", "a", imageAutomation, true))
	served := map[string]bool{fluxImageGroup: true, fluxImageGroup + "/v1": true, fluxImageGroup + "/v1beta2": true}
	ic := (&Detector{cs: both}).detectImageControllers(context.Background(), served, "flux-system")
	if f, g := ic.Problems(); len(f) != 1 || !strings.Contains(f[0], "flux-system/r is not ready") || len(g) != 0 {
		t.Fatalf("a proven need is read despite another version's gap: %v %v", f, g)
	}
}

// Finding 4: a ready shard never certifies another; a list cut short is a
// gap, never a certification.
func TestImageControllerShards(t *testing.T) {
	shards := detectImages(imageCluster(allImageKinds, imageDep("flux-system", "r1", imageReflector, true), imageDep("flux-system", "r2", imageReflector, false),
		imageDep("flux-system", "a", imageAutomation, true)))
	if f, _ := shards.Problems(); len(f) != 1 || !strings.Contains(f[0], "flux-system/r2 is not ready") {
		t.Fatalf("the down shard: %v", f)
	}
	cs := imageCluster(allImageKinds)
	cs.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &appsv1.DeploymentList{ListMeta: metav1.ListMeta{Continue: "more"}, Items: []appsv1.Deployment{*imageDep("flux-system", "r1", imageReflector, true)}}, nil
	})
	if f, g := detectImages(cs).Problems(); len(f) != 0 || len(g) != 2 || !strings.Contains(g[0], "cut short") {
		t.Fatalf("a list cut short: %v %v", f, g)
	}
}

// Finding 3: image automation alone is a Flux installation: the view is
// present, and no kustomize-controller or operator failure is invented.
func TestImageOnlyInstallation(t *testing.T) {
	d := &Detector{cs: imageCluster(allImageKinds, imageDep("flux-system", "r", imageReflector, true), imageDep("flux-system", "a", imageAutomation, true))}
	g := d.detectGitOps(context.Background(), imageServed)
	if !g.Flux.Present || !g.Flux.Images.Served || !g.Flux.Synthesized || g.FluxIssue() != "" || g.Tier == Absent {
		t.Fatalf("image-only: %+v issue %q", g, g.FluxIssue())
	}
	f, _ := g.FluxProblems()
	if len(f) != 0 {
		t.Fatalf("no invented failure: %v", f)
	}
}

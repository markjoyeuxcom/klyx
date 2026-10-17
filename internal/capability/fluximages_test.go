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

// Coverage S1c §3: source-watcher and notification-controller, each needed
// only for its own resources, reach the Flux task.
func TestFluxComponents(t *testing.T) {
	cs := fake.NewSimpleClientset(imageDep("flux-system", "nc", notificationController, false))
	cs.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: notificationGroup + "/v1", APIResources: []metav1.APIResource{{Name: "receivers"}}},
		{GroupVersion: sourceExtensionsGroup + "/v1beta1", APIResources: []metav1.APIResource{{Name: "artifactgenerators"}}},
	}
	served := map[string]bool{notificationGroup: true, notificationGroup + "/v1": true, sourceExtensionsGroup: true, sourceExtensionsGroup + "/v1beta1": true}
	c := (&Detector{cs: cs}).detectFluxComponents(context.Background(), served, "flux-system")
	f, g := c.Problems()
	if len(f) != 2 || !strings.Contains(f[0], "no source-watcher in flux-system") || !strings.Contains(f[1], "flux-system/nc is not ready") || len(g) != 0 {
		t.Fatalf("components: %v %v", f, g)
	}
	if !c.Composition || !c.Notifications {
		t.Fatalf("the views are present: %+v", c)
	}
	none := (&Detector{cs: fake.NewSimpleClientset()}).detectFluxComponents(context.Background(), map[string]bool{}, "flux-system")
	if f, g := none.Problems(); len(f)+len(g) > 0 || none.Composition || none.Notifications {
		t.Fatalf("not served: nothing: %+v", none)
	}
	// an ExternalArtifact alone, served by source-controller, opens the view
	cs2 := fake.NewSimpleClientset()
	cs2.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "source.toolkit.fluxcd.io/v1", APIResources: []metav1.APIResource{{Name: "gitrepositories"}, {Name: "externalartifacts"}}}}
	ea := (&Detector{cs: cs2}).detectFluxComponents(context.Background(), map[string]bool{"source.toolkit.fluxcd.io": true, "source.toolkit.fluxcd.io/v1": true}, "flux-system")
	if !ea.Composition || ea.Notifications || ea.SourceWatcher.Needed {
		t.Fatalf("artifacts alone: %+v", ea)
	}
	gc := GitOpsCapability{Flux: FluxInfo{Present: true, Healthy: true, Verified: true, Components: c}}
	if !strings.Contains(gc.FluxIssue(), "no source-watcher") {
		t.Fatalf("the Flux task names it: %q", gc.FluxIssue())
	}
}

// Codex's S1c implementation review, finding 6: source composition or
// notifications alone are a Flux installation, judged by their own
// controllers, with no kustomize-controller invented.
func TestS1cOnlyInstallations(t *testing.T) {
	ea := fake.NewSimpleClientset()
	ea.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "source.toolkit.fluxcd.io/v1", APIResources: []metav1.APIResource{{Name: "externalartifacts"}}}}
	g := (&Detector{cs: ea}).detectGitOps(context.Background(), map[string]bool{"source.toolkit.fluxcd.io": true, "source.toolkit.fluxcd.io/v1": true})
	if !g.Flux.Present || !g.Flux.Components.Composition || !g.Flux.Synthesized || g.FluxIssue() != "" || g.Tier == Absent {
		t.Fatalf("artifacts alone: %+v issue %q", g, g.FluxIssue())
	}
	rc := fake.NewSimpleClientset()
	rc.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: notificationGroup + "/v1", APIResources: []metav1.APIResource{{Name: "receivers"}}}}
	g = (&Detector{cs: rc}).detectGitOps(context.Background(), map[string]bool{notificationGroup: true, notificationGroup + "/v1": true})
	f, _ := g.FluxProblems()
	if !g.Flux.Present || !g.Flux.Components.Notifications || g.Flux.Healthy || len(f) != 1 || !strings.Contains(f[0], "notification-controller") {
		t.Fatalf("Receivers alone, no controller: %+v %v", g.Flux, f)
	}
	for _, p := range f {
		if strings.Contains(p, "kustomize-controller") {
			t.Fatalf("no kustomize-controller is invented: %v", f)
		}
	}
	// source-controller without ExternalArtifacts is not Flux by itself
	if g := (&Detector{cs: fake.NewSimpleClientset()}).detectGitOps(context.Background(), map[string]bool{"source.toolkit.fluxcd.io": true}); g.Flux.Present {
		t.Fatalf("a source group alone: %+v", g.Flux)
	}
}

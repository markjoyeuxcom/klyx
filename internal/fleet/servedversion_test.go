package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gitops/flux"
)

// fluxSplitDiscovery advertises the Flux source group preferring v1 while
// OCIRepository is served only at v1beta2 - the case where the group's
// preferred version is wrong for one resource (review 2026-10-03 #11). Buckets
// are not served at all.
func fluxSplitDiscovery() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{
		{GroupVersion: "kustomize.toolkit.fluxcd.io/v1", APIResources: []metav1.APIResource{{Name: "kustomizations", Namespaced: true, Kind: "Kustomization"}}},
		{GroupVersion: "helm.toolkit.fluxcd.io/v2", APIResources: []metav1.APIResource{{Name: "helmreleases", Namespaced: true, Kind: "HelmRelease"}}},
		{GroupVersion: "source.toolkit.fluxcd.io/v1", APIResources: []metav1.APIResource{
			{Name: "gitrepositories", Namespaced: true, Kind: "GitRepository"},
			{Name: "helmrepositories", Namespaced: true, Kind: "HelmRepository"},
			{Name: "helmcharts", Namespaced: true, Kind: "HelmChart"},
		}},
		{GroupVersion: "source.toolkit.fluxcd.io/v1beta2", APIResources: []metav1.APIResource{
			{Name: "ocirepositories", Namespaced: true, Kind: "OCIRepository"},
			{Name: "gitrepositories", Namespaced: true, Kind: "GitRepository"},
		}},
	}
}

func splitListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}:    "KustomizationList",
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}:           "HelmReleaseList",
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}:      "GitRepositoryList",
		{Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: "ocirepositories"}: "OCIRepositoryList",
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"}:     "HelmRepositoryList",
		{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmcharts"}:           "HelmChartList",
		// Under partial discovery (v1beta2 unreadable) an unserved Bucket can't
		// be ruled out, so it is requested at the unreadable version and
		// surfaces in health instead of being silently absent.
		{Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: "buckets"}: "BucketList",
	}
}

func ociRepo(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1beta2",
		"kind":       "OCIRepository",
		"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system"},
		"spec":       map[string]interface{}{"url": "oci://ghcr.io/org/manifests"},
	}}
}

func splitConn(t *testing.T, objs ...runtime.Object) (*ClusterConn, *dynamicfake.FakeDynamicClient, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	typed := fake.NewSimpleClientset()
	typed.Resources = fluxSplitDiscovery()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), splitListKinds(), objs...)
	c := NewClusterConn("x", typed, nil, dyn, capability.NewDetector(typed), clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx
	return c, dyn, cancel
}

func TestServedVersion_ResolvesPerResource(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.Resources = fluxSplitDiscovery()
	disc := typed.Discovery()
	cases := []struct {
		resource, wantVer string
		wantServed        bool
	}{
		{"gitrepositories", "v1", true},      // the preferred version serves it
		{"ocirepositories", "v1beta2", true}, // only a non-preferred version does
		{"buckets", "", false},               // advertised group, resource not served
	}
	for _, tc := range cases {
		ver, served, _ := servedVersion(disc, "source.toolkit.fluxcd.io", tc.resource, "v1")
		if ver != tc.wantVer || served != tc.wantServed {
			t.Errorf("%s: servedVersion = (%q, %v), want (%q, %v)", tc.resource, ver, served, tc.wantVer, tc.wantServed)
		}
	}
	// A group discovery does not advertise keeps the documented fallback.
	if ver, served, _ := servedVersion(disc, "example.com", "widgets", "v9"); ver != "v9" || !served {
		t.Errorf("unadvertised group: (%q, %v), want the fallback", ver, served)
	}
}

// The direct read (FluxObject, design §5b) and the reconcile-with-source
// patch both use OCIRepository's own served version; an unserved source kind
// is not found, never a guessed version.
func TestFluxGVRs_UseTheResourcesOwnServedVersion(t *testing.T) {
	ks := withObjUID(ksObj("apps"), "uid-apps")
	ks.Object["spec"] = map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "OCIRepository", "name": "manifests"}}
	c, dyn, cancel := splitConn(t, ociRepo("manifests"), ks)
	defer cancel()

	u, err := c.FluxObject(context.Background(), "OCIRepository", "flux-system", "manifests")
	if err != nil || u.GetAPIVersion() != "source.toolkit.fluxcd.io/v1beta2" {
		t.Fatalf("FluxObject = %v, %v; want the v1beta2 object", u, err)
	}
	if _, err := c.FluxObject(context.Background(), "Bucket", "flux-system", "b"); !apierrors.IsNotFound(err) {
		t.Fatalf("an unserved source kind is not found: %v", err)
	}

	dyn.ClearActions()
	if err := c.ReconcileWithSource(context.Background(), "Kustomization", "flux-system", "apps", "uid-apps"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	patchedOCI := false
	for _, a := range dyn.Actions() {
		if p, ok := a.(clienttesting.PatchAction); ok && p.GetResource().Resource == "ocirepositories" {
			if v := p.GetResource().Version; v != "v1beta2" {
				t.Fatalf("source patched at %s, want v1beta2", v)
			}
			patchedOCI = true
		}
	}
	if !patchedOCI {
		t.Fatal("reconcile-with-source must patch the bound OCIRepository")
	}

	if _, err := c.gvrForKind(context.Background(), flux.Kind("Bucket")); err == nil {
		t.Fatal("gvrForKind must refuse a kind it does not handle")
	}
}

// partialDiscovery fails every v1beta2 resource lookup: discovery that answers
// for one advertised version but not another.
type partialDiscovery struct{ discoveryLike }

func (d partialDiscovery) ServerResourcesForGroupVersion(gv string) (*metav1.APIResourceList, error) {
	if strings.HasSuffix(gv, "/v1beta2") {
		return nil, errors.New("temporary discovery failure")
	}
	return d.discoveryLike.ServerResourcesForGroupVersion(gv)
}

// Codex batch-3 review R2 probes, adopted: one unreadable version is
// uncertainty, never absence - the request goes to the version that could not
// be ruled out, so an installed OCIRepository is watched (or fails visibly).
func TestServedVersion_PartialDiscoveryIsNotAbsence(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.Resources = fluxSplitDiscovery()
	d := partialDiscovery{typed.Discovery()}
	if v, served, _ := servedVersion(d, "source.toolkit.fluxcd.io", "ocirepositories", "v1beta2"); !served || v != "v1beta2" {
		t.Fatalf("partial discovery = (%q, %v), want the unreadable fallback v1beta2", v, served)
	}
	// Complete discovery still establishes absence.
	if _, served, _ := servedVersion(typed.Discovery(), "source.toolkit.fluxcd.io", "buckets", "v1"); served {
		t.Fatal("complete discovery without the resource is absence")
	}
}

// Codex batch-3 review R2, on the direct read: one unreadable discovery
// version is uncertainty, never absence - an installed OCIRepository is read
// at the version that could not be ruled out.
func TestFluxObject_PartialDiscoveryCannotHideAnInstalledSource(t *testing.T) {
	c, _, cancel := splitConn(t, ociRepo("present"))
	defer cancel()
	c.disc = newCachedDiscovery(partialDiscovery{c.typed.Discovery()}, nil)
	if u, err := c.FluxObject(context.Background(), "OCIRepository", "flux-system", "present"); err != nil || u.GetName() != "present" {
		t.Fatalf("partial discovery hid an installed source: %v %v", u, err)
	}
}

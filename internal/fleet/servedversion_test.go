package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

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
		ver, served := servedVersion(disc, "source.toolkit.fluxcd.io", tc.resource, "v1")
		if ver != tc.wantVer || served != tc.wantServed {
			t.Errorf("%s: servedVersion = (%q, %v), want (%q, %v)", tc.resource, ver, served, tc.wantVer, tc.wantServed)
		}
	}
	// A group discovery does not advertise keeps the documented fallback.
	if ver, served := servedVersion(disc, "example.com", "widgets", "v9"); ver != "v9" || !served {
		t.Errorf("unadvertised group: (%q, %v), want the fallback", ver, served)
	}
}

// The watch, the source URL read and the reconcile-with-source patch all use
// OCIRepository's own served version, and an unserved source kind is not
// watched at all (no informer, no error).
func TestFluxGVRs_UseTheResourcesOwnServedVersion(t *testing.T) {
	ks := ksObj("apps")
	ks.Object["spec"] = map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "OCIRepository", "name": "manifests"}}
	c, dyn, cancel := splitConn(t, ociRepo("manifests"), ks)
	defer cancel()

	c.OpenGitOps()
	defer c.CloseGitOps()
	waitFor(t, 2*time.Second, func() bool {
		for _, s := range c.GitOpsSources() {
			if s.Kind == flux.OCIRepositoryKind && s.Name == "manifests" {
				return true
			}
		}
		return false
	})
	c.mu.RLock()
	_, bucketWatched := c.gitops.srcInf[flux.BucketKind]
	c.mu.RUnlock()
	if bucketWatched {
		t.Fatal("an unserved source kind must not be watched")
	}
	waitFor(t, 2*time.Second, func() bool { synced, _ := c.GitOpsWatchHealth(); return synced })
	if _, lastErr := c.GitOpsWatchHealth(); lastErr != "" {
		t.Fatalf("a healthy split-version watch reported %q", lastErr)
	}

	if url, ok := c.SourceURL(context.Background(), "OCIRepository", "flux-system", "manifests"); !ok || url != "oci://ghcr.io/org/manifests" {
		t.Fatalf("SourceURL = (%q, %v)", url, ok)
	}

	dyn.ClearActions()
	if err := c.ReconcileWithSource(context.Background(), "Kustomization", "flux-system", "apps"); err != nil {
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

	if _, err := c.gvrForKind(flux.Kind("Bucket")); err == nil {
		t.Fatal("gvrForKind must refuse a kind it does not handle")
	}
}

// A failing SOURCE watch surfaces even though Kustomizations and HelmReleases
// synced - it used to read as a clean, empty source list.
func TestGitOpsWatchHealth_IncludesSourceInformers(t *testing.T) {
	c, dyn, cancel := splitConn(t)
	defer cancel()
	dyn.PrependReactor("list", "ocirepositories", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("ocirepositories.source.toolkit.fluxcd.io is forbidden")
	})
	c.OpenGitOps()
	defer c.CloseGitOps()

	waitFor(t, 2*time.Second, func() bool { _, e := c.GitOpsWatchHealth(); return e != "" })
	synced, lastErr := c.GitOpsWatchHealth()
	if synced {
		t.Fatal("health must not be synced while a source informer is failing")
	}
	if !strings.Contains(lastErr, "OCIRepository") || !strings.Contains(lastErr, "forbidden") {
		t.Fatalf("the failing source watch must be named: %q", lastErr)
	}
}

// rvInformer stubs the one informer method currentError reads.
type rvInformer struct {
	cache.SharedIndexInformer
	rv string
}

func (r *rvInformer) LastSyncResourceVersion() string { return r.rv }

// An error is current only until a list/watch succeeds after it: the
// reflector's last-synced resourceVersion moving clears it, and an error
// recorded after a successful sync is current (the "synced, then failing"
// case the old sync-gated check never reported).
func TestWatchErrTracker_CurrentErrorClearsOnRecovery(t *testing.T) {
	inf := &rvInformer{rv: "100"}
	tr := &watchErrTracker{}
	if tr.currentError(inf) != "" {
		t.Fatal("no error recorded")
	}
	tr.record(errors.New("watch failed: forbidden"), "100")
	if tr.currentError(inf) == "" {
		t.Fatal("an error with no success since must be current")
	}
	inf.rv = "105" // a relist succeeded
	if got := tr.currentError(inf); got != "" {
		t.Fatalf("a recovered watch must not report %q", got)
	}
	if tr.lastError() == "" {
		t.Fatal("lastError keeps the history")
	}
}

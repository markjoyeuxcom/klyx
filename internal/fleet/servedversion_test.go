package fleet

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
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
	if v, served := servedVersion(d, "source.toolkit.fluxcd.io", "ocirepositories", "v1beta2"); !served || v != "v1beta2" {
		t.Fatalf("partial discovery = (%q, %v), want the unreadable fallback v1beta2", v, served)
	}
	// Complete discovery still establishes absence.
	if _, served := servedVersion(typed.Discovery(), "source.toolkit.fluxcd.io", "buckets", "v1"); served {
		t.Fatal("complete discovery without the resource is absence")
	}
}

func TestOpenGitOps_PartialDiscoveryCannotHideAnInstalledSource(t *testing.T) {
	c, _, cancel := splitConn(t, ociRepo("present"))
	defer cancel()
	c.disc = newCachedDiscovery(partialDiscovery{c.typed.Discovery()}, nil)
	c.OpenGitOps()
	defer c.CloseGitOps()
	waitFor(t, 2*time.Second, func() bool {
		for _, s := range c.GitOpsSources() {
			if s.Name == "present" {
				return true
			}
		}
		return false
	})
}

// Codex batch-3 review R3 probe, adopted (WatchListClient off, as in the
// probe): a watch fails once, then the relist and reopened watch succeed at
// the SAME resourceVersion. Recovery is explicit (withRecovery), so the
// failure stops being current; RV movement was no signal.
func TestWatchErrTracker_RecoveryAtSameRV(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	var watches atomic.Int32
	watcher := watch.NewRaceFreeFake()
	defer watcher.Stop()
	tr := &watchErrTracker{}
	inf := cache.NewSharedIndexInformer(withRecovery(&cache.ListWatch{
		ListWithContextFunc: func(context.Context, metav1.ListOptions) (runtime.Object, error) {
			return &corev1.PodList{ListMeta: metav1.ListMeta{ResourceVersion: "100"}}, nil
		},
		WatchFuncWithContext: func(context.Context, metav1.ListOptions) (watch.Interface, error) {
			if watches.Add(1) == 1 {
				return nil, errors.New("transient watch setup failure")
			}
			return watcher, nil
		},
	}, tr), &corev1.Pod{}, 0, cache.Indexers{})
	trackWatchErrors(inf, tr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go inf.Run(ctx.Done())

	waitFor(t, 5*time.Second, func() bool { return tr.lastError() != "" })
	waitFor(t, 10*time.Second, func() bool { return watches.Load() >= 2 && inf.HasSynced() })
	if rv := inf.LastSyncResourceVersion(); rv != "100" {
		t.Fatalf("fixture RV %q", rv)
	}
	waitFor(t, 2*time.Second, func() bool { return tr.currentError() == "" })
}

// A failure AFTER a success is current - the "synced, then broke" case the
// old sync-gated check never reported - and only a later success clears it.
func TestWatchErrTracker_FailureAfterSuccessIsCurrent(t *testing.T) {
	tr := &watchErrTracker{}
	tr.recovered()
	tr.record(errors.New("watch failed: forbidden"))
	if tr.currentError() == "" {
		t.Fatal("a failure after a success must be current")
	}
	tr.recovered()
	if got := tr.currentError(); got != "" {
		t.Fatalf("a recovered watch must not report %q", got)
	}
	if tr.lastError() == "" {
		t.Fatal("lastError keeps the history")
	}
}

// End to end: a Flux source watch that fails once and then recovers stops
// reporting through GitOpsWatchHealth.
func TestGitOpsWatchHealth_ClearsAfterRecovery(t *testing.T) {
	c, dyn, cancel := splitConn(t, ociRepo("manifests"))
	defer cancel()
	var failed atomic.Bool
	dyn.PrependWatchReactor("ocirepositories", func(clienttesting.Action) (bool, watch.Interface, error) {
		if failed.CompareAndSwap(false, true) {
			return true, nil, errors.New("transient watch failure")
		}
		return false, nil, nil
	})
	c.OpenGitOps()
	defer c.CloseGitOps()
	waitFor(t, 5*time.Second, func() bool { return failed.Load() })
	waitFor(t, 10*time.Second, func() bool {
		synced, e := c.GitOpsWatchHealth()
		return synced && e == ""
	})
}

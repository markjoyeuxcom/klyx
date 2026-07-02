package fleet

import (
	"context"
	"sort"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/gitops/flux"
)

type gitopsWatch struct {
	cancel context.CancelFunc
	ksInf  cache.SharedIndexInformer
	hrInf  cache.SharedIndexInformer
	srcInf map[flux.Kind]cache.SharedIndexInformer
	errs   *watchErrTracker
}

// watchErrTracker records the most recent reflector error for a lazy watch
// group, so a permanently failing list/watch (RBAC denied, CRD version
// mismatch) can be surfaced instead of rendering as an empty, error-free view.
type watchErrTracker struct {
	mu   sync.Mutex
	last string
}

func (t *watchErrTracker) record(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	t.last = err.Error()
	t.mu.Unlock()
}

func (t *watchErrTracker) lastError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

// trackWatchErrors replaces an informer's default watch-error handler (which
// only logs) with one that records into the tracker. Must run before Start.
func trackWatchErrors(inf cache.SharedIndexInformer, t *watchErrTracker) {
	_ = inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		t.record(err)
	})
}

// preferredVersion returns the served preferred version for a CRD group, or
// fallback when the group is not advertised (the fake discovery in tests has no
// groups, so the fallback is used there).
func preferredVersion(disc discovery.DiscoveryInterface, group, fallback string) string {
	groups, err := disc.ServerGroups()
	if err != nil || groups == nil {
		return fallback
	}
	for _, g := range groups.Groups {
		if g.Name == group {
			if g.PreferredVersion.Version != "" {
				return g.PreferredVersion.Version
			}
			if len(g.Versions) > 0 {
				return g.Versions[0].Version
			}
		}
	}
	return fallback
}

// OpenGitOps starts (idempotently) the lazy dynamic informers on the Flux CRDs.
//
// The discovery + factory build happens outside the lock (network I/O), so the
// check-and-install is guarded by two flags: gitopsOpening single-flights
// concurrent Opens, and gitopsWanted records the desired state so a Close that
// lands mid-open makes the install abort and tear the factory down instead of
// leaking its watches.
func (c *ClusterConn) OpenGitOps() {
	c.mu.Lock()
	c.gitopsWanted = true
	if c.gitops != nil || c.gitopsOpening {
		c.mu.Unlock()
		return
	}
	c.gitopsOpening = true
	dyn := c.dyn
	parent := c.ctx
	c.mu.Unlock()
	if dyn == nil || parent == nil {
		c.mu.Lock()
		c.gitopsOpening = false
		c.mu.Unlock()
		return
	}

	ksVer := preferredVersion(c.typed.Discovery(), "kustomize.toolkit.fluxcd.io", "v1")
	hrVer := preferredVersion(c.typed.Discovery(), "helm.toolkit.fluxcd.io", "v2")
	ksGVR := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: ksVer, Resource: "kustomizations"}
	hrGVR := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: hrVer, Resource: "helmreleases"}

	gctx, cancel := context.WithCancel(parent)
	factory := dynamicinformer.NewDynamicSharedInformerFactory(dyn, defaultResync)
	errs := &watchErrTracker{}
	ksInf := factory.ForResource(ksGVR).Informer()
	hrInf := factory.ForResource(hrGVR).Informer()
	trackWatchErrors(ksInf, errs)
	trackWatchErrors(hrInf, errs)

	// Watch the source kinds alongside ks/hr so a stuck resource's root cause
	// (a source that is not pulling) is visible without a separate API read.
	srcInf := make(map[flux.Kind]cache.SharedIndexInformer, len(sourceKinds))
	for _, s := range sourceKinds {
		ver := preferredVersion(c.typed.Discovery(), s.group, s.fallback)
		gvr := schema.GroupVersionResource{Group: s.group, Version: ver, Resource: s.resource}
		inf := factory.ForResource(gvr).Informer()
		trackWatchErrors(inf, errs)
		srcInf[s.kind] = inf
	}

	factory.Start(gctx.Done())

	c.mu.Lock()
	c.gitopsOpening = false
	if !c.gitopsWanted {
		// Closed while we were starting: don't install; stop the factory.
		c.mu.Unlock()
		cancel()
		return
	}
	c.gitops = &gitopsWatch{cancel: cancel, ksInf: ksInf, hrInf: hrInf, srcInf: srcInf, errs: errs}
	c.mu.Unlock()
}

// GitOpsWatchHealth reports whether the primary Flux informers have synced and
// the most recent watch error. A not-synced watch with an error is the
// "permanently failing" signal (RBAC denied, CRD mismatch) that must render as
// an error, never as an empty resource list.
func (c *ClusterConn) GitOpsWatchHealth() (synced bool, lastErr string) {
	c.mu.RLock()
	g := c.gitops
	c.mu.RUnlock()
	if g == nil {
		return false, ""
	}
	return g.ksInf.HasSynced() && g.hrInf.HasSynced(), g.errs.lastError()
}

// sourceKinds is the set of Flux source CRDs Klyx watches, with the fallback
// version used when discovery does not advertise the group (e.g. in tests).
var sourceKinds = []struct {
	kind     flux.Kind
	group    string
	resource string
	fallback string
}{
	{flux.GitRepositoryKind, "source.toolkit.fluxcd.io", "gitrepositories", "v1"},
	{flux.OCIRepositoryKind, "source.toolkit.fluxcd.io", "ocirepositories", "v1beta2"},
	{flux.BucketKind, "source.toolkit.fluxcd.io", "buckets", "v1"},
	{flux.HelmRepositoryKind, "source.toolkit.fluxcd.io", "helmrepositories", "v1"},
	{flux.HelmChartKind, "source.toolkit.fluxcd.io", "helmcharts", "v1"},
}

// CloseGitOps stops the Flux informers. Clearing gitopsWanted also aborts an
// in-flight OpenGitOps install (see OpenGitOps).
func (c *ClusterConn) CloseGitOps() {
	c.mu.Lock()
	c.gitopsWanted = false
	g := c.gitops
	c.gitops = nil
	c.mu.Unlock()
	if g != nil {
		g.cancel()
	}
}

// GitOpsObject returns the watched Flux object of the given kind by namespace/name
// from the live informer store. false if the watch is closed or not found.
func (c *ClusterConn) GitOpsObject(kind, namespace, name string) (*unstructured.Unstructured, bool) {
	c.mu.RLock()
	g := c.gitops
	c.mu.RUnlock()
	if g == nil {
		return nil, false
	}
	var inf cache.SharedIndexInformer
	switch flux.Kind(kind) {
	case flux.KustomizationKind:
		inf = g.ksInf
	case flux.HelmReleaseKind:
		inf = g.hrInf
	default:
		return nil, false
	}
	if inf == nil {
		return nil, false
	}
	for _, obj := range inf.GetStore().List() {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			if u.GetNamespace() == namespace && u.GetName() == name {
				return u, true
			}
		}
	}
	return nil, false
}

// GitOpsSources reads the source informer stores and parses them. nil when not
// open. Sorted (kind then namespace/name) so the UI list does not reshuffle.
func (c *ClusterConn) GitOpsSources() []flux.Source {
	c.mu.RLock()
	g := c.gitops
	c.mu.RUnlock()
	if g == nil {
		return nil
	}
	var out []flux.Source
	for _, inf := range g.srcInf {
		for _, obj := range inf.GetStore().List() {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				out = append(out, flux.ParseSource(u))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

// GitOpsSourceObject returns a watched source object by kind/namespace/name from
// the live informer store. false if the watch is closed or not found.
func (c *ClusterConn) GitOpsSourceObject(kind, namespace, name string) (*unstructured.Unstructured, bool) {
	c.mu.RLock()
	g := c.gitops
	c.mu.RUnlock()
	if g == nil {
		return nil, false
	}
	inf, ok := g.srcInf[flux.Kind(kind)]
	if !ok || inf == nil {
		return nil, false
	}
	for _, obj := range inf.GetStore().List() {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			if u.GetNamespace() == namespace && u.GetName() == name {
				return u, true
			}
		}
	}
	return nil, false
}

// GitOpsResources reads the informer stores and parses them. nil when not open.
func (c *ClusterConn) GitOpsResources() []flux.Resource {
	c.mu.RLock()
	g := c.gitops
	c.mu.RUnlock()
	if g == nil {
		return nil
	}
	var out []flux.Resource
	for _, obj := range g.ksInf.GetStore().List() {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			out = append(out, flux.ParseKustomization(u))
		}
	}
	for _, obj := range g.hrInf.GetStore().List() {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			out = append(out, flux.ParseHelmRelease(u))
		}
	}
	// Stable order: informer stores list in an unstable order, so sort
	// deterministically (Kustomizations first, then namespace/name) to stop the
	// UI list reshuffling on every push.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := kindRank(a.Kind), kindRank(b.Kind); ra != rb {
			return ra < rb
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

// kindRank orders Kustomizations before HelmReleases in the resource list.
func kindRank(k flux.Kind) int {
	if k == flux.KustomizationKind {
		return 0
	}
	return 1
}

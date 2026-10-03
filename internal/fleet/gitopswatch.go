package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/gitops/flux"
)

type gitopsWatch struct {
	cancel context.CancelFunc
	ksInf  cache.SharedIndexInformer // nil: Kustomizations not served
	hrInf  cache.SharedIndexInformer // nil: HelmReleases not served
	srcInf map[flux.Kind]cache.SharedIndexInformer
	// watched is every informer started, primary and source, each with its own
	// error tracker: health covers all of them (review 2026-10-03 #11 - a
	// failing source watch used to render as a clean, empty source list).
	watched []watchedInformer
}

// watchedInformer is one informer of a lazy watch group with its own error
// tracker, so health can say which kind is failing and whether it recovered.
type watchedInformer struct {
	kind flux.Kind
	inf  cache.SharedIndexInformer
	errs *watchErrTracker
}

// watchErrTracker records the most recent reflector error for a lazy watch
// group, so a permanently failing list/watch (RBAC denied, CRD version
// mismatch) can be surfaced instead of rendering as an empty, error-free view.
type watchErrTracker struct {
	mu      sync.Mutex
	last    string
	current bool // no successful list/watch since last was recorded
}

func (t *watchErrTracker) record(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	t.last = err.Error()
	t.current = true
	t.mu.Unlock()
}

// recovered marks a SUCCESSFUL list or watch establishment (withRecovery).
func (t *watchErrTracker) recovered() {
	t.mu.Lock()
	t.current = false
	t.mu.Unlock()
}

func (t *watchErrTracker) lastError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

// currentError is the recorded error only while it is still CURRENT: no list
// or watch has succeeded since it was recorded. A recovered watch therefore
// stops reporting, while a watch that synced once and then started failing
// (RBAC revoked, CRD removed) reports even though its initial sync completed.
// Recovery is signalled explicitly by withRecovery - a quiet relist can
// succeed at the very same resourceVersion, so RV movement is no signal
// (batch-3 review R3).
func (t *watchErrTracker) currentError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.current {
		return ""
	}
	return t.last
}

// withRecovery wraps lw so every SUCCESSFUL list or watch establishment marks
// t recovered; failures still reach t through trackWatchErrors' handler. The
// reflector runs list, watch and the error handler sequentially in one
// goroutine, so a recovery can never be recorded ahead of the failure it
// follows.
func withRecovery(lw cache.ListerWatcherWithContext, t *watchErrTracker) *cache.ListWatch {
	return &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			obj, err := lw.ListWithContext(ctx, o)
			if err == nil {
				t.recovered()
			}
			return obj, err
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			w, err := lw.WatchWithContext(ctx, o)
			if err == nil {
				t.recovered()
			}
			return w, err
		},
	}
}

// trackWatchErrors replaces an informer's default watch-error handler (which
// only logs) with one that records into the tracker, and strips managedFields
// before objects enter the cache (the lazy sets watch full objects and live as
// long as the view is open). Must run before Start.
func trackWatchErrors(inf cache.SharedIndexInformer, t *watchErrTracker) {
	_ = inf.SetTransform(stripManagedFields)
	_ = inf.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		t.record(err)
	})
}

// preferredVersion returns the served preferred version for a CRD group, or
// fallback when the group is not advertised (the fake discovery in tests has no
// groups, so the fallback is used there).
func preferredVersion(disc discoveryLike, group, fallback string) string {
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

	gctx, cancel := context.WithCancel(parent)
	g := &gitopsWatch{cancel: cancel, srcInf: map[flux.Kind]cache.SharedIndexInformer{}}
	// watchKind builds an informer for one Flux resource at the version discovery
	// says THAT RESOURCE is served at (review 2026-10-03 #11), or nothing when
	// its CRD is not installed - an unserved kind is absent, not an error. Its
	// list/watch report successes to the informer's own tracker (withRecovery),
	// so health knows when a failed watch has recovered.
	watchKind := func(kind flux.Kind, group, resource, fallback string) cache.SharedIndexInformer {
		ver, served := servedVersion(c.discovery(), group, resource, fallback)
		if !served {
			return nil
		}
		gvr := schema.GroupVersionResource{Group: group, Version: ver, Resource: resource}
		ri := dyn.Resource(gvr)
		errs := &watchErrTracker{}
		lw := withRecovery(&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return ri.List(ctx, o)
			},
			WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return ri.Watch(ctx, o)
			},
		}, errs)
		// Built like dynamicinformer's informer, including telling the
		// reflector whether this client supports watch-list semantics.
		inf := cache.NewSharedIndexInformerWithOptions(
			cache.ToListWatcherWithWatchListSemantics(lw, dyn),
			&unstructured.Unstructured{},
			cache.SharedIndexInformerOptions{
				ResyncPeriod:      defaultResync,
				Indexers:          cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
				ObjectDescription: gvr.String(),
			},
		)
		trackWatchErrors(inf, errs)
		g.watched = append(g.watched, watchedInformer{kind: kind, inf: inf, errs: errs})
		return inf
	}
	g.ksInf = watchKind(flux.KustomizationKind, "kustomize.toolkit.fluxcd.io", "kustomizations", "v1")
	g.hrInf = watchKind(flux.HelmReleaseKind, "helm.toolkit.fluxcd.io", "helmreleases", "v2")

	// Watch the source kinds alongside ks/hr so a stuck resource's root cause
	// (a source that is not pulling) is visible without a separate API read.
	for _, s := range sourceKinds {
		if inf := watchKind(s.kind, s.group, s.resource, s.fallback); inf != nil {
			g.srcInf[s.kind] = inf
		}
	}

	for _, w := range g.watched {
		go w.inf.Run(gctx.Done())
	}

	c.mu.Lock()
	c.gitopsOpening = false
	if !c.gitopsWanted {
		// Closed while we were starting: don't install; stop the factory.
		c.mu.Unlock()
		cancel()
		return
	}
	c.gitops = g
	c.mu.Unlock()
}

// GitOpsWatchHealth reports whether every watched Flux informer - primary AND
// source - has completed its initial sync, and the CURRENT watch failures
// (watchErrTracker.currentError), each named by kind. A failure on an informer
// that already synced says the view is showing last-known state. Any current
// failure must render as an error, never as an empty or quietly stale list
// (review 2026-10-03 #11: only Kustomization/HelmRelease sync was checked, and
// only before it completed).
func (c *ClusterConn) GitOpsWatchHealth() (synced bool, lastErr string) {
	c.mu.RLock()
	g := c.gitops
	c.mu.RUnlock()
	if g == nil {
		return false, ""
	}
	synced = true
	var errs []string
	for _, w := range g.watched {
		done := w.inf.HasSynced()
		if !done {
			synced = false
		}
		e := w.errs.currentError()
		switch {
		case e == "":
		case done:
			errs = append(errs, fmt.Sprintf("%s watch failing (showing last-known state): %s", w.kind, e))
		default:
			errs = append(errs, fmt.Sprintf("%s: %s", w.kind, e))
		}
	}
	return synced, strings.Join(errs, "; ")
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
	return storeGet(inf, namespace, name)
}

// storeGet looks an object up by its cache key (ns/name; bare name for
// cluster-scoped objects) - O(1) against the informer index, replacing the
// full-store scans these detail lookups used to do on every push tick.
func storeGet(inf cache.SharedIndexInformer, namespace, name string) (*unstructured.Unstructured, bool) {
	key := name
	if namespace != "" {
		key = namespace + "/" + name
	}
	obj, exists, err := inf.GetStore().GetByKey(key)
	if err != nil || !exists {
		return nil, false
	}
	u, ok := obj.(*unstructured.Unstructured)
	return u, ok
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
	return storeGet(inf, namespace, name)
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
	if g.ksInf != nil {
		for _, obj := range g.ksInf.GetStore().List() {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				out = append(out, flux.ParseKustomization(u))
			}
		}
	}
	if g.hrInf != nil {
		for _, obj := range g.hrInf.GetStore().List() {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				out = append(out, flux.ParseHelmRelease(u))
			}
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

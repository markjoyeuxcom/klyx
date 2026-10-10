package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/certmanager"
)

type certmanagerWatch struct {
	cancel  context.CancelFunc
	certInf cache.SharedIndexInformer                      // nil when Certificate is not served
	issInf  map[certmanager.Kind]cache.SharedIndexInformer // Issuer + ClusterIssuer (served ones)
	// watched: one entry per watched kind with its OWN recovery-aware error
	// tracker, so coverage is per kind - a synced Certificate informer says
	// nothing about a pending or forbidden ClusterIssuer watch.
	watched []cmWatched
	// unserved: kinds whose CRD is not installed (absent, not an error).
	unserved []string
}

type cmWatched struct {
	kind string
	inf  cache.SharedIndexInformer
	errs *watchErrTracker
}

// KindCoverage is one watched kind's evidence state: Served=false is a
// definitive absence; otherwise Synced (initial list done) and Error (a
// CURRENT failure - including one after the initial sync, while the view is
// showing last-known state).
type KindCoverage struct {
	Kind   string
	Served bool
	Synced bool
	Error  string
}

// issuerKinds is the set of cert-manager issuer CRDs Klyx watches alongside
// Certificates, so a stuck cert's root cause (a not-ready issuer) is visible
// without a separate API read.
var issuerKinds = []struct {
	kind     certmanager.Kind
	resource string
}{
	{certmanager.IssuerKind, "issuers"},
	{certmanager.ClusterIssuerKind, "clusterissuers"},
}

// OpenCertManager starts (idempotently) the lazy dynamic informers on the
// cert-manager CRDs (Certificate + Issuer + ClusterIssuer). Mirrors OpenGitOps,
// including the opening/wanted flags that keep a Close landing mid-open from
// leaking the freshly started factory.
func (c *ClusterConn) OpenCertManager() {
	c.mu.Lock()
	c.certmgrWanted = true
	if c.certmgr != nil || c.certmgrOpening {
		c.mu.Unlock()
		return
	}
	c.certmgrOpening = true
	dyn := c.dyn
	parent := c.ctx
	c.mu.Unlock()
	if dyn == nil || parent == nil {
		c.mu.Lock()
		c.certmgrOpening = false
		c.mu.Unlock()
		return
	}

	gctx, cancel := context.WithCancel(parent)
	g := &certmanagerWatch{cancel: cancel, issInf: map[certmanager.Kind]cache.SharedIndexInformer{}}
	// watchKind mirrors OpenGitOps: each kind at the version discovery says
	// THAT resource is served at, or nothing when its CRD is absent; list/watch
	// successes mark the kind's own tracker recovered (withRecovery).
	watchKind := func(kind, resource string) cache.SharedIndexInformer {
		ver, served := servedVersion(c.discovery(), "cert-manager.io", resource, "v1")
		if !served {
			g.unserved = append(g.unserved, kind)
			return nil
		}
		gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: ver, Resource: resource}
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
		g.watched = append(g.watched, cmWatched{kind: kind, inf: inf, errs: errs})
		return inf
	}
	g.certInf = watchKind("Certificate", "certificates")
	for _, k := range issuerKinds {
		if inf := watchKind(string(k.kind), k.resource); inf != nil {
			g.issInf[k.kind] = inf
		}
	}
	for _, w := range g.watched {
		go w.inf.Run(gctx.Done())
	}

	c.mu.Lock()
	c.certmgrOpening = false
	if !c.certmgrWanted {
		// Closed while we were starting: don't install; stop the factory.
		c.mu.Unlock()
		cancel()
		return
	}
	c.certmgr = g
	c.mu.Unlock()
}

// CertManagerWatchHealth reports whether EVERY watched cert-manager informer
// has synced, and the CURRENT failures of any kind (a failure after the
// initial sync included - the view is then showing last-known state). Mirrors
// GitOpsWatchHealth. Per-kind detail is CertManagerCoverage.
func (c *ClusterConn) CertManagerWatchHealth() (synced bool, lastErr string) {
	cov := c.CertManagerCoverage()
	if cov == nil {
		return false, ""
	}
	synced = true
	var errs []string
	for _, k := range cov {
		if !k.Served {
			continue
		}
		if !k.Synced {
			synced = false
		}
		switch {
		case k.Error == "":
		case k.Synced:
			errs = append(errs, fmt.Sprintf("%s watch failing (showing last-known state): %s", k.Kind, k.Error))
		default:
			errs = append(errs, fmt.Sprintf("%s: %s", k.Kind, k.Error))
		}
	}
	return synced, strings.Join(errs, "; ")
}

// CertManagerCoverage reports evidence per kind (Certificate, Issuer,
// ClusterIssuer). nil when the watch is not open.
func (c *ClusterConn) CertManagerCoverage() []KindCoverage {
	c.mu.RLock()
	g := c.certmgr
	c.mu.RUnlock()
	if g == nil {
		return nil
	}
	out := make([]KindCoverage, 0, len(g.watched)+len(g.unserved))
	for _, w := range g.watched {
		out = append(out, KindCoverage{Kind: w.kind, Served: true, Synced: w.inf.HasSynced(), Error: w.errs.currentError()})
	}
	for _, k := range g.unserved {
		out = append(out, KindCoverage{Kind: k, Served: false, Synced: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// CloseCertManager stops the cert-manager informers. Clearing certmgrWanted
// also aborts an in-flight OpenCertManager install (see OpenGitOps).
func (c *ClusterConn) CloseCertManager() {
	c.mu.Lock()
	c.certmgrWanted = false
	g := c.certmgr
	c.certmgr = nil
	c.mu.Unlock()
	if g != nil {
		g.cancel()
	}
}

// CertManagerCertificates reads the Certificate informer store and parses it.
// nil when not open. Sorted (namespace/name) so the UI list does not reshuffle.
func (c *ClusterConn) CertManagerCertificates() []certmanager.Certificate {
	c.mu.RLock()
	g := c.certmgr
	c.mu.RUnlock()
	if g == nil {
		return nil
	}
	var out []certmanager.Certificate
	if g.certInf == nil {
		return nil
	}
	for _, obj := range g.certInf.GetStore().List() {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			out = append(out, certmanager.ParseCertificate(u))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

// CertManagerIssuers reads the Issuer + ClusterIssuer informer stores and parses
// them. nil when not open. Sorted (kind then namespace/name).
func (c *ClusterConn) CertManagerIssuers() []certmanager.Issuer {
	c.mu.RLock()
	g := c.certmgr
	c.mu.RUnlock()
	if g == nil {
		return nil
	}
	var out []certmanager.Issuer
	for _, inf := range g.issInf {
		for _, obj := range inf.GetStore().List() {
			if u, ok := obj.(*unstructured.Unstructured); ok {
				out = append(out, certmanager.ParseIssuer(u))
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

// CertManagerObject returns a watched cert-manager object by kind/namespace/name
// from the live informer store. false if the watch is closed or not found.
// ClusterIssuers are cluster-scoped, so pass an empty namespace for them.
func (c *ClusterConn) CertManagerObject(kind, namespace, name string) (*unstructured.Unstructured, bool) {
	c.mu.RLock()
	g := c.certmgr
	c.mu.RUnlock()
	if g == nil {
		return nil, false
	}
	var inf cache.SharedIndexInformer
	if certmanager.Kind(kind) == certmanager.CertificateKind {
		inf = g.certInf
	} else {
		inf = g.issInf[certmanager.Kind(kind)]
	}
	if inf == nil {
		return nil, false
	}
	return storeGet(inf, namespace, name)
}

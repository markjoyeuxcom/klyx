package fleet

import (
	"context"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/certmanager"
)

type certmanagerWatch struct {
	cancel  context.CancelFunc
	certInf cache.SharedIndexInformer                      // nil when Certificate is not served
	issInf  map[certmanager.Kind]cache.SharedIndexInformer // Issuer + ClusterIssuer (served ones)
	kinds   kindSet                                        // per-kind coverage (kindcoverage.go)
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
	g.certInf = g.kinds.watch(c.discovery(), dyn, "Certificate", "cert-manager.io", "certificates", "v1")
	for _, k := range issuerKinds {
		if inf := g.kinds.watch(c.discovery(), dyn, string(k.kind), "cert-manager.io", k.resource, "v1"); inf != nil {
			g.issInf[k.kind] = inf
		}
	}
	g.kinds.run(gctx.Done())

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
// has synced, and the CURRENT failures of any kind (after-sync failures
// included). Per-kind detail is CertManagerCoverage.
func (c *ClusterConn) CertManagerWatchHealth() (synced bool, lastErr string) {
	cov := c.CertManagerCoverage()
	if cov == nil {
		return false, ""
	}
	return healthOf(cov)
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
	return g.kinds.coverage()
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

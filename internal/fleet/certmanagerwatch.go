package fleet

import (
	"context"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/certmanager"
)

type certmanagerWatch struct {
	cancel  context.CancelFunc
	certInf cache.SharedIndexInformer
	issInf  map[certmanager.Kind]cache.SharedIndexInformer // Issuer + ClusterIssuer
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
// cert-manager CRDs (Certificate + Issuer + ClusterIssuer). Mirrors OpenGitOps.
func (c *ClusterConn) OpenCertManager() {
	c.mu.Lock()
	if c.certmgr != nil {
		c.mu.Unlock()
		return
	}
	dyn := c.dyn
	parent := c.ctx
	c.mu.Unlock()
	if dyn == nil || parent == nil {
		return
	}

	ver := preferredVersion(c.typed.Discovery(), "cert-manager.io", "v1")
	certGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: ver, Resource: "certificates"}

	gctx, cancel := context.WithCancel(parent)
	factory := dynamicinformer.NewDynamicSharedInformerFactory(dyn, defaultResync)
	certInf := factory.ForResource(certGVR).Informer()

	issInf := make(map[certmanager.Kind]cache.SharedIndexInformer, len(issuerKinds))
	for _, k := range issuerKinds {
		gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: ver, Resource: k.resource}
		issInf[k.kind] = factory.ForResource(gvr).Informer()
	}

	factory.Start(gctx.Done())

	c.mu.Lock()
	c.certmgr = &certmanagerWatch{cancel: cancel, certInf: certInf, issInf: issInf}
	c.mu.Unlock()
}

// CloseCertManager stops the cert-manager informers.
func (c *ClusterConn) CloseCertManager() {
	c.mu.Lock()
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
	for _, obj := range inf.GetStore().List() {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			if u.GetNamespace() == namespace && u.GetName() == name {
				return u, true
			}
		}
	}
	return nil, false
}

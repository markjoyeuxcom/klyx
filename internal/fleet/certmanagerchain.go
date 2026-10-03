package fleet

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/certmanager"
)

// GetCertificateChain resolves a Certificate's issuance chain on demand:
// the bound Issuer/ClusterIssuer's health (the headline), then the latest
// CertificateRequest and, for ACME issuers, its Order -> Challenge subtree.
// Ephemeral, high-churn objects (requests/orders/challenges) are listed on
// demand rather than watched, mirroring GetGatewayTopology. Soft failures
// (non-ACME issuers have no orders; RBAC on requests) degrade to warnings so
// the chain still renders.
func (c *ClusterConn) GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error) {
	certU, ok := c.CertManagerObject(string(certmanager.CertificateKind), ns, name)
	if !ok {
		ver := preferredVersion(c.discovery(), "cert-manager.io", "v1")
		gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: ver, Resource: "certificates"}
		u, err := c.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return certmanager.Chain{}, fmt.Errorf("certificate %s/%s not found: %w", ns, name, err)
		}
		certU = u
	}

	chain := certmanager.Chain{Certificate: certmanager.ParseCertificate(certU)}

	// Bound issuer health is the headline for a stuck cert (the M10 source pattern).
	if ref, ok := certmanager.BoundIssuer(certU); ok {
		if issU, ok := c.CertManagerObject(ref.Kind, ref.Namespace, ref.Name); ok {
			iss := certmanager.ParseIssuer(issU)
			chain.Issuer = &iss
		} else {
			// The headline went missing silently: an external issuer kind, an
			// issuer that does not exist, or the watch not synced yet. Say so -
			// "no issuer shown" must not read as "issuer fine".
			chain.Warnings = append(chain.Warnings, fmt.Sprintf("bound issuer %s %s is not in the watched issuers (external issuer kind, missing, or not yet synced) - its health is unknown", ref.Kind, nsName(ref.Namespace, ref.Name)))
		}
	}

	cmVer := preferredVersion(c.discovery(), "cert-manager.io", "v1")
	crGVR := schema.GroupVersionResource{Group: "cert-manager.io", Version: cmVer, Resource: "certificaterequests"}
	reqs, err := c.dyn.Resource(crGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		chain.Warnings = append(chain.Warnings, "could not list certificaterequests: "+err.Error())
		return chain, nil
	}

	// Keep the requests owned by this Certificate, newest revision first, and
	// resolve the full subtree for the latest one only (the current attempt -
	// the actionable chain). Older requests would just be historical noise.
	var owned []unstructured.Unstructured
	for i := range reqs.Items {
		if ownedBy(&reqs.Items[i], "Certificate", name) {
			owned = append(owned, reqs.Items[i])
		}
	}
	if len(owned) == 0 {
		return chain, nil
	}
	sort.SliceStable(owned, func(i, j int) bool {
		return certRequestRevision(&owned[i]) > certRequestRevision(&owned[j])
	})
	latest := &owned[0]
	node := certmanager.ParseChainNode(latest)
	node.Children = c.resolveOrders(ctx, ns, latest.GetName(), &chain.Warnings)
	chain.Requests = []certmanager.ChainNode{node}
	chain.FailingLink = certmanager.FirstFailingLink(chain.Requests)
	return chain, nil
}

// resolveOrders lists ACME Orders owned by a CertificateRequest and their
// Challenges. Only "the ACME CRDs are not installed" (NotFound) is expected and
// silent - a non-ACME setup simply has no Orders. Any other failure (RBAC
// denial, timeout) is recorded as a chain warning: an RBAC-denied Order list
// used to look exactly like a non-ACME issuer, hiding the stuck link.
func (c *ClusterConn) resolveOrders(ctx context.Context, ns, requestName string, warnings *[]string) []certmanager.ChainNode {
	acmeVer := preferredVersion(c.discovery(), "acme.cert-manager.io", "v1")
	orderGVR := schema.GroupVersionResource{Group: "acme.cert-manager.io", Version: acmeVer, Resource: "orders"}
	orders, err := c.dyn.Resource(orderGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			*warnings = append(*warnings, "could not list ACME orders: "+err.Error())
		}
		return nil
	}
	var out []certmanager.ChainNode
	for i := range orders.Items {
		if !ownedBy(&orders.Items[i], "CertificateRequest", requestName) {
			continue
		}
		onode := certmanager.ParseChainNode(&orders.Items[i])
		onode.Children = c.resolveChallenges(ctx, ns, orders.Items[i].GetName(), warnings)
		out = append(out, onode)
	}
	return out
}

func (c *ClusterConn) resolveChallenges(ctx context.Context, ns, orderName string, warnings *[]string) []certmanager.ChainNode {
	acmeVer := preferredVersion(c.discovery(), "acme.cert-manager.io", "v1")
	challengeGVR := schema.GroupVersionResource{Group: "acme.cert-manager.io", Version: acmeVer, Resource: "challenges"}
	challenges, err := c.dyn.Resource(challengeGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			*warnings = append(*warnings, "could not list ACME challenges: "+err.Error())
		}
		return nil
	}
	var out []certmanager.ChainNode
	for i := range challenges.Items {
		if ownedBy(&challenges.Items[i], "Order", orderName) {
			out = append(out, certmanager.ParseChainNode(&challenges.Items[i]))
		}
	}
	return out
}

// ownedBy reports whether u carries an ownerReference of the given kind+name.
func ownedBy(u *unstructured.Unstructured, kind, name string) bool {
	for _, r := range u.GetOwnerReferences() {
		if r.Kind == kind && r.Name == name {
			return true
		}
	}
	return false
}

// certRequestRevision reads cert-manager's certificate-revision annotation; 0
// when absent, so requests without it sort last.
func certRequestRevision(u *unstructured.Unstructured) int {
	if v, ok := u.GetAnnotations()["cert-manager.io/certificate-revision"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// nsName renders "ns/name", or just "name" for a cluster-scoped object.
func nsName(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "/" + name
}

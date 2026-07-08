package certmanager

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// IssuerRef points at the Issuer/ClusterIssuer a Certificate references.
// Namespace is empty for a ClusterIssuer (cluster-scoped).
type IssuerRef struct {
	Kind      string
	Name      string
	Namespace string
}

// BoundIssuer resolves the issuer a Certificate is issued by (spec.issuerRef).
// kind defaults to Issuer; an Issuer is looked up in the cert's namespace, a
// ClusterIssuer cluster-wide (empty namespace).
func BoundIssuer(u *unstructured.Unstructured) (IssuerRef, bool) {
	name, _, _ := unstructured.NestedString(u.Object, "spec", "issuerRef", "name")
	if name == "" {
		return IssuerRef{}, false
	}
	kind, _, _ := unstructured.NestedString(u.Object, "spec", "issuerRef", "kind")
	if kind == "" {
		kind = string(IssuerKind)
	}
	ns := ""
	if kind == string(IssuerKind) {
		ns = u.GetNamespace()
	}
	return IssuerRef{Kind: kind, Name: name, Namespace: ns}, true
}

// ChainNode is one object in the issuance chain (a CertificateRequest, Order, or
// Challenge) with its resolved state and the objects it owns.
type ChainNode struct {
	Kind      Kind
	Namespace string
	Name      string
	Ready     ReadyState
	Reason    string
	Message   string
	Children  []ChainNode
}

// Chain is a Certificate's issuance chain: the bound issuer's health (the
// headline for a stuck cert) and the CertificateRequest -> Order -> Challenge
// tree, with the deepest stuck node singled out as FailingLink.
type Chain struct {
	Certificate Certificate
	Issuer      *Issuer
	Requests    []ChainNode
	FailingLink *ChainNode
	Warnings    []string
}

// ParseChainNode reads a CertificateRequest/Order/Challenge into a ChainNode.
// CertificateRequests carry a Ready condition; ACME Orders/Challenges instead
// carry status.state (+ status.reason), so fall back to that when no Ready
// condition is present.
func ParseChainNode(u *unstructured.Unstructured) ChainNode {
	n := ChainNode{Kind: Kind(u.GetKind()), Namespace: u.GetNamespace(), Name: u.GetName()}
	n.Ready, n.Reason, n.Message = readyFromConditions(u)
	if n.Kind == CertificateRequestKind && n.Ready == Failed && n.Reason == "Pending" {
		// Ready=False reason=Pending is a CertificateRequest's normal in-flight
		// state, not a failure - its terminal reasons are Failed and Denied.
		// Certificates signal in-flight via a separate Issuing condition, so
		// this remap applies only to requests.
		n.Ready = Issuing
	}
	if n.Ready == Unknown {
		if st, _, _ := unstructured.NestedString(u.Object, "status", "state"); st != "" {
			n.Ready = acmeStateToReady(st)
		}
	}
	if n.Reason == "" {
		n.Reason, _, _ = unstructured.NestedString(u.Object, "status", "reason")
	}
	if n.Message == "" {
		n.Message, _, _ = unstructured.NestedString(u.Object, "status", "message")
	}
	return n
}

// acmeStateToReady maps an ACME Order/Challenge status.state to a ReadyState.
func acmeStateToReady(state string) ReadyState {
	switch state {
	case "valid", "ready":
		return Ready
	case "pending", "processing":
		return Issuing
	case "invalid", "errored", "expired":
		return Failed
	default:
		return Unknown
	}
}

// FirstFailingLink returns a pointer to the deepest not-Ready node in the chain
// (the most specific place issuance is stuck), or nil if every node is Ready.
// Children are searched before parents so the most actionable link wins.
func FirstFailingLink(nodes []ChainNode) *ChainNode {
	for i := range nodes {
		n := &nodes[i]
		if deeper := FirstFailingLink(n.Children); deeper != nil {
			return deeper
		}
		if n.Ready == Failed || n.Ready == Issuing {
			return n
		}
	}
	return nil
}

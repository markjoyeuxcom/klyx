package certmanager

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBoundIssuerClusterScoped(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":     "Certificate",
		"metadata": map[string]interface{}{"name": "acme-demo", "namespace": "payments"},
		"spec":     map[string]interface{}{"issuerRef": map[string]interface{}{"name": "lab-acme-staging", "kind": "ClusterIssuer"}},
	}}
	ref, ok := BoundIssuer(u)
	if !ok || ref.Kind != "ClusterIssuer" || ref.Name != "lab-acme-staging" || ref.Namespace != "" {
		t.Fatalf("bound: %+v ok=%v", ref, ok)
	}
}

func TestBoundIssuerNamespacedDefaultsKind(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":     "Certificate",
		"metadata": map[string]interface{}{"name": "x", "namespace": "payments"},
		"spec":     map[string]interface{}{"issuerRef": map[string]interface{}{"name": "ca"}},
	}}
	ref, ok := BoundIssuer(u)
	if !ok || ref.Kind != "Issuer" || ref.Namespace != "payments" {
		t.Fatalf("bound: %+v ok=%v", ref, ok)
	}
}

func acmeNode(kind, name, state, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":     kind,
		"metadata": map[string]interface{}{"name": name, "namespace": "payments"},
		"status":   map[string]interface{}{"state": state, "reason": reason},
	}}
}

func TestParseChainNodeACMEState(t *testing.T) {
	// An ACME Challenge has no Ready condition; state/reason drive it.
	n := ParseChainNode(acmeNode("Challenge", "acme-demo-1-abc", "pending", "Waiting for DNS-01 propagation"))
	if n.Kind != ChallengeKind || n.Ready != Issuing {
		t.Fatalf("challenge node: %+v", n)
	}
	if n.Reason != "Waiting for DNS-01 propagation" {
		t.Fatalf("reason: %q", n.Reason)
	}
}

func TestParseChainNodeCertificateRequestCondition(t *testing.T) {
	req := func(status, reason, message string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"kind":     "CertificateRequest",
			"metadata": map[string]interface{}{"name": "acme-demo-1", "namespace": "payments"},
			"status": map[string]interface{}{"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": status, "reason": reason, "message": message},
			}},
		}}
	}
	// Ready=False reason=Pending is the request's normal in-flight state - it
	// must read Issuing, not Failed (a plain renewal is not a broken chain).
	n := ParseChainNode(req("False", "Pending", "Waiting on order"))
	if n.Ready != Issuing || n.Reason != "Pending" {
		t.Fatalf("pending request node: %+v", n)
	}
	// Failed and Denied are the terminal reasons.
	if n := ParseChainNode(req("False", "Failed", "order errored")); n.Ready != Failed {
		t.Fatalf("failed request node: %+v", n)
	}
	if n := ParseChainNode(req("False", "Denied", "denied by approver")); n.Ready != Failed {
		t.Fatalf("denied request node: %+v", n)
	}
	if n := ParseChainNode(req("True", "Issued", "issued")); n.Ready != Ready {
		t.Fatalf("issued request node: %+v", n)
	}
}

func TestParseChainNodePendingRemapOnlyForRequests(t *testing.T) {
	// The Pending remap is request-specific: any other kind with Ready=False
	// reason=Pending keeps Failed semantics.
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":     "Order",
		"metadata": map[string]interface{}{"name": "ord", "namespace": "payments"},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "False", "reason": "Pending", "message": "x"},
		}},
	}}
	if n := ParseChainNode(u); n.Ready != Failed {
		t.Fatalf("order node: %+v", n)
	}
}

func TestFirstFailingLinkReturnsDeepest(t *testing.T) {
	// Request (Issuing) -> Order (Issuing) -> Challenge (Failed): the Challenge is
	// the most specific stuck link.
	chain := []ChainNode{{
		Kind: CertificateRequestKind, Name: "req", Ready: Issuing,
		Children: []ChainNode{{
			Kind: OrderKind, Name: "ord", Ready: Issuing,
			Children: []ChainNode{{Kind: ChallengeKind, Name: "chal", Ready: Failed, Reason: "DNSError"}},
		}},
	}}
	link := FirstFailingLink(chain)
	if link == nil || link.Kind != ChallengeKind || link.Reason != "DNSError" {
		t.Fatalf("failing link: %+v", link)
	}
}

func TestFirstFailingLinkNilWhenAllReady(t *testing.T) {
	chain := []ChainNode{{
		Kind: CertificateRequestKind, Name: "req", Ready: Ready,
		Children: []ChainNode{{Kind: OrderKind, Name: "ord", Ready: Ready}},
	}}
	if link := FirstFailingLink(chain); link != nil {
		t.Fatalf("want nil failing link, got %+v", link)
	}
}

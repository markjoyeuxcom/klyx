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
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":     "CertificateRequest",
		"metadata": map[string]interface{}{"name": "acme-demo-1", "namespace": "payments"},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "False", "reason": "Pending", "message": "Waiting on order"},
		}},
	}}
	n := ParseChainNode(u)
	if n.Ready != Failed || n.Reason != "Pending" {
		t.Fatalf("request node: %+v", n)
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

package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func ownerRef(kind, name string) interface{} {
	return map[string]interface{}{"apiVersion": "cert-manager.io/v1", "kind": kind, "name": name, "uid": name + "-uid"}
}

func certRequestObj(name, ownerCert, revision string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "CertificateRequest",
		"metadata": map[string]interface{}{
			"name": name, "namespace": "payments",
			"annotations":     map[string]interface{}{"cert-manager.io/certificate-revision": revision},
			"ownerReferences": []interface{}{ownerRef("Certificate", ownerCert)},
		},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "False", "reason": "Pending", "message": "Waiting on order"},
		}},
	}}
}

func acmeObj(kind, name, owner, ownerKind, state, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "acme.cert-manager.io/v1",
		"kind":       kind,
		"metadata": map[string]interface{}{
			"name": name, "namespace": "payments",
			"ownerReferences": []interface{}{ownerRef(ownerKind, owner)},
		},
		"status": map[string]interface{}{"state": state, "reason": reason},
	}}
}

func chainGVRToListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}:        "CertificateList",
		{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}:             "IssuerList",
		{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}:      "ClusterIssuerList",
		{Group: "cert-manager.io", Version: "v1", Resource: "certificaterequests"}: "CertificateRequestList",
		{Group: "acme.cert-manager.io", Version: "v1", Resource: "orders"}:         "OrderList",
		{Group: "acme.cert-manager.io", Version: "v1", Resource: "challenges"}:     "ChallengeList",
	}
}

func TestGetCertificateChainResolvesFailingLink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, chainGVRToListKinds(),
		certUnstructured("acme-demo", "payments", "False", "DoesNotExist"),
		clusterIssuerUnstructured("lab-acme-staging", "False"),
		certRequestObj("acme-demo-1", "acme-demo", "1"),
		acmeObj("Order", "acme-demo-1-123", "acme-demo-1", "CertificateRequest", "errored", "order errored"),
		acmeObj("Challenge", "acme-demo-1-123-456", "acme-demo-1-123", "Order", "pending", "DNS-01 propagation"),
	)

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx
	c.OpenCertManager()
	defer c.CloseCertManager()

	// Wait for the watch store so the bound-issuer lookup (CertManagerObject) resolves.
	waitFor(t, 2*time.Second, func() bool {
		_, ok := c.CertManagerObject("ClusterIssuer", "", "lab-acme-staging")
		return ok
	})

	chain, err := c.GetCertificateChain(ctx, "payments", "acme-demo")
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if chain.Certificate.Name != "acme-demo" || chain.Certificate.Ready != certmanager.Failed {
		t.Fatalf("certificate: %+v", chain.Certificate)
	}
	if chain.Issuer == nil || chain.Issuer.Ready != certmanager.Failed {
		t.Fatalf("bound issuer headline: %+v", chain.Issuer)
	}
	if len(chain.Requests) != 1 || chain.Requests[0].Kind != certmanager.CertificateRequestKind {
		t.Fatalf("requests: %+v", chain.Requests)
	}
	order := chain.Requests[0].Children
	if len(order) != 1 || order[0].Kind != certmanager.OrderKind || order[0].Ready != certmanager.Failed {
		t.Fatalf("order: %+v", order)
	}
	chal := order[0].Children
	if len(chal) != 1 || chal[0].Kind != certmanager.ChallengeKind {
		t.Fatalf("challenge: %+v", chal)
	}
	// Deepest stuck node = the pending Challenge.
	if chain.FailingLink == nil || chain.FailingLink.Kind != certmanager.ChallengeKind {
		t.Fatalf("failing link: %+v", chain.FailingLink)
	}
}

func TestGetCertificateChainNoRequestsIsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, chainGVRToListKinds(),
		certUnstructured("self-signed", "payments", "True", "Ready"),
		clusterIssuerUnstructured("lab-acme-staging", "True"))

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx
	c.OpenCertManager()
	defer c.CloseCertManager()
	waitFor(t, 2*time.Second, func() bool {
		_, ok := c.CertManagerObject("ClusterIssuer", "", "lab-acme-staging")
		return len(c.CertManagerCertificates()) == 1 && ok
	})

	chain, err := c.GetCertificateChain(ctx, "payments", "self-signed")
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain.Requests) != 0 || chain.FailingLink != nil || len(chain.Warnings) != 0 {
		t.Fatalf("expected a clean empty chain, got %+v", chain)
	}
}

// Review 2026-10-03 (K): a bound issuer the watch does not hold (external
// issuer kind, deleted, not synced) used to leave the headline blank with no
// warning - "no issuer shown" read as "issuer fine".
func TestGetCertificateChainWarnsWhenBoundIssuerIsUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), chainGVRToListKinds(),
		certUnstructured("orphan", "payments", "True", "Ready"))
	c := NewClusterConn("x", typed, nil, dyn, capability.NewDetector(typed), clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx
	c.OpenCertManager()
	defer c.CloseCertManager()
	waitFor(t, 2*time.Second, func() bool { return len(c.CertManagerCertificates()) == 1 })

	chain, err := c.GetCertificateChain(ctx, "payments", "orphan")
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if chain.Issuer != nil || len(chain.Warnings) != 1 || !strings.Contains(chain.Warnings[0], "ClusterIssuer lab-acme-staging") {
		t.Fatalf("want an unknown-issuer warning, got issuer=%v warnings=%v", chain.Issuer, chain.Warnings)
	}
}

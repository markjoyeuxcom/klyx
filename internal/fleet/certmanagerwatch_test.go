package fleet

import (
	"context"
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

func certUnstructured(name, ns, ready, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"secretName": name + "-tls",
			"issuerRef":  map[string]interface{}{"name": "lab-acme-staging", "kind": "ClusterIssuer"},
		},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": ready, "reason": reason},
		}},
	}}
}

func clusterIssuerUnstructured(name, ready string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "ClusterIssuer",
		"metadata":   map[string]interface{}{"name": name},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": ready},
		}},
	}}
}

func certManagerGVRToListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}:   "CertificateList",
		{Group: "cert-manager.io", Version: "v1", Resource: "issuers"}:        "IssuerList",
		{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}: "ClusterIssuerList",
	}
}

func TestOpenCertManagerListsCertificatesSorted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	// Seed out of order; the list must come back namespace/name sorted.
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, certManagerGVRToListKinds(),
		certUnstructured("z-cert", "payments", "True", "Ready"),
		certUnstructured("a-cert", "payments", "False", "DoesNotExist"),
		clusterIssuerUnstructured("lab-acme-staging", "False"))

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx
	c.OpenCertManager()
	defer c.CloseCertManager()

	waitFor(t, 2*time.Second, func() bool { return len(c.CertManagerCertificates()) == 2 })

	certs := c.CertManagerCertificates()
	if certs[0].Name != "a-cert" || certs[1].Name != "z-cert" {
		t.Fatalf("want a-cert,z-cert order, got %s,%s", certs[0].Name, certs[1].Name)
	}
	if certs[0].Ready != certmanager.Failed || certs[0].Reason != "DoesNotExist" {
		t.Fatalf("failing cert not parsed: %+v", certs[0])
	}
}

func TestCertManagerIssuersAndObject(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed := fake.NewSimpleClientset()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, certManagerGVRToListKinds(),
		certUnstructured("acme-demo", "payments", "False", "DoesNotExist"),
		clusterIssuerUnstructured("lab-acme-staging", "False"))

	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.ctx = ctx
	c.OpenCertManager()
	defer c.CloseCertManager()

	// Wait for BOTH informers: the object lookups below read the certificate
	// store too, and its list can lag the issuer list (observed flake).
	waitFor(t, 2*time.Second, func() bool {
		return len(c.CertManagerIssuers()) == 1 && len(c.CertManagerCertificates()) == 1
	})

	iss := c.CertManagerIssuers()
	if iss[0].Kind != certmanager.ClusterIssuerKind || iss[0].Namespace != "" || iss[0].Ready != certmanager.Failed {
		t.Fatalf("clusterissuer: %+v", iss[0])
	}

	// ClusterIssuer is cluster-scoped: empty namespace.
	if _, ok := c.CertManagerObject("ClusterIssuer", "", "lab-acme-staging"); !ok {
		t.Fatal("expected to find the watched ClusterIssuer")
	}
	if _, ok := c.CertManagerObject("Certificate", "payments", "acme-demo"); !ok {
		t.Fatal("expected to find the watched Certificate")
	}
	if _, ok := c.CertManagerObject("Certificate", "payments", "nope"); ok {
		t.Fatal("did not expect to find a nonexistent certificate")
	}
}

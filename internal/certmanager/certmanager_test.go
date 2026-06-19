package certmanager

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func certObj(ready, reason string, issuing bool) *unstructured.Unstructured {
	conds := []interface{}{
		map[string]interface{}{"type": "Ready", "status": ready, "reason": reason, "message": "the cert is " + ready},
	}
	if issuing {
		conds = append(conds, map[string]interface{}{"type": "Issuing", "status": "True", "reason": "Requested", "message": "issuing"})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "Certificate",
		"metadata":   map[string]interface{}{"name": "acme-demo", "namespace": "payments"},
		"spec": map[string]interface{}{
			"secretName": "acme-demo-tls",
			"dnsNames":   []interface{}{"acme-demo.klustr.io"},
			"issuerRef":  map[string]interface{}{"name": "lab-acme-staging", "kind": "ClusterIssuer", "group": "cert-manager.io"},
		},
		"status": map[string]interface{}{
			"notAfter":    "2026-09-01T00:00:00Z",
			"renewalTime": "2026-08-01T00:00:00Z",
			"conditions":  conds,
		},
	}}
}

func TestParseCertificateReady(t *testing.T) {
	c := ParseCertificate(certObj("True", "Ready", false))
	if c.Namespace != "payments" || c.Name != "acme-demo" {
		t.Fatalf("identity: %+v", c)
	}
	if c.Ready != Ready {
		t.Fatalf("ready: %q", c.Ready)
	}
	if c.IssuerKind != "ClusterIssuer" || c.IssuerName != "lab-acme-staging" {
		t.Fatalf("issuer: %q/%q", c.IssuerKind, c.IssuerName)
	}
	if c.SecretName != "acme-demo-tls" {
		t.Fatalf("secret: %q", c.SecretName)
	}
	if len(c.DNSNames) != 1 || c.DNSNames[0] != "acme-demo.klustr.io" {
		t.Fatalf("dnsNames: %v", c.DNSNames)
	}
	if c.NotAfter.IsZero() || c.RenewalTime.IsZero() {
		t.Fatalf("times: notAfter=%v renewal=%v", c.NotAfter, c.RenewalTime)
	}
}

func TestParseCertificateFailedCarriesReason(t *testing.T) {
	// The Klustr screenshot case: Ready=False, reason DoesNotExist, not issuing.
	c := ParseCertificate(certObj("False", "DoesNotExist", false))
	if c.Ready != Failed || c.Reason != "DoesNotExist" {
		t.Fatalf("failed cert: %+v", c)
	}
}

func TestParseCertificateIssuingTakesPrecedence(t *testing.T) {
	// Issuing=True while Ready=False (first issuance / stuck issuance) -> Issuing,
	// but the Ready reason is still surfaced so the chain headline is informative.
	c := ParseCertificate(certObj("False", "DoesNotExist", true))
	if c.Ready != Issuing {
		t.Fatalf("want Issuing, got %q", c.Ready)
	}
	if c.Reason != "DoesNotExist" {
		t.Fatalf("want Ready-condition reason preserved, got %q", c.Reason)
	}
}

func TestParseCertificateIssuerKindDefaultsToIssuer(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":     "Certificate",
		"metadata": map[string]interface{}{"name": "x", "namespace": "y"},
		"spec":     map[string]interface{}{"issuerRef": map[string]interface{}{"name": "ca"}},
	}}
	c := ParseCertificate(u)
	if c.IssuerKind != "Issuer" {
		t.Fatalf("default issuer kind: %q", c.IssuerKind)
	}
}

func TestParseIssuerClusterScoped(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "ClusterIssuer",
		"metadata":   map[string]interface{}{"name": "lab-acme-staging"},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": "False", "reason": "ACMEAccountRegistrationFailed", "message": "no account"},
		}},
	}}
	i := ParseIssuer(u)
	if i.Kind != ClusterIssuerKind || i.Namespace != "" || i.Name != "lab-acme-staging" {
		t.Fatalf("identity: %+v", i)
	}
	if i.Ready != Failed || i.Reason != "ACMEAccountRegistrationFailed" {
		t.Fatalf("issuer health: %+v", i)
	}
}

func TestParseTimeTolerant(t *testing.T) {
	if got := parseTime(&unstructured.Unstructured{Object: map[string]interface{}{}}, "status", "notAfter"); !got.IsZero() {
		t.Fatalf("missing time should be zero, got %v", got)
	}
	want, _ := time.Parse(time.RFC3339, "2026-09-01T00:00:00Z")
	u := &unstructured.Unstructured{Object: map[string]interface{}{"status": map[string]interface{}{"notAfter": "2026-09-01T00:00:00Z"}}}
	if got := parseTime(u, "status", "notAfter"); !got.Equal(want) {
		t.Fatalf("parseTime: %v want %v", got, want)
	}
}

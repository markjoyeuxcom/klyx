package fleet

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
)

func scanCert(t *testing.T, cn string, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notAfter.Add(-24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestExpiryScan_SecretsAndWebhooksSoonestFirst(t *testing.T) {
	soon := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2027, 8, 1, 0, 0, 0, 0, time.UTC)

	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web-tls"},
		Type:       corev1.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": scanCert(t, "web.team-a", later), "tls.key": []byte("SECRET")},
	}
	sideEffects := admissionv1.SideEffectClassNone
	webhook := &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cert-manager-webhook"},
		Webhooks: []admissionv1.ValidatingWebhook{{
			Name:                    "webhook.cert-manager.io",
			SideEffects:             &sideEffects,
			AdmissionReviewVersions: []string{"v1"},
			ClientConfig:            admissionv1.WebhookClientConfig{CABundle: scanCert(t, "cert-manager-ca", soon)},
		}},
	}
	cs := k8sfake.NewSimpleClientset(tlsSecret, webhook)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}

	r, err := c.ExpiryScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 2 {
		t.Fatalf("items: %+v", r.Items)
	}
	// Soonest first: the webhook CA (2026) before the TLS secret (2027).
	if r.Items[0].Source != "webhook-ca" || r.Items[0].Name != "cert-manager-webhook" || r.Items[0].Detail != "webhook.cert-manager.io" {
		t.Fatalf("first: %+v", r.Items[0])
	}
	if r.Items[1].Source != "tls-secret" || r.Items[1].Namespace != "team-a" || r.Items[1].Subject != "CN=web.team-a" {
		t.Fatalf("second: %+v", r.Items[1])
	}
	// The private key is never carried anywhere.
	for _, it := range r.Items {
		if it.Subject == "SECRET" || it.Detail == "SECRET" {
			t.Fatal("key material leaked")
		}
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("warnings: %+v", r.Warnings)
	}
}

package expiry

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func selfSigned(t *testing.T, cn string, notAfter time.Time) []byte {
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

func TestParseLeaf(t *testing.T) {
	exp := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	leaf := selfSigned(t, "db.team-a.svc", exp)
	// A bundle with a leading non-certificate block still finds the cert.
	bundle := append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("junk")}), leaf...)

	info, err := ParseLeaf(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !info.NotAfter.Equal(exp) || info.Subject != "CN=db.team-a.svc" {
		t.Fatalf("info: %+v", info)
	}
}

func TestParseLeafRejectsGarbage(t *testing.T) {
	if _, err := ParseLeaf([]byte("not pem at all")); err == nil {
		t.Fatal("garbage must error, never invent an expiry")
	}
}

func TestDaysLeft(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	it := Item{NotAfter: now.Add(72 * time.Hour)}
	if d := it.DaysLeft(now); d != 3 {
		t.Fatalf("days: %d", d)
	}
	expired := Item{NotAfter: now.Add(-48 * time.Hour)}
	if d := expired.DaysLeft(now); d != -2 {
		t.Fatalf("expired days: %d", d)
	}
}

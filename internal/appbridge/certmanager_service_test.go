package appbridge

import (
	"context"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
)

type fakeCertManagerConn struct {
	opened, closed int
	certs          []certmanager.Certificate
	iss            []certmanager.Issuer
	chain          certmanager.Chain
}

func (f *fakeCertManagerConn) OpenCertManager()                                   { f.opened++ }
func (f *fakeCertManagerConn) CloseCertManager()                                  { f.closed++ }
func (f *fakeCertManagerConn) CertManagerCertificates() []certmanager.Certificate { return f.certs }
func (f *fakeCertManagerConn) CertManagerWatchHealth() (bool, string)             { return true, "" }
func (f *fakeCertManagerConn) CertManagerIssuers() []certmanager.Issuer           { return f.iss }
func (f *fakeCertManagerConn) GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error) {
	return f.chain, nil
}

func TestToCertificateDTOComputesExpiry(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	notBefore := now.Add(-24 * time.Hour)
	notAfter := now.Add(72 * time.Hour)
	dto := toCertificateDTO(certmanager.Certificate{
		Namespace: "payments", Name: "acme-demo", Ready: certmanager.Failed, Reason: "DoesNotExist",
		IssuerKind: "ClusterIssuer", IssuerName: "lab-acme-staging", SecretName: "acme-demo-tls",
		DNSNames: []string{"acme-demo.klustr.io"}, NotBefore: notBefore, NotAfter: notAfter,
	}, now)
	if dto.Ready != "Failed" || dto.Reason != "DoesNotExist" {
		t.Fatalf("status: %+v", dto)
	}
	if dto.IssuerKind != "ClusterIssuer" || dto.IssuerName != "lab-acme-staging" || dto.SecretName != "acme-demo-tls" {
		t.Fatalf("issuer/secret: %+v", dto)
	}
	if dto.NotBeforeUnix != notBefore.Unix() || dto.NotAfterUnix != notAfter.Unix() || dto.ExpiresInSeconds != int64(72*3600) {
		t.Fatalf("expiry: notBefore=%d notAfter=%d expires=%d", dto.NotBeforeUnix, dto.NotAfterUnix, dto.ExpiresInSeconds)
	}
}

func TestToCertificateDTONoExpiryWhenUnissued(t *testing.T) {
	dto := toCertificateDTO(certmanager.Certificate{Name: "x"}, time.Now())
	if dto.NotBeforeUnix != 0 || dto.NotAfterUnix != 0 || dto.ExpiresInSeconds != 0 {
		t.Fatalf("want zero expiry for an unissued cert: %+v", dto)
	}
	if dto.DNSNames == nil {
		t.Fatalf("want empty dnsNames array, got nil")
	}
}

func TestCertManagerServiceSnapshot(t *testing.T) {
	now := time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)
	conn := &fakeCertManagerConn{
		certs: []certmanager.Certificate{{Namespace: "payments", Name: "acme-demo", Ready: certmanager.Failed}},
		iss:   []certmanager.Issuer{{Kind: certmanager.ClusterIssuerKind, Name: "lab-acme-staging", Ready: certmanager.Failed}},
	}
	svc := NewCertManagerService(func(string) (CertManagerConn, bool) { return conn, true }, &fakeEmitter{}, func() time.Time { return now }, time.Second)
	snap := certmanagerDomainSnap{certs: conn.CertManagerCertificates(), issuers: conn.CertManagerIssuers()}
	p := svc.buildPayload("nelli", snap, now)
	if p.Cluster != "nelli" || len(p.Certificates) != 1 || len(p.Issuers) != 1 {
		t.Fatalf("payload: %+v", p)
	}
	if p.Issuers[0].Kind != "ClusterIssuer" || p.Issuers[0].Ready != "Failed" {
		t.Fatalf("issuer dto: %+v", p.Issuers[0])
	}
}

func TestGetCertificateChainMapsTree(t *testing.T) {
	failing := &certmanager.ChainNode{Kind: certmanager.ChallengeKind, Name: "chal", Ready: certmanager.Failed, Reason: "DNSError"}
	conn := &fakeCertManagerConn{chain: certmanager.Chain{
		Certificate: certmanager.Certificate{Namespace: "payments", Name: "acme-demo", Ready: certmanager.Failed},
		Issuer:      &certmanager.Issuer{Kind: certmanager.ClusterIssuerKind, Name: "lab-acme-staging", Ready: certmanager.Failed, Reason: "ACMEAccountRegistrationFailed"},
		Requests: []certmanager.ChainNode{{
			Kind: certmanager.CertificateRequestKind, Name: "acme-demo-1", Ready: certmanager.Failed,
			Children: []certmanager.ChainNode{{Kind: certmanager.OrderKind, Name: "ord", Ready: certmanager.Failed,
				Children: []certmanager.ChainNode{*failing}}},
		}},
		FailingLink: failing,
	}}
	svc := NewCertManagerService(func(string) (CertManagerConn, bool) { return conn, true }, &fakeEmitter{}, func() time.Time { return time.Now() }, time.Second)
	dto := svc.GetCertificateChain("nelli", "payments", "acme-demo")
	if dto.Issuer == nil || dto.Issuer.Reason != "ACMEAccountRegistrationFailed" {
		t.Fatalf("issuer headline: %+v", dto.Issuer)
	}
	if len(dto.Requests) != 1 || len(dto.Requests[0].Children) != 1 || len(dto.Requests[0].Children[0].Children) != 1 {
		t.Fatalf("chain tree not mapped: %+v", dto.Requests)
	}
	if dto.FailingLink == nil || dto.FailingLink.Kind != "Challenge" || dto.FailingLink.Reason != "DNSError" {
		t.Fatalf("failing link: %+v", dto.FailingLink)
	}
}

func TestGetCertificateChainUsesEmptyArraysForLeafAndNoRequests(t *testing.T) {
	conn := &fakeCertManagerConn{chain: certmanager.Chain{
		Certificate: certmanager.Certificate{Namespace: "cnpg-system", Name: "selfsigned", Ready: certmanager.Ready},
		Requests: []certmanager.ChainNode{{
			Kind: certmanager.CertificateRequestKind, Namespace: "cnpg-system", Name: "selfsigned-1", Ready: certmanager.Ready,
		}},
	}}
	svc := NewCertManagerService(func(string) (CertManagerConn, bool) { return conn, true }, &fakeEmitter{}, func() time.Time { return time.Now() }, time.Second)
	dto := svc.GetCertificateChain("nelli", "cnpg-system", "selfsigned")
	if dto.Certificate.DNSNames == nil {
		t.Fatalf("want empty certificate dnsNames array, got nil")
	}
	if dto.Requests == nil || len(dto.Requests) != 1 {
		t.Fatalf("want one request, got %+v", dto.Requests)
	}
	if dto.Requests[0].Children == nil {
		t.Fatalf("want empty leaf children array, got nil")
	}
	if dto.Warnings == nil {
		t.Fatalf("want empty warnings array, got nil")
	}
}

func TestGetCertificateChainClusterMiss(t *testing.T) {
	svc := NewCertManagerService(func(string) (CertManagerConn, bool) { return nil, false }, &fakeEmitter{}, func() time.Time { return time.Now() }, time.Second)
	dto := svc.GetCertificateChain("gone", "ns", "n")
	if len(dto.Warnings) == 0 {
		t.Fatal("expected a warning on cluster miss")
	}
	if dto.Requests == nil || dto.Certificate.DNSNames == nil {
		t.Fatalf("want warning DTO arrays to be non-nil: %+v", dto)
	}
}

func TestCertManagerServiceOpenEmitsAndCloses(t *testing.T) {
	now := time.Now()
	conn := &fakeCertManagerConn{}
	em := &fakeEmitter{}
	svc := NewCertManagerService(func(string) (CertManagerConn, bool) { return conn, true }, em, func() time.Time { return now }, 10*time.Millisecond)

	svc.Open("nelli")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		em.mu.Lock()
		n := em.events
		em.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	em.mu.Lock()
	n := em.events
	em.mu.Unlock()
	if n == 0 {
		t.Fatal("expected at least one certmanager:updated emit")
	}
	if conn.opened == 0 {
		t.Fatal("expected OpenCertManager to be called")
	}
	svc.Close("nelli")
	if conn.closed == 0 {
		t.Fatal("expected CloseCertManager to be called")
	}
}

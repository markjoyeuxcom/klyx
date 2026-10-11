package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
)

// CertManagerConn is what CertManagerService needs: the on-demand issuance
// chain. The certificate and issuer lists are the health Observer's shared
// rows (1c slice 3: certs/certificates, certs/issuers) - no lens watch.
type CertManagerConn interface {
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
}

// KindCoverageDTO is one watched kind's evidence state (the External
// Secrets lens's payload; the cert-manager lens maps the Observer's
// per-kind coverage instead).
type KindCoverageDTO struct {
	Kind   string `json:"kind"`
	Served bool   `json:"served"`
	Synced bool   `json:"synced"`
	Error  string `json:"error,omitempty"`
}

// CertManagerService is bound to JS: the issuance-chain inspector's read.
type CertManagerService struct {
	lookup func(string) (CertManagerConn, bool)
	now    func() time.Time
}

func NewCertManagerService(lookup func(string) (CertManagerConn, bool), now func() time.Time) *CertManagerService {
	return &CertManagerService{lookup: lookup, now: now}
}

// GetCertificateChain resolves a certificate's issuance chain on demand (the
// inspector calls it when a cert is selected). Cluster-miss or resolve error
// degrades to a warnings-only DTO so the UI can show the message. The chain
// carries its certificate's UID and read time; the inspector shows it only
// for the selected row's UID.
func (s *CertManagerService) GetCertificateChain(cluster, ns, name string) CertChainDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return chainErrorDTO(ns, name, "cluster not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	chain, err := conn.GetCertificateChain(ctx, ns, name)
	if err != nil {
		return chainErrorDTO(ns, name, err.Error())
	}
	return toChainDTO(chain, s.now())
}

// chainErrorDTO is a failed chain read for the requested certificate: the
// request's key and the error, never an identity or chain evidence.
func chainErrorDTO(ns, name, msg string) CertChainDTO {
	return CertChainDTO{
		Error:       msg,
		Certificate: CertificateDTO{Namespace: ns, Name: name, DNSNames: []string{}},
		Requests:    []ChainNodeDTO{},
		Warnings:    []string{},
	}
}

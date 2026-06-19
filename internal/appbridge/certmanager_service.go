package appbridge

import (
	"context"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
)

// CertManagerConn is the per-cluster surface CertManagerService needs: the lazy
// watch (list) plus the on-demand chain resolver.
type CertManagerConn interface {
	OpenCertManager()
	CloseCertManager()
	CertManagerCertificates() []certmanager.Certificate
	CertManagerIssuers() []certmanager.Issuer
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
}

// CertManagerUpdatedEvent is emitted with { cluster, certificates, issuers }.
const CertManagerUpdatedEvent = "certmanager:updated"

type certManagerPayload struct {
	Cluster      string           `json:"cluster"`
	Certificates []CertificateDTO `json:"certificates"`
	Issuers      []IssuerDTO      `json:"issuers"`
}

// CertManagerService is bound to JS. Open starts a cluster's lazy cert-manager
// watch and pushes certmanager:updated on a tick; Close stops it. Mirrors
// GitOpsService exactly.
type CertManagerService struct {
	lookup   func(string) (CertManagerConn, bool)
	em       Emitter
	now      func() time.Time
	interval time.Duration

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func NewCertManagerService(lookup func(string) (CertManagerConn, bool), em Emitter, now func() time.Time, interval time.Duration) *CertManagerService {
	return &CertManagerService{lookup: lookup, em: em, now: now, interval: interval, cancels: map[string]context.CancelFunc{}}
}

func (s *CertManagerService) Open(cluster string) {
	conn, ok := s.lookup(cluster)
	if !ok {
		return
	}
	s.mu.Lock()
	if _, active := s.cancels[cluster]; active {
		s.mu.Unlock()
		return // idempotent
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancels[cluster] = cancel
	s.mu.Unlock()

	conn.OpenCertManager()
	go s.pushLoop(ctx, cluster, conn)
}

func (s *CertManagerService) Close(cluster string) {
	s.mu.Lock()
	cancel := s.cancels[cluster]
	delete(s.cancels, cluster)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn, ok := s.lookup(cluster); ok {
		conn.CloseCertManager()
	}
}

func (s *CertManagerService) pushLoop(ctx context.Context, cluster string, conn CertManagerConn) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.em.Emit(CertManagerUpdatedEvent, s.snapshot(cluster, conn))
		}
	}
}

// GetCertificateChain resolves a certificate's issuance chain on demand (the
// inspector calls it when a cert is selected). Cluster-miss or resolve error
// degrades to a warnings-only DTO so the UI can show the message.
func (s *CertManagerService) GetCertificateChain(cluster, ns, name string) CertChainDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return CertChainDTO{Warnings: []string{"cluster not connected"}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	chain, err := conn.GetCertificateChain(ctx, ns, name)
	if err != nil {
		return CertChainDTO{Warnings: []string{err.Error()}}
	}
	return toChainDTO(chain, s.now())
}

func (s *CertManagerService) snapshot(cluster string, conn CertManagerConn) certManagerPayload {
	now := s.now()
	certs := conn.CertManagerCertificates()
	certDTOs := make([]CertificateDTO, 0, len(certs))
	for _, c := range certs {
		certDTOs = append(certDTOs, toCertificateDTO(c, now))
	}
	iss := conn.CertManagerIssuers()
	issDTOs := make([]IssuerDTO, 0, len(iss))
	for _, i := range iss {
		issDTOs = append(issDTOs, toIssuerDTO(i))
	}
	return certManagerPayload{Cluster: cluster, Certificates: certDTOs, Issuers: issDTOs}
}

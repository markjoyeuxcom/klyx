package appbridge

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/certmanager"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/health"
)

// CertManagerConn is the per-cluster surface CertManagerService needs: the lazy
// watch (list) plus the on-demand chain resolver.
type CertManagerConn interface {
	OpenCertManager()
	CloseCertManager()
	CertManagerCertificates() []certmanager.Certificate
	CertManagerIssuers() []certmanager.Issuer
	// CertManagerWatchHealth reports informer sync + the last watch error, so a
	// failing watch renders as an error instead of an empty list.
	CertManagerWatchHealth() (synced bool, lastErr string)
	// CertManagerCoverage reports evidence per kind (Certificate, Issuer,
	// ClusterIssuer): served / synced / current error.
	CertManagerCoverage() []fleet.KindCoverage
	GetCertificateChain(ctx context.Context, ns, name string) (certmanager.Chain, error)
}

// CertManagerUpdatedEvent is emitted with { cluster, certificates, issuers }.
const CertManagerUpdatedEvent = "certmanager:updated"

type certManagerPayload struct {
	Cluster      string           `json:"cluster"`
	Certificates []CertificateDTO `json:"certificates"`
	Issuers      []IssuerDTO      `json:"issuers"`
	// WatchError carries every CURRENT watch failure of any kind - before the
	// initial sync, or after it (the view is then showing last-known state).
	// The view must show the failure, never an empty or quietly stale list.
	WatchError string `json:"watchError,omitempty"`
	// Coverage is per kind, so a synced Certificate list never vouches for a
	// pending or forbidden Issuer/ClusterIssuer watch (or the reverse).
	Coverage []KindCoverageDTO `json:"coverage"`
}

// KindCoverageDTO is one watched kind's evidence state.
type KindCoverageDTO struct {
	Kind   string `json:"kind"`
	Served bool   `json:"served"`
	Synced bool   `json:"synced"`
	Error  string `json:"error,omitempty"`
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

	// Re-check after the (slow, discovery-bound) open: a Close that landed in
	// between already cancelled our entry, and the watch it closed at the conn
	// may have been re-opened by the line above - tear it down again so no
	// consumer-less watch survives.
	s.mu.Lock()
	_, stillActive := s.cancels[cluster]
	s.mu.Unlock()
	if !stillActive {
		conn.CloseCertManager()
		return
	}
	safeGo("certmanager-push:"+cluster, func() { s.pushLoop(ctx, cluster, conn) })
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

// certmanagerDomainSnap is the change-detection unit for the cert-manager push
// loop: parsed domain values (absolute timestamps), not the DTOs (relative
// expiry seconds - different every tick). Same pattern as the gitops loop.
type certmanagerDomainSnap struct {
	certs    []certmanager.Certificate
	issuers  []certmanager.Issuer
	watchErr string
	coverage []fleet.KindCoverage
	// certRules: each certificate's rule at this tick. Expiry changes the
	// rule (Ready -> Expired, included) with no object change, so a
	// boundary crossing emits at the next tick, never only at heartbeat.
	certRules []health.NativeEval
}

func (s *CertManagerService) pushLoop(ctx context.Context, cluster string, conn CertManagerConn) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	var last *certmanagerDomainSnap
	var lastEmit time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := s.now()
			// every CURRENT failure, synced or not (a failure after the initial
			// sync used to be suppressed - the view then showed stale state as
			// if current)
			_, watchErr := conn.CertManagerWatchHealth()
			snap := certmanagerDomainSnap{certs: conn.CertManagerCertificates(), issuers: conn.CertManagerIssuers(), watchErr: watchErr, coverage: conn.CertManagerCoverage()}
			for _, c := range snap.certs {
				snap.certRules = append(snap.certRules, certmanager.CertificateRule(c, now))
			}
			if last != nil && now.Sub(lastEmit) < pushHeartbeat && reflect.DeepEqual(*last, snap) {
				continue
			}
			last = &snap
			lastEmit = now
			s.em.Emit(CertManagerUpdatedEvent, s.buildPayload(cluster, snap, now))
		}
	}
}

// GetCertificateChain resolves a certificate's issuance chain on demand (the
// inspector calls it when a cert is selected). Cluster-miss or resolve error
// degrades to a warnings-only DTO so the UI can show the message.
func (s *CertManagerService) GetCertificateChain(cluster, ns, name string) CertChainDTO {
	conn, ok := s.lookup(cluster)
	if !ok {
		return warningCertChainDTO("cluster not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	chain, err := conn.GetCertificateChain(ctx, ns, name)
	if err != nil {
		return warningCertChainDTO(err.Error())
	}
	return toChainDTO(chain, s.now())
}

func warningCertChainDTO(warning string) CertChainDTO {
	return CertChainDTO{
		Certificate: CertificateDTO{DNSNames: []string{}},
		Requests:    []ChainNodeDTO{},
		Warnings:    []string{warning},
	}
}

func (s *CertManagerService) buildPayload(cluster string, snap certmanagerDomainSnap, now time.Time) certManagerPayload {
	certDTOs := make([]CertificateDTO, 0, len(snap.certs))
	for _, c := range snap.certs {
		certDTOs = append(certDTOs, toCertificateDTO(c, now))
	}
	issDTOs := make([]IssuerDTO, 0, len(snap.issuers))
	for _, i := range snap.issuers {
		issDTOs = append(issDTOs, toIssuerDTO(i))
	}
	cov := make([]KindCoverageDTO, 0, len(snap.coverage))
	for _, k := range snap.coverage {
		cov = append(cov, KindCoverageDTO{Kind: k.Kind, Served: k.Served, Synced: k.Synced, Error: k.Error})
	}
	return certManagerPayload{Cluster: cluster, Certificates: certDTOs, Issuers: issDTOs, WatchError: snap.watchErr, Coverage: cov}
}

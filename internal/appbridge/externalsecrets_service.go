package appbridge

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/externalsecrets"
	"github.com/moomora/klyx/internal/fleet"
)

// ExternalSecretsConn is the per-cluster surface ExternalSecretsService needs:
// the lazy watch (list) over ExternalSecrets and their stores.
type ExternalSecretsConn interface {
	OpenExternalSecrets()
	CloseExternalSecrets()
	ExternalSecretsList() []externalsecrets.ExternalSecret
	ExternalSecretsStores() []externalsecrets.Store
	// ExternalSecretsWatchHealth reports informer sync + the last watch error,
	// so a failing watch renders as an error instead of an empty list.
	ExternalSecretsWatchHealth() (synced bool, lastErr string)
	// ExternalSecretsCoverage reports evidence per kind (ExternalSecret,
	// SecretStore, ClusterSecretStore): served / synced / current error.
	ExternalSecretsCoverage() []fleet.KindCoverage
}

// ExternalSecretsUpdatedEvent is emitted with { cluster, secrets, stores }.
const ExternalSecretsUpdatedEvent = "externalsecrets:updated"

type esoPayload struct {
	Cluster string              `json:"cluster"`
	Secrets []ExternalSecretDTO `json:"secrets"`
	Stores  []StoreDTO          `json:"stores"`
	// WatchError is set while the watch has not synced AND has recorded an
	// error - the view must show the failure, not an empty list.
	WatchError string `json:"watchError,omitempty"`
	// Coverage is per kind: a synced ExternalSecret list never vouches for a
	// pending or forbidden (Cluster)SecretStore watch, or the reverse.
	Coverage []KindCoverageDTO `json:"coverage"`
}

// ExternalSecretsService is bound to JS. Open starts a cluster's lazy ESO
// watch and pushes externalsecrets:updated on a tick; Close stops it. Mirrors
// CertManagerService exactly.
type ExternalSecretsService struct {
	lookup   func(string) (ExternalSecretsConn, bool)
	em       Emitter
	now      func() time.Time
	interval time.Duration

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func NewExternalSecretsService(lookup func(string) (ExternalSecretsConn, bool), em Emitter, now func() time.Time, interval time.Duration) *ExternalSecretsService {
	return &ExternalSecretsService{lookup: lookup, em: em, now: now, interval: interval, cancels: map[string]context.CancelFunc{}}
}

func (s *ExternalSecretsService) Open(cluster string) {
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

	conn.OpenExternalSecrets()

	// Re-check after the (slow, discovery-bound) open: a Close that landed in
	// between already cancelled our entry, and the watch it closed at the conn
	// may have been re-opened by the line above - tear it down again so no
	// consumer-less watch survives.
	s.mu.Lock()
	_, stillActive := s.cancels[cluster]
	s.mu.Unlock()
	if !stillActive {
		conn.CloseExternalSecrets()
		return
	}
	safeGo("externalsecrets-push:"+cluster, func() { s.pushLoop(ctx, cluster, conn) })
}

func (s *ExternalSecretsService) Close(cluster string) {
	s.mu.Lock()
	cancel := s.cancels[cluster]
	delete(s.cancels, cluster)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn, ok := s.lookup(cluster); ok {
		conn.CloseExternalSecrets()
	}
}

// esoDomainSnap is the change-detection unit for the ESO push loop: parsed
// domain values (absolute timestamps), not the DTOs (relative refresh ages -
// different every tick). Same pattern as the cert-manager loop.
type esoDomainSnap struct {
	secrets  []externalsecrets.ExternalSecret
	stores   []externalsecrets.Store
	watchErr string
	coverage []fleet.KindCoverage
}

func (s *ExternalSecretsService) pushLoop(ctx context.Context, cluster string, conn ExternalSecretsConn) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	var last *esoDomainSnap
	var lastEmit time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := s.now()
			// every CURRENT failure, synced or not (after-sync failures used to
			// be suppressed, so stale rows read as current)
			_, watchErr := conn.ExternalSecretsWatchHealth()
			snap := esoDomainSnap{secrets: conn.ExternalSecretsList(), stores: conn.ExternalSecretsStores(), watchErr: watchErr, coverage: conn.ExternalSecretsCoverage()}
			if last != nil && now.Sub(lastEmit) < pushHeartbeat && reflect.DeepEqual(*last, snap) {
				continue
			}
			last = &snap
			lastEmit = now
			s.em.Emit(ExternalSecretsUpdatedEvent, s.buildPayload(cluster, snap, now))
		}
	}
}

func (s *ExternalSecretsService) buildPayload(cluster string, snap esoDomainSnap, now time.Time) esoPayload {
	secretDTOs := make([]ExternalSecretDTO, 0, len(snap.secrets))
	for _, e := range snap.secrets {
		secretDTOs = append(secretDTOs, toExternalSecretDTO(e, now))
	}
	storeDTOs := make([]StoreDTO, 0, len(snap.stores))
	for _, st := range snap.stores {
		storeDTOs = append(storeDTOs, toStoreDTO(st))
	}
	cov := make([]KindCoverageDTO, 0, len(snap.coverage))
	for _, k := range snap.coverage {
		cov = append(cov, KindCoverageDTO{Kind: k.Kind, Served: k.Served, Synced: k.Synced, Error: k.Error})
	}
	return esoPayload{Cluster: cluster, Secrets: secretDTOs, Stores: storeDTOs, WatchError: snap.watchErr, Coverage: cov}
}

package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/externalsecrets"
)

type fakeExternalSecretsConn struct {
	opened, closed int
	secrets        []externalsecrets.ExternalSecret
	stores         []externalsecrets.Store
}

func (f *fakeExternalSecretsConn) OpenExternalSecrets()  { f.opened++ }
func (f *fakeExternalSecretsConn) CloseExternalSecrets() { f.closed++ }
func (f *fakeExternalSecretsConn) ExternalSecretsList() []externalsecrets.ExternalSecret {
	return f.secrets
}
func (f *fakeExternalSecretsConn) ExternalSecretsStores() []externalsecrets.Store { return f.stores }
func (f *fakeExternalSecretsConn) ExternalSecretsWatchHealth() (bool, string)     { return true, "" }

func TestToExternalSecretDTOComputesRefreshAge(t *testing.T) {
	now := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	refreshed := now.Add(-3 * time.Hour)
	dto := toExternalSecretDTO(externalsecrets.ExternalSecret{
		Namespace: "payments", Name: "stripe-api", State: externalsecrets.Synced, Reason: "SecretSynced",
		StoreKind: externalsecrets.ClusterSecretStoreKind, StoreName: "azure-kv", TargetSecret: "stripe-api-secret",
		RefreshInterval: time.Hour, RefreshTime: refreshed, Stale: true,
	}, now)
	if dto.State != "Synced" || dto.Reason != "SecretSynced" {
		t.Fatalf("status: %+v", dto)
	}
	if dto.StoreKind != "ClusterSecretStore" || dto.StoreName != "azure-kv" || dto.TargetSecret != "stripe-api-secret" {
		t.Fatalf("store/target: %+v", dto)
	}
	if dto.RefreshIntervalSeconds != 3600 || dto.LastRefreshAgeSeconds != int64(3*3600) {
		t.Fatalf("refresh: interval=%d age=%d", dto.RefreshIntervalSeconds, dto.LastRefreshAgeSeconds)
	}
	if !dto.Stale {
		t.Fatalf("want stale carried through: %+v", dto)
	}
}

func TestToExternalSecretDTONoAgeWhenNeverSynced(t *testing.T) {
	dto := toExternalSecretDTO(externalsecrets.ExternalSecret{Name: "x", RefreshInterval: time.Hour}, time.Now())
	if dto.LastRefreshAgeSeconds != 0 {
		t.Fatalf("want zero refresh age for a never-synced secret: %+v", dto)
	}
	if dto.Stale {
		t.Fatalf("want not stale when never synced: %+v", dto)
	}
}

func TestExternalSecretsServiceSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	conn := &fakeExternalSecretsConn{
		secrets: []externalsecrets.ExternalSecret{{Namespace: "payments", Name: "stripe-api", State: externalsecrets.Failed}},
		stores:  []externalsecrets.Store{{Kind: externalsecrets.ClusterSecretStoreKind, Name: "azure-kv", State: externalsecrets.Failed, Provider: "azurekv"}},
	}
	svc := NewExternalSecretsService(func(string) (ExternalSecretsConn, bool) { return conn, true }, &fakeEmitter{}, func() time.Time { return now }, time.Second)
	snap := esoDomainSnap{secrets: conn.ExternalSecretsList(), stores: conn.ExternalSecretsStores()}
	p := svc.buildPayload("nelli", snap, now)
	if p.Cluster != "nelli" || len(p.Secrets) != 1 || len(p.Stores) != 1 {
		t.Fatalf("payload: %+v", p)
	}
	if p.Stores[0].Kind != "ClusterSecretStore" || p.Stores[0].State != "Failed" || p.Stores[0].Provider != "azurekv" {
		t.Fatalf("store dto: %+v", p.Stores[0])
	}
}

func TestExternalSecretsServiceOpenEmitsAndCloses(t *testing.T) {
	now := time.Now()
	conn := &fakeExternalSecretsConn{}
	em := &fakeEmitter{}
	svc := NewExternalSecretsService(func(string) (ExternalSecretsConn, bool) { return conn, true }, em, func() time.Time { return now }, 10*time.Millisecond)

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
		t.Fatal("expected at least one externalsecrets:updated emit")
	}
	if conn.opened == 0 {
		t.Fatal("expected OpenExternalSecrets to be called")
	}
	svc.Close("nelli")
	if conn.closed == 0 {
		t.Fatal("expected CloseExternalSecrets to be called")
	}
}

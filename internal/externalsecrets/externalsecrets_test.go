package externalsecrets

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func es(spec, status map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "external-secrets.io/v1",
		"kind":       "ExternalSecret",
		"metadata":   map[string]interface{}{"name": "db-creds", "namespace": "payments"},
		"spec":       spec,
		"status":     status,
	}}
}

func ready(status, reason, message string) map[string]interface{} {
	return map[string]interface{}{
		"conditions": []interface{}{
			map[string]interface{}{"type": "Ready", "status": status, "reason": reason, "message": message},
		},
	}
}

var now = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

func TestParseExternalSecretSynced(t *testing.T) {
	st := ready("True", "SecretSynced", "secret synced")
	st["refreshTime"] = now.Add(-10 * time.Minute).Format(time.RFC3339)
	e := ParseExternalSecret(es(map[string]interface{}{
		"refreshInterval": "1h",
		"secretStoreRef":  map[string]interface{}{"name": "azure-kv", "kind": "ClusterSecretStore"},
		"target":          map[string]interface{}{"name": "db-creds-secret"},
	}, st), now)
	if e.State != Synced || e.Reason != "SecretSynced" {
		t.Fatalf("state: %+v", e)
	}
	if e.StoreKind != ClusterSecretStoreKind || e.StoreName != "azure-kv" {
		t.Fatalf("store ref: %+v", e)
	}
	if e.TargetSecret != "db-creds-secret" || e.Stale {
		t.Fatalf("target/stale: %+v", e)
	}
}

func TestParseExternalSecretDefaults(t *testing.T) {
	// Unset store kind defaults to SecretStore; unset target defaults to the
	// ExternalSecret's own name; unset refreshInterval defaults to 1h.
	e := ParseExternalSecret(es(map[string]interface{}{
		"secretStoreRef": map[string]interface{}{"name": "vault"},
	}, ready("True", "SecretSynced", "")), now)
	if e.StoreKind != SecretStoreKind || e.TargetSecret != "db-creds" {
		t.Fatalf("defaults: %+v", e)
	}
	if e.RefreshInterval != time.Hour {
		t.Fatalf("default interval: %v", e.RefreshInterval)
	}
}

func TestParseExternalSecretFailed(t *testing.T) {
	e := ParseExternalSecret(es(map[string]interface{}{}, ready("False", "SecretSyncedError", "could not get secret from provider")), now)
	if e.State != Failed || e.Reason != "SecretSyncedError" || e.Message == "" {
		t.Fatalf("failed: %+v", e)
	}
}

func TestParseExternalSecretNoConditionsIsUnknown(t *testing.T) {
	if e := ParseExternalSecret(es(map[string]interface{}{}, map[string]interface{}{}), now); e.State != Unknown {
		t.Fatalf("want Unknown, got %+v", e)
	}
}

func TestRefreshStaleness(t *testing.T) {
	cases := []struct {
		name        string
		interval    string // "" = absent
		refreshedAt time.Duration
		wantStale   bool
	}{
		// Within the interval: fresh.
		{"fresh", "1h", -30 * time.Minute, false},
		// Between 1x and 2x: one missed cycle tolerated (controller restart).
		{"one missed cycle", "1h", -90 * time.Minute, false},
		// Past 2x: the controller has stopped refreshing.
		{"stale", "1h", -3 * time.Hour, true},
		// Explicit 0 disables refresh - never stale by time.
		{"refresh disabled", "0", -100 * time.Hour, false},
		// Absent interval uses ESO's 1h default.
		{"default interval stale", "", -3 * time.Hour, true},
	}
	for _, tc := range cases {
		spec := map[string]interface{}{}
		if tc.interval != "" {
			spec["refreshInterval"] = tc.interval
		}
		st := ready("True", "SecretSynced", "")
		st["refreshTime"] = now.Add(tc.refreshedAt).Format(time.RFC3339)
		e := ParseExternalSecret(es(spec, st), now)
		if e.Stale != tc.wantStale {
			t.Errorf("%s: Stale=%v, want %v (%+v)", tc.name, e.Stale, tc.wantStale, e)
		}
	}
}

func TestNeverSyncedIsNotStale(t *testing.T) {
	// No refreshTime: the state (Failed/Unknown) is the loud signal; a
	// time-based stale flag on a never-synced secret would be noise.
	e := ParseExternalSecret(es(map[string]interface{}{"refreshInterval": "1h"}, ready("False", "SecretSyncedError", "x")), now)
	if e.Stale {
		t.Fatalf("never-synced must not be time-stale: %+v", e)
	}
}

func TestParseStore(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "external-secrets.io/v1",
		"kind":       "ClusterSecretStore",
		"metadata":   map[string]interface{}{"name": "azure-kv"},
		"spec": map[string]interface{}{
			"provider": map[string]interface{}{
				"azurekv": map[string]interface{}{"vaultUrl": "https://kv.vault.azure.net"},
			},
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True", "reason": "Valid", "message": "store validated"},
			},
		},
	}}
	s := ParseStore(u)
	if s.Kind != ClusterSecretStoreKind || s.Namespace != "" || s.Name != "azure-kv" {
		t.Fatalf("identity: %+v", s)
	}
	if s.State != Synced || s.Reason != "Valid" || s.Provider != "azurekv" {
		t.Fatalf("state/provider: %+v", s)
	}
}

func TestParseStoreInvalid(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "external-secrets.io/v1",
		"kind":       "SecretStore",
		"metadata":   map[string]interface{}{"name": "vault", "namespace": "payments"},
		"spec":       map[string]interface{}{"provider": map[string]interface{}{"vault": map[string]interface{}{}}},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "False", "reason": "InvalidProviderConfig", "message": "unable to log in"},
			},
		},
	}}
	s := ParseStore(u)
	if s.State != Failed || s.Reason != "InvalidProviderConfig" || s.Provider != "vault" {
		t.Fatalf("invalid store: %+v", s)
	}
}

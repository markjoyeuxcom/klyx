package externalsecrets

import (
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// The Line §8.1: the parser keeps the Ready condition's lastTransitionTime.
func TestParseKeepsReadySince(t *testing.T) {
	st := ready("True", "SecretSynced", "synced")
	st["conditions"].([]interface{})[0].(map[string]interface{})["lastTransitionTime"] = "2026-07-03T09:00:00Z"
	e := ParseExternalSecret(es(map[string]interface{}{}, st), now)
	if !e.ReadySince.Equal(time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("ready since %v", e.ReadySince)
	}
	if e := ParseExternalSecret(es(map[string]interface{}{}, ready("True", "SecretSynced", "")), now); !e.ReadySince.IsZero() {
		t.Fatalf("no lastTransitionTime reads as absent, got %v", e.ReadySince)
	}
}

func TestExternalSecretRuleSinceAndReason(t *testing.T) {
	at := now.Add(-5 * time.Hour)
	refreshed := now.Add(-3 * time.Hour)

	failed := ExternalSecret{State: Failed, Reason: "SecretSyncedError", Message: "could not get secret", ReadySince: at, RefreshInterval: time.Hour}
	if e := ExternalSecretRule(failed); e.Since == nil || !e.Since.Equal(at) || e.Reason != "SecretSyncedError: could not get secret" {
		t.Errorf("failed: dated by Ready, got %+v", e)
	}

	stale := ExternalSecret{State: Synced, Stale: true, RefreshInterval: time.Hour, RefreshTime: refreshed, ReadySince: at}
	e := ExternalSecretRule(stale)
	if e.Role != health.Degraded || e.Since == nil || !e.Since.Equal(stale.StaleAt()) || !strings.HasPrefix(e.Reason, "refresh overdue: last refresh ") {
		t.Errorf("stale is derived from refreshTime + 2x interval, got %+v", e)
	}
	stale.ReadySince = now.Add(-time.Minute) // Ready turned True after the stale instant
	if e := ExternalSecretRule(stale); e.Since == nil || !e.Since.Equal(stale.ReadySince) {
		t.Errorf("stale: the later Ready time dates the role, got %+v", e)
	}

	periodic := ExternalSecret{State: Synced, RefreshInterval: time.Hour, RefreshTime: now.Add(-10 * time.Minute), ReadySince: at}
	if e := ExternalSecretRule(periodic); e.Since != nil || e.Reason != "" {
		t.Errorf("a periodic synced secret may have been stale in between: no Since, got %+v", e)
	}
	once := ExternalSecret{State: Synced, RefreshPolicy: "CreatedOnce", RefreshInterval: time.Hour, ReadySince: at}
	if e := ExternalSecretRule(once); e.Since == nil || !e.Since.Equal(at) || e.Reason != "" {
		t.Errorf("a non-periodic synced secret never goes stale: dated by Ready, got %+v", e)
	}
	if e := ExternalSecretRule(ExternalSecret{State: Unknown}); e.Since != nil {
		t.Errorf("no Ready condition: no Since, got %+v", e)
	}
}

func TestStoreRuleSinceAndReason(t *testing.T) {
	at := now.Add(-time.Hour)
	if e := StoreRule(Store{State: Failed, Reason: "InvalidProviderConfig", Message: "bad", ReadySince: at}); e.Since == nil || !e.Since.Equal(at) || e.Reason != "InvalidProviderConfig: bad" {
		t.Errorf("failed store, got %+v", e)
	}
	if e := StoreRule(Store{State: Synced, ReadySince: at}); e.Since == nil || e.Reason != "" {
		t.Errorf("ready store: dated, no reason, got %+v", e)
	}
}

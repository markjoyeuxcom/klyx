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

	stale := ExternalSecret{State: Synced, Stale: true, RefreshInterval: time.Hour, RefreshTime: refreshed, ReadySince: at, Generation: 1}
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
	once := ExternalSecret{State: Synced, RefreshPolicy: "CreatedOnce", RefreshInterval: time.Hour, ReadySince: at, Generation: 1}
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

// Codex review of 75b3990: the stale threshold is computed from the current
// interval and policy, so only a never-edited spec lets it date the role.
func TestExternalSecretSinceNeedsAnUneditedSpec(t *testing.T) {
	// OK at 12:00 with a 2h interval and refreshTime 09:00; the interval is
	// shortened to 1h at 12:01 (generation 2), so the secret is stale from
	// 12:01, not 11:00 - and nothing on the object says when it changed.
	stale := ExternalSecret{State: Synced, Stale: true, RefreshInterval: time.Hour, RefreshTime: now.Add(-3 * time.Hour),
		ReadySince: now.Add(-5 * time.Hour), Generation: 2}
	if e := ExternalSecretRule(stale); e.Role != health.Degraded || e.Since != nil || e.Reason == "" {
		t.Errorf("an edited spec: undated, reason kept: %+v", e)
	}
	stale.Generation = 1
	if e := ExternalSecretRule(stale); e.Since == nil || !e.Since.Equal(stale.StaleAt()) {
		t.Errorf("a never-edited spec: dated by the threshold: %+v", e)
	}

	// A stale periodic secret switched to CreatedOnce is OK from the switch,
	// not from Ready's old time.
	once := ExternalSecret{State: Synced, RefreshPolicy: "CreatedOnce", RefreshInterval: time.Hour, ReadySince: now.Add(-5 * time.Hour), Generation: 3}
	if e := ExternalSecretRule(once); e.Since != nil || e.Cond == nil || e.Cond.Status != "True" {
		t.Errorf("an edited non-periodic secret: undated, Ready's age as evidence: %+v", e)
	}

	// Failed is a function of Ready alone: dated at any generation.
	failed := ExternalSecret{State: Failed, Reason: "SecretSyncedError", ReadySince: now.Add(-time.Hour), Generation: 7}
	if e := ExternalSecretRule(failed); e.Since == nil || e.Cond == nil || e.Cond.Status != "False" {
		t.Errorf("failed: dated by Ready at any generation: %+v", e)
	}
}

// Codex review of 75b3990: a missing or invalid time leaves Since nil but
// never drops a known failure reason.
func TestExternalSecretReasonWithoutTime(t *testing.T) {
	for name, lt := range map[string]any{"missing": nil, "invalid": "yesterday"} {
		st := ready("False", "SecretSyncedError", "provider unavailable")
		if lt != nil {
			st["conditions"].([]interface{})[0].(map[string]interface{})["lastTransitionTime"] = lt
		}
		e := ExternalSecretRule(ParseExternalSecret(es(map[string]interface{}{}, st), now))
		if e.Role != health.Fault || e.Since != nil || e.Reason != "SecretSyncedError: provider unavailable" {
			t.Errorf("%s time: got %+v", name, e)
		}
	}
}

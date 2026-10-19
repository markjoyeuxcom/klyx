package appbridge

import (
	"github.com/moomora/klyx/internal/healthsrc"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
	"testing"
	"time"
)

func TestReviewS2aLocationDisplayURLs(t *testing.T) {
	u := s2aObj("BackupStorageLocation", "default", time.Now().Add(-time.Hour), nil,
		map[string]any{"provider": "aws", "objectStorage": map[string]any{"bucket": "backups", "prefix": "https://user:S2aReviewNeedle@store/path"}, "config": map[string]any{"region": "https://user:S2aReviewNeedle@store/path"}},
		map[string]any{"phase": "Available"})
	out := evaluateVelero(t, "locations", map[string][]*unstructured.Unstructured{healthsrc.KindVeleroLocations: {u}})
	raw := rawJSONOf(t, out)
	if strings.Contains(raw, "S2aReviewNeedle") {
		t.Fatalf("credential crossed bridge: %s", raw)
	}
}

// A cron is kept raw for the parser and passes the URL rule wherever it is
// shown: the row and Klyx's own parse error, which echoes it.
func TestReviewS2aCronDisplayURLs(t *testing.T) {
	u := s2aObj("Schedule", "odd", time.Now().Add(-time.Hour), nil,
		map[string]any{"schedule": "https://user:S2aReviewNeedle@store/path 2 * *", "template": map[string]any{}},
		map[string]any{"phase": "FailedValidation", "validationErrors": []any{"invalid schedule: https://user:S2aReviewNeedle@store/path"}})
	out := evaluateVelero(t, "schedules", map[string][]*unstructured.Unstructured{healthsrc.KindVeleroSchedules: {u}})
	if raw := rawJSONOf(t, out); strings.Contains(raw, "S2aReviewNeedle") {
		t.Fatalf("credential crossed the bridge: %s", raw)
	}
}

package crd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewS2aForgedPlaceholderCannotCarryText(t *testing.T) {
	d := veleroDoc("BackupStorageLocation", nil, map[string]any{"message": "<omitted> × S2aReviewMessageNeedle"})
	s := SanitizeEmbedded("velero.io", "BackupStorageLocation", d)
	b, _ := json.Marshal(s.Object)
	if strings.Contains(string(b), "S2aReviewMessageNeedle") {
		t.Fatalf("unclassified message crossed: %s; payload=%v describe=%s edit=%v", b, s.Payload, s.DescribeMode, s.EditEligible)
	}
}

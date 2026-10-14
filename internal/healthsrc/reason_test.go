package healthsrc

import (
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
)

// Codex review of b547e60: a summary Top ref's reason is scrubbed before it
// is capped - it feeds the queue, the log and notifications.
func TestTopRefReasonIsScrubbed(t *testing.T) {
	src := flux.Source{Ready: flux.Failed, Reason: "GitOperationFailed",
		Message: "failed to clone https://alice:review-password@repo.invalid/a: token=review-credential rejected"}
	res := nativeResult("x", "flux", "sources", []nativeItem{{eval: flux.SourceRule(src), ref: refInput{ns: "flux-system", name: "fleet", uid: "u1"}}})
	if len(res.Top) != 1 {
		t.Fatalf("top: %+v", res.Top)
	}
	r := res.Top[0].Reason
	for _, leak := range []string{"alice", "review-password", "review-credential"} {
		if strings.Contains(r, leak) {
			t.Fatalf("%q reached the summary: %s", leak, r)
		}
	}
	if !strings.HasPrefix(r, "GitOperationFailed: failed to clone https://[redacted]@repo.invalid/a") {
		t.Fatalf("reason: %s", r)
	}
}

// A secret crossing the 200-rune cap is replaced whole, never cut into a
// readable prefix.
func TestReasonCapNeverCutsASecret(t *testing.T) {
	msg := strings.Repeat("x", 185) + " token=review-credential-0123456789"
	r := health.CapReason("Failed", msg)
	if strings.Contains(r, "review-cr") || len([]rune(r)) > health.ReasonMax {
		t.Fatalf("cut secret or over cap (%d): %s", len([]rune(r)), r)
	}
}

// A Top ref carries the rule's condition evidence apart from its role start:
// a Flux failure has Ready's age but no proven Since (The Line §8.1).
func TestTopRefCarriesConditionEvidence(t *testing.T) {
	at := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	src := flux.Source{Ready: flux.Failed, Reason: "GitOperationFailed", ReadySince: at}
	res := nativeResult("x", "flux", "sources", []nativeItem{{eval: flux.SourceRule(src), ref: refInput{ns: "flux-system", name: "fleet", uid: "u1"}}})
	if len(res.Top) != 1 {
		t.Fatalf("top: %+v", res.Top)
	}
	r := res.Top[0]
	if r.Since != nil || r.Condition == nil || *r.Condition != (observe.Condition{Type: "Ready", Status: "False", Since: at}) {
		t.Fatalf("ref: since %v, condition %+v", r.Since, r.Condition)
	}
}

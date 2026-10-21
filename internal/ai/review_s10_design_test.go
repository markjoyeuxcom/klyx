package ai

import (
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/urlscrub"
)

// Codex's S10 design review (cd00d87) demonstrated that the generic URL and
// credential scrubbing kept an ARM ID from a provider condition; with the
// identifier redactor in ScrubEvidence it is now a regression (S10 §8).
func TestReviewS10DesignConditionIDExclusion(t *testing.T) {
	id := "/subscriptions/11111111-1111-1111-1111-111111111111/resourceGroups/S10PrivateNeedle/providers/Microsoft.Network/virtualNetworks/v/subnets/s"
	msg := "resource not found: " + id
	shown := urlscrub.Text(msg).Text
	got, _ := ScrubEvidence(shown, 500)
	if strings.Contains(got, "S10PrivateNeedle") {
		t.Fatalf("generic URL/credential scrubbing retains the ARM ID from a provider condition: %s", got)
	}
}

package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// The HealthDTO is the 1b frontend contract: kind, label, message and rule
// must survive the bridge, and an unsupported kind must never read healthy.
func TestHealthDTOContract(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	d := toHealthDTO(health.Verdict{Kind: health.Fault, Label: "Exited 1", Reason: "Exited", Message: "worker exited 1 (restarted 6 times)",
		Native: "phase Running · Ready=False", Note: "", Rule: "pod.current-failure", Rank: 35, ReclassifyAt: &at})
	want := HealthDTO{Kind: "fault", Label: "Exited 1", Reason: "Exited", Message: "worker exited 1 (restarted 6 times)",
		Native: "phase Running · Ready=False", Rule: "pod.current-failure", Rank: 35, ReclassifyAtUnix: 1_800_000_000}
	if d != want {
		t.Fatalf("got %+v\nwant %+v", d, want)
	}
	if got := toHealthDTO(health.Verdict{Kind: health.Kind(42)}).Kind; got != "unknown" {
		t.Fatalf("unsupported kind serialised as %q", got)
	}
}

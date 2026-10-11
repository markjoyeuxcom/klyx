package helmcli

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/health"
)

// Next is the first instant the verdict changes with no new data: the
// rule holds just before it and has moved at it.
func TestReleaseRuleNextIsTheExactStep(t *testing.T) {
	updated := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	r := Release{Status: "pending-upgrade", UpdatedUnix: updated.Unix()}
	e := ReleaseRule(r, updated.Add(time.Minute))
	if e.Role != health.Progressing || e.Included || e.Next == nil {
		t.Fatalf("within the deadline: %+v", e)
	}
	if !e.Next.Equal(updated.Add(PendingDeadline + time.Second)) {
		t.Fatalf("Next = %v, want updated + 601s", e.Next)
	}
	if before := ReleaseRule(r, e.Next.Add(-time.Nanosecond)); before.Role != health.Progressing {
		t.Fatalf("1ns before Next: %+v", before)
	}
	at := ReleaseRule(r, *e.Next)
	if at.Role != health.Degraded || !at.Included || at.Label != "pending-upgrade, appears stuck" || at.Next != nil {
		t.Fatalf("at Next: %+v", at)
	}
	if e := ReleaseRule(Release{Status: "pending-install"}, updated); e.Next != nil {
		t.Fatalf("no updated time has no boundary: %+v", e)
	}
}

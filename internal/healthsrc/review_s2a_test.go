package healthsrc

import (
	"github.com/moomora/klyx/internal/velero"
	"testing"
	"time"
)

func TestReviewS2aDefaultLocationNeedsCompleteCandidates(t *testing.T) {
	now := time.Now()
	s := &velero.Schedule{Meta: velero.Meta{Namespace: "velero", Name: "daily"}}
	retained := &velero.Location{Meta: velero.Meta{Namespace: "velero", Name: "a", Created: now.Add(-time.Hour)}, Default: true, Phase: "Available"}
	servers := velero.Servers{Read: true, ByNamespace: map[string]velero.ServerFacts{"velero": {}}}
	link := locationLink(s, []*velero.Location{retained}, "incomplete", servers, now)
	if link.State == "found" {
		t.Fatalf("default claimed from partial candidates; unseen z can be newer: %+v", link)
	}
}

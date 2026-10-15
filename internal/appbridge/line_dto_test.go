package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/line"
)

// The Line slice 5 note §5.1 (design review d25f7275): comparedAt dates the
// compared revision only - the artifact time read from the same source
// object - and never an applied revision the source has moved past.
func TestLineUnitComparedAtDatesOnlyTheComparedRevision(t *testing.T) {
	key := line.Key{Kind: line.KeyGit, Repo: "github.com/acme/fleet", RefKind: "branch", RefName: "main"}
	at := time.Date(2026, 10, 15, 9, 41, 0, 0, time.UTC)
	sl := line.StationLine{Placed: line.Placed{Station: line.Station{Cluster: "c1", Env: "dev"}}, Units: []line.Unit{
		{Origin: "Kustomization", Name: "moved", Key: key, Applied: &line.Revision{SHA: "aaaaaaa"}, Compared: &line.Revision{SHA: "bbbbbbb"}, ArtifactTime: at},
		{Origin: "Kustomization", Name: "no-compared", Key: key, Applied: &line.Revision{SHA: "aaaaaaa"}, ArtifactTime: at},
		{Origin: "Kustomization", Name: "no-time", Key: key, Compared: &line.Revision{SHA: "bbbbbbb"}},
	}}
	d := stationDTO(sl, &key)
	got := map[string]*time.Time{}
	for _, u := range d.Units {
		got[u.Name] = u.ComparedAt
	}
	if got["moved"] == nil || !got["moved"].Equal(at) || d.Units[0].Compared != "bbbbbbb" || d.Units[0].Applied != "aaaaaaa" {
		t.Fatalf("moved: comparedAt %v must date compared bbbbbbb, beside applied aaaaaaa: %+v", got["moved"], d.Units[0])
	}
	if got["no-compared"] != nil {
		t.Fatalf("an artifact time with no compared revision dates nothing, got %v", got["no-compared"])
	}
	if got["no-time"] != nil {
		t.Fatalf("no artifact time, no comparedAt, got %v", got["no-time"])
	}
}

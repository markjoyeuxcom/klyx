package line

import (
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/clustermesh"
)

func TestEnvRank(t *testing.T) {
	for env, want := range map[string]int{
		"dev": RankDev, "Development": RankDev, " qa ": RankTest, "uat": RankTest, "int": RankTest,
		"stg": RankStg, "pre-prod": RankStg, "PRD": RankPrd, "production": RankPrd,
		"": RankNone, "sandbox": RankNone, "prod-eu": RankNone,
	} {
		if got := EnvRank(env); got != want {
			t.Errorf("%q: %d, want %d", env, got, want)
		}
	}
}

func names(ps []Placed) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Track+"/"+p.Cluster)
	}
	return strings.Join(out, " ")
}

func TestPlaceByRegionRankAndFleetOrder(t *testing.T) {
	pl := Place([]Station{
		{Cluster: "prd-we", Env: "prd", Region: "westeurope", Order: 0},
		{Cluster: "dev-we", Env: "dev", Region: "westeurope", Order: 1},
		{Cluster: "prd-ne", Env: "prd", Region: "northeurope", Order: 2},
		{Cluster: "stg-ne", Env: "stg", Region: "northeurope", Order: 3},
		{Cluster: "stg-ne-2", Env: "stg", Region: "northeurope", Order: 4}, // a collision: side by side in the cell
		{Cluster: "lab", Env: "sandbox", Region: "home", Order: 5},
	})
	if got := names(pl.Placed); got != "northeurope/stg-ne northeurope/stg-ne-2 northeurope/prd-ne westeurope/dev-we westeurope/prd-we" {
		t.Fatalf("placed: %s", got)
	}
	if len(pl.Siding) != 1 || pl.Siding[0].Cluster != "lab" || !pl.Siding[0].Siding {
		t.Fatalf("siding: %+v", pl.Siding)
	}
	if len(pl.Tracks) != 2 || pl.Tracks[0].Kind != TrackRegion || pl.Fallback {
		t.Fatalf("tracks: %+v fallback %v", pl.Tracks, pl.Fallback)
	}
}

func TestPlaceByGroupIsLabelledAsAGroup(t *testing.T) {
	pl := Place([]Station{{Cluster: "a", Env: "dev", Group: "core"}, {Cluster: "b", Env: "prd", Group: "core"}, {Cluster: "c", Env: "prd"}})
	if len(pl.Tracks) != 2 || pl.Tracks[0].Label != "group: core" || pl.Tracks[0].Kind != TrackGroup || pl.Tracks[1].Label != "no group" {
		t.Fatalf("group tracks: %+v", pl.Tracks)
	}
}

func TestPlaceSingleFleetTrackAndOneRowFallback(t *testing.T) {
	pl := Place([]Station{{Cluster: "a", Env: "prd"}, {Cluster: "b", Env: "prod"}})
	if len(pl.Tracks) != 1 || pl.Tracks[0].Label != "Fleet" || !pl.Fallback || len(pl.Ranks) != 1 {
		t.Fatalf("fleet: %+v", pl)
	}
}

func TestParseGitRevision(t *testing.T) {
	sha := "a1b9e3c4d5e6f708192a3b4c5d6e7f8091a2b3c4"
	sha256 := strings.Repeat("ab", 32)
	cases := []struct {
		in        string
		ref, want string
		ok        bool
	}{
		{"main@sha1:" + sha, "main", sha, true},
		{"refs/heads/main@sha1:" + strings.ToUpper(sha), "refs/heads/main", sha, true},
		{"v1.2.3@sha256:" + sha256, "v1.2.3", sha256, true},
		{"sha1:" + sha, "", sha, true},
		{sha, "", sha, true}, // Argo's bare sha
		{"main/" + sha, "", "", false},
		{"@sha1:" + sha, "", "", false},
		{"main@sha1:abc", "", "", false},
		{"1.2.3", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		got, ok := ParseGitRevision(c.in)
		if ok != c.ok || got.Ref != c.ref || got.SHA != c.want {
			t.Errorf("%q: %+v %v", c.in, got, ok)
		}
	}
}

func TestNormalizeRepo(t *testing.T) {
	want := "github.com/acme/fleet"
	for _, in := range []string{
		"https://github.com/acme/fleet", "https://GitHub.com/acme/fleet.git/", "http://user:pw@github.com/acme/fleet",
		"ssh://git@github.com/acme/fleet.git", "git@github.com:acme/fleet.git", "ssh://git@github.com:22/acme/fleet",
		"https://github.com:443/acme/fleet",
	} {
		if got := NormalizeRepo(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if got := NormalizeRepo("https://git.example:8443/Acme/Fleet"); got != "git.example:8443/Acme/Fleet" {
		t.Errorf("a non-default port and the path's case stay: %q", got)
	}
	// A key is shown: an unparsable URL, or a bare user@ prefix, never keeps
	// its user info, query or fragment (slice 6c design review).
	for in, want := range map[string]string{
		"https://ghp_tok@github.com/acme/fleet%zz":    "github.com/acme/fleet%zz",
		"https://u:pw@GitHub.com/acme/fleet%zz?t=1#x": "github.com/acme/fleet%zz",
		"ghp_tok@github.com/acme/fleet":               "github.com/acme/fleet",
	} {
		if got := NormalizeRepo(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestMeshRules(t *testing.T) {
	pl := Place([]Station{
		{Cluster: "prd-a", Env: "prd", Region: "a"}, {Cluster: "prd-b", Env: "prd", Region: "b"}, {Cluster: "prd-c", Env: "prd", Region: "c"},
		{Cluster: "dev-a", Env: "dev", Region: "a"}, {Cluster: "lab", Env: "sandbox", Region: "a"},
	})
	g := clustermesh.Graph{Edges: []clustermesh.MeshEdge{
		{A: "prd-a", B: "prd-b", Mutual: true},             // same rank, adjacent: drawn
		{A: "prd-b", B: "prd-c", From: "prd-b"},            // one-way, adjacent: drawn, dashed
		{A: "prd-a", B: "prd-c", Mutual: true},             // same rank, not adjacent: cards
		{A: "dev-a", B: "prd-b", Mutual: true},             // different ranks: cards
		{A: "lab", B: "prd-a", Mutual: true},               // the siding: cards
		{A: "prd-a", B: "cilium-elsewhere", From: "prd-a"}, // off-fleet
	}}
	m := Mesh(pl, g)
	if len(m.Drawn) != 2 || m.Drawn[1].Mutual || m.Drawn[1].From != "prd-b" {
		t.Fatalf("drawn: %+v", m.Drawn)
	}
	if len(m.Card["prd-a"]) != 2 || len(m.Card["prd-c"]) != 1 || len(m.Card["dev-a"]) != 1 || len(m.Card["lab"]) != 1 {
		t.Fatalf("cards: %+v", m.Card)
	}
	if len(m.OffFleet) != 1 || m.OffFleet[0].B != "cilium-elsewhere" {
		t.Fatalf("off-fleet: %+v", m.OffFleet)
	}
}

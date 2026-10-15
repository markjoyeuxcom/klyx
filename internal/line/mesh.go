package line

import (
	"sort"

	"github.com/moomora/klyx/internal/clustermesh"
)

// MeshLink is one configured peering. Mutual is false for a one-way
// peering, and From names its configuring side.
type MeshLink struct {
	A, B   string
	Mutual bool
	From   string
}

// MeshLines places the fleet's peerings on the board (§3.5): only stations
// at the same rank on adjacent tracks get a drawn link; any other fleet pair
// is listed on its stations' cards; a peering with an off-fleet end is listed
// under the board.
type MeshLines struct {
	Drawn    []MeshLink
	Card     map[string][]MeshLink // per cluster
	OffFleet []MeshLink
}

// Mesh applies §3.5 to a configured-peering graph.
func Mesh(pl Placement, g clustermesh.Graph) MeshLines {
	out := MeshLines{Card: map[string][]MeshLink{}}
	trackIdx := map[string]int{}
	for i, t := range pl.Tracks {
		trackIdx[t.Name] = i
	}
	pos := map[string]Placed{}
	fleet := map[string]bool{}
	for _, p := range pl.Placed {
		pos[p.Cluster] = p
		fleet[p.Cluster] = true
	}
	for _, p := range pl.Siding {
		fleet[p.Cluster] = true
	}
	for _, e := range g.Edges {
		l := MeshLink{A: e.A, B: e.B, Mutual: e.Mutual, From: e.From}
		if !fleet[e.A] || !fleet[e.B] {
			out.OffFleet = append(out.OffFleet, l)
			continue
		}
		pa, oka := pos[e.A]
		pb, okb := pos[e.B]
		if oka && okb && pa.Rank == pb.Rank && abs(trackIdx[pa.Track]-trackIdx[pb.Track]) == 1 {
			out.Drawn = append(out.Drawn, l)
			continue
		}
		out.Card[e.A] = append(out.Card[e.A], l)
		out.Card[e.B] = append(out.Card[e.B], l)
	}
	sort.Slice(out.Drawn, func(i, j int) bool { return out.Drawn[i].A+out.Drawn[i].B < out.Drawn[j].A+out.Drawn[j].B })
	sort.Slice(out.OffFleet, func(i, j int) bool { return out.OffFleet[i].A+out.OffFleet[i].B < out.OffFleet[j].A+out.OffFleet[j].B })
	return out
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

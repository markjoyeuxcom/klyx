package clustermesh

import "sort"

// BuildGraph assembles the fleet peering graph from per-cluster Members. Peer
// matching is by Cilium cluster-name (Member.Identity.Name, falling back to the
// fleet key). A peer not present in the fleet becomes an off-fleet node
// (Present=false). Mutual = both sides configure each other; off-fleet edges are
// never mutual (we can't read the other side).
func BuildGraph(members []Member) Graph {
	// Index fleet members by their Cilium name (fallback fleet key) for resolution.
	byName := make(map[string]*Member, len(members))
	for i := range members {
		m := &members[i]
		byName[nameKey(m)] = m
	}

	g := Graph{}
	// Fleet nodes.
	for i := range members {
		m := &members[i]
		g.Nodes = append(g.Nodes, MeshNode{
			Cluster: m.Cluster, Name: m.Identity.Name, ClusterID: m.Identity.ID,
			State: stateOf(m, members), Present: true, ReadNote: m.ReadNote,
		})
	}

	// Edges + off-fleet nodes.
	type pk struct{ a, b string }
	edgeAt := map[pk]int{} // canonical pair -> index in g.Edges
	offFleet := map[string]bool{}

	for i := range members {
		m := &members[i]
		self := nameKey(m)
		for _, peer := range dedup(m.Peers) {
			if peer == self || peer == m.Identity.Name {
				continue // ignore a cluster naming itself
			}
			var endpoint string
			var mutual bool
			if other, ok := byName[peer]; ok {
				endpoint = other.Cluster
				mutual = lists(other, m) // other configures us back
			} else {
				endpoint = peer // off-fleet endpoint keyed by Cilium name
				if !offFleet[peer] {
					offFleet[peer] = true
					g.Nodes = append(g.Nodes, MeshNode{Name: peer, State: "", Present: false})
				}
			}
			a, b := m.Cluster, endpoint
			if a > b {
				a, b = b, a
			}
			key := pk{a, b}
			if idx, ok := edgeAt[key]; ok {
				if mutual {
					g.Edges[idx].Mutual = true
					g.Edges[idx].From = ""
				}
				continue
			}
			edgeAt[key] = len(g.Edges)
			from := ""
			if !mutual {
				from = m.Cluster
			}
			g.Edges = append(g.Edges, MeshEdge{A: a, B: b, Mutual: mutual, From: from})
		}
	}
	return g
}

func nameKey(m *Member) string {
	if m.Identity.Name != "" {
		return m.Identity.Name
	}
	return m.Cluster
}

// lists reports whether other configures m as a peer (by m's Cilium name).
func lists(other, m *Member) bool {
	target := m.Identity.Name
	if target == "" {
		target = m.Cluster
	}
	for _, p := range other.Peers {
		if p == target {
			return true
		}
	}
	return false
}

func stateOf(m *Member, all []Member) MeshState {
	if !m.Installed {
		return MeshUnavailable
	}
	if len(m.Peers) > 0 {
		return MeshPeered
	}
	for i := range all {
		if &all[i] == m {
			continue
		}
		if lists(&all[i], m) {
			return MeshPeered
		}
	}
	return MeshEnabled
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

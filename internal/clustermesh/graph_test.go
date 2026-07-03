package clustermesh

import (
	"sort"
	"testing"
)

func mem(fleetKey, ciliumName string, peers []string, installed bool) Member {
	return Member{Cluster: fleetKey, Identity: Identity{Name: ciliumName}, Peers: peers, Present: true, Installed: installed}
}

func findEdge(g Graph, a, b string) (MeshEdge, bool) {
	for _, e := range g.Edges {
		if (e.A == a && e.B == b) || (e.A == b && e.B == a) {
			return e, true
		}
	}
	return MeshEdge{}, false
}
func nodeState(g Graph, fleetKey string) MeshState {
	for _, n := range g.Nodes {
		if n.Cluster == fleetKey {
			return n.State
		}
	}
	return ""
}

func TestBuildGraphMutualAsymmetricStandalone(t *testing.T) {
	g := BuildGraph([]Member{
		mem("ctx-blue", "homelab-blue", []string{"homelab-orange"}, true),
		mem("ctx-orange", "homelab-orange", []string{"homelab-blue"}, true),
		mem("ctx-nelli", "homelab-nelli", nil, true), // installed, no peers
	})
	e, ok := findEdge(g, "ctx-blue", "ctx-orange")
	if !ok || !e.Mutual {
		t.Fatalf("blue<->orange should be a mutual edge: %+v", g.Edges)
	}
	if nodeState(g, "ctx-blue") != MeshPeered || nodeState(g, "ctx-nelli") != MeshEnabled {
		t.Fatalf("states: blue=%s nelli=%s", nodeState(g, "ctx-blue"), nodeState(g, "ctx-nelli"))
	}
	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %+v", g.Edges)
	}
}

func TestBuildGraphAsymmetric(t *testing.T) {
	// blue lists orange; orange does NOT list blue -> asymmetric.
	g := BuildGraph([]Member{
		mem("ctx-blue", "homelab-blue", []string{"homelab-orange"}, true),
		mem("ctx-orange", "homelab-orange", nil, true),
	})
	e, ok := findEdge(g, "ctx-blue", "ctx-orange")
	if !ok || e.Mutual {
		t.Fatalf("want asymmetric (non-mutual) edge: %+v", g.Edges)
	}
	// One-way edges record the configuring side, so the UI can say who is
	// missing the return peering.
	if e.From != "ctx-blue" {
		t.Fatalf("one-way edge must record From=ctx-blue: %+v", e)
	}
}

func TestBuildGraphMutualEdgeHasNoFrom(t *testing.T) {
	g := BuildGraph([]Member{
		mem("ctx-blue", "homelab-blue", []string{"homelab-orange"}, true),
		mem("ctx-orange", "homelab-orange", []string{"homelab-blue"}, true),
	})
	e, ok := findEdge(g, "ctx-blue", "ctx-orange")
	if !ok || !e.Mutual {
		t.Fatalf("want mutual edge: %+v", g.Edges)
	}
	if e.From != "" {
		t.Fatalf("mutual edges are undirected - From must be empty: %+v", e)
	}
}

func TestBuildGraphIdentityNameDiffersFromFleetKey(t *testing.T) {
	// Fleet keys are kubeconfig contexts; peers are Cilium names. Matching MUST be by Cilium name.
	g := BuildGraph([]Member{
		mem("kubernetes-admin@homelab-blue", "homelab-blue", []string{"homelab-orange"}, true),
		mem("kubernetes-admin@homelab-orange", "homelab-orange", []string{"homelab-blue"}, true),
	})
	if _, ok := findEdge(g, "kubernetes-admin@homelab-blue", "kubernetes-admin@homelab-orange"); !ok {
		t.Fatalf("edge must resolve across differing fleet keys: %+v", g.Edges)
	}
}

func TestBuildGraphOffFleetPeerSelfDupUninstalled(t *testing.T) {
	g := BuildGraph([]Member{
		mem("ctx-blue", "homelab-blue", []string{"homelab-orange", "homelab-orange", "homelab-blue", "aks-prd-we"}, true),
	})
	// duplicate peer collapses; self-peer ignored; aks-prd-we is off-fleet.
	var offFleet []MeshNode
	for _, n := range g.Nodes {
		if !n.Present {
			offFleet = append(offFleet, n)
		}
	}
	if len(offFleet) != 2 || offFleet[0].Name != "aks-prd-we" {
		t.Fatalf("off-fleet node: %+v", offFleet)
	}
	// Off-fleet nodes must not assert a mesh state - Klyx never read their config.
	for _, n := range offFleet {
		if n.State != "" {
			t.Fatalf("off-fleet node must claim no state: %+v", n)
		}
	}
	// one edge to the off-fleet peer; no self edge.
	if e, ok := findEdge(g, "ctx-blue", "aks-prd-we"); !ok || e.Mutual {
		t.Fatalf("off-fleet edge (non-mutual): %+v", g.Edges)
	}
	for _, e := range g.Edges {
		if e.A == e.B {
			t.Fatalf("self edge present: %+v", e)
		}
	}
	// orange is named but not a fleet member -> off-fleet too; total off-fleet = orange + aks
	names := []string{}
	for _, n := range g.Nodes {
		if !n.Present {
			names = append(names, n.Name)
		}
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "aks-prd-we" || names[1] != "homelab-orange" {
		t.Fatalf("off-fleet names: %+v", names)
	}
}

func TestBuildGraphUninstalled(t *testing.T) {
	g := BuildGraph([]Member{mem("ctx-x", "x", nil, false)})
	if nodeState(g, "ctx-x") != MeshUnavailable {
		t.Fatalf("uninstalled -> unavailable: %s", nodeState(g, "ctx-x"))
	}
}

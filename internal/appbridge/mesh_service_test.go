package appbridge

import (
	"context"
	"testing"

	"github.com/moomora/klyx/internal/clustermesh"
)

func TestMeshServiceGraph(t *testing.T) {
	members := []clustermesh.Member{
		{Cluster: "ctx-blue", Identity: clustermesh.Identity{Name: "homelab-blue"}, Peers: []string{"homelab-orange"}, Present: true, Installed: true},
		{Cluster: "ctx-orange", Identity: clustermesh.Identity{Name: "homelab-orange"}, Peers: []string{"homelab-blue"}, Present: true, Installed: true},
		{Cluster: "ctx-nelli", Identity: clustermesh.Identity{Name: "homelab-nelli"}, Present: true, Installed: true},
	}
	svc := NewMeshService(func(context.Context) []clustermesh.Member { return members })
	g := svc.GetMeshGraph()

	if len(g.Nodes) != 3 {
		t.Fatalf("nodes: %+v", g.Nodes)
	}
	var mutual bool
	for _, e := range g.Edges {
		if (e.A == "ctx-blue" && e.B == "ctx-orange") || (e.A == "ctx-orange" && e.B == "ctx-blue") {
			mutual = e.Mutual
		}
	}
	if len(g.Edges) != 1 || !mutual {
		t.Fatalf("edges: %+v", g.Edges)
	}
	// nelli state mapped through.
	for _, n := range g.Nodes {
		if n.Cluster == "ctx-nelli" && n.State != "enabled" {
			t.Fatalf("nelli state: %s", n.State)
		}
	}
}

func TestMeshServiceEmpty(t *testing.T) {
	svc := NewMeshService(func(context.Context) []clustermesh.Member { return nil })
	g := svc.GetMeshGraph()
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("empty: %+v", g)
	}
}

// The Line slice 3 note §6: the reader's context reaches the member reads,
// so a deadline or cancellation stops them.
func TestMeshServiceGraphPassesItsContext(t *testing.T) {
	var got context.Context
	svc := NewMeshService(func(ctx context.Context) []clustermesh.Member { got = ctx; return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.graph(ctx)
	if got == nil || got.Err() == nil {
		t.Fatal("the caller's cancelled context must reach the member reads")
	}
	svc.GetMeshGraph()
	if _, ok := got.Deadline(); !ok {
		t.Fatal("an on-demand read is bounded")
	}
}

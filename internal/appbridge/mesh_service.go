package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/clustermesh"
)

type MeshNodeDTO struct {
	Cluster   string `json:"cluster"`
	Name      string `json:"name"`
	ClusterID *int   `json:"clusterId"`
	State     string `json:"state"`
	Present   bool   `json:"present"`
	// ReadNote: this fleet member's mesh facts could not be read - its
	// peerings are unknown, not "none".
	ReadNote string `json:"readNote,omitempty"`
}
type MeshEdgeDTO struct {
	A      string `json:"a"`
	B      string `json:"b"`
	Mutual bool   `json:"mutual"`
	// From: the configuring side, set only on one-way (non-mutual) edges.
	From string `json:"from,omitempty"`
}
type MeshGraphDTO struct {
	Nodes []MeshNodeDTO `json:"nodes"`
	Edges []MeshEdgeDTO `json:"edges"`
}

// MeshService builds the fleet ClusterMesh graph on demand. listMembers does the
// live per-cluster reads (wired in main.go from the registry), bounded and
// cancelled by its context (The Line slice 3 note §6).
type MeshService struct {
	listMembers func(ctx context.Context) []clustermesh.Member
}

func NewMeshService(listMembers func(ctx context.Context) []clustermesh.Member) *MeshService {
	return &MeshService{listMembers: listMembers}
}

// meshReadTimeout bounds an on-demand graph read.
const meshReadTimeout = 30 * time.Second

func (s *MeshService) GetMeshGraph() MeshGraphDTO {
	ctx, cancel := context.WithTimeout(context.Background(), meshReadTimeout)
	defer cancel()
	return s.graph(ctx)
}

// graph reads the graph under ctx: its deadline and cancellation reach every
// member read.
func (s *MeshService) graph(ctx context.Context) MeshGraphDTO {
	g := clustermesh.BuildGraph(s.listMembers(ctx))
	out := MeshGraphDTO{Nodes: make([]MeshNodeDTO, 0, len(g.Nodes)), Edges: make([]MeshEdgeDTO, 0, len(g.Edges))}
	for _, n := range g.Nodes {
		out.Nodes = append(out.Nodes, MeshNodeDTO{Cluster: n.Cluster, Name: n.Name, ClusterID: n.ClusterID, State: string(n.State), Present: n.Present, ReadNote: n.ReadNote})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, MeshEdgeDTO{A: e.A, B: e.B, Mutual: e.Mutual, From: e.From})
	}
	return out
}

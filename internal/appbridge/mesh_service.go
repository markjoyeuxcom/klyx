package appbridge

import "github.com/moomora/klyx/internal/clustermesh"

type MeshNodeDTO struct {
	Cluster   string `json:"cluster"`
	Name      string `json:"name"`
	ClusterID *int   `json:"clusterId"`
	State     string `json:"state"`
	Present   bool   `json:"present"`
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
// live per-cluster reads (wired in main.go from the registry).
type MeshService struct {
	listMembers func() []clustermesh.Member
}

func NewMeshService(listMembers func() []clustermesh.Member) *MeshService {
	return &MeshService{listMembers: listMembers}
}

func (s *MeshService) GetMeshGraph() MeshGraphDTO {
	g := clustermesh.BuildGraph(s.listMembers())
	out := MeshGraphDTO{Nodes: make([]MeshNodeDTO, 0, len(g.Nodes)), Edges: make([]MeshEdgeDTO, 0, len(g.Edges))}
	for _, n := range g.Nodes {
		out.Nodes = append(out.Nodes, MeshNodeDTO{Cluster: n.Cluster, Name: n.Name, ClusterID: n.ClusterID, State: string(n.State), Present: n.Present})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, MeshEdgeDTO{A: e.A, B: e.B, Mutual: e.Mutual, From: e.From})
	}
	return out
}

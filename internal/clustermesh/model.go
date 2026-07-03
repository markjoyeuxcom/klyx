// Package clustermesh parses Cilium ClusterMesh state (cilium-config + the
// cilium-clustermesh Secret) into per-cluster Members and assembles a fleet
// peering Graph. It renders CONFIGURED peering only - never live connectivity,
// which needs agent metrics (M7). Pure: no client-go dependency beyond the
// typed objects passed in.
package clustermesh

// MeshState is a cluster's mesh status (coarsest -> richest).
type MeshState string

const (
	MeshUnavailable MeshState = "unavailable" // ClusterMesh not installed
	MeshEnabled     MeshState = "enabled"     // installed, no configured peers
	MeshPeered      MeshState = "peered"      // >=1 configured peer, or named by another
)

// Identity is a cluster's Cilium identity. ID is optional display metadata; the
// graph identity is Name (the Cilium cluster-name).
type Identity struct {
	Name string
	ID   *int
}

// Member is one fleet cluster's mesh facts (fed to BuildGraph).
type Member struct {
	Cluster   string   // fleet key (kubeconfig context / Snapshot.Name)
	Identity  Identity // Cilium cluster-name / id
	Peers     []string // configured remote peer Cilium names (from the Secret)
	Present   bool     // connected to Klyx (always true for real fleet members)
	Installed bool     // ClusterMesh installed on this cluster
}

// MeshNode is a node in the fleet peering graph.
type MeshNode struct {
	Cluster   string // fleet key (display + click target); "" for off-fleet peers
	Name      string // Cilium cluster-name (display for off-fleet)
	ClusterID *int   // optional
	State     MeshState
	Present   bool // false = off-fleet (named by a member, not connected to Klyx)
}

// MeshEdge is an undirected peering edge. Endpoints are fleet keys for present
// clusters, or the Cilium peer name for an off-fleet endpoint.
type MeshEdge struct {
	A, B   string
	Mutual bool // both sides configure each other; off-fleet edges are never mutual
	// From is the side that configured the peering, only meaningful when
	// !Mutual - it lets the UI say "A -> B one-way: B does not list A" instead
	// of an undirected shrug. Empty on mutual edges.
	From string
}

type Graph struct {
	Nodes []MeshNode
	Edges []MeshEdge
}

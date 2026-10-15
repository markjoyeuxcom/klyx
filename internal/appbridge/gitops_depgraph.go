package appbridge

import (
	"context"

	"github.com/moomora/klyx/internal/gitops/depgraph"
)

// The GitOps dependency graph (The Line slice 8b, parent §5.1, slice 8 note
// §3), bound for the Flux inspector (8c). The first parameter is a context:
// the frontend cancels the walk when the selection changes.

type DepEdgeDTO struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Type     string `json:"type"`     // source | dependsOn
	Reported bool   `json:"reported"` // the downstream controller reports this edge blocks it
}

type DepSupplierDTO struct {
	Kind          string           `json:"kind"`
	Namespace     string           `json:"namespace"`
	Name          string           `json:"name"`
	UID           string           `json:"uid"`
	ReadAt        string           `json:"readAt"`
	Ready         string           `json:"ready"`
	Reason        string           `json:"reason,omitempty"`
	Message       string           `json:"message,omitempty"`
	LastWrite     *ManagedWriteDTO `json:"lastWrite,omitempty"`
	LastSpecWrite *ManagedWriteDTO `json:"lastSpecWrite,omitempty"`
}

// DepSupplyDTO is a credential a node reads: never a cause, only a fact.
type DepSupplyDTO struct {
	Field         string           `json:"field"`
	Namespace     string           `json:"namespace"`
	Name          string           `json:"name"`
	UID           string           `json:"uid,omitempty"`
	ReadAt        string           `json:"readAt,omitempty"`
	Error         string           `json:"error,omitempty"`
	NotFound      bool             `json:"notFound,omitempty"`
	LastWrite     *ManagedWriteDTO `json:"lastWrite,omitempty"`
	LastSpecWrite *ManagedWriteDTO `json:"lastSpecWrite,omitempty"`
	Supplier      *DepSupplierDTO  `json:"supplier,omitempty"`
	SupplierNote  string           `json:"supplierNote,omitempty"`
}

type DepInventoryDTO struct {
	LastApplied string `json:"lastApplied"`
	Entries     int    `json:"entries"`
	Workloads   int    `json:"workloads"`
}

// DepGitDTO are the facts Git's "not here yet" needs, as the cluster states them.
type DepGitDTO struct {
	URL     string `json:"url"`
	RefKind string `json:"refKind"`
	RefName string `json:"refName"`
	Applied string `json:"applied"`
	Path    string `json:"path"`
}

type DepNodeDTO struct {
	ID        string `json:"id"`
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
	ReadAt    string `json:"readAt,omitempty"`
	// Stub: where evidence stops (depth, node limit, read limit, cycle,
	// unreadable, replaced, cancelled), or "not found" (a read that answered).
	Stub string `json:"stub,omitempty"`
	Note string `json:"note,omitempty"`

	Ready         string `json:"ready"` // the Ready condition: True | False | Unknown | ""
	Reason        string `json:"reason,omitempty"`
	Message       string `json:"message,omitempty"`
	ConditionTime string `json:"conditionTime,omitempty"` // labelled "condition time", never a role start
	// ConditionGeneration is the condition's own observedGeneration (0: none):
	// a condition behind Generation is evidence about that generation.
	ConditionGeneration int64 `json:"conditionGeneration"`
	Generation          int64 `json:"generation"`
	ObservedGeneration  int64 `json:"observedGeneration"`
	Stale               bool  `json:"stale"`
	Suspended           bool  `json:"suspended"`
	Failing             bool  `json:"failing"`
	UpstreamFailure     bool  `json:"upstreamFailure"` // "upstream failure"
	ChainEnd            bool  `json:"chainEnd"`        // "reported chain ends here"

	LastWrite     *ManagedWriteDTO `json:"lastWrite,omitempty"`
	LastSpecWrite *ManagedWriteDTO `json:"lastSpecWrite,omitempty"`
	Supply        []DepSupplyDTO   `json:"supply"`
	Inventory     *DepInventoryDTO `json:"inventory,omitempty"`
	Git           *DepGitDTO       `json:"git,omitempty"`
}

// DepPairDTO places a main-resource update beside a failing node's
// condition time: two qualified observations, never a cause.
type DepPairDTO struct {
	Failing       string          `json:"failing"`
	ConditionTime string          `json:"conditionTime"`
	Object        string          `json:"object"`
	Write         ManagedWriteDTO `json:"write"`
}

type DepGraphDTO struct {
	Start      string       `json:"start"`
	Nodes      []DepNodeDTO `json:"nodes"`
	Edges      []DepEdgeDTO `json:"edges"`
	Incomplete []string     `json:"incomplete"`
	Proximity  []DepPairDTO `json:"proximity"`
	Reads      int          `json:"reads"`
	Error      string       `json:"error,omitempty"`
}

// DependencyGraph walks from one Kustomization or HelmRelease, pinned to
// the selection's UID. Cancelling the call cancels the walk.
func (s *GitOpsService) DependencyGraph(ctx context.Context, cluster, kind, namespace, name, uid string) DepGraphDTO {
	empty := DepGraphDTO{Nodes: []DepNodeDTO{}, Edges: []DepEdgeDTO{}, Incomplete: []string{}, Proximity: []DepPairDTO{}}
	if kind != "Kustomization" && kind != "HelmRelease" {
		empty.Error = "a dependency graph starts at a Kustomization or a HelmRelease"
		return empty
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		empty.Error = "cluster not connected: " + cluster
		return empty
	}
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	return depGraphDTO(conn.DependencyGraph(ctx, kind, namespace, name, uid))
}

func depGraphDTO(g depgraph.Graph) DepGraphDTO {
	out := DepGraphDTO{Start: g.Start, Reads: g.Reads, Nodes: make([]DepNodeDTO, 0, len(g.Nodes)), Edges: make([]DepEdgeDTO, 0, len(g.Edges)),
		Incomplete: append([]string{}, g.Incomplete...), Proximity: make([]DepPairDTO, 0, len(g.Proximity))}
	for _, n := range g.Nodes {
		d := DepNodeDTO{ID: n.ID, Group: n.Ref.Group, Kind: n.Ref.Kind, Namespace: n.Ref.Namespace, Name: n.Ref.Name, UID: n.UID, ReadAt: rfc3339(n.ReadAt),
			Stub: string(n.Stub), Note: n.Note, Ready: n.Ready, Reason: n.Reason, Message: n.Message, ConditionTime: rfc3339(n.ConditionTime),
			ConditionGeneration: n.ConditionGeneration, Generation: n.Generation, ObservedGeneration: n.ObservedGeneration,
			Stale: n.Stale, Suspended: n.Suspended, Failing: n.Failing, UpstreamFailure: n.UpstreamFailure, ChainEnd: n.ChainEnd,
			LastWrite: writeDTO(n.Writes.Last), LastSpecWrite: writeDTO(n.Writes.LastSpec), Supply: make([]DepSupplyDTO, 0, len(n.Supply))}
		for _, s := range n.Supply {
			sd := DepSupplyDTO{Field: s.Field, Namespace: s.Secret.Namespace, Name: s.Secret.Name, UID: s.SecretUID, ReadAt: rfc3339(s.ReadAt), Error: s.Error, NotFound: s.NotFound,
				LastWrite: writeDTO(s.Writes.Last), LastSpecWrite: writeDTO(s.Writes.LastSpec), SupplierNote: s.SupplierNote}
			if p := s.Supplier; p != nil {
				sd.Supplier = &DepSupplierDTO{Kind: p.Ref.Kind, Namespace: p.Ref.Namespace, Name: p.Ref.Name, UID: p.UID, ReadAt: rfc3339(p.ReadAt), Ready: p.Ready, Reason: p.Reason, Message: p.Message,
					LastWrite: writeDTO(p.Writes.Last), LastSpecWrite: writeDTO(p.Writes.LastSpec)}
			}
			d.Supply = append(d.Supply, sd)
		}
		if i := n.Inventory; i != nil {
			d.Inventory = &DepInventoryDTO{LastApplied: i.LastApplied, Entries: i.Entries, Workloads: i.Workloads}
		}
		if g := n.Git; g != nil {
			d.Git = &DepGitDTO{URL: g.URL, RefKind: g.RefKind, RefName: g.RefName, Applied: g.Applied, Path: g.Path}
		}
		out.Nodes = append(out.Nodes, d)
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, DepEdgeDTO{From: e.From, To: e.To, Type: string(e.Type), Reported: e.Reported})
	}
	for _, p := range g.Proximity {
		w := p.Write
		out.Proximity = append(out.Proximity, DepPairDTO{Failing: p.Failing, ConditionTime: rfc3339(p.ConditionTime), Object: p.Object, Write: *writeDTO(&w)})
	}
	return out
}

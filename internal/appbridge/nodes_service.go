package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

const nodesTimeout = 30 * time.Second

// NodesConn is the per-cluster surface NodesService needs.
type NodesConn interface {
	ListNodes(ctx context.Context) ([]workloads.NodeSummary, error)
	NodeDetail(ctx context.Context, name string) (fleet.NodeDetail, error)
}

// NodesService is bound to JS. Pure request/response: ListNodes returns
// problem-first node rows; GetNodeDetail returns the full drill-down.
type NodesService struct {
	lookup func(string) (NodesConn, bool)
}

// NewNodesService creates a NodesService with the given cluster-lookup function.
func NewNodesService(lookup func(string) (NodesConn, bool)) *NodesService {
	return &NodesService{lookup: lookup}
}

// ListNodes returns problem-first node rows for a cluster. Cluster miss returns
// non-nil empty.
func (s *NodesService) ListNodes(cluster string) NodesResultDTO {
	out := NodesResultDTO{Nodes: []NodeSummaryDTO{}}
	conn, ok := s.lookup(cluster)
	if !ok {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), nodesTimeout)
	defer cancel()
	nodes, err := conn.ListNodes(ctx)
	if err != nil {
		return out
	}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, toNodeSummaryDTO(n))
	}
	return out
}

// GetNodeDetail returns the full node detail. Cluster miss or error returns a
// zero DTO with empty-but-non-nil collections (never panics on null).
func (s *NodesService) GetNodeDetail(cluster, name string) NodeDetailDTO {
	empty := NodeDetailDTO{
		Labels:      map[string]string{},
		Annotations: map[string]string{},
		Taints:      []NodeTaintDTO{},
		Conditions:  []ConditionDTO{},
		Events:      []EventDTO{},
		PodsOnNode:  []PodOnNodeDTO{},
	}
	conn, ok := s.lookup(cluster)
	if !ok {
		return empty
	}
	ctx, cancel := context.WithTimeout(context.Background(), nodesTimeout)
	defer cancel()
	d, err := conn.NodeDetail(ctx, name)
	if err != nil {
		return empty
	}

	labels := d.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	annotations := d.Annotations
	if annotations == nil {
		annotations = map[string]string{}
	}

	taints := make([]NodeTaintDTO, 0, len(d.Taints))
	for _, t := range d.Taints {
		taints = append(taints, NodeTaintDTO{Key: t.Key, Value: t.Value, Effect: t.Effect})
	}

	conds := make([]ConditionDTO, 0, len(d.Conditions))
	for _, c := range d.Conditions {
		conds = append(conds, ConditionDTO{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message})
	}

	events := make([]EventDTO, 0, len(d.Events))
	for _, e := range d.Events {
		events = append(events, EventDTO{Type: e.Type, Reason: e.Reason, Message: e.Message, Count: int(e.Count), LastSeen: rfc3339(e.Last)})
	}

	pods := make([]PodOnNodeDTO, 0, len(d.PodsOnNode))
	for _, p := range d.PodsOnNode {
		pods = append(pods, PodOnNodeDTO{Namespace: p.Namespace, Name: p.Name, Phase: p.Phase})
	}

	return NodeDetailDTO{
		Summary:     toNodeSummaryDTO(d.Summary),
		Labels:      labels,
		Annotations: annotations,
		Taints:      taints,
		Conditions:  conds,
		Events:      events,
		YAML:        d.YAML,
		PodsOnNode:  pods,
	}
}

func toNodeSummaryDTO(n workloads.NodeSummary) NodeSummaryDTO {
	roles := n.Roles
	if roles == nil {
		roles = []string{}
	}
	problems := n.Problems
	if problems == nil {
		problems = []string{}
	}
	return NodeSummaryDTO{
		Name:           n.Name,
		Roles:          roles,
		Ready:          n.Ready,
		Unschedulable:  n.Unschedulable,
		Problems:       problems,
		Version:        n.Version,
		OS:             n.OS,
		Arch:           n.Arch,
		TaintCount:     n.TaintCount,
		CPUCapacity:    n.CPUCapacity,
		CPUAllocatable: n.CPUAllocatable,
		MemCapacity:    n.MemCapacity,
		MemAllocatable: n.MemAllocatable,
		PodCapacity:    n.PodCapacity,
		AgeSeconds:     n.AgeSeconds,
	}
}

package workloads

import (
	"k8s.io/apimachinery/pkg/types"
	"sort"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/health"
	corev1 "k8s.io/api/core/v1"
)

// NodeSummary is the nodes-lens row: one node with capacity, role, and problem
// summary. Capacity bars use spec quantities only (no Prometheus in this path).
type NodeSummary struct {
	Name          string
	UID           types.UID
	Generation    int64
	Roles         []string // from node-role.kubernetes.io/* labels; empty stays empty (render "-")
	Ready         bool     // NodeReady condition True
	Unschedulable bool     // spec.unschedulable (cordoned)
	Problems      []string // NotReady / MemoryPressure / DiskPressure / PIDPressure when True
	Version       string   // kubelet version
	OS            string
	Arch          string
	TaintCount    int
	// Capacity quantities from the Node object (no Prometheus)
	CPUCapacity    float64 // cores
	CPUAllocatable float64 // cores
	MemCapacity    float64 // bytes
	MemAllocatable float64 // bytes
	PodCapacity    int64
	AgeSeconds     int
	// Pool is the provider node-pool/group identity (AKS agentpool, EKS
	// nodegroup, GKE nodepool, Karpenter nodepool), "" when no known label.
	// Zone is topology.kubernetes.io/zone (rack/zone on on-prem via the same
	// well-known label), "" when unlabeled.
	Pool string
	Zone string
	// Health is the shared verdict (internal/health). Ready/Problems are
	// legacy and stay only until every consumer has migrated.
	Health health.Verdict
}

// poolLabels are the provider labels that name a node's pool/group, checked in
// order. Provider-agnostic by design: AKS, EKS, GKE, Karpenter.
var poolLabels = []string{
	"agentpool",
	"kubernetes.azure.com/agentpool",
	"eks.amazonaws.com/nodegroup",
	"cloud.google.com/gke-nodepool",
	"karpenter.sh/nodepool",
}

// SummarizeNodes classifies each node and returns them in health verdict
// order (fault, degraded, unknown, ..., ok), then name.
func SummarizeNodes(nodes []corev1.Node, now time.Time) []NodeSummary {
	out := make([]NodeSummary, 0, len(nodes))
	for i := range nodes {
		n := &nodes[i]
		s := summarizeNode(n, now)
		s.Health = health.Node(n)
		out = append(out, s)
	}

	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Health.Kind != out[b].Health.Kind {
			return out[a].Health.Kind < out[b].Health.Kind
		}
		return out[a].Name < out[b].Name
	})

	return out
}

func summarizeNode(n *corev1.Node, now time.Time) NodeSummary {
	s := NodeSummary{
		Name:          n.Name,
		UID:           n.UID,
		Generation:    n.Generation,
		Unschedulable: n.Spec.Unschedulable,
		Version:       n.Status.NodeInfo.KubeletVersion,
		OS:            n.Status.NodeInfo.OperatingSystem,
		Arch:          n.Status.NodeInfo.Architecture,
		TaintCount:    len(n.Spec.Taints),
		AgeSeconds:    ageSeconds(n.CreationTimestamp.Time, now),
	}

	// Roles from node-role.kubernetes.io/* labels.
	for k := range n.Labels {
		if strings.HasPrefix(k, "node-role.kubernetes.io/") {
			role := strings.TrimPrefix(k, "node-role.kubernetes.io/")
			s.Roles = append(s.Roles, role)
		}
	}
	sort.Strings(s.Roles)

	for _, l := range poolLabels {
		if v := n.Labels[l]; v != "" {
			s.Pool = v
			break
		}
	}
	s.Zone = n.Labels["topology.kubernetes.io/zone"]

	// Conditions: extract Ready state and pressure conditions.
	s.Ready = true // default optimistic until we see the condition
	foundReady := false
	for _, c := range n.Status.Conditions {
		switch c.Type {
		case corev1.NodeReady:
			foundReady = true
			s.Ready = c.Status == corev1.ConditionTrue
			if c.Status != corev1.ConditionTrue {
				s.Problems = append(s.Problems, "NotReady")
			}
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			if c.Status == corev1.ConditionTrue {
				s.Problems = append(s.Problems, string(c.Type))
			}
		}
	}
	if !foundReady {
		// No NodeReady condition at all means not ready.
		s.Ready = false
		s.Problems = append(s.Problems, "NotReady")
	}

	// Capacity quantities.
	if q, ok := n.Status.Capacity[corev1.ResourceCPU]; ok {
		s.CPUCapacity = q.AsApproximateFloat64()
	}
	if q, ok := n.Status.Allocatable[corev1.ResourceCPU]; ok {
		s.CPUAllocatable = q.AsApproximateFloat64()
	}
	if q, ok := n.Status.Capacity[corev1.ResourceMemory]; ok {
		s.MemCapacity = q.AsApproximateFloat64()
	}
	if q, ok := n.Status.Allocatable[corev1.ResourceMemory]; ok {
		s.MemAllocatable = q.AsApproximateFloat64()
	}
	if q, ok := n.Status.Capacity[corev1.ResourcePods]; ok {
		s.PodCapacity = q.Value()
	}

	return s
}

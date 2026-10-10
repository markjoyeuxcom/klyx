// Package karpenter parses Karpenter's NodePool and NodeClaim objects into
// the triage rows the lens renders - the M10/M11/M12 diagnosis pattern applied
// to node autoscaling, speaking Karpenter's own vocabulary (design principle
// 8): NodePool limits vs provisioned capacity, the NodeClaim launch chain
// (Launched -> Registered -> Initialized -> Ready) with the FAILING link
// named, drift and consolidatability as informational states, spot vs
// on-demand identity. Pure: unstructured in, rows out; nothing here talks to
// a cluster.
package karpenter

import (
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Karpenter label/condition vocabulary. Single source of truth.
const (
	LabelNodePool     = "karpenter.sh/nodepool"
	LabelCapacityType = "karpenter.sh/capacity-type"
	LabelInstanceType = "node.kubernetes.io/instance-type"

	condReady          = "Ready"
	condLaunched       = "Launched"
	condRegistered     = "Registered"
	condInitialized    = "Initialized"
	condDrifted        = "Drifted"
	condConsolidatable = "Consolidatable"
)

// NodePool is one pool's triage row. Limit/used pointers are nil when the
// pool declares no limit / reports no capacity - never a fabricated zero.
type NodePool struct {
	Name     string
	Ready    string // "ready" | "not-ready" | "unknown"
	Reason   string
	Message  string
	Weight   int64
	CPULimit *float64
	CPUUsed  *float64
	MemLimit *float64
	MemUsed  *float64
	Nodes    int // NodeClaims currently attributed to this pool (assembled by the caller)
}

// NodeClaim is one claim's lifecycle row. FailingCondition names the first
// broken link of the launch chain when the claim is not Ready.
type NodeClaim struct {
	Name        string
	Pool        string
	NodeName    string
	Ready       string // "ready" | "not-ready" | "unknown"
	FailingType string // "" when Ready
	// FailingStatus is the chosen link's native condition status. Only
	// "False" proves that link failed; "Unknown" (or anything else) is a link
	// still in progress or unverified.
	FailingStatus  string
	FailingReason  string
	FailingMessage string
	Drifted        bool
	Consolidatable bool
	CapacityType   string // "spot" | "on-demand" | ""
	InstanceType   string
	AgeSeconds     int
}

type condition struct {
	Status  string
	Reason  string
	Message string
}

func conditionsOf(u *unstructured.Unstructured) map[string]condition {
	out := map[string]condition{}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		if t == "" {
			continue
		}
		st, _ := m["status"].(string)
		reason, _ := m["reason"].(string)
		msg, _ := m["message"].(string)
		out[t] = condition{Status: st, Reason: reason, Message: msg}
	}
	return out
}

// readyString maps a Ready condition: only "True" and "False" are evidence;
// a missing, Unknown or unsupported status is "unknown", never a fault.
func readyString(c condition, found bool) string {
	switch {
	case found && c.Status == "True":
		return "ready"
	case found && c.Status == "False":
		return "not-ready"
	default:
		return "unknown"
	}
}

func quantityPtr(s string) *float64 {
	if s == "" {
		return nil
	}
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return nil
	}
	v := q.AsApproximateFloat64()
	return &v
}

// ParseNodePool reads one NodePool CR (cluster-scoped).
func ParseNodePool(u *unstructured.Unstructured) NodePool {
	out := NodePool{Name: u.GetName()}
	conds := conditionsOf(u)
	ready, found := conds[condReady]
	out.Ready = readyString(ready, found)
	out.Reason, out.Message = ready.Reason, ready.Message

	out.Weight, _, _ = unstructured.NestedInt64(u.Object, "spec", "weight")

	cpuLimit, _, _ := unstructured.NestedString(u.Object, "spec", "limits", "cpu")
	memLimit, _, _ := unstructured.NestedString(u.Object, "spec", "limits", "memory")
	out.CPULimit = quantityPtr(cpuLimit)
	out.MemLimit = quantityPtr(memLimit)

	cpuUsed, _, _ := unstructured.NestedString(u.Object, "status", "resources", "cpu")
	memUsed, _, _ := unstructured.NestedString(u.Object, "status", "resources", "memory")
	out.CPUUsed = quantityPtr(cpuUsed)
	out.MemUsed = quantityPtr(memUsed)
	return out
}

// launchChain is the ordered NodeClaim lifecycle; the FIRST non-True link is
// the diagnosis (a claim that never launched is stuck earlier than one that
// launched but never registered).
var launchChain = []string{condLaunched, condRegistered, condInitialized}

// ParseNodeClaim reads one NodeClaim CR (cluster-scoped).
func ParseNodeClaim(u *unstructured.Unstructured, now time.Time) NodeClaim {
	labels := u.GetLabels()
	out := NodeClaim{
		Name:         u.GetName(),
		Pool:         labels[LabelNodePool],
		CapacityType: labels[LabelCapacityType],
		InstanceType: labels[LabelInstanceType],
		AgeSeconds:   int(now.Sub(u.GetCreationTimestamp().Time).Seconds()),
	}
	out.NodeName, _, _ = unstructured.NestedString(u.Object, "status", "nodeName")

	conds := conditionsOf(u)
	ready, found := conds[condReady]
	out.Ready = readyString(ready, found)

	if out.Ready != "ready" {
		// A confirmed False link is the diagnosis; failing that, the first
		// link not yet True (in progress / unverified), then Ready itself.
		pick := func(match func(condition) bool) bool {
			for _, t := range launchChain {
				if c, ok := conds[t]; ok && match(c) {
					out.FailingType, out.FailingStatus, out.FailingReason, out.FailingMessage = t, c.Status, c.Reason, c.Message
					return true
				}
			}
			return false
		}
		if !pick(func(c condition) bool { return c.Status == "False" }) && !pick(func(c condition) bool { return c.Status != "True" }) && found && ready.Status != "True" {
			out.FailingType, out.FailingStatus, out.FailingReason, out.FailingMessage = condReady, ready.Status, ready.Reason, ready.Message
		}
	}

	// Informational states, not errors: a drifted claim will be replaced, a
	// consolidatable one may be disrupted to save capacity.
	if c, ok := conds[condDrifted]; ok && c.Status == "True" {
		out.Drifted = true
	}
	if c, ok := conds[condConsolidatable]; ok && c.Status == "True" {
		out.Consolidatable = true
	}
	return out
}

// SortPoolsBrokenFirst: not-ready, then unknown, then ready; name-stable.
func SortPoolsBrokenFirst(pools []NodePool) {
	rank := map[string]int{"not-ready": 0, "unknown": 1, "ready": 2}
	sort.SliceStable(pools, func(i, j int) bool {
		if rank[pools[i].Ready] != rank[pools[j].Ready] {
			return rank[pools[i].Ready] < rank[pools[j].Ready]
		}
		return pools[i].Name < pools[j].Name
	})
}

// SortClaimsStuckFirst: not-ready first (the stuck ones), then unknown, then
// ready; within a rank, youngest first (the claim stuck for 40m matters more
// than the node running for 40d).
func SortClaimsStuckFirst(claims []NodeClaim) {
	rank := map[string]int{"not-ready": 0, "unknown": 1, "ready": 2}
	sort.SliceStable(claims, func(i, j int) bool {
		if rank[claims[i].Ready] != rank[claims[j].Ready] {
			return rank[claims[i].Ready] < rank[claims[j].Ready]
		}
		if claims[i].Ready != "ready" && claims[i].AgeSeconds != claims[j].AgeSeconds {
			return claims[i].AgeSeconds < claims[j].AgeSeconds
		}
		return claims[i].Name < claims[j].Name
	})
}

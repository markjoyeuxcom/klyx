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
	"fmt"
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

// NodePool is one pool's projection (coverage S10 §7): the lens row and
// the rule's authoritative fields, computed from every condition before the
// display cap. Limit/used pointers are nil when the pool declares no limit /
// reports no capacity - never a fabricated zero.
type NodePool struct {
	Meta     Meta
	Ref      *ClassRef // spec.template.spec.nodeClassRef; nil when absent
	Ready    string    // "ready" | "not-ready" | "unknown"
	Reason   string    // Ready's reason token
	Message  string    // Ready's native message: the local inspector only (§8)
	Weight   int64
	CPULimit *float64
	CPUUsed  *float64
	MemLimit *float64
	MemUsed  *float64
	Nodes    int // NodeClaims currently attributed to this pool (assembled by the caller)
	// ReadySince is the Ready condition's lastTransitionTime, zero when
	// absent (The Line §8.1).
	ReadySince time.Time
	// The authoritative conditions the rule reads (§3, §7); nil when absent.
	CondReady, CondValidation, CondNodeClassReady, CondRegistration *Cond
	// NodeClassObservedGeneration is the class generation registration
	// health last observed; 0 when not reported.
	NodeClassObservedGeneration int64
	Conds                       Conds
	size                        int
}

// NodeClaim is one claim's lifecycle row. FailingCondition names the first
// broken link of the launch chain when the claim is not Ready.
type NodeClaim struct {
	Meta        Meta
	Ref         *ClassRef // spec.nodeClassRef; nil when absent
	Pool        string
	NodeName    string
	Ready       string // "ready" | "not-ready" | "unknown"
	FailingType string // "" when Ready
	// FailingStatus is the chosen link's native condition status. Only
	// "False" proves that link failed; "Unknown" (or anything else) is a link
	// still in progress or unverified.
	FailingStatus string
	// FailingReason is a reason token; FailingReasonWithheld when the native
	// reason was outside the schema's pattern (§8).
	FailingReason         string
	FailingReasonWithheld bool
	// FailingMessage is the native message: the local inspector only (§8).
	FailingMessage string
	Drifted        bool
	Consolidatable bool
	CapacityType   string // "spot" | "on-demand" | ""
	InstanceType   string
	AgeSeconds     int
	Created        time.Time // creation timestamp: the launch-deadline boundary
	// ReadySince is the Ready condition's lastTransitionTime, zero when
	// absent (The Line §8.1).
	ReadySince time.Time
	size       int
}

// At returns the claim with its age relative to now: a projection is parsed
// once, so the evaluator ages it at each evaluation.
func (c NodeClaim) At(now time.Time) NodeClaim {
	c.AgeSeconds = int(now.Sub(c.Created).Seconds())
	return c
}

// readyString maps a Ready condition: only "True" and "False" are evidence;
// a missing, Unknown or unsupported status is "unknown", never a fault.
func readyString(c *Cond) string {
	switch {
	case c != nil && c.Status == "True":
		return "ready"
	case c != nil && c.Status == "False":
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

// The NodePool's own conditions (§1.1).
const (
	condValidation     = "ValidationSucceeded"
	condNodeClassReady = "NodeClassReady"
	condRegistration   = "NodeRegistrationHealthy"
)

// ParseNodePool projects one NodePool (cluster-scoped).
func ParseNodePool(u *unstructured.Unstructured) NodePool {
	out := NodePool{Meta: metaOf(u, true), Ref: refOf(u, "spec", "template", "spec", "nodeClassRef")}
	all := condsOf(u)
	out.Conds = display(all)
	var idx [4]int
	out.CondReady, idx[0] = find(all, condReady)
	out.CondValidation, idx[1] = find(all, condValidation)
	out.CondNodeClassReady, idx[2] = find(all, condNodeClassReady)
	out.CondRegistration, idx[3] = find(all, condRegistration)
	out.Ready = readyString(out.CondReady)
	if r := out.CondReady; r != nil {
		out.Reason, out.Message, out.ReadySince = r.Reason, r.Message, r.At
	}
	out.NodeClassObservedGeneration, _, _ = unstructured.NestedInt64(u.Object, "status", "nodeClassObservedGeneration")

	out.Weight, _, _ = unstructured.NestedInt64(u.Object, "spec", "weight")

	cpuLimit, _, _ := unstructured.NestedString(u.Object, "spec", "limits", "cpu")
	memLimit, _, _ := unstructured.NestedString(u.Object, "spec", "limits", "memory")
	out.CPULimit = quantityPtr(cpuLimit)
	out.MemLimit = quantityPtr(memLimit)

	cpuUsed, _, _ := unstructured.NestedString(u.Object, "status", "resources", "cpu")
	memUsed, _, _ := unstructured.NestedString(u.Object, "status", "resources", "memory")
	out.CPUUsed = quantityPtr(cpuUsed)
	out.MemUsed = quantityPtr(memUsed)

	n := metaSize(out.Meta) + 160 + refSize(out.Ref) + condsSize(out.Conds)
	for i, c := range []*Cond{out.CondReady, out.CondValidation, out.CondNodeClassReady, out.CondRegistration} {
		n += authSize(c, idx[i])
	}
	out.size = n
	return out
}

// launchChain is the ordered NodeClaim lifecycle; the FIRST non-True link is
// the diagnosis (a claim that never launched is stuck earlier than one that
// launched but never registered).
var launchChain = []string{condLaunched, condRegistered, condInitialized}

// ParseNodeClaim projects one NodeClaim (cluster-scoped), aged at now.
func ParseNodeClaim(u *unstructured.Unstructured, now time.Time) NodeClaim {
	labels := u.GetLabels()
	out := NodeClaim{
		Meta:         metaOf(u, false),
		Ref:          refOf(u, "spec", "nodeClassRef"),
		Pool:         clip(labels[LabelNodePool], maxName),
		CapacityType: clip(labels[LabelCapacityType], maxStatus),
		InstanceType: clip(labels[LabelInstanceType], maxToken),
		Created:      u.GetCreationTimestamp().Time,
	}
	out = out.At(now)
	out.NodeName = str(u, maxName, "status", "nodeName")

	all := condsOf(u)
	ready, _ := find(all, condReady)
	out.Ready = readyString(ready)
	if ready != nil {
		out.ReadySince = ready.At
	}

	if out.Ready != "ready" {
		// A confirmed False link is the diagnosis; failing that, the first
		// link not yet True (in progress / unverified), then Ready itself.
		set := func(t string, c *Cond) {
			out.FailingType, out.FailingStatus = t, c.Status
			out.FailingReason, out.FailingReasonWithheld, out.FailingMessage = c.Reason, c.ReasonWithheld, c.Message
		}
		pick := func(match func(*Cond) bool) bool {
			for _, t := range launchChain {
				if c, _ := find(all, t); c != nil && match(c) {
					set(t, c)
					return true
				}
			}
			return false
		}
		if !pick(func(c *Cond) bool { return c.Status == "False" }) && !pick(func(c *Cond) bool { return c.Status != "True" }) && ready != nil && ready.Status != "True" {
			set(condReady, ready)
		}
	}

	// Informational states, not errors: a drifted claim will be replaced, a
	// consolidatable one may be disrupted to save capacity.
	if c, _ := find(all, condDrifted); c != nil && c.Status == "True" {
		out.Drifted = true
	}
	if c, _ := find(all, condConsolidatable); c != nil && c.Status == "True" {
		out.Consolidatable = true
	}
	out.size = metaSize(out.Meta) + 200 + refSize(out.Ref) + len(out.Pool) + len(out.CapacityType) + len(out.InstanceType) +
		len(out.NodeName) + len(out.FailingType) + len(out.FailingStatus) + len(out.FailingReason) + len(out.FailingMessage)
	return out
}

// SizeOf is the budget's Size function for every Karpenter projection.
func SizeOf(v any) int {
	switch o := v.(type) {
	case *NodePool:
		return o.size
	case *NodeClaim:
		return o.size
	case *NodeClass:
		return o.size
	}
	return MaxClassObject + 1 // never retained
}

// SortPoolsBrokenFirst: not-ready, then unknown, then ready; name-stable.
func SortPoolsBrokenFirst(pools []NodePool) {
	rank := map[string]int{"not-ready": 0, "unknown": 1, "ready": 2}
	sort.SliceStable(pools, func(i, j int) bool {
		if rank[pools[i].Ready] != rank[pools[j].Ready] {
			return rank[pools[i].Ready] < rank[pools[j].Ready]
		}
		return pools[i].Meta.Name < pools[j].Meta.Name
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
		return claims[i].Meta.Name < claims[j].Meta.Name
	})
}

// The karpenter.sh resources (§2).
const (
	Group              = "karpenter.sh"
	Version            = "v1"
	ResourceNodePools  = "nodepools"
	ResourceNodeClaims = "nodeclaims"
)

// Project is the KindSpec projection for one Karpenter resource: the core
// kinds and the three class kinds (§7). A claim is projected unaged; the
// evaluator ages it (NodeClaim.At).
func Project(resource string) func(obj any) (any, error) {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("karpenter: want *Unstructured, got %T", obj)
		}
		switch resource {
		case ResourceNodePools:
			p := ParseNodePool(u)
			return &p, nil
		case ResourceNodeClaims:
			c := ParseNodeClaim(u, u.GetCreationTimestamp().Time)
			return &c, nil
		}
		for _, k := range ClassKinds {
			if k.Resource == resource {
				c := ParseNodeClass(u, k.Adapter)
				return &c, nil
			}
		}
		return nil, fmt.Errorf("karpenter: no projection for %s", resource)
	}
}

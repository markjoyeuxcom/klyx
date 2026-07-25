package fleet

import (
	"context"
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/karpenter"
	"github.com/moomora/klyx/internal/workloads"
)

// karpenterEventCap bounds the disruption feed: it is recent activity, not an
// audit (the full event stream lives in the Events lens).
const karpenterEventCap = 30

// KarpenterSnapshot is one cluster's Karpenter state for the lens: pools
// broken-first, claims stuck-first, controller health re-probed live (the
// capability snapshot is connect-time; the lens headline must not go stale),
// and recent NodePool/NodeClaim events - the "why did my node get replaced"
// feed.
type KarpenterSnapshot struct {
	Detected          bool
	ControllerHealthy bool
	Pools             []karpenter.NodePool
	Claims            []karpenter.NodeClaim
	Events            []workloads.EventSummary
}

// KarpenterState returns the Karpenter lens snapshot. Capability-gated
// (honest "not detected", never an empty lens); dynamic lists on demand, no
// lazy watch - pool/claim counts are small.
func (c *ClusterConn) KarpenterState(ctx context.Context) (KarpenterSnapshot, error) {
	c.mu.RLock()
	present := c.caps.Karpenter.Present
	c.mu.RUnlock()
	if !present {
		return KarpenterSnapshot{Detected: false}, nil
	}
	if c.dyn == nil {
		return KarpenterSnapshot{}, fmt.Errorf("dynamic client not available")
	}
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	now := clk.Now()

	out := KarpenterSnapshot{Detected: true}

	ver := preferredVersion(c.discovery(), "karpenter.sh", "v1")
	poolGVR := schema.GroupVersionResource{Group: "karpenter.sh", Version: ver, Resource: "nodepools"}
	claimGVR := schema.GroupVersionResource{Group: "karpenter.sh", Version: ver, Resource: "nodeclaims"}

	poolList, err := c.dyn.Resource(poolGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return KarpenterSnapshot{}, fmt.Errorf("list nodepools: %w", err)
	}
	for i := range poolList.Items {
		out.Pools = append(out.Pools, karpenter.ParseNodePool(&unstructured.Unstructured{Object: poolList.Items[i].Object}))
	}

	claimList, err := c.dyn.Resource(claimGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return KarpenterSnapshot{}, fmt.Errorf("list nodeclaims: %w", err)
	}
	claimsPerPool := map[string]int{}
	for i := range claimList.Items {
		claim := karpenter.ParseNodeClaim(&unstructured.Unstructured{Object: claimList.Items[i].Object}, now)
		out.Claims = append(out.Claims, claim)
		claimsPerPool[claim.Pool]++
	}
	for i := range out.Pools {
		out.Pools[i].Nodes = claimsPerPool[out.Pools[i].Name]
	}
	karpenter.SortPoolsBrokenFirst(out.Pools)
	karpenter.SortClaimsStuckFirst(out.Claims)

	// Controller health, re-probed live (mirrors the detector's namespace pair).
	for _, ns := range []string{"kube-system", "karpenter"} {
		if dep, derr := c.typed.AppsV1().Deployments(ns).Get(ctx, "karpenter", metav1.GetOptions{}); derr == nil && dep != nil {
			if dep.Status.ReadyReplicas > 0 {
				out.ControllerHealthy = true
			}
			break
		}
	}

	// Disruption feed: events about NodeClaims and NodePools (launch failures,
	// disruption/consolidation decisions, drift replacements). Best-effort: a
	// failing event list degrades to an empty feed, the triage above stands.
	for _, kind := range []string{"NodeClaim", "NodePool"} {
		evs, eerr := c.typed.CoreV1().Events("").List(ctx, metav1.ListOptions{
			FieldSelector: "involvedObject.kind=" + kind,
			Limit:         karpenterEventCap,
		})
		if eerr != nil || evs == nil {
			continue
		}
		for i := range evs.Items {
			e := &evs.Items[i]
			// Kind from the event itself, and a client-side guard: a server
			// (or fake) that ignores the field selector must not duplicate
			// rows across the two queries.
			if e.InvolvedObject.Kind != kind {
				continue
			}
			last := e.LastTimestamp.Time
			if last.IsZero() {
				last = e.CreationTimestamp.Time
			}
			out.Events = append(out.Events, workloads.EventSummary{
				Type: e.Type, Reason: e.Reason, Message: e.Message, Count: e.Count,
				Namespace: e.Namespace, Kind: e.InvolvedObject.Kind, Name: e.InvolvedObject.Name,
				LastSeenUnix: last.Unix(),
			})
		}
	}
	sort.Slice(out.Events, func(i, j int) bool { return out.Events[i].LastSeenUnix > out.Events[j].LastSeenUnix })
	if len(out.Events) > karpenterEventCap {
		out.Events = out.Events[:karpenterEventCap]
	}
	return out, nil
}

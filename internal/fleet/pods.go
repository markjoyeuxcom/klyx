package fleet

import (
	"context"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/workloads"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// liveListCap bounds a single pods/workloads live list so a pathological cluster
// can never OOM the render (the DOM is already virtualized; this caps the objects
// held in memory). It is deliberately high: at the owner's fleet size a namespace
// - or even all-namespaces - list never approaches it, so the broken-first
// ranking that happens AFTER the list stays intact in practice. When the cap IS
// hit the result is flagged truncated and the UI shows a "scope by namespace"
// banner, so a broken pod past the cap is never silently passed off as the full
// triage picture.
const liveListCap = 5000

// ListPods lists pods scoped to namespace ("" = all; set namespace scopes the
// typed list at source) and classifies them with the shared severity engine.
// The second return reports whether the list was truncated at liveListCap.
// On-demand; no watch.
func (c *ClusterConn) ListPods(ctx context.Context, namespace string) ([]workloads.PodSummary, bool, error) {
	pods, err := c.typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: liveListCap})
	if err != nil {
		return nil, false, err
	}

	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	sums := workloads.SummarizePods(pods.Items, clk.Now())
	c.resolveReplicaSetOwners(ctx, namespace, sums)
	return sums, pods.GetContinue() != "", nil
}

var replicaSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}

// resolveReplicaSetOwners rewrites each pod's ReplicaSet owner to the RS's own
// controller (Deployment, Argo Rollout, ...) with one metadata-only LIST. The
// UI's restart-owner verb needs the real controller: deriving a Deployment
// name by stripping the pod-template-hash guesses wrong for bare ReplicaSets
// and Rollouts. Pods under a bare ReplicaSet keep OwnerKind=ReplicaSet (there
// is no controller to restart). Best-effort: on any error the summaries keep
// the direct RS owner.
func (c *ClusterConn) resolveReplicaSetOwners(ctx context.Context, namespace string, sums []workloads.PodSummary) {
	need := false
	for i := range sums {
		if sums[i].OwnerKind == "ReplicaSet" {
			need = true
			break
		}
	}
	if !need || c.meta == nil {
		return
	}
	list, err := c.meta.Resource(replicaSetGVR).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: liveListCap})
	if err != nil {
		return
	}
	owner := map[string]metav1.OwnerReference{} // "ns/name" -> RS's controller ref
	for i := range list.Items {
		m := &list.Items[i]
		for _, ref := range m.OwnerReferences {
			if ref.Controller != nil && *ref.Controller {
				owner[m.Namespace+"/"+m.Name] = ref
				break
			}
		}
	}
	for i := range sums {
		if sums[i].OwnerKind != "ReplicaSet" {
			continue
		}
		if ref, ok := owner[sums[i].Namespace+"/"+sums[i].OwnerName]; ok {
			sums[i].OwnerKind = ref.Kind
			sums[i].OwnerName = ref.Name
		}
	}
}

package fleet

import (
	"context"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/workloads"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ListPods lists pods scoped to namespace ("" = all; set namespace scopes the
// typed list at source) and classifies them with the shared severity engine.
// On-demand; no watch.
func (c *ClusterConn) ListPods(ctx context.Context, namespace string) ([]workloads.PodSummary, error) {
	pods, err := c.typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	sums := workloads.SummarizePods(pods.Items, clk.Now())
	c.resolveReplicaSetOwners(ctx, namespace, sums)
	return sums, nil
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
	list, err := c.meta.Resource(replicaSetGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
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

package fleet

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/workloads"
)

// ListEvents lists events scoped to namespace ("" = all) sorted warning-first.
// On-demand; no watch. Capped server-side via limit to keep huge clusters fast.
//
// truncated reports that the apiserver had more events than the cap. The cap
// returns the first 500 in etcd key order (effectively arbitrary), so on a
// busy cluster the newest warnings can be missing from the "warning-first"
// sort - the UI must say so rather than present the subset as complete.
func (c *ClusterConn) ListEvents(ctx context.Context, namespace string) ([]workloads.EventSummary, bool, error) {
	list, err := c.typed.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{Limit: 500})
	if err != nil {
		return nil, false, err
	}
	truncated := list.Continue != "" || (list.RemainingItemCount != nil && *list.RemainingItemCount > 0)
	return workloads.SummarizeEvents(list.Items), truncated, nil
}

package fleet

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// CheckWorkloadArmed confirms the workload at kind namespace/name is the
// object a verb was armed against (its UID, design §5b) before anything is
// written. A missing workload or another UID is ErrReplaced.
func (c *ClusterConn) CheckWorkloadArmed(ctx context.Context, kind, namespace, name, uid string) error {
	what := fmt.Sprintf("%s %s/%s", kind, namespace, name)
	if uid == "" {
		return fmt.Errorf("%s: no object identity was armed - reload the list and retry", what)
	}
	var got types.UID
	var err error
	switch kind {
	case "Deployment":
		d, e := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err = e; e == nil {
			got = d.UID
		}
	case "StatefulSet":
		s, e := c.typed.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err = e; e == nil {
			got = s.UID
		}
	case "DaemonSet":
		d, e := c.typed.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err = e; e == nil {
			got = d.UID
		}
	default:
		return fmt.Errorf("unsupported kind %q: must be Deployment, StatefulSet, or DaemonSet", kind)
	}
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%s: %w", what, ErrReplaced)
	case err != nil:
		return fmt.Errorf("%s: %w", what, err)
	case string(got) != uid:
		return fmt.Errorf("%s: %w", what, ErrReplaced)
	}
	return nil
}

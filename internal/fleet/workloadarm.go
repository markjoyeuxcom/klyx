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
	_, err := c.armedWorkload(ctx, kind, namespace, name, uid)
	return err
}

// armedWorkload is CheckWorkloadArmed returning the armed read's
// generation (A3 §5.2: a restart's receipt must be above it).
func (c *ClusterConn) armedWorkload(ctx context.Context, kind, namespace, name, uid string) (int64, error) {
	what := fmt.Sprintf("%s %s/%s", kind, namespace, name)
	if uid == "" {
		return 0, fmt.Errorf("%s: no object identity was armed - reload the list and retry", what)
	}
	var got types.UID
	var gen int64
	var err error
	switch kind {
	case "Deployment":
		d, e := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err = e; e == nil {
			got, gen = d.UID, d.Generation
		}
	case "StatefulSet":
		s, e := c.typed.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err = e; e == nil {
			got, gen = s.UID, s.Generation
		}
	case "DaemonSet":
		d, e := c.typed.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err = e; e == nil {
			got, gen = d.UID, d.Generation
		}
	default:
		return 0, fmt.Errorf("unsupported kind %q: must be Deployment, StatefulSet, or DaemonSet", kind)
	}
	switch {
	case apierrors.IsNotFound(err):
		return 0, fmt.Errorf("%s: %w", what, ErrReplaced)
	case err != nil:
		return 0, fmt.Errorf("%s: %w", what, err)
	case string(got) != uid:
		return 0, fmt.Errorf("%s: %w", what, ErrReplaced)
	}
	return gen, nil
}

// CheckPodArmed confirms the pod at namespace/name is the object a verb was
// armed against (its UID, design §5b). It fences a verb that can only name
// a pod (kubectl debug); a delete uses the server's UID precondition instead.
func (c *ClusterConn) CheckPodArmed(ctx context.Context, namespace, name, uid string) error {
	what := fmt.Sprintf("pod %s/%s", namespace, name)
	if uid == "" {
		return fmt.Errorf("%s: no object identity was armed - reload the list and retry", what)
	}
	p, err := c.typed.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%s: %w", what, ErrReplaced)
	case err != nil:
		return fmt.Errorf("%s: %w", what, err)
	case string(p.UID) != uid:
		return fmt.Errorf("%s: %w", what, ErrReplaced)
	}
	return nil
}

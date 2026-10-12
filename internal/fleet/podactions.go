package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/clock"
)

// DeletePod deletes the ARMED pod (uid, design §5b). The owning controller
// (ReplicaSet, StatefulSet, DaemonSet) recreates it - this is the standard
// imperative bounce. The delete carries the UID as a precondition, so the
// server never deletes a same-name replacement (a StatefulSet's recreated
// pod-0, say): that is ErrReplaced, and a pod already gone says so.
func (c *ClusterConn) DeletePod(ctx context.Context, namespace, name, uid string) error {
	what := fmt.Sprintf("pod %s/%s", namespace, name)
	if uid == "" {
		return fmt.Errorf("delete %s: no object identity was armed - reload the list and retry", what)
	}
	u := types.UID(uid)
	err := c.typed.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &u}})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("delete %s: it is already gone - nothing was deleted: %w", what, err)
	}
	return armedWriteErr("delete", what, err)
}

// RolloutRestart triggers a rolling restart of the ARMED workload (uid,
// design §5b) by patching the pod-template restartedAt annotation - exactly
// what kubectl rollout restart does. The workload is confirmed first and the
// patch carries metadata.uid, so a same-name replacement is refused by the
// server. kind must be "Deployment", "StatefulSet", or "DaemonSet".
func (c *ClusterConn) RolloutRestart(ctx context.Context, kind, namespace, name, uid string) error {
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	ts := clk.Now().UTC().Format(time.RFC3339)

	if err := c.CheckWorkloadArmed(ctx, kind, namespace, name, uid); err != nil {
		return fmt.Errorf("rollout restart %w - nothing was written", err)
	}
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{"uid": uid},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"annotations": map[string]interface{}{
						"kubectl.kubernetes.io/restartedAt": ts,
					},
				},
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("rollout restart marshal: %w", err)
	}

	switch kind {
	case "Deployment":
		_, err = c.typed.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{})
	case "StatefulSet":
		_, err = c.typed.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{})
	case "DaemonSet":
		_, err = c.typed.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{})
	}
	return armedWriteErr("rollout restart", fmt.Sprintf("%s %s/%s", kind, namespace, name), err)
}

package fleet

import (
	"context"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/admission"
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
		return notSent(fmt.Errorf("delete %s: no object identity was armed - reload the list and retry", what))
	}
	u := types.UID(uid)
	err := c.typed.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &u}})
	err = admission.Wrap(admission.Request{Operation: "DELETE", Version: "v1", Resource: "pods", Namespace: namespace}, "Pod", namespace, name, err)
	// a webhook denying with NotFound is its denial, never the pod gone
	// (Codex's implementation review, finding 7)
	if apierrors.IsNotFound(err) && !admission.Classify(statusOf(err)).Attributed() {
		return fmt.Errorf("delete %s: it is already gone - nothing was deleted: %w", what, err)
	}
	if err == nil {
		// the UID precondition held: the server deleted exactly this pod
		NoteReceipt(ctx, Receipt{UID: uid})
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
	// this attempt's own token (A3 §1): unique in the process, so two
	// restarts are distinct templates and the token is this request's witness
	ts := attemptToken(clk.Now())

	prior, err := c.armedWorkload(ctx, kind, namespace, name, uid)
	if err != nil {
		return notSent(fmt.Errorf("rollout restart %w - nothing was written", err))
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
		return notSent(fmt.Errorf("rollout restart marshal: %w", err))
	}

	NoteAttempt(ctx, Attempt{UID: uid, Token: ts})
	var meta metav1.Object
	switch kind {
	case "Deployment":
		var out *appsv1.Deployment
		if out, err = c.typed.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{}); err == nil {
			meta = out
		}
	case "StatefulSet":
		var out *appsv1.StatefulSet
		if out, err = c.typed.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{}); err == nil {
			meta = out
		}
	case "DaemonSet":
		var out *appsv1.DaemonSet
		if out, err = c.typed.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{}); err == nil {
			meta = out
		}
	}
	err = admission.Wrap(admission.Request{Operation: "UPDATE", Group: "apps", Version: "v1", Resource: workloadResource(kind), Namespace: namespace}, kind, namespace, name, err)
	if err == nil && meta != nil {
		NoteReceipt(ctx, Receipt{UID: string(meta.GetUID()), Generation: meta.GetGeneration(), ResourceVersion: meta.GetResourceVersion(), Token: ts, PriorGeneration: prior})
	}
	return armedWriteErr("rollout restart", fmt.Sprintf("%s %s/%s", kind, namespace, name), err)
}

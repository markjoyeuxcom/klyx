package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/workloads"
)

const deployRevisionAnn = "deployment.kubernetes.io/revision"
const changeCauseAnn = "kubernetes.io/change-cause"

// RolloutHistory returns a workload's revision history, newest first.
// Deployments read their owned ReplicaSets (the revision annotation is the
// join key, exactly kubectl's view); StatefulSets/DaemonSets read their owned
// ControllerRevisions. Current is annotation-derived for Deployments,
// updateRevision-derived for StatefulSets, and highest-revision for DaemonSets
// (their status names no revision). It also returns the UID of the workload
// it read: the history is that object's, never a same-name replacement's
// (design §5b).
func (c *ClusterConn) RolloutHistory(ctx context.Context, kind, namespace, name string) ([]workloads.RolloutRevision, types.UID, error) {
	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	now := clk.Now()

	switch kind {
	case "Deployment":
		return c.deploymentHistory(ctx, namespace, name, now)
	case "StatefulSet", "DaemonSet":
		return c.controllerRevisionHistory(ctx, kind, namespace, name, now)
	default:
		return nil, "", fmt.Errorf("unsupported kind %q: must be Deployment, StatefulSet, or DaemonSet", kind)
	}
}

func (c *ClusterConn) deploymentHistory(ctx context.Context, namespace, name string, now time.Time) ([]workloads.RolloutRevision, types.UID, error) {
	dep, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	currentRev, _ := strconv.ParseInt(dep.Annotations[deployRevisionAnn], 10, 64)

	rss, err := c.typed.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("list replicasets in %s: %w", namespace, err)
	}
	var out []workloads.RolloutRevision
	for i := range rss.Items {
		rs := &rss.Items[i]
		if !ownedByUID(rs.OwnerReferences, dep.UID) {
			continue
		}
		rev, perr := strconv.ParseInt(rs.Annotations[deployRevisionAnn], 10, 64)
		if perr != nil {
			continue // an RS without the revision annotation is not history
		}
		out = append(out, workloads.RolloutRevision{
			Revision:    rev,
			AgeSeconds:  int(now.Sub(rs.CreationTimestamp.Time).Seconds()),
			Images:      workloads.TemplateImages(rs.Spec.Template.Spec),
			Current:     rev == currentRev,
			ChangeCause: rs.Annotations[changeCauseAnn],
		})
	}
	sortRevisionsDesc(out)
	return out, dep.UID, nil
}

func (c *ClusterConn) controllerRevisionHistory(ctx context.Context, kind, namespace, name string, now time.Time) ([]workloads.RolloutRevision, types.UID, error) {
	uid, updateRevName, err := c.rolloutTargetIdentity(ctx, kind, namespace, name)
	if err != nil {
		return nil, "", err
	}
	crs, err := c.typed.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("list controllerrevisions in %s: %w", namespace, err)
	}
	var maxRev int64
	for i := range crs.Items {
		if ownedByUID(crs.Items[i].OwnerReferences, uid) && crs.Items[i].Revision > maxRev {
			maxRev = crs.Items[i].Revision
		}
	}
	var out []workloads.RolloutRevision
	for i := range crs.Items {
		cr := &crs.Items[i]
		if !ownedByUID(cr.OwnerReferences, uid) {
			continue
		}
		current := cr.Name == updateRevName
		if updateRevName == "" { // DaemonSet: status names no revision
			current = cr.Revision == maxRev
		}
		out = append(out, workloads.RolloutRevision{
			Revision:    cr.Revision,
			AgeSeconds:  int(now.Sub(cr.CreationTimestamp.Time).Seconds()),
			Images:      workloads.RevisionDataImages(cr.Data.Raw),
			Current:     current,
			ChangeCause: cr.Annotations[changeCauseAnn],
		})
	}
	sortRevisionsDesc(out)
	return out, uid, nil
}

// rolloutTargetIdentity returns the workload's UID plus, for StatefulSets, the
// status.updateRevision name that marks the current ControllerRevision.
func (c *ClusterConn) rolloutTargetIdentity(ctx context.Context, kind, namespace, name string) (types.UID, string, error) {
	switch kind {
	case "StatefulSet":
		sts, err := c.typed.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", "", fmt.Errorf("get statefulset %s/%s: %w", namespace, name, err)
		}
		return sts.UID, sts.Status.UpdateRevision, nil
	default: // DaemonSet
		ds, err := c.typed.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", "", fmt.Errorf("get daemonset %s/%s: %w", namespace, name, err)
		}
		return ds.UID, "", nil
	}
}

// RolloutUndo rolls a workload back to a prior revision with kubectl's own
// semantics: for a Deployment the target ReplicaSet's pod template (minus the
// pod-template-hash label) replaces spec.template; for StatefulSets and
// DaemonSets the target ControllerRevision's data IS the patch. Undoing to the
// CURRENT revision is refused - a no-op undo presented as success would be a
// lie. Destructive by design: the caller (appbridge) gates it behind the
// protected-cluster grant. uid is the workload the undo was armed against
// (design §5b): it is confirmed before the history is read, and the write
// itself is guarded by it (a JSON-patch test for a Deployment, metadata.uid
// for a StatefulSet/DaemonSet patch).
func (c *ClusterConn) RolloutUndo(ctx context.Context, kind, namespace, name, uid string, revision int64) error {
	if err := c.CheckWorkloadArmed(ctx, kind, namespace, name, uid); err != nil {
		return fmt.Errorf("rollout undo %w - nothing was written", err)
	}
	history, histUID, err := c.RolloutHistory(ctx, kind, namespace, name)
	if err != nil {
		return err
	}
	if string(histUID) != uid { // replaced between the check and the history read
		return fmt.Errorf("rollout undo %s %s/%s: %w - nothing was written", kind, namespace, name, ErrReplaced)
	}
	var target *workloads.RolloutRevision
	for i := range history {
		if history[i].Revision == revision {
			target = &history[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("revision %d not found in %s %s/%s history", revision, kind, namespace, name)
	}
	if target.Current {
		return fmt.Errorf("%s %s/%s is already at revision %d", kind, namespace, name, revision)
	}

	switch kind {
	case "Deployment":
		return c.undoDeployment(ctx, namespace, name, uid, revision)
	case "StatefulSet", "DaemonSet":
		return c.undoFromControllerRevision(ctx, kind, namespace, name, uid, revision)
	default:
		return fmt.Errorf("unsupported kind %q", kind)
	}
}

func (c *ClusterConn) undoDeployment(ctx context.Context, namespace, name, uid string, revision int64) error {
	dep, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	if string(dep.UID) != uid {
		return fmt.Errorf("rollout undo Deployment %s/%s: %w - nothing was written", namespace, name, ErrReplaced)
	}
	rss, err := c.typed.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list replicasets in %s: %w", namespace, err)
	}
	var target *appsv1.ReplicaSet
	for i := range rss.Items {
		rs := &rss.Items[i]
		if !ownedByUID(rs.OwnerReferences, dep.UID) {
			continue
		}
		if rev, perr := strconv.ParseInt(rs.Annotations[deployRevisionAnn], 10, 64); perr == nil && rev == revision {
			target = rs
			break
		}
	}
	if target == nil {
		return fmt.Errorf("replicaset for revision %d of %s/%s not found", revision, namespace, name)
	}

	body, err := deploymentUndoPatch(dep, target)
	if err != nil {
		return fmt.Errorf("rollout undo marshal: %w", err)
	}
	_, err = c.typed.AppsV1().Deployments(namespace).Patch(ctx, name, types.JSONPatchType, body, metav1.PatchOptions{})
	if apierrors.IsInvalid(err) {
		// A failed JSON-patch op is a 422 that does not say which op failed:
		// name a replacement only when one is confirmed (the uid test op).
		if cerr := c.CheckWorkloadArmed(ctx, "Deployment", namespace, name, uid); errors.Is(cerr, ErrReplaced) {
			return fmt.Errorf("rollout undo Deployment %s/%s to revision %d: %w - its write was refused (%v)", namespace, name, revision, ErrReplaced, err)
		}
	}
	return armedWriteErr("rollout undo", fmt.Sprintf("Deployment %s/%s to revision %d", namespace, name, revision), err)
}

// deploymentUndoPatch builds the JSON patch that rolls dep back to target's
// pod template, the way kubectl rollout undo does: the old ReplicaSet template
// (minus the synthetic pod-template-hash label) REPLACES spec.template. A
// strategic merge would keep every container, env var and label added after
// the target revision - a hybrid of old and new reported as a successful undo.
//
// The patch also:
//   - tests metadata.uid first, so a Deployment deleted and recreated under the
//     same name between our read and the patch is never rolled back blind;
//   - carries the target revision's change-cause onto the Deployment (or drops
//     the current one when the target has none), so the rollout history does
//     not attribute the rollback to the newer change. This is deliberately
//     narrower than kubectl, which restores every non-bookkeeping annotation of
//     the target ReplicaSet: Klyx moves change-cause only and otherwise leaves
//     the Deployment's annotations untouched - no claim of full metadata parity.
func deploymentUndoPatch(dep *appsv1.Deployment, target *appsv1.ReplicaSet) ([]byte, error) {
	tmpl := target.Spec.Template.DeepCopy()
	delete(tmpl.Labels, appsv1.DefaultDeploymentUniqueLabelKey)

	ops := []map[string]interface{}{
		{"op": "test", "path": "/metadata/uid", "value": string(dep.UID)},
		{"op": "replace", "path": "/spec/template", "value": tmpl},
	}
	causePath := "/metadata/annotations/" + jsonPointerEscape(changeCauseAnn)
	cause := target.Annotations[changeCauseAnn]
	_, depHasCause := dep.Annotations[changeCauseAnn]
	switch {
	case cause != "" && len(dep.Annotations) == 0:
		// No annotations parent in the serialised object (an empty map is
		// omitted), so add the whole map rather than a nested key.
		ops = append(ops, map[string]interface{}{"op": "add", "path": "/metadata/annotations", "value": map[string]string{changeCauseAnn: cause}})
	case cause != "":
		ops = append(ops, map[string]interface{}{"op": "add", "path": causePath, "value": cause})
	case depHasCause:
		ops = append(ops, map[string]interface{}{"op": "remove", "path": causePath})
	}
	return json.Marshal(ops)
}

// jsonPointerEscape escapes one RFC 6901 reference token ("~" -> "~0",
// "/" -> "~1"), e.g. for annotation keys like kubernetes.io/change-cause.
func jsonPointerEscape(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func (c *ClusterConn) undoFromControllerRevision(ctx context.Context, kind, namespace, name, armed string, revision int64) error {
	uid, _, err := c.rolloutTargetIdentity(ctx, kind, namespace, name)
	if err != nil {
		return err
	}
	if string(uid) != armed {
		return fmt.Errorf("rollout undo %s %s/%s: %w - nothing was written", kind, namespace, name, ErrReplaced)
	}
	crs, err := c.typed.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list controllerrevisions in %s: %w", namespace, err)
	}
	for i := range crs.Items {
		cr := &crs.Items[i]
		if ownedByUID(cr.OwnerReferences, uid) && cr.Revision == revision {
			// kubectl's rollback for apps/v1: the revision data IS the patch,
			// guarded by the armed UID.
			body, gerr := guardUID(cr.Data.Raw, armed)
			if gerr != nil {
				return fmt.Errorf("rollout undo %s %s/%s: controllerrevision %d data: %w", kind, namespace, name, revision, gerr)
			}
			var perr error
			switch kind {
			case "StatefulSet":
				_, perr = c.typed.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{})
			default:
				_, perr = c.typed.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{})
			}
			return armedWriteErr("rollout undo", fmt.Sprintf("%s %s/%s to revision %d", kind, namespace, name, revision), perr)
		}
	}
	return fmt.Errorf("controllerrevision %d of %s %s/%s not found", revision, kind, namespace, name)
}

// RolloutPause sets the ARMED Deployment's spec.paused (uid, design §5b) -
// holds rollouts without touching pods. Reversible and Deployment-only (the
// API has no paused on StatefulSet/DaemonSet). The patch carries metadata.uid
// so a same-name replacement is refused by the server.
func (c *ClusterConn) RolloutPause(ctx context.Context, namespace, name, uid string, pause bool) error {
	verb := "rollout pause"
	if !pause {
		verb = "rollout resume"
	}
	if err := c.CheckWorkloadArmed(ctx, "Deployment", namespace, name, uid); err != nil {
		return fmt.Errorf("%s %w - nothing was written", verb, err)
	}
	body, err := json.Marshal(map[string]interface{}{"metadata": map[string]string{"uid": uid}, "spec": map[string]bool{"paused": pause}})
	if err != nil {
		return fmt.Errorf("%s marshal: %w", verb, err)
	}
	_, err = c.typed.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{})
	return armedWriteErr(verb, fmt.Sprintf("Deployment %s/%s", namespace, name), err)
}

func ownedByUID(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, r := range refs {
		if r.UID == uid {
			return true
		}
	}
	return false
}

func sortRevisionsDesc(revs []workloads.RolloutRevision) {
	sort.Slice(revs, func(i, j int) bool { return revs[i].Revision > revs[j].Revision })
}

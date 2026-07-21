package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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
// (their status names no revision).
func (c *ClusterConn) RolloutHistory(ctx context.Context, kind, namespace, name string) ([]workloads.RolloutRevision, error) {
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
		return nil, fmt.Errorf("unsupported kind %q: must be Deployment, StatefulSet, or DaemonSet", kind)
	}
}

func (c *ClusterConn) deploymentHistory(ctx context.Context, namespace, name string, now time.Time) ([]workloads.RolloutRevision, error) {
	dep, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	currentRev, _ := strconv.ParseInt(dep.Annotations[deployRevisionAnn], 10, 64)

	rss, err := c.typed.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list replicasets in %s: %w", namespace, err)
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
	return out, nil
}

func (c *ClusterConn) controllerRevisionHistory(ctx context.Context, kind, namespace, name string, now time.Time) ([]workloads.RolloutRevision, error) {
	uid, updateRevName, err := c.rolloutTargetIdentity(ctx, kind, namespace, name)
	if err != nil {
		return nil, err
	}
	crs, err := c.typed.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list controllerrevisions in %s: %w", namespace, err)
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
	return out, nil
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
// pod-template-hash label) is patched into spec.template; for StatefulSets and
// DaemonSets the target ControllerRevision's data IS the patch. Undoing to the
// CURRENT revision is refused - a no-op undo presented as success would be a
// lie. Destructive by design: the caller (appbridge) gates it behind the
// protected-cluster grant.
func (c *ClusterConn) RolloutUndo(ctx context.Context, kind, namespace, name string, revision int64) error {
	history, err := c.RolloutHistory(ctx, kind, namespace, name)
	if err != nil {
		return err
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
		return c.undoDeployment(ctx, namespace, name, revision)
	case "StatefulSet", "DaemonSet":
		return c.undoFromControllerRevision(ctx, kind, namespace, name, revision)
	default:
		return fmt.Errorf("unsupported kind %q", kind)
	}
}

func (c *ClusterConn) undoDeployment(ctx context.Context, namespace, name string, revision int64) error {
	dep, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
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

	// kubectl rollout undo: the old RS template becomes the deployment
	// template, minus the synthetic pod-template-hash label.
	tmpl := target.Spec.Template.DeepCopy()
	delete(tmpl.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	patch := map[string]interface{}{"spec": map[string]interface{}{"template": tmpl}}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("rollout undo marshal: %w", err)
	}
	if _, err := c.typed.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("rollout undo deployment %s/%s to revision %d: %w", namespace, name, revision, err)
	}
	return nil
}

func (c *ClusterConn) undoFromControllerRevision(ctx context.Context, kind, namespace, name string, revision int64) error {
	uid, _, err := c.rolloutTargetIdentity(ctx, kind, namespace, name)
	if err != nil {
		return err
	}
	crs, err := c.typed.AppsV1().ControllerRevisions(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list controllerrevisions in %s: %w", namespace, err)
	}
	for i := range crs.Items {
		cr := &crs.Items[i]
		if ownedByUID(cr.OwnerReferences, uid) && cr.Revision == revision {
			// kubectl's rollback for apps/v1: the revision data IS the patch.
			var perr error
			switch kind {
			case "StatefulSet":
				_, perr = c.typed.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, cr.Data.Raw, metav1.PatchOptions{})
			default:
				_, perr = c.typed.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, cr.Data.Raw, metav1.PatchOptions{})
			}
			if perr != nil {
				return fmt.Errorf("rollout undo %s %s/%s to revision %d: %w", kind, namespace, name, revision, perr)
			}
			return nil
		}
	}
	return fmt.Errorf("controllerrevision %d of %s %s/%s not found", revision, kind, namespace, name)
}

// RolloutPause sets a Deployment's spec.paused - holds rollouts without
// touching pods. Reversible and Deployment-only (the API has no paused on
// StatefulSet/DaemonSet).
func (c *ClusterConn) RolloutPause(ctx context.Context, namespace, name string, pause bool) error {
	body, err := json.Marshal(map[string]interface{}{"spec": map[string]bool{"paused": pause}})
	if err != nil {
		return fmt.Errorf("rollout pause marshal: %w", err)
	}
	if _, err := c.typed.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, body, metav1.PatchOptions{}); err != nil {
		verb := "pause"
		if !pause {
			verb = "resume"
		}
		return fmt.Errorf("rollout %s deployment %s/%s: %w", verb, namespace, name, err)
	}
	return nil
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

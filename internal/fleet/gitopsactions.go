package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/gitops/flux"
)

// fluxGroupForKind returns the API group + fallback preferred version for a Flux
// kind, used to build the GVR for a write.
func fluxGroupForKind(kind flux.Kind) (group, fallbackVersion string, ok bool) {
	switch kind {
	case flux.KustomizationKind:
		return "kustomize.toolkit.fluxcd.io", "v1", true
	case flux.HelmReleaseKind:
		return "helm.toolkit.fluxcd.io", "v2", true
	default:
		return "", "", false
	}
}

// gvrForKind resolves the served GVR for a Flux kind via per-resource discovery
// (servedVersion; the documented version when discovery cannot answer, e.g. in
// tests).
func (c *ClusterConn) gvrForKind(kind flux.Kind) (schema.GroupVersionResource, error) {
	group, fallback, ok := fluxGroupForKind(kind)
	if !ok {
		return schema.GroupVersionResource{}, fmt.Errorf("unsupported kind %q", kind)
	}
	resource, ok := flux.ResourceForKind(kind)
	if !ok {
		return schema.GroupVersionResource{}, fmt.Errorf("unsupported kind %q", kind)
	}
	version, served := servedVersion(c.discovery(), group, resource, fallback)
	if !served {
		return schema.GroupVersionResource{}, fmt.Errorf("%s is not served by this cluster (%s CRD not installed)", kind, resource)
	}
	return schema.GroupVersionResource{Group: group, Version: version, Resource: resource}, nil
}

// ErrReplaced: the object at a verb's name is not the one the verb was armed
// against (a same-name recreation, or it is gone). It names the identity
// only: what was or was not written is said by the error wrapping it -
// "nothing was written" only where that is true (Codex review of 6b43068).
var ErrReplaced = errors.New("the object was replaced since the action was armed")

// guardUID adds metadata.uid to a merge patch body. metadata.uid is
// immutable, so the API server refuses the write when the object at that name
// carries another UID: a verb armed against one object never writes its
// same-name replacement, even if the replacement lands after the last check
// (design §5b).
func guardUID(body []byte, uid string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	meta, _ := m["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
		m["metadata"] = meta
	}
	meta["uid"] = uid
	return json.Marshal(m)
}

// checkArmed reads the target and confirms it is the armed object (by UID)
// before anything is written - a dependency write never runs for a replaced
// target.
func (c *ClusterConn) checkArmed(ctx context.Context, gvr schema.GroupVersionResource, kind, ns, name, uid string) (*unstructured.Unstructured, error) {
	if uid == "" {
		return nil, fmt.Errorf("%s %s/%s: no object identity was armed - reload the list and retry", kind, ns, name)
	}
	u, err := c.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return nil, fmt.Errorf("%s %s/%s: %w - nothing was written", kind, ns, name, ErrReplaced)
	case err != nil:
		return nil, fmt.Errorf("%s %s/%s: %w - nothing was written", kind, ns, name, err)
	case string(u.GetUID()) != uid:
		return nil, fmt.Errorf("%s %s/%s: %w - nothing was written", kind, ns, name, ErrReplaced)
	}
	return u, nil
}

// patchArmed writes a UID-guarded merge patch to the armed object. A write
// the server refuses for the UID (a replacement after checkArmed) is
// ErrReplaced; a replacement that is gone again is too. The error speaks
// for this write only - a caller that wrote something before says so.
func (c *ClusterConn) patchArmed(ctx context.Context, gvr schema.GroupVersionResource, kind, ns, name, uid string, body []byte) error {
	guarded, err := guardUID(body, uid)
	if err != nil {
		return err
	}
	_, err = c.dyn.Resource(gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, guarded, metav1.PatchOptions{})
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err), (apierrors.IsInvalid(err) || apierrors.IsConflict(err)) && strings.Contains(strings.ToLower(err.Error()), "uid"):
		return fmt.Errorf("%s %s/%s: %w - its write was refused (%v)", kind, ns, name, ErrReplaced, err)
	}
	return fmt.Errorf("%s %s/%s: %w", kind, ns, name, err)
}

// Reconcile stamps the Flux reconcile annotation so the controller
// re-reconciles - on the armed object only (uid; design §5b).
func (c *ClusterConn) Reconcile(ctx context.Context, kind, ns, name, uid string) error {
	gvr, err := c.gvrForKind(flux.Kind(kind))
	if err != nil {
		return err
	}
	if _, err := c.checkArmed(ctx, gvr, kind, ns, name, uid); err != nil {
		return fmt.Errorf("reconcile %w", err)
	}
	if err := c.patchArmed(ctx, gvr, kind, ns, name, uid, flux.ReconcilePatch(c.clk.Now())); err != nil {
		return fmt.Errorf("reconcile %w", err)
	}
	return nil
}

// ReconcileWithSource stamps the reconcile annotation on the resource AND its
// bound source (equivalent to `flux reconcile <kind> <name> --with-source`).
// The armed resource is validated first - no source is written for a
// replaced target - then its source is patched (best effort: a source patch
// failure must not block the resource), then the resource itself, guarded.
// Degrades to a plain reconcile when the source can't be resolved.
func (c *ClusterConn) ReconcileWithSource(ctx context.Context, kind, ns, name, uid string) error {
	gvr, err := c.gvrForKind(flux.Kind(kind))
	if err != nil {
		return err
	}
	u, err := c.checkArmed(ctx, gvr, kind, ns, name, uid)
	if err != nil {
		return fmt.Errorf("reconcile %w", err)
	}
	// The source's outcome is kept: if the target's write then fails, the
	// error says what already happened to the source - never "nothing".
	source := ""
	if ref, ok := flux.BoundSource(u); ok {
		if sgvr, ok := c.sourceServedGVR(ref.Kind); ok {
			body := flux.ReconcilePatch(c.clk.Now())
			what := fmt.Sprintf("its source %s %s/%s", ref.Kind, ref.Namespace, ref.Name)
			if _, err := c.dyn.Resource(sgvr).Namespace(ref.Namespace).Patch(ctx, ref.Name, types.MergePatchType, body, metav1.PatchOptions{}); err != nil {
				source = what + " was not asked to reconcile either: " + err.Error()
			} else {
				source = what + " was already asked to reconcile"
			}
		}
	}
	if err := c.patchArmed(ctx, gvr, kind, ns, name, uid, flux.ReconcilePatch(c.clk.Now())); err != nil {
		if source != "" {
			return fmt.Errorf("reconcile %w; %s", err, source)
		}
		return fmt.Errorf("reconcile %w", err)
	}
	return nil
}

// SetSuspend toggles spec.suspend on the armed Flux resource (uid).
func (c *ClusterConn) SetSuspend(ctx context.Context, kind, ns, name, uid string, suspend bool) error {
	gvr, err := c.gvrForKind(flux.Kind(kind))
	if err != nil {
		return err
	}
	if _, err := c.checkArmed(ctx, gvr, kind, ns, name, uid); err != nil {
		return fmt.Errorf("set suspend=%v %w", suspend, err)
	}
	if err := c.patchArmed(ctx, gvr, kind, ns, name, uid, flux.SuspendPatch(suspend)); err != nil {
		return fmt.Errorf("set suspend=%v %w", suspend, err)
	}
	return nil
}

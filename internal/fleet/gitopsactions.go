package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/admission"
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
	case flux.ImageRepositoryKind, flux.ImagePolicyKind, flux.ImageUpdateAutomationKind:
		return "image.toolkit.fluxcd.io", "v1", true
	case flux.ReceiverKind:
		return "notification.toolkit.fluxcd.io", "v1", true
	case flux.ProviderKind, flux.AlertKind:
		return "notification.toolkit.fluxcd.io", "v1beta3", true
	default:
		return "", "", false
	}
}

// gvrForKind resolves the served GVR for a Flux kind via per-resource discovery
// (servedVersion; the documented version when discovery cannot answer, e.g. in
// tests).
func (c *ClusterConn) gvrForKind(kind flux.Kind) (schema.GroupVersionResource, error) {
	return gvrForKindIn(c.discovery(), kind)
}

// gvrForKindIn is gvrForKind through disc: a composite read passes its
// context-bound discovery (discoveryFor).
func gvrForKindIn(disc discoveryLike, kind flux.Kind) (schema.GroupVersionResource, error) {
	group, fallback, ok := fluxGroupForKind(kind)
	if !ok {
		return schema.GroupVersionResource{}, fmt.Errorf("unsupported kind %q", kind)
	}
	resource, ok := flux.ResourceForKind(kind)
	if !ok {
		return schema.GroupVersionResource{}, fmt.Errorf("unsupported kind %q", kind)
	}
	version, served := servedVersion(disc, group, resource, fallback)
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

// definiteRefusal: the API server answered and refused a write (a 4xx
// status other than a request timeout) - nothing was committed. Anything
// else (transport loss, a deadline, a 5xx) may have committed before the
// answer was lost, so its outcome is unverified (Codex review of 9c58cca).
func definiteRefusal(err error) bool {
	var st apierrors.APIStatus
	if !errors.As(err, &st) {
		return false
	}
	// coverage S3b §2.1: admission precedes storage, so an attributed
	// finding - a fail-closed webhook's call failure (500) among them -
	// is a definite refusal; any other 500 stays unverified
	if admission.Classify(st.Status()).Attributed() {
		return true
	}
	code := st.Status().Code
	return code >= 400 && code < 500 && code != http.StatusRequestTimeout
}

// stepRefused: a step's request was refused, so nothing of it was written -
// a definite refusal, or patchArmed's ErrReplaced, which it returns only for
// the server's UID-guard answers (NotFound, the UID precondition Conflict,
// the metadata.uid Invalid) with their status formatted away (Codex's
// implementation re-review 1). A lost answer is neither.
func stepRefused(err error) bool {
	return err != nil && (errors.Is(err, ErrReplaced) || definiteRefusal(err))
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
	out, err := c.dyn.Resource(gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, guarded, metav1.PatchOptions{})
	err = admission.Wrap(admission.Request{Operation: "UPDATE", Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Namespace: ns}, kind, ns, name, err)
	switch {
	case err == nil:
		if out != nil {
			NoteReceipt(ctx, Receipt{UID: string(out.GetUID()), Generation: out.GetGeneration(), ResourceVersion: out.GetResourceVersion(), Token: tokenOf(ctx)})
		}
		return nil
	case refusedForUID(err):
		return fmt.Errorf("%s %s/%s: %w - its write was refused (%v)", kind, ns, name, ErrReplaced, err)
	}
	return fmt.Errorf("%s %s/%s: %w", kind, ns, name, err)
}

// armedWriteErr reports a UID-guarded write's outcome honestly: a refusal for
// the UID is ErrReplaced ("its write was refused"); another definite refusal
// wrote nothing; anything else (transport, deadline, 5xx) may have landed.
func armedWriteErr(verb, what string, err error) error {
	switch {
	case err == nil:
		return nil
	case refusedForUID(err):
		return fmt.Errorf("%s %s: %w - its write was refused (%v)", verb, what, ErrReplaced, err)
	case definiteRefusal(err):
		return fmt.Errorf("%s %s: %w - nothing was written", verb, what, err)
	}
	return fmt.Errorf("%s %s: %w - the outcome is unverified", verb, what, err)
}

// refusedForUID: a UID-guarded write the server refused because the object
// at that name is not the armed one - gone (NotFound), or another UID (the
// immutable metadata.uid Invalid, or the wrapped UID precondition
// Conflict), read by their wire shapes and never a substring (coverage S3b
// §2.5): a policy denial whose text says "uid" is not a replacement.
func refusedForUID(err error) bool {
	var st apierrors.APIStatus
	return errors.As(err, &st) && admission.UIDRefusal(st.Status())
}

// Reconcile stamps the Flux reconcile annotation so the controller
// re-reconciles - on the armed object only (uid; design §5b).
func (c *ClusterConn) Reconcile(ctx context.Context, kind, ns, name, uid string) error {
	gvr, err := c.gvrForKind(flux.Kind(kind))
	if err != nil {
		return notSent(err)
	}
	if err := imagePolicyVerb(flux.Kind(kind), gvr, "reconcile", "a reconcile request could never be confirmed: its status has no lastHandledReconcileAt"); err != nil {
		return notSent(err)
	}
	if k := flux.Kind(kind); k == flux.ProviderKind || k == flux.AlertKind {
		// S1c §5: notification-controller does not reconcile them
		return notSent(fmt.Errorf("reconcile is not offered for a %s: it is static, and notification-controller has nothing to reconcile", kind))
	}
	if _, err := c.checkArmed(ctx, gvr, kind, ns, name, uid); err != nil {
		return notSent(fmt.Errorf("reconcile %w", err))
	}
	// the requestedAt token is this request's own witness (A3 §1)
	now := c.clk.Now()
	if err := c.patchArmed(withToken(ctx, flux.ReconcileToken(now)), gvr, kind, ns, name, uid, flux.ReconcilePatch(now)); err != nil {
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
	_, err := c.ReconcileWithSourceSteps(ctx, kind, ns, name, uid)
	return err
}

// Step is one request of a composite action and its outcome (coverage S3b
// §2.3): each step keeps its own, so a refused source is reported beside a
// written target, and a refused target beside a written source.
type Step struct {
	Verb, Subject string
	Written       bool
	// Refused: the server refused the request (definiteRefusal), so nothing
	// of it was written. A step neither written nor refused is unverified:
	// its answer was lost and the write may have landed (Codex's
	// implementation review, finding 5).
	Refused bool
	// Err is the step's error, wrapped as its request's (admission.Wrap).
	Err error
}

// ReconcileWithSourceSteps is ReconcileWithSource with each step's
// outcome: the source's (when there is one), then the target's.
func (c *ClusterConn) ReconcileWithSourceSteps(ctx context.Context, kind, ns, name, uid string) ([]Step, error) {
	if flux.IsImageKind(flux.Kind(kind)) || flux.IsNotificationKind(flux.Kind(kind)) {
		// an automation clones Git itself and never reads the GitRepository's
		// artifact, and a chained verb is not offered (S1b §5); a
		// notification kind binds no source (S1c §5)
		return nil, fmt.Errorf("reconcile with source is not offered for %s", kind)
	}
	gvr, err := c.gvrForKind(flux.Kind(kind))
	if err != nil {
		return nil, err
	}
	u, err := c.checkArmed(ctx, gvr, kind, ns, name, uid)
	if err != nil {
		return nil, fmt.Errorf("reconcile %w", err)
	}
	var steps []Step
	// The source's outcome is kept: if the target's write then fails, the
	// error says what already happened to the source - never "nothing".
	source := ""
	if ref, ok := flux.BoundSource(u); ok && ref.Kind == string(flux.ExternalArtifactKind) {
		// S1c §5: its producer reconciles an artifact; nothing was written
		return nil, fmt.Errorf("reconcile with source is not offered for %s %s/%s: its source is an ExternalArtifact, which its producer reconciles; reconcile it alone", kind, ns, name)
	}
	if ref, ok := flux.BoundSource(u); ok {
		if sgvr, ok := c.sourceServedGVR(ref.Kind); ok {
			body := flux.ReconcilePatch(c.clk.Now())
			what := fmt.Sprintf("its source %s %s/%s", ref.Kind, ref.Namespace, ref.Name)
			_, err := c.dyn.Resource(sgvr).Namespace(ref.Namespace).Patch(ctx, ref.Name, types.MergePatchType, body, metav1.PatchOptions{})
			err = admission.Wrap(admission.Request{Operation: "UPDATE", Group: sgvr.Group, Version: sgvr.Version, Resource: sgvr.Resource, Namespace: ref.Namespace},
				ref.Kind, ref.Namespace, ref.Name, err)
			steps = append(steps, Step{Verb: "reconcile", Subject: fmt.Sprintf("%s %s/%s", ref.Kind, ref.Namespace, ref.Name), Written: err == nil, Refused: stepRefused(err), Err: err})
			switch {
			case err == nil:
				source = what + " was already asked to reconcile"
			case definiteRefusal(err):
				source = what + " was not asked to reconcile either: " + err.Error()
			default:
				source = what + " reconcile request failed and its outcome is unverified: " + err.Error()
			}
		}
	}
	err = c.patchArmed(ctx, gvr, kind, ns, name, uid, flux.ReconcilePatch(c.clk.Now()))
	steps = append(steps, Step{Verb: "reconcile", Subject: fmt.Sprintf("%s %s/%s", kind, ns, name), Written: err == nil, Refused: stepRefused(err), Err: err})
	if err != nil {
		if source != "" {
			return steps, fmt.Errorf("reconcile %w; %s", err, source)
		}
		return steps, fmt.Errorf("reconcile %w", err)
	}
	return steps, nil
}

// SetSuspend toggles spec.suspend on the armed Flux resource (uid).
func (c *ClusterConn) SetSuspend(ctx context.Context, kind, ns, name, uid string, suspend bool) error {
	gvr, err := c.gvrForKind(flux.Kind(kind))
	if err != nil {
		return notSent(err)
	}
	if err := imagePolicyVerb(flux.Kind(kind), gvr, "suspend or resume", "its v1beta2 spec has no suspend field"); err != nil {
		return notSent(err)
	}
	if _, err := c.checkArmed(ctx, gvr, kind, ns, name, uid); err != nil {
		return notSent(fmt.Errorf("set suspend=%v %w", suspend, err))
	}
	if err := c.patchArmed(ctx, gvr, kind, ns, name, uid, flux.SuspendPatch(suspend)); err != nil {
		return fmt.Errorf("set suspend=%v %w", suspend, err)
	}
	return nil
}

// imagePolicyVerb refuses a verb on an ImagePolicy served below v1 (S1b
// §5): v1beta2 has neither spec.suspend nor lastHandledReconcileAt.
func imagePolicyVerb(kind flux.Kind, gvr schema.GroupVersionResource, verb, why string) error {
	if kind == flux.ImagePolicyKind && gvr.Version != "v1" {
		return fmt.Errorf("%s is not offered for an ImagePolicy at %s: %s", verb, gvr.Version, why)
	}
	return nil
}

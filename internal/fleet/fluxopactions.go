package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gitops/composition"
	"github.com/moomora/klyx/internal/gitops/fluxop"
)

// The Flux Operator's verbs (coverage S1a §5), and an ArtifactGenerator's
// (S1c §5), which source-watcher reads the same way under its own key.
// Each kind uses its controller's own mechanism: the reconcile annotation,
// never a spec.suspend the kinds do not have. Every write is one merge
// patch of annotations, guarded by the armed object's UID.

// Operator verbs.
const (
	FluxOpReconcile = "reconcile" // requestedAt
	FluxOpFetch     = "fetch"     // a provider: requestedAt and forceAt, the same fresh value
	FluxOpSuspend   = "suspend"   // reconcile: disabled
	FluxOpResume    = "resume"    // reconcile: enabled, and requestedAt
	FluxOpRefresh   = "refresh"   // a FluxReport: requestedAt
)

// fluxOpVerbs is the per-kind verb matrix.
var fluxOpVerbs = map[string]map[string]bool{
	fluxop.KindInstance:       {FluxOpReconcile: true, FluxOpSuspend: true, FluxOpResume: true},
	fluxop.KindSet:            {FluxOpReconcile: true, FluxOpSuspend: true, FluxOpResume: true},
	fluxop.KindProvider:       {FluxOpReconcile: true, FluxOpFetch: true, FluxOpSuspend: true, FluxOpResume: true},
	fluxop.KindReport:         {FluxOpRefresh: true},
	composition.KindGenerator: {FluxOpReconcile: true, FluxOpSuspend: true, FluxOpResume: true},
}

var fluxOpResources = map[string]string{
	fluxop.KindInstance: "fluxinstances", fluxop.KindReport: "fluxreports",
	fluxop.KindSet: "resourcesets", fluxop.KindProvider: "resourcesetinputproviders",
	composition.KindGenerator: "artifactgenerators",
}

// annotationKind is where a kind's verbs write: its group and the one
// version Klyx reads, and its reconcile annotation's key.
func annotationKind(kind string) (group, version, reconcileKey string) {
	if kind == composition.KindGenerator {
		return composition.GeneratorGroup, composition.GeneratorVersion, composition.AnnReconcile
	}
	return fluxop.Group, fluxop.Version, fluxop.AnnReconcile
}

// fluxOpToken is a request token unique to this write: an RFC 3339 time
// with nanoseconds, as the operator's CLI writes.
var fluxOpToken = func() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// FluxOpAnnotations is the annotation patch an operator verb writes.
func FluxOpAnnotations(verb, token string) (map[string]any, error) {
	return verbAnnotations(fluxop.AnnReconcile, verb, token)
}

// verbAnnotations is the annotation patch a verb writes, with the kind's
// reconcile key. requestedAt is the same key for every Flux controller.
func verbAnnotations(reconcileKey, verb, token string) (map[string]any, error) {
	switch verb {
	case FluxOpReconcile, FluxOpRefresh:
		return map[string]any{fluxop.AnnRequestedAt: token}, nil
	case FluxOpFetch:
		return map[string]any{fluxop.AnnRequestedAt: token, fluxop.AnnForceAt: token}, nil
	case FluxOpSuspend:
		return map[string]any{reconcileKey: "disabled"}, nil
	case FluxOpResume:
		return map[string]any{reconcileKey: "enabled", fluxop.AnnRequestedAt: token}, nil
	}
	return nil, fmt.Errorf("unknown verb %q", verb)
}

// FluxOpAct runs one operator verb on the armed object (uid): the object
// is read and its UID checked first, then one UID-guarded merge patch
// writes the annotations atomically.
func (c *ClusterConn) FluxOpAct(ctx context.Context, kind, ns, name, uid, verb string) error {
	if !fluxOpVerbs[kind][verb] {
		return fmt.Errorf("%s does not support %q", kind, verb)
	}
	if c.dyn == nil {
		return fmt.Errorf("dynamic client unavailable")
	}
	group, version, reconcileKey := annotationKind(kind)
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: fluxOpResources[kind]}
	if kind == composition.KindGenerator {
		// S1c §3: Klyx reads a generator at one version, and writes only
		// what it reads
		if served, ok := servedVersion(c.discovery(), group, gvr.Resource, version); !ok || served != version {
			return fmt.Errorf("%s is not offered: this cluster does not serve %s at %s", verb, gvr.Resource, version)
		}
	}
	if _, err := c.checkArmed(ctx, gvr, kind, ns, name, uid); err != nil {
		return fmt.Errorf("%s %w", verb, err)
	}
	ann, err := verbAnnotations(reconcileKey, verb, fluxOpToken())
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": ann}})
	if err != nil {
		return err
	}
	if err := c.patchArmed(ctx, gvr, kind, ns, name, uid, body); err != nil {
		return fmt.Errorf("%s %w", verb, err)
	}
	c.signalRefresh()
	return nil
}

// FluxOpObject reads one operator object directly, for the inspector's
// on-demand facts (history, inventory, a dependency's readyExpr): the
// caller sanitises it before anything crosses (S0b).
func (c *ClusterConn) FluxOpObject(ctx context.Context, kind, ns, name string) (*unstructured.Unstructured, error) {
	res, ok := fluxOpResources[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported kind %q", kind)
	}
	if c.dyn == nil {
		return nil, fmt.Errorf("dynamic client unavailable")
	}
	return c.dyn.Resource(schema.GroupVersionResource{Group: fluxop.Group, Version: fluxop.Version, Resource: res}).
		Namespace(ns).Get(ctx, name, metav1.GetOptions{})
}

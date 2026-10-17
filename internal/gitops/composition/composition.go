// Package composition reads Flux source composition (coverage S1c, design
// accepted at 04d638b): source-watcher's ArtifactGenerator
// (source.extensions.fluxcd.io/v1beta1) and the ExternalArtifacts it
// produces (source.toolkit.fluxcd.io/v1).
//
// Each watched object is projected field by field (S1c §3): classification
// facts and join inputs are kept whole, display strings are clipped, and
// nothing a producer may sign or free-text is copied - an artifact's URL
// shows as its scheme and host, and of its metadata only the origin
// revision. Messages pass Origins (S1c §6) before they are kept; a
// generator's resume fingerprint is taken from the raw Ready first.
package composition

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/gitops/resume"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Groups, versions and kinds.
const (
	GeneratorGroup   = "source.extensions.fluxcd.io"
	GeneratorVersion = "v1beta1"
	ArtifactGroup    = "source.toolkit.fluxcd.io"
	ArtifactVersion  = "v1"
	KindGenerator    = "ArtifactGenerator"
	KindArtifact     = "ExternalArtifact"
)

// Annotations and labels the rules and joins read.
const (
	AnnReconcile   = "source.extensions.fluxcd.io/reconcile" // "disabled" suspends, case-insensitive
	AnnRequestedAt = "reconcile.fluxcd.io/requestedAt"
	LabelGenerator = "source.extensions.fluxcd.io/generator" // the producing generator's UID
	LabelManagedBy = "app.kubernetes.io/managed-by"
	OriginRevision = "org.opencontainers.image.revision"
)

// Reasons the rules read.
const (
	ReasonDisabled    = "ReconciliationDisabled"
	ReasonProgressing = "Progressing"
)

// Projection limits (S1c §3).
const (
	MaxObject   = 4 << 10
	messageClip = 1 << 10
	tightClip   = 256
	maxRefs     = 50 // sources, artifacts, inventory
)

// Object is one projected object.
type Object struct {
	*unstructured.Unstructured
	fingerprint [32]byte
	hasReady    bool
	// Clipped names the display fields clipped or dropped; Incomplete the
	// join inputs that could not be kept whole (a join that needs one
	// claims nothing).
	Clipped, Incomplete []string
	Size                int
}

// Fingerprint is Ready's evaluation, from the raw condition (S1a §4).
func (o *Object) Fingerprint() ([32]byte, bool) { return o.fingerprint, o.hasReady }

// SizeOf is the budget's Size function.
func SizeOf(v any) int {
	if o, ok := v.(*Object); ok {
		return o.Size
	}
	return MaxObject + 1
}

func clip(s string, n int, path string, clipped *[]string) string {
	if len(s) <= n {
		return s
	}
	*clipped = append(*clipped, path)
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// Project builds the projection of one watched object of kind; the
// observer refuses one still over MaxObject.
func Project(kind string) func(obj any) (any, error) {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("composition: want *Unstructured, got %T", obj)
		}
		out := build(kind, u, messageClip, false)
		if out.Size > MaxObject {
			out = build(kind, u, tightClip, false)
		}
		if out.Size > MaxObject {
			out = build(kind, u, tightClip, true)
		}
		return out, nil
	}
}

func build(kind string, u *unstructured.Unstructured, limit int, dropJoins bool) *Object {
	var clipped, incomplete []string
	md := map[string]any{"name": u.GetName(), "uid": string(u.GetUID()), "generation": u.GetGeneration(),
		"creationTimestamp": u.GetCreationTimestamp().UTC().Format(time.RFC3339)}
	if ns := u.GetNamespace(); ns != "" {
		md["namespace"] = ns
	}
	if rv := u.GetResourceVersion(); rv != "" {
		md["resourceVersion"] = rv
	}
	ann := map[string]any{}
	for _, k := range []string{AnnReconcile, AnnRequestedAt} {
		if v, ok := u.GetAnnotations()[k]; ok {
			ann[k] = clip(v, tightClip, "metadata.annotations", &clipped)
		}
	}
	if len(ann) > 0 {
		md["annotations"] = ann
	}
	labels := map[string]any{}
	for _, k := range []string{LabelGenerator, LabelManagedBy, "kustomize.toolkit.fluxcd.io/name", "kustomize.toolkit.fluxcd.io/namespace",
		"helm.toolkit.fluxcd.io/name", "helm.toolkit.fluxcd.io/namespace"} {
		if v, ok := u.GetLabels()[k]; ok {
			labels[k] = v // the generator label is a join input: whole
		}
	}
	if len(labels) > 0 {
		md["labels"] = labels
	}
	m := map[string]any{"apiVersion": u.GetAPIVersion(), "kind": u.GetKind(), "metadata": md}
	status := map[string]any{}
	conds, fp, hasReady := conditionsOf(u, kind, limit, &clipped)
	if len(conds) > 0 {
		status["conditions"] = conds
	}
	spec := map[string]any{}
	switch kind {
	case KindGenerator:
		generatorFields(u, spec, status, limit, dropJoins, &clipped, &incomplete)
	case KindArtifact:
		artifactFields(u, spec, status, limit, &clipped)
	}
	if len(spec) > 0 {
		m["spec"] = spec
	}
	if len(status) > 0 {
		m["status"] = status
	}
	sort.Strings(clipped)
	sort.Strings(incomplete)
	o := &Object{Unstructured: &unstructured.Unstructured{Object: m}, fingerprint: fp, hasReady: hasReady, Clipped: clipped, Incomplete: incomplete}
	b, err := json.Marshal(m)
	if err != nil {
		o.Size = MaxObject + 1
		return o
	}
	o.Size = len(b) + len(fp)
	return o
}

// conditionsOf keeps Ready, Reconciling and Stalled; messages pass the
// kind's rule (Origins) and are clipped; Ready is fingerprinted raw first.
func conditionsOf(u *unstructured.Unstructured, kind string, limit int, clipped *[]string) ([]any, [32]byte, bool) {
	var fp [32]byte
	has := false
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	var out []any
	for i, x := range conds {
		cm, ok := x.(map[string]any)
		if !ok {
			continue
		}
		t, _ := cm["type"].(string)
		if t != "Ready" && t != "Reconciling" && t != "Stalled" {
			continue
		}
		status, _ := cm["status"].(string)
		reason, _ := cm["reason"].(string)
		msg, _ := cm["message"].(string)
		ltt, _ := cm["lastTransitionTime"].(string)
		if t == "Ready" {
			fp, has = resume.ReadyFingerprint(status, reason, msg, ltt), true
		}
		msg, _ = crd.MessageRule(kind, reason, msg)
		keep := map[string]any{"type": t, "status": status, "reason": clip(reason, tightClip, "reason", clipped), "lastTransitionTime": ltt,
			"message": clip(urlscrub.Text(msg).Text, limit, fmt.Sprintf("status.conditions[%d].message", i), clipped)}
		switch g := cm["observedGeneration"].(type) {
		case int64:
			keep["observedGeneration"] = g
		case float64:
			keep["observedGeneration"] = int64(g)
		}
		out = append(out, keep)
	}
	return out, fp, has
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func generatorFields(u *unstructured.Unstructured, spec, status map[string]any, limit int, dropJoins bool, clipped, incomplete *[]string) {
	o := u.Object
	// join inputs, whole: each declared source and each generated artifact
	if srcs, ok, _ := unstructured.NestedSlice(o, "spec", "sources"); ok {
		if dropJoins || len(srcs) > maxRefs {
			*incomplete = append(*incomplete, "spec.sources")
		} else {
			var out []any
			for _, x := range srcs {
				sm, _ := x.(map[string]any)
				out = append(out, map[string]any{"alias": str(sm, "alias"), "kind": str(sm, "kind"), "name": str(sm, "name"), "namespace": str(sm, "namespace")})
			}
			spec["sources"] = out
		}
	}
	if arts, ok, _ := unstructured.NestedSlice(o, "spec", "artifacts"); ok {
		var out []any
		for i, x := range arts {
			if i == maxRefs {
				*clipped = append(*clipped, "spec.artifacts")
				break
			}
			am, _ := x.(map[string]any)
			a := map[string]any{"name": clip(str(am, "name"), tightClip, "spec.artifacts.name", clipped)}
			if r := str(am, "revision"); r != "" {
				a["revision"] = clip(r, tightClip, "spec.artifacts.revision", clipped)
			}
			cp, _ := am["copy"].([]any)
			var rules []any
			for _, c := range cp {
				cm, ok := c.(map[string]any)
				if !ok {
					// a rule that is not an object is not shown as one
					if len(*incomplete) == 0 || (*incomplete)[len(*incomplete)-1] != "spec.artifacts.copy" {
						*incomplete = append(*incomplete, "spec.artifacts.copy")
					}
					continue
				}
				rule := str(cm, "from") + " → " + str(cm, "to")
				if s := str(cm, "strategy"); s != "" {
					rule += " (" + s + ")"
				}
				rules = append(rules, clip(rule, limit/4, "spec.artifacts.copy", clipped))
			}
			if len(rules) > 0 {
				a["copy"] = rules
			}
			out = append(out, a)
		}
		spec["artifacts"] = out
	}
	if inv, ok, _ := unstructured.NestedSlice(o, "status", "inventory"); ok {
		if dropJoins || len(inv) > maxRefs {
			*incomplete = append(*incomplete, "status.inventory")
		} else {
			var out []any
			for _, x := range inv {
				im, _ := x.(map[string]any)
				out = append(out, map[string]any{"name": str(im, "name"), "namespace": str(im, "namespace")})
			}
			status["inventory"] = out
		}
	}
	if s, ok, _ := unstructured.NestedString(o, "status", "lastHandledReconcileAt"); ok {
		status["lastHandledReconcileAt"] = clip(s, tightClip, "status.lastHandledReconcileAt", clipped)
	}
}

func artifactFields(u *unstructured.Unstructured, spec, status map[string]any, limit int, clipped *[]string) {
	o := u.Object
	if ref, ok, _ := unstructured.NestedMap(o, "spec", "sourceRef"); ok {
		spec["sourceRef"] = map[string]any{"apiVersion": str(ref, "apiVersion"), "kind": str(ref, "kind"), "name": str(ref, "name"), "namespace": str(ref, "namespace")}
	}
	art, ok, _ := unstructured.NestedMap(o, "status", "artifact")
	if !ok {
		return
	}
	a := map[string]any{"revision": str(art, "revision"), "digest": str(art, "digest")} // join inputs: whole
	if t := str(art, "lastUpdateTime"); t != "" {
		a["lastUpdateTime"] = t
	}
	if n, ok := art["size"].(int64); ok {
		a["size"] = n
	}
	// the URL shows as its scheme and host only (S1c §6); of the metadata,
	// only the origin revision, which the producer records
	if url := str(art, "url"); url != "" {
		a["origin"] = clip(urlscrub.OriginOf(url), tightClip, "status.artifact.url", clipped)
	}
	if md, ok := art["metadata"].(map[string]any); ok {
		if r, ok := md[OriginRevision].(string); ok {
			a["originRevision"] = clip(r, limit/4, "status.artifact.metadata", clipped)
		}
	}
	status["artifact"] = a
}

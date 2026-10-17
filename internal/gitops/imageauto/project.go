// Package imageauto reads Flux image automation (coverage S1b, design
// accepted at 6f8824f): ImageRepository and ImagePolicy
// (image-reflector-controller) and ImageUpdateAutomation
// (image-automation-controller), all image.toolkit.fluxcd.io, at v1 or
// v1beta2.
//
// Each watched object is projected field by field into a new object (S1b
// §3): classification facts and join inputs are never clipped, display
// strings are, and no free-form automation surface (the message template,
// its values, push option values) is copied. The projection's size is
// measured so the observer's budget can admit it.
package imageauto

import (
	"encoding/json"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Group is the image automation API group; the versions read are v1, else
// v1beta2 (S1b §1).
const (
	Group      = "image.toolkit.fluxcd.io"
	Version    = "v1"
	VersionOld = "v1beta2"
)

// Kinds.
const (
	KindRepository = "ImageRepository"
	KindPolicy     = "ImagePolicy"
	KindAutomation = "ImageUpdateAutomation"
)

// Annotations the rules and verbs read.
const AnnRequestedAt = "reconcile.fluxcd.io/requestedAt"

// Projection limits (S1b §3).
const (
	MaxObject           = 4 << 10 // one projection, measured after it is built
	messageClip         = 1 << 10 // a condition message, for display
	tightClip           = 256     // every display string, when over MaxObject
	maxConditions       = 10
	maxTags             = 10
	maxExclusions       = 10
	maxObservedPolicies = 100
)

// Object is one projected object: a new unstructured object holding only
// the projected fields.
type Object struct {
	*unstructured.Unstructured
	// Clipped names the display fields clipped or dropped to fit.
	Clipped []string
	// Incomplete names the join inputs that could not be kept whole within
	// MaxObject: a join that needs one claims nothing (S1b §3).
	Incomplete []string
	// Size is the projection's encoded size in bytes, the budget's measure.
	Size int
}

// SizeOf is the budget's Size function for a projected Object.
func SizeOf(v any) int {
	if o, ok := v.(*Object); ok {
		return o.Size
	}
	return MaxObject + 1
}

// clipper clips display strings to a limit and records what it clipped.
type clipper struct {
	limit   int
	clipped []string
}

func (c *clipper) str(path, s string) string {
	if c.limit > 0 && len(s) > c.limit {
		c.clipped = append(c.clipped, path)
		cut := c.limit
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		return s[:cut] + "…"
	}
	return s
}

// url applies the URL rule, then clips.
func (c *clipper) url(path, s string) string { return c.str(path, urlscrub.Field(s).Text) }

// stage is how hard a build squeezes: first messages at 1 KiB, then every
// display string at 256 bytes, then without the join inputs too large to
// keep (each marked incomplete).
type stage int

const (
	stageNormal stage = iota
	stageTight
	stageNoJoins
)

// Project builds the projection of one watched object of kind. It never
// mutates obj. The observer refuses a projection still over MaxObject.
func Project(kind string) func(obj any) (any, error) {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("imageauto: want *Unstructured, got %T", obj)
		}
		out := build(kind, u, stageNormal)
		for st := stageTight; out.Size > MaxObject && st <= stageNoJoins; st++ {
			out = build(kind, u, st)
		}
		return out, nil
	}
}

func build(kind string, u *unstructured.Unstructured, st stage) *Object {
	c := &clipper{limit: messageClip}
	if st >= stageTight {
		c.limit = tightClip
	}
	m := map[string]any{"apiVersion": u.GetAPIVersion(), "kind": u.GetKind(), "metadata": metadataOf(u, kind, st)}
	status := map[string]any{}
	if conds := conditionsOf(u, kind, c); len(conds) > 0 {
		status["conditions"] = conds
	}
	copyInt(u.Object, status, "status", "observedGeneration")
	copyStr(u.Object, status, c, false, "status", "lastHandledReconcileAt")
	spec := map[string]any{}
	if v, ok, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); ok {
		spec["suspend"] = v
	}
	var incomplete []string
	switch kind {
	case KindRepository:
		repositoryFields(u, spec, status, c)
	case KindPolicy:
		incomplete = policyFields(u, spec, status, c, st)
	case KindAutomation:
		incomplete = automationFields(u, spec, status, c, st)
	}
	if len(spec) > 0 {
		m["spec"] = spec
	}
	if len(status) > 0 {
		m["status"] = status
	}
	o := &Object{Unstructured: &unstructured.Unstructured{Object: m}}
	sort.Strings(c.clipped)
	sort.Strings(incomplete)
	o.Clipped, o.Incomplete = c.clipped, incomplete
	b, err := json.Marshal(m)
	if err != nil {
		o.Size = MaxObject + 1
		return o
	}
	o.Size = len(b)
	return o
}

// ownership labels and annotations the verbs' consequence line reads (C4).
var ownershipKeys = []string{
	"kustomize.toolkit.fluxcd.io/name", "kustomize.toolkit.fluxcd.io/namespace",
	"helm.toolkit.fluxcd.io/name", "helm.toolkit.fluxcd.io/namespace",
	"argocd.argoproj.io/tracking-id",
}

// metadataOf keeps identity, the request and ownership keys, and for a
// policy every label whole: an automation's selector matches them (a join
// input), unless the last stage dropped them.
func metadataOf(u *unstructured.Unstructured, kind string, st stage) map[string]any {
	md := map[string]any{"name": u.GetName(), "uid": string(u.GetUID()), "generation": u.GetGeneration(),
		"creationTimestamp": u.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z")}
	if ns := u.GetNamespace(); ns != "" {
		md["namespace"] = ns
	}
	if rv := u.GetResourceVersion(); rv != "" {
		md["resourceVersion"] = rv
	}
	ann := map[string]any{}
	for _, k := range append([]string{AnnRequestedAt}, ownershipKeys...) {
		if v, ok := u.GetAnnotations()[k]; ok {
			ann[k] = clipTo(v, tightClip)
		}
	}
	if len(ann) > 0 {
		md["annotations"] = ann
	}
	labels := map[string]any{}
	if kind == KindPolicy && st < stageNoJoins {
		for k, v := range u.GetLabels() {
			labels[k] = v
		}
	} else {
		for _, k := range ownershipKeys {
			if v, ok := u.GetLabels()[k]; ok {
				labels[k] = clipTo(v, tightClip)
			}
		}
	}
	if len(labels) > 0 {
		md["labels"] = labels
	}
	return md
}

func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// conditionsOf keeps Ready, Reconciling and Stalled whole for the rule;
// messages are display strings. An ImageUpdateAutomation's messages pass
// its allowlist first (S1b §6): the projection never holds the raw text.
func conditionsOf(u *unstructured.Unstructured, kind string, c *clipper) []any {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	var out []any
	for i, x := range conds {
		if len(out) == maxConditions {
			c.clipped = append(c.clipped, "status.conditions")
			break
		}
		cm, ok := x.(map[string]any)
		if !ok {
			continue
		}
		t, _ := cm["type"].(string)
		if t != "Ready" && t != "Reconciling" && t != "Stalled" {
			continue
		}
		keep := map[string]any{"type": t}
		for _, f := range []string{"status", "reason", "lastTransitionTime"} {
			if s, ok := cm[f].(string); ok {
				keep[f] = clipTo(s, tightClip)
			}
		}
		if g, ok := cm["observedGeneration"].(int64); ok {
			keep["observedGeneration"] = g
		} else if g, ok := cm["observedGeneration"].(float64); ok {
			keep["observedGeneration"] = int64(g)
		}
		if msg, ok := cm["message"].(string); ok {
			msg, _ = crd.MessageRule(kind, msg)
			keep["message"] = c.url(fmt.Sprintf("status.conditions[%d].message", i), msg)
		}
		out = append(out, keep)
	}
	return out
}

func copyStr(src, dst map[string]any, c *clipper, url bool, path ...string) {
	s, ok, _ := unstructured.NestedString(src, path...)
	if !ok || s == "" {
		return
	}
	if url {
		s = c.url(joinPath(path), s)
	} else {
		s = c.str(joinPath(path), s)
	}
	dst[path[len(path)-1]] = s
}

// copyWhole copies a join input or fact without clipping.
func copyWhole(src, dst map[string]any, path ...string) {
	if s, ok, _ := unstructured.NestedString(src, path...); ok && s != "" {
		dst[path[len(path)-1]] = s
	}
}

func copyInt(src, dst map[string]any, path ...string) {
	if v, ok, _ := unstructured.NestedInt64(src, path...); ok {
		dst[path[len(path)-1]] = v
	} else if f, ok, _ := unstructured.NestedFloat64(src, path...); ok {
		dst[path[len(path)-1]] = int64(f)
	}
}

func joinPath(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += "."
		}
		out += s
	}
	return out
}

func strList(src map[string]any, max, each int, path string, c *clipper, keys ...string) []any {
	list, _, _ := unstructured.NestedStringSlice(src, keys...)
	var out []any
	for i, s := range list {
		if i == max {
			c.clipped = append(c.clipped, path)
			break
		}
		out = append(out, clipTo(s, each))
	}
	return out
}

func refName(src map[string]any, keys ...string) string {
	s, _, _ := unstructured.NestedString(src, append(keys, "name")...)
	return s
}

func repositoryFields(u *unstructured.Unstructured, spec, status map[string]any, c *clipper) {
	o := u.Object
	copyStr(o, spec, c, true, "spec", "image")
	for _, f := range []string{"provider", "serviceAccountName", "interval", "timeout"} {
		copyStr(o, spec, c, false, "spec", f)
	}
	if v, ok, _ := unstructured.NestedBool(o, "spec", "insecure"); ok {
		spec["insecure"] = v
	}
	for _, f := range []string{"secretRef", "certSecretRef", "proxySecretRef"} {
		if n := refName(o, "spec", f); n != "" {
			spec[f] = clipTo(n, tightClip)
		}
	}
	if ex := strList(o, maxExclusions, tightClip, "spec.exclusionList", c, "spec", "exclusionList"); len(ex) > 0 {
		spec["exclusionList"] = ex
	}
	if af, ok, _ := unstructured.NestedMap(o, "spec", "accessFrom"); ok {
		sel, _, _ := unstructured.NestedSlice(af, "namespaceSelectors")
		spec["accessFromSelectors"] = int64(len(sel))
	}
	copyStr(o, status, c, true, "status", "canonicalImageName")
	if scan, ok, _ := unstructured.NestedMap(o, "status", "lastScanResult"); ok {
		out := map[string]any{}
		copyInt(scan, out, "tagCount")
		copyWhole(scan, out, "scanTime")
		copyStr(scan, out, c, false, "revision")
		if tags := strList(scan, maxTags, tightClip, "status.lastScanResult.latestTags", c, "latestTags"); len(tags) > 0 {
			out["latestTags"] = tags
		}
		status["lastScanResult"] = out
	}
}

// policyFields: the repository reference and the resolved images are join
// inputs, kept whole (S1b §3).
func policyFields(u *unstructured.Unstructured, spec, status map[string]any, c *clipper, st stage) []string {
	o := u.Object
	var incomplete []string
	if ref, ok, _ := unstructured.NestedMap(o, "spec", "imageRepositoryRef"); ok {
		r := map[string]any{}
		copyWhole(ref, r, "name")
		copyWhole(ref, r, "namespace")
		spec["imageRepositoryRef"] = r
	}
	if p, ok, _ := unstructured.NestedMap(o, "spec", "policy"); ok {
		pol := map[string]any{}
		for _, t := range []string{"semver", "alphabetical", "numerical"} {
			if m, ok := p[t].(map[string]any); ok {
				x := map[string]any{}
				copyStr(m, x, c, false, "range")
				copyStr(m, x, c, false, "order")
				pol[t] = x
			}
		}
		spec["policy"] = pol
	}
	if ft, ok, _ := unstructured.NestedMap(o, "spec", "filterTags"); ok {
		x := map[string]any{}
		if s, ok := ft["pattern"].(string); ok {
			x["pattern"] = clipTo(s, tightClip)
		}
		if s, ok := ft["extract"].(string); ok {
			x["extract"] = clipTo(s, tightClip)
		}
		spec["filterTags"] = x
	}
	copyStr(o, spec, c, false, "spec", "digestReflectionPolicy")
	copyStr(o, spec, c, false, "spec", "interval")
	for _, f := range []string{"latestRef", "observedPreviousRef"} {
		if ref, ok, _ := unstructured.NestedMap(o, "status", f); ok {
			r := map[string]any{}
			copyWhole(ref, r, "name")
			copyWhole(ref, r, "tag")
			copyWhole(ref, r, "digest")
			status[f] = r
		}
	}
	copyWhole(o, status, "status", "latestImage")
	copyWhole(o, status, "status", "observedPreviousImage")
	if st == stageNoJoins && len(u.GetLabels()) > 0 {
		incomplete = append(incomplete, "metadata.labels")
	}
	return incomplete
}

// automationFields: the selector, the source reference, the push commit,
// the observed revision and the observed policies are join inputs, kept
// whole; the template, its values and push option values are never read.
func automationFields(u *unstructured.Unstructured, spec, status map[string]any, c *clipper, st stage) []string {
	o := u.Object
	var incomplete []string
	if ref, ok, _ := unstructured.NestedMap(o, "spec", "sourceRef"); ok {
		r := map[string]any{}
		for _, f := range []string{"apiVersion", "kind", "name", "namespace"} {
			copyWhole(ref, r, f)
		}
		spec["sourceRef"] = r
	}
	copyStr(o, spec, c, false, "spec", "interval")
	git := map[string]any{}
	if ref, ok, _ := unstructured.NestedMap(o, "spec", "git", "checkout", "ref"); ok {
		r := map[string]any{}
		for _, f := range []string{"branch", "tag", "semver", "name", "commit"} {
			copyWhole(ref, r, f)
		}
		git["checkout"] = map[string]any{"ref": r}
	}
	if push, ok, _ := unstructured.NestedMap(o, "spec", "git", "push"); ok {
		p := map[string]any{}
		copyWhole(push, p, "branch")
		copyWhole(push, p, "refspec")
		if opts, ok := push["options"].(map[string]any); ok {
			p["optionCount"] = int64(len(opts))
		}
		git["push"] = p
	}
	if commit, ok, _ := unstructured.NestedMap(o, "spec", "git", "commit"); ok {
		cm := map[string]any{}
		if a, ok := commit["author"].(map[string]any); ok {
			author := map[string]any{}
			copyStr(a, author, c, false, "name")
			copyStr(a, author, c, false, "email")
			cm["author"] = author
		}
		if n := refName(commit, "signingKey", "secretRef"); n != "" {
			cm["signingKeySecret"] = clipTo(n, tightClip)
		}
		git["commit"] = cm
	}
	if len(git) > 0 {
		spec["git"] = git
	}
	if up, ok, _ := unstructured.NestedMap(o, "spec", "update"); ok {
		x := map[string]any{}
		copyStr(up, x, c, false, "strategy")
		copyStr(up, x, c, false, "path")
		spec["update"] = x
	}
	if sel, ok, _ := unstructured.NestedMap(o, "spec", "policySelector"); ok {
		if st == stageNoJoins {
			incomplete = append(incomplete, "spec.policySelector")
		} else {
			spec["policySelector"] = runtimeCopy(sel)
		}
	}
	copyWhole(o, status, "status", "lastAutomationRunTime")
	copyWhole(o, status, "status", "lastPushCommit")
	copyWhole(o, status, "status", "lastPushTime")
	copyWhole(o, status, "status", "observedSourceRevision")
	if pols, ok, _ := unstructured.NestedMap(o, "status", "observedPolicies"); ok {
		switch {
		case st == stageNoJoins:
			incomplete = append(incomplete, "status.observedPolicies")
		case len(pols) > maxObservedPolicies:
			incomplete = append(incomplete, "status.observedPolicies")
			status["observedPoliciesCount"] = int64(len(pols))
		default:
			status["observedPolicies"] = runtimeCopy(pols)
		}
	}
	return incomplete
}

// runtimeCopy deep-copies a JSON value.
func runtimeCopy(m map[string]any) map[string]any {
	return deepCopy(m).(map[string]any)
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = deepCopy(e)
		}
		return out
	}
	return v
}

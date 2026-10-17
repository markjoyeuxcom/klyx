// Package fluxop reads the Flux Operator's kinds (coverage S1a, design
// accepted at 421fb2e): FluxInstance, FluxReport, ResourceSet and
// ResourceSetInputProvider, all fluxcd.controlplane.io/v1.
//
// Each watched object is projected field by field into a new object (S1a
// §3): the classification facts a rule reads are never clipped, display
// strings are, and nothing an S0b payload surface holds is copied. The
// projection's size is measured so the observer's budget can admit it.
package fluxop

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/resume"
	"github.com/moomora/klyx/internal/urlscrub"
)

// Group is the operator's API group; Version the one version read.
const (
	Group   = "fluxcd.controlplane.io"
	Version = "v1"
)

// Kinds.
const (
	KindInstance = "FluxInstance"
	KindReport   = "FluxReport"
	KindSet      = "ResourceSet"
	KindProvider = "ResourceSetInputProvider"
)

// Annotations the rules read.
const (
	AnnReconcile   = "fluxcd.controlplane.io/reconcile" // "disabled" suspends
	AnnRequestedAt = "reconcile.fluxcd.io/requestedAt"
	AnnForceAt     = "reconcile.fluxcd.io/forceAt"
)

// Projection limits (S1a §3).
const (
	MaxObject      = 8 << 10 // one projection, measured after it is built
	messageClip    = 1 << 10 // a condition message, for display
	tightClip      = 256     // every display string, when over MaxObject
	maxConditions  = 10
	maxComponents  = 20
	maxReconcilers = 50
	maxRefs        = 50 // dependsOn, inputsFrom
	maxSteps       = 20
	maxSchedules   = 10
	maxShards      = 10
)

// Object is one projected object: a new unstructured object holding only
// the projected fields, plus facts kept in the backend alone. The embedded
// object's own MarshalJSON is what serialises, so the unexported
// fingerprint never leaves the backend (S1a §4).
type Object struct {
	*unstructured.Unstructured
	fingerprint [32]byte
	hasReady    bool
	// Clipped names the display fields clipped or dropped to fit.
	Clipped []string
	// Size is the projection's encoded size in bytes, the budget's measure.
	Size int
}

// Fingerprint is the SHA-256 of Ready's raw status, reason, full message
// and lastTransitionTime, taken before any clipping (S1a §4). ok is false
// without a Ready condition.
func (o *Object) Fingerprint() (fp [32]byte, ok bool) { return o.fingerprint, o.hasReady }

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
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
		return s[:cut] + "…"
	}
	return s
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// url applies the URL rule, then clips.
func (c *clipper) url(path, s string) string { return c.str(path, urlscrub.Field(s).Text) }

// Project builds the projection of one watched object of kind. It never
// mutates obj. The first build clips messages at 1 KiB; one over MaxObject
// is rebuilt with every display string at 256 bytes, and lists shortened;
// the observer refuses one still over.
func Project(kind string) func(obj any) (any, error) {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("fluxop: want *Unstructured, got %T", obj)
		}
		out := build(kind, u, &clipper{limit: messageClip}, false)
		if out.Size > MaxObject {
			out = build(kind, u, &clipper{limit: tightClip}, true)
		}
		return out, nil
	}
}

func build(kind string, u *unstructured.Unstructured, c *clipper, tight bool) *Object {
	m := map[string]any{"apiVersion": u.GetAPIVersion(), "kind": u.GetKind(), "metadata": metadataOf(u)}
	conds, fp, hasReady := conditionsOf(u, c, tight)
	status := map[string]any{}
	if len(conds) > 0 {
		status["conditions"] = conds
	}
	spec := map[string]any{}
	switch kind {
	case KindInstance:
		instanceFields(u, spec, status, c, tight)
	case KindReport:
		reportFields(u, spec, c, tight)
	case KindSet:
		setFields(u, spec, status, c, tight)
	case KindProvider:
		providerFields(u, spec, status, c, tight)
	}
	if len(spec) > 0 {
		m["spec"] = spec
	}
	if len(status) > 0 {
		m["status"] = status
	}
	o := &Object{Unstructured: &unstructured.Unstructured{Object: m}, fingerprint: fp, hasReady: hasReady}
	sort.Strings(c.clipped)
	o.Clipped = c.clipped
	b, err := json.Marshal(m)
	if err != nil {
		o.Size = MaxObject + 1
		return o
	}
	o.Size = len(b) + len(fp)
	return o
}

// ownership labels and annotations the verbs' consequence line reads (C4).
var ownershipKeys = []string{
	"kustomize.toolkit.fluxcd.io/name", "kustomize.toolkit.fluxcd.io/namespace",
	"helm.toolkit.fluxcd.io/name", "helm.toolkit.fluxcd.io/namespace",
	"argocd.argoproj.io/tracking-id", "argocd.argoproj.io/instance", "app.kubernetes.io/instance",
	"app.kubernetes.io/name", "app.kubernetes.io/managed-by",
}

func metadataOf(u *unstructured.Unstructured) map[string]any {
	md := map[string]any{"name": u.GetName(), "uid": string(u.GetUID()), "generation": u.GetGeneration(),
		"creationTimestamp": u.GetCreationTimestamp().UTC().Format("2006-01-02T15:04:05Z")}
	if ns := u.GetNamespace(); ns != "" {
		md["namespace"] = ns
	}
	if rv := u.GetResourceVersion(); rv != "" {
		md["resourceVersion"] = rv
	}
	pick := func(src map[string]string, keys []string) map[string]any {
		out := map[string]any{}
		for _, k := range keys {
			if v, ok := src[k]; ok {
				out[k] = clipTo(v, tightClip)
			}
		}
		return out
	}
	if a := pick(u.GetAnnotations(), append([]string{AnnReconcile, AnnRequestedAt, AnnForceAt}, ownershipKeys...)); len(a) > 0 {
		md["annotations"] = a
	}
	if l := pick(u.GetLabels(), ownershipKeys); len(l) > 0 {
		md["labels"] = l
	}
	return md
}

func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8Start(s[n]) {
		n--
	}
	return s[:n]
}

// conditionsOf projects the conditions (type, status, reason and
// observedGeneration never clipped; the message clipped, after the URL
// rule) and fingerprints Ready from the raw condition.
func conditionsOf(u *unstructured.Unstructured, c *clipper, tight bool) ([]any, [32]byte, bool) {
	var fp [32]byte
	hasReady := false
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	var out []any
	for i, x := range raw {
		cm, ok := x.(map[string]any)
		if !ok {
			continue
		}
		t, _ := cm["type"].(string)
		status, _ := cm["status"].(string)
		reason, _ := cm["reason"].(string)
		msg, _ := cm["message"].(string)
		ltt, _ := cm["lastTransitionTime"].(string)
		if t == "Ready" && !hasReady {
			hasReady = true
			fp = resume.ReadyFingerprint(status, reason, msg, ltt)
		}
		if i >= maxConditions {
			c.clipped = append(c.clipped, "status.conditions")
			continue
		}
		pc := map[string]any{"type": t, "status": status, "reason": reason, "lastTransitionTime": ltt}
		if g, ok := cm["observedGeneration"]; ok {
			pc["observedGeneration"] = g
		}
		if msg != "" {
			pc["message"] = c.url(fmt.Sprintf("status.conditions[%d].message", i), msg)
		}
		out = append(out, pc)
	}
	return out, fp, hasReady
}

// listCap is a list's length cap, halved when tight.
func listCap(n int, tight bool) int {
	if tight {
		return n / 2
	}
	return n
}

func str(u *unstructured.Unstructured, path ...string) string {
	s, _, _ := unstructured.NestedString(u.Object, path...)
	return s
}

func setStr(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func instanceFields(u *unstructured.Unstructured, spec, status map[string]any, c *clipper, tight bool) {
	if d, ok, _ := unstructured.NestedMap(u.Object, "spec", "distribution"); ok {
		pd := map[string]any{}
		for _, k := range []string{"version", "registry", "variant", "artifact"} {
			if s, ok := d[k].(string); ok {
				setStr(pd, k, c.url("spec.distribution."+k, s))
			}
		}
		spec["distribution"] = pd
	}
	if comps, ok, _ := unstructured.NestedStringSlice(u.Object, "spec", "components"); ok {
		spec["components"] = capStrings(comps, listCap(maxComponents, tight), "spec.components", c)
	}
	if cl, ok, _ := unstructured.NestedMap(u.Object, "spec", "cluster"); ok {
		pc := map[string]any{}
		for _, k := range []string{"type", "domain", "size"} {
			if s, ok := cl[k].(string); ok {
				setStr(pc, k, c.str("spec.cluster."+k, s))
			}
		}
		for _, k := range []string{"multitenant", "networkPolicy", "objectLevelWorkloadIdentity", "multitenantWorkloadIdentity"} {
			if b, ok := cl[k].(bool); ok {
				pc[k] = b
			}
		}
		spec["cluster"] = pc
	}
	if sh, ok, _ := unstructured.NestedMap(u.Object, "spec", "sharding"); ok {
		ps := map[string]any{}
		if k, ok := sh["key"].(string); ok {
			setStr(ps, "key", c.str("spec.sharding.key", k))
		}
		if shards, ok, _ := unstructured.NestedStringSlice(u.Object, "spec", "sharding", "shards"); ok {
			ps["shards"] = capStrings(shards, listCap(maxShards, tight), "spec.sharding.shards", c)
		}
		spec["sharding"] = ps
	}
	if sy, ok, _ := unstructured.NestedMap(u.Object, "spec", "sync"); ok {
		ps := map[string]any{}
		for _, k := range []string{"kind", "ref", "path", "name", "provider", "interval"} {
			if s, ok := sy[k].(string); ok {
				setStr(ps, k, c.str("spec.sync."+k, s))
			}
		}
		if s, ok := sy["url"].(string); ok {
			setStr(ps, "url", c.url("spec.sync.url", s))
		}
		spec["sync"] = ps
	}
	if w, ok, _ := unstructured.NestedBool(u.Object, "spec", "wait"); ok {
		spec["wait"] = w
	}
	if p, ok, _ := unstructured.NestedSlice(u.Object, "spec", "kustomize", "patches"); ok {
		spec["patchCount"] = int64(len(p)) // the patches are an S0b payload: a count only
	}
	if comps, ok, _ := unstructured.NestedSlice(u.Object, "status", "components"); ok {
		var out []any
		for i, x := range comps {
			if i >= listCap(maxComponents, tight) {
				c.clipped = append(c.clipped, "status.components")
				break
			}
			cm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			pc := map[string]any{}
			for _, k := range []string{"name", "repository", "tag", "digest"} {
				if s, ok := cm[k].(string); ok {
					setStr(pc, k, c.str(fmt.Sprintf("status.components[%d].%s", i, k), s))
				}
			}
			out = append(out, pc)
		}
		status["components"] = out
	}
	for _, k := range []string{"lastAppliedRevision", "lastAttemptedRevision", "lastArtifactRevision"} {
		setStr(status, k, c.str("status."+k, str(u, "status", k)))
	}
}

func reportFields(u *unstructured.Unstructured, spec map[string]any, c *clipper, tight bool) {
	if d, ok, _ := unstructured.NestedMap(u.Object, "spec", "distribution"); ok {
		pd := map[string]any{}
		for _, k := range []string{"entitlement", "status", "version", "managedBy"} {
			if s, ok := d[k].(string); ok {
				setStr(pd, k, c.str("spec.distribution."+k, s))
			}
		}
		spec["distribution"] = pd
	}
	for _, sec := range []struct {
		key  string
		strs []string
		ints []string
	}{
		{"cluster", []string{"serverVersion", "platform"}, []string{"nodes"}},
		{"operator", []string{"apiVersion", "version", "platform"}, nil},
	} {
		if d, ok, _ := unstructured.NestedMap(u.Object, "spec", sec.key); ok {
			pd := map[string]any{}
			for _, k := range sec.strs {
				if s, ok := d[k].(string); ok {
					setStr(pd, k, c.str("spec."+sec.key+"."+k, s))
				}
			}
			for _, k := range sec.ints {
				if n, ok, _ := unstructured.NestedInt64(d, k); ok {
					pd[k] = n
				}
			}
			spec[sec.key] = pd
		}
	}
	if comps, ok, _ := unstructured.NestedSlice(u.Object, "spec", "components"); ok {
		var out []any
		for i, x := range comps {
			if i >= listCap(maxComponents, tight) {
				c.clipped = append(c.clipped, "spec.components")
				break
			}
			cm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			pc := map[string]any{}
			for _, k := range []string{"name", "status", "image"} {
				if s, ok := cm[k].(string); ok {
					setStr(pc, k, c.url(fmt.Sprintf("spec.components[%d].%s", i, k), s))
				}
			}
			if b, ok := cm["ready"].(bool); ok {
				pc["ready"] = b
			}
			out = append(out, pc)
		}
		spec["components"] = out
	}
	if recs, ok, _ := unstructured.NestedSlice(u.Object, "spec", "reconcilers"); ok {
		var out []any
		for i, x := range recs {
			if i >= listCap(maxReconcilers, tight) {
				c.clipped = append(c.clipped, "spec.reconcilers")
				break
			}
			rm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			pr := map[string]any{}
			for _, k := range []string{"apiVersion", "kind"} {
				if s, ok := rm[k].(string); ok {
					setStr(pr, k, c.str(fmt.Sprintf("spec.reconcilers[%d].%s", i, k), s))
				}
			}
			if st, ok := rm["stats"].(map[string]any); ok {
				ps := map[string]any{}
				for _, k := range []string{"running", "failing", "suspended"} {
					if n, ok, _ := unstructured.NestedInt64(st, k); ok {
						ps[k] = n
					}
				}
				if s, ok := st["totalSize"].(string); ok {
					setStr(ps, "totalSize", c.str(fmt.Sprintf("spec.reconcilers[%d].stats.totalSize", i), s))
				}
				pr["stats"] = ps
			}
			out = append(out, pr)
		}
		spec["reconcilers"] = out
	}
	if sy, ok, _ := unstructured.NestedMap(u.Object, "spec", "sync"); ok {
		ps := map[string]any{}
		for _, k := range []string{"id", "path", "status", "source"} {
			if s, ok := sy[k].(string); ok {
				setStr(ps, k, c.url("spec.sync."+k, s))
			}
		}
		if b, ok := sy["ready"].(bool); ok {
			ps["ready"] = b
		}
		spec["sync"] = ps
	}
}

func setFields(u *unstructured.Unstructured, spec, status map[string]any, c *clipper, tight bool) {
	if s := str(u, "spec", "inputStrategy", "name"); s != "" {
		spec["inputStrategy"] = map[string]any{"name": c.str("spec.inputStrategy.name", s)}
	}
	if refs, ok, _ := unstructured.NestedSlice(u.Object, "spec", "inputsFrom"); ok {
		var out []any
		for i, x := range refs {
			if i >= listCap(maxRefs, tight) {
				c.clipped = append(c.clipped, "spec.inputsFrom")
				break
			}
			rm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			pr := map[string]any{}
			for _, k := range []string{"apiVersion", "kind", "name"} {
				if s, ok := rm[k].(string); ok {
					setStr(pr, k, c.str(fmt.Sprintf("spec.inputsFrom[%d].%s", i, k), s))
				}
			}
			if sel, ok := rm["selector"].(map[string]any); ok {
				pr["selector"] = c.str(fmt.Sprintf("spec.inputsFrom[%d].selector", i), selectorString(sel))
			}
			out = append(out, pr)
		}
		spec["inputsFrom"] = out
	}
	if deps, ok, _ := unstructured.NestedSlice(u.Object, "spec", "dependsOn"); ok {
		var out []any
		for i, x := range deps {
			if i >= listCap(maxRefs, tight) {
				c.clipped = append(c.clipped, "spec.dependsOn")
				break
			}
			dm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			pd := map[string]any{}
			for _, k := range []string{"apiVersion", "kind", "name", "namespace"} {
				if s, ok := dm[k].(string); ok {
					setStr(pd, k, c.str(fmt.Sprintf("spec.dependsOn[%d].%s", i, k), s))
				}
			}
			if b, ok := dm["ready"].(bool); ok {
				pd["ready"] = b
			}
			if e, ok := dm["readyExpr"].(string); ok && e != "" {
				pd["hasReadyExpr"] = true // shown on demand, not retained
			}
			out = append(out, pd)
		}
		spec["dependsOn"] = out
	}
	if steps, ok, _ := unstructured.NestedSlice(u.Object, "spec", "steps"); ok {
		var names []string
		for _, x := range steps {
			if sm, ok := x.(map[string]any); ok {
				n, _ := sm["name"].(string)
				names = append(names, n)
			}
		}
		spec["steps"] = capStrings(names, listCap(maxSteps, tight), "spec.steps", c)
		spec["stepCount"] = int64(len(steps))
	}
	if in, ok, _ := unstructured.NestedSlice(u.Object, "spec", "inputs"); ok {
		spec["inputCount"] = int64(len(in))
	}
	if res, ok, _ := unstructured.NestedSlice(u.Object, "spec", "resources"); ok {
		spec["resourceCount"] = int64(len(res))
	}
	if _, ok, _ := unstructured.NestedString(u.Object, "spec", "resourcesTemplate"); ok {
		spec["hasTemplate"] = true
	}
	setStr(spec, "serviceAccountName", c.str("spec.serviceAccountName", str(u, "spec", "serviceAccountName")))
	if w, ok, _ := unstructured.NestedBool(u.Object, "spec", "wait"); ok {
		spec["wait"] = w
	}
	setStr(status, "lastAppliedRevision", c.str("status.lastAppliedRevision", str(u, "status", "lastAppliedRevision")))
	if inv, ok, _ := unstructured.NestedSlice(u.Object, "status", "inventory", "entries"); ok {
		status["inventoryCount"] = int64(len(inv)) // the entries are read on demand
	}
}

func providerFields(u *unstructured.Unstructured, spec, status map[string]any, c *clipper, tight bool) {
	setStr(spec, "type", c.str("spec.type", str(u, "spec", "type")))
	setStr(spec, "url", c.url("spec.url", str(u, "spec", "url")))
	setStr(spec, "serviceAccountName", c.str("spec.serviceAccountName", str(u, "spec", "serviceAccountName")))
	for _, k := range []string{"secretRef", "certSecretRef"} {
		if n := str(u, "spec", k, "name"); n != "" {
			spec[k] = map[string]any{"name": c.str("spec."+k+".name", n)}
		}
	}
	if b, ok, _ := unstructured.NestedBool(u.Object, "spec", "insecure"); ok {
		spec["insecure"] = b
	}
	if sch, ok, _ := unstructured.NestedSlice(u.Object, "spec", "schedule"); ok {
		var out []any
		for i, x := range sch {
			if i >= listCap(maxSchedules, tight) {
				c.clipped = append(c.clipped, "spec.schedule")
				break
			}
			if sm, ok := x.(map[string]any); ok {
				out = append(out, scheduleOf(sm, fmt.Sprintf("spec.schedule[%d]", i), c))
			}
		}
		spec["schedule"] = out
	}
	setStr(status, "lastExportedRevision", c.str("status.lastExportedRevision", str(u, "status", "lastExportedRevision")))
	if ns, ok, _ := unstructured.NestedMap(u.Object, "status", "nextSchedule"); ok {
		p := scheduleOf(ns, "status.nextSchedule", c)
		if w, ok := ns["when"].(string); ok {
			p["when"] = w
		}
		status["nextSchedule"] = p
	}
	if ex, ok, _ := unstructured.NestedSlice(u.Object, "status", "exportedInputs"); ok {
		status["exportedInputCount"] = int64(len(ex)) // values never: S0b, and S1a §6
	}
}

func scheduleOf(sm map[string]any, path string, c *clipper) map[string]any {
	p := map[string]any{}
	for _, k := range []string{"cron", "timeZone", "window"} {
		if s, ok := sm[k].(string); ok {
			setStr(p, k, c.str(path+"."+k, s))
		}
	}
	return p
}

func capStrings(in []string, n int, path string, c *clipper) []any {
	out := make([]any, 0, len(in))
	for i, s := range in {
		if i >= n {
			c.clipped = append(c.clipped, path)
			break
		}
		out = append(out, c.str(fmt.Sprintf("%s[%d]", path, i), s))
	}
	return out
}

// selectorString renders a label selector compactly, as kubectl does.
func selectorString(sel map[string]any) string {
	var parts []string
	if ml, ok := sel["matchLabels"].(map[string]any); ok {
		keys := make([]string, 0, len(ml))
		for k := range ml {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, ml[k]))
		}
	}
	if me, ok := sel["matchExpressions"].([]any); ok {
		for _, x := range me {
			em, ok := x.(map[string]any)
			if !ok {
				continue
			}
			var vals []string
			if vs, ok := em["values"].([]any); ok {
				for _, v := range vs {
					vals = append(vals, fmt.Sprint(v))
				}
			}
			parts = append(parts, fmt.Sprintf("%v %v (%s)", em["key"], strings.ToLower(fmt.Sprint(em["operator"])), strings.Join(vals, ",")))
		}
	}
	return strings.Join(parts, ",")
}

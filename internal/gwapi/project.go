package gwapi

import (
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Projection limits (coverage S7 §8): one projection's encoded size. The
// design's 16 KiB for a Gateway or ListenerSet assumed a listener without
// its status; measured, a listener with its status is about 1.15 KiB, so a
// 64-listener object (the API's maximum) needs 64 KiB once tight.
const (
	MaxGatewayClass = 4 << 10
	MaxGateway      = 64 << 10
	MaxListenerSet  = 64 << 10
	messageClip     = 1 << 10 // a condition message, for display
	tightClip       = 256     // the object's own condition messages, when over the limit
)

// MaxObject is a projected resource's size limit: a larger projection is
// never retained (the kind then reads truncated).
func MaxObject(resource string) int {
	switch resource {
	case "gatewayclasses":
		return MaxGatewayClass
	case "gateways":
		return MaxGateway
	case "listenersets":
		return MaxListenerSet
	}
	return 0
}

// Object is one projected Gateway API object (S7 §8), parsed once when it
// is admitted: its identity, and the typed reading every rule uses.
//
// The projection keeps only the fields Klyx reads - identity and every
// field a rule compares whole, condition messages (display) clipped - in
// the API's own shape, and Size is that shape's JSON-encoded length, the
// budget's measure. The typed value then holds those fields in a fraction
// of the heap an unstructured map would (S1a measured 3.6 heap bytes per
// encoded byte for unstructured projections).
type Object struct {
	// ObjectMeta holds identity, generation, creation time, resource
	// version and labels: what the Observer's refs and the fleet index read.
	metav1.ObjectMeta
	APIVersion string

	Gateway     *GatewayRef   // gateways
	ListenerSet *ListenerSet  // listenersets
	Class       *GatewayClass // gatewayclasses

	// Size is the API-shaped projection's JSON-encoded length.
	Size int
	// Clipped names the fields clipped to fit.
	Clipped []string
}

// SizeOf is the budget's Size function for a projected Object.
func SizeOf(v any) int {
	if o, ok := v.(*Object); ok {
		return o.Size
	}
	return 1 << 30
}

// Project builds the projection of one watched object of resource. It never
// mutates obj. Condition messages are made safe (SafeMessage), then clipped
// at 1 KiB; one over its limit is rebuilt tight - the object's own
// messages at 256 bytes, its listeners' dropped - and the observer refuses
// one still over.
func Project(resource string) func(obj any) (any, error) {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("gwapi: want *Unstructured, got %T", obj)
		}
		pu, c := build(resource, u, &clip{limit: messageClip, listenerLimit: messageClip})
		size := encodedSize(pu)
		if size > MaxObject(resource) {
			pu, c = build(resource, u, &clip{limit: tightClip, listenerLimit: 0})
			size = encodedSize(pu)
		}
		out := &Object{APIVersion: u.GetAPIVersion(), Size: size, Clipped: c.clipped}
		out.Name, out.Namespace, out.UID, out.Generation = u.GetName(), u.GetNamespace(), u.GetUID(), u.GetGeneration()
		out.CreationTimestamp, out.ResourceVersion, out.Labels = u.GetCreationTimestamp(), u.GetResourceVersion(), u.GetLabels()
		switch resource {
		case "gateways":
			g := ParseGatewayRef(pu)
			out.Gateway = &g
		case "listenersets":
			ls := ParseListenerSet(pu)
			out.ListenerSet = &ls
		case "gatewayclasses":
			gc := ParseGatewayClass(pu)
			out.Class = &gc
		}
		return out, nil
	}
}

func encodedSize(u *unstructured.Unstructured) int {
	b, err := json.Marshal(u.Object)
	if err != nil {
		return 1 << 30
	}
	return len(b)
}

// clip clips condition messages, the object's own at limit and its
// listeners' at listenerLimit (0 drops them), and records what it clipped.
type clip struct {
	limit, listenerLimit int
	clipped              []string
}

func (c *clip) message(path, s string, limit int) string {
	// made safe before it is clipped: a credential crossing the clip is
	// removed whole, never cut into a readable prefix
	s = SafeMessage(s)
	if len(s) <= limit {
		return s
	}
	c.clipped = append(c.clipped, path)
	if limit == 0 {
		return ""
	}
	cut := limit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// build is the API-shaped projection: the fields kept, as the API writes
// them.
func build(resource string, u *unstructured.Unstructured, c *clip) (*unstructured.Unstructured, *clip) {
	m := map[string]any{"apiVersion": u.GetAPIVersion(), "kind": u.GetKind(), "metadata": metadataOf(u)}
	spec, status := map[string]any{}, map[string]any{}
	src := u.Object
	if conds := conditionsOf(nested(src, "status", "conditions"), "status.conditions", c, c.limit); conds != nil {
		status["conditions"] = conds
	}
	switch resource {
	case "gatewayclasses":
		copyKeys(spec, mapAt(src, "spec"), "controllerName")
		if fs := supportedFeatures(nested(src, "status", "supportedFeatures")); fs != nil {
			status["supportedFeatures"] = fs
		}
	case "gateways":
		copyKeys(spec, mapAt(src, "spec"), "gatewayClassName")
		if al, ok := mapAt(src, "spec")["allowedListeners"]; ok {
			spec["allowedListeners"] = copyJSON(al)
		}
		listenersInto(spec, status, src, c)
		if addrs := addressesOf(nested(src, "status", "addresses")); addrs != nil {
			status["addresses"] = addrs
		}
		copyKeys(status, mapAt(src, "status"), "attachedListenerSets")
	case "listenersets":
		if p, ok := mapAt(src, "spec")["parentRef"].(map[string]any); ok {
			pr := map[string]any{}
			copyKeys(pr, p, "group", "kind", "namespace", "name")
			spec["parentRef"] = pr
		}
		listenersInto(spec, status, src, c)
	}
	if len(spec) > 0 {
		m["spec"] = spec
	}
	if len(status) > 0 {
		m["status"] = status
	}
	return &unstructured.Unstructured{Object: m}, c
}

// metadataOf keeps identity, the generation the conditions are judged
// against, the creation time the merge's precedence reads, and the labels
// the fleet index reads.
func metadataOf(u *unstructured.Unstructured) map[string]any {
	md := map[string]any{"name": u.GetName(), "uid": string(u.GetUID()), "generation": u.GetGeneration()}
	if ts := u.GetCreationTimestamp(); !ts.IsZero() {
		md["creationTimestamp"] = ts.UTC().Format("2006-01-02T15:04:05Z")
	}
	if ns := u.GetNamespace(); ns != "" {
		md["namespace"] = ns
	}
	if rv := u.GetResourceVersion(); rv != "" {
		md["resourceVersion"] = rv
	}
	if l := u.GetLabels(); len(l) > 0 {
		lm := make(map[string]any, len(l))
		for k, v := range l {
			lm[k] = v
		}
		md["labels"] = lm
	}
	return md
}

// listenersInto projects spec.listeners and status.listeners (S7 §3).
func listenersInto(spec, status, src map[string]any, c *clip) {
	if ls, ok := nested(src, "spec", "listeners").([]any); ok {
		out := make([]any, 0, len(ls))
		for _, l := range ls {
			lm, ok := l.(map[string]any)
			if !ok {
				continue
			}
			pl := map[string]any{}
			copyKeys(pl, lm, "name", "hostname", "port", "protocol")
			if tls, ok := lm["tls"].(map[string]any); ok {
				pt := map[string]any{}
				copyKeys(pt, tls, "mode")
				if refs, ok := tls["certificateRefs"].([]any); ok {
					prs := make([]any, 0, len(refs))
					for _, r := range refs {
						if rm, ok := r.(map[string]any); ok {
							pr := map[string]any{}
							copyKeys(pr, rm, "group", "kind", "namespace", "name")
							prs = append(prs, pr)
						}
					}
					pt["certificateRefs"] = prs
				}
				pl["tls"] = pt
			}
			if ar, ok := lm["allowedRoutes"]; ok {
				pl["allowedRoutes"] = copyJSON(ar)
			}
			out = append(out, pl)
		}
		spec["listeners"] = out
	}
	if ls, ok := nested(src, "status", "listeners").([]any); ok {
		out := make([]any, 0, len(ls))
		for i, l := range ls {
			lm, ok := l.(map[string]any)
			if !ok {
				continue
			}
			pl := map[string]any{}
			copyKeys(pl, lm, "name", "attachedRoutes")
			if ks, ok := lm["supportedKinds"].([]any); ok {
				pks := make([]any, 0, len(ks))
				for _, k := range ks {
					if km, ok := k.(map[string]any); ok {
						pk := map[string]any{}
						copyKeys(pk, km, "group", "kind")
						pks = append(pks, pk)
					}
				}
				pl["supportedKinds"] = pks
			}
			if conds := conditionsOf(lm["conditions"], fmt.Sprintf("status.listeners[%d].conditions", i), c, c.listenerLimit); conds != nil {
				pl["conditions"] = conds
			}
			out = append(out, pl)
		}
		status["listeners"] = out
	}
}

// conditionsOf projects conditions: type, status, reason, observedGeneration
// and lastTransitionTime whole; the message scrubbed and clipped at limit.
func conditionsOf(v any, path string, c *clip, limit int) []any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(raw))
	for i, x := range raw {
		cm, ok := x.(map[string]any)
		if !ok {
			continue
		}
		pc := map[string]any{}
		copyKeys(pc, cm, "type", "status", "reason", "observedGeneration", "lastTransitionTime")
		if msg, _ := cm["message"].(string); msg != "" {
			if m := c.message(fmt.Sprintf("%s[%d].message", path, i), msg, limit); m != "" {
				pc["message"] = m
			}
		}
		out = append(out, pc)
	}
	return out
}

// supportedFeatures keeps the feature names: v1.2 and later write
// {name: X}, older versions a bare string.
func supportedFeatures(v any) []any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(raw))
	for _, f := range raw {
		switch x := f.(type) {
		case string:
			out = append(out, map[string]any{"name": x})
		case map[string]any:
			if n, ok := x["name"].(string); ok {
				out = append(out, map[string]any{"name": n})
			}
		}
	}
	return out
}

func addressesOf(v any) []any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(raw))
	for _, a := range raw {
		if am, ok := a.(map[string]any); ok {
			pa := map[string]any{}
			copyKeys(pa, am, "type", "value")
			out = append(out, pa)
		}
	}
	return out
}

func nested(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func mapAt(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

// copyKeys copies the keys present in src, as written (presence matters: an
// absent parentRef group defaults, an empty one does not).
func copyKeys(dst, src map[string]any, keys ...string) {
	for _, k := range keys {
		if v, ok := src[k]; ok {
			dst[k] = copyJSON(v)
		}
	}
}

// copyJSON copies maps and slices; scalars are values. Unlike
// runtime.DeepCopyJSONValue it never panics on a number type JSON decoding
// would not produce.
func copyJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = copyJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = copyJSON(e)
		}
		return out
	}
	return v
}

package appbridge

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/crd"
)

// AI A1 §3.2: two source renders compared object by object. Visible changes
// are compared after chat's projection, so their values are only what chat
// may send. Two projections can be equal while the objects differ (env
// values, Secret data, a same-length ConfigMap value, Helm values, an
// annotation outside the allowlist, an S0b payload), so the raw objects are
// compared too, in process: every raw difference the projection does not
// show becomes a path marker, "changed (not shown)". Neither a hidden value
// nor any digest of one is ever serialized.

const (
	renderObjectsMax  = 20   // changed objects listed, counted beyond
	renderPathsMax    = 20   // paths listed per object, counted beyond
	renderRawPathsMax = 1000 // raw changed paths walked per object
	renderValueMax    = 120  // a visible value, in characters
)

// pseg is one path segment: a map key, or a list item by name or index.
type pseg struct {
	key    string
	item   string
	isItem bool
}

var plainKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func renderPath(segs []pseg) string {
	var b strings.Builder
	for i, s := range segs {
		switch {
		case s.isItem:
			b.WriteString("[" + s.item + "]")
		case !plainKey.MatchString(s.key):
			b.WriteString("[" + s.key + "]")
		default:
			if i > 0 {
				b.WriteString(".")
			}
			b.WriteString(s.key)
		}
	}
	return b.String()
}

func segsHavePrefix(s, prefix []pseg) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

// namedItems reports whether a list's items are all maps with a unique
// string name: such a list is matched by name, not by position.
func namedItems(l []any) (map[string]any, []string, bool) {
	if len(l) == 0 {
		return nil, nil, false
	}
	by := map[string]any{}
	var order []string
	for _, x := range l {
		m, ok := x.(map[string]any)
		if !ok {
			return nil, nil, false
		}
		n, ok := m["name"].(string)
		if !ok || n == "" {
			return nil, nil, false
		}
		if _, dup := by[n]; dup {
			return nil, nil, false
		}
		by[n] = m
		order = append(order, n)
	}
	return by, order, true
}

func scalarList(l []any) bool {
	for _, x := range l {
		switch x.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

// treeDiff records the paths where a and b differ: a key or item present
// on one side only, a leaf, or a scalar list compared whole.
type treeDiff struct {
	paths [][]pseg
	total int
	max   int
}

func (d *treeDiff) add(at []pseg) {
	d.total++
	if len(d.paths) < d.max {
		d.paths = append(d.paths, append([]pseg(nil), at...))
	}
}

// with is at and one more segment, in a slice of its own.
func with(at []pseg, s pseg) []pseg {
	return append(append(make([]pseg, 0, len(at)+1), at...), s)
}

func (d *treeDiff) walk(a, b any, at []pseg) {
	if reflect.DeepEqual(a, b) {
		return
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			d.add(at)
			return
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			x, inA := av[k]
			y, inB := bv[k]
			next := with(at, pseg{key: k})
			if !inA || !inB {
				d.add(next)
				continue
			}
			d.walk(x, y, next)
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			d.add(at)
			return
		}
		if scalarList(av) && scalarList(bv) {
			d.add(at)
			return
		}
		an, aOrder, aNamed := namedItems(av)
		bn, bOrder, bNamed := namedItems(bv)
		if aNamed && bNamed {
			seen := map[string]bool{}
			for _, n := range append(aOrder, bOrder...) {
				if seen[n] {
					continue
				}
				seen[n] = true
				x, inA := an[n]
				y, inB := bn[n]
				next := with(at, pseg{item: n, isItem: true})
				if !inA || !inB {
					d.add(next)
					continue
				}
				d.walk(x, y, next)
			}
			return
		}
		for i := 0; i < len(av) || i < len(bv); i++ {
			next := with(at, pseg{item: strconv.Itoa(i), isItem: true})
			if i >= len(av) || i >= len(bv) {
				d.add(next)
				continue
			}
			d.walk(av[i], bv[i], next)
		}
	default:
		d.add(at)
	}
}

// lookup follows a path into a tree.
func lookup(v any, segs []pseg) (any, bool) {
	for _, s := range segs {
		if s.isItem {
			l, ok := v.([]any)
			if !ok {
				return nil, false
			}
			if by, _, named := namedItems(l); named {
				if v, ok = by[s.item]; !ok {
					return nil, false
				}
				continue
			}
			i, err := strconv.Atoi(s.item)
			if err != nil || i < 0 || i >= len(l) {
				return nil, false
			}
			v = l[i]
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[s.key]; !ok {
			return nil, false
		}
	}
	return v, true
}

func isContainer(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// alignRule maps a raw path prefix to the projection's: chat's object puts
// labels and annotations at its top, and a workload's pod spec directly
// under spec.template.
type alignRule struct{ raw, proj []string }

func alignRules(group, kind string) []alignRule {
	rules := []alignRule{
		{[]string{"metadata", "labels"}, []string{"labels"}},
		{[]string{"metadata", "annotations"}, []string{"annotations"}},
	}
	switch {
	case group == "apps" && (kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "ReplicaSet"),
		group == "batch" && kind == "Job":
		rules = append(rules, alignRule{[]string{"spec", "template", "spec"}, []string{"spec", "template"}})
	case group == "batch" && kind == "CronJob":
		rules = append(rules, alignRule{[]string{"spec", "jobTemplate", "spec", "template", "spec"}, []string{"spec", "jobTemplate", "template"}})
	}
	return rules
}

// align maps a raw path (or a prefix of one) into the projection's space.
func align(segs []pseg, rules []alignRule) []pseg {
	for _, r := range rules {
		n := 0
		for n < len(r.raw) && n < len(segs) && !segs[n].isItem && segs[n].key == r.raw[n] {
			n++
		}
		if n == 0 {
			continue
		}
		if n == len(r.raw) {
			out := make([]pseg, 0, len(r.proj)+len(segs)-n)
			for _, k := range r.proj {
				out = append(out, pseg{key: k})
			}
			return append(out, segs[n:]...)
		}
		if n == len(segs) {
			// a prefix of the rule: the leading segments both spell alike
			common := 0
			for common < len(r.raw) && common < len(r.proj) && r.raw[common] == r.proj[common] {
				common++
			}
			if n > common {
				n = common
			}
			return append([]pseg(nil), segs[:n]...)
		}
	}
	return segs
}

// unalign maps a projection path back to the manifest's, for display: a
// field chat's projection makes up (a Secret's key list, a ConfigMap's key
// sizes) keeps its projection path.
func unalign(segs []pseg, rules []alignRule) []pseg {
	for _, r := range rules {
		if len(segs) < len(r.proj) {
			continue
		}
		match := true
		for i, k := range r.proj {
			if segs[i].isItem || segs[i].key != k {
				match = false
				break
			}
		}
		if match {
			out := make([]pseg, 0, len(r.raw)+len(segs)-len(r.proj))
			for _, k := range r.raw {
				out = append(out, pseg{key: k})
			}
			return append(out, segs[len(r.proj):]...)
		}
	}
	return segs
}

// Hidden subtrees whose name a marker may carry: chat drops each of them,
// and its name says what kind of change it is.
var revealKeys = map[string]bool{
	"values": true, "valuesObject": true, "valuesFrom": true, "postRenderers": true,
	"data": true, "stringData": true, "binaryData": true,
	"command": true, "args": true, "env": true, "envFrom": true,
}

// revealable: a marker may name segs[k], given what precedes it, past what
// the projection holds. Annotation keys and a Secret's or ConfigMap's data
// keys are names chat already sends or the change's own subject; nothing
// below them is named. (An env var's name needs no rule: the projection
// keeps env entries by name, so the walk reaches it.)
func revealable(segs []pseg, k int, coreData bool) bool {
	s := segs[k]
	if k == 0 {
		return true // a manifest's top-level field
	}
	parent := segs[k-1]
	switch {
	case !s.isItem && revealKeys[s.key] && (parent.isItem || !revealKeys[parent.key]):
		return true
	case !s.isItem && k == 1 && parent.key == "metadata":
		return true
	case !s.isItem && !parent.isItem && s.key == "metadata" && parent.key == "template":
		return true // a pod template's metadata: chat's projection holds none of it
	case !s.isItem && !parent.isItem && parent.key == "metadata" && (s.key == "labels" || s.key == "annotations"):
		return true
	case !s.isItem && k == 2 && segs[0].key == "metadata" && parent.key == "annotations":
		return true
	case !s.isItem && k == 1 && coreData && (parent.key == "data" || parent.key == "stringData" || parent.key == "binaryData"):
		return true
	}
	return false
}

// markerPath is how far a hidden change is named: down the raw path while
// the projection holds containers there, stopping at a leaf the projection
// holds (a placeholder, a masked value) or where it holds nothing, then on
// through names a marker may carry.
func markerPath(raw []pseg, rules []alignRule, projA, projB any, coreData bool) []pseg {
	k := 0
	for i := range raw {
		ap := align(raw[:i+1], rules)
		va, inA := lookup(projA, ap)
		vb, inB := lookup(projB, ap)
		if !inA && !inB {
			break
		}
		k = i + 1
		if !isContainer(va) && !isContainer(vb) {
			break
		}
	}
	for k < len(raw) && revealable(raw, k, coreData) {
		k++
	}
	return raw[:k]
}

func hasEncrypted(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.HasPrefix(t, "ENC[")
	case map[string]any:
		for _, x := range t {
			if hasEncrypted(x) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if hasEncrypted(x) {
				return true
			}
		}
	}
	return false
}

// chatVisibleChange is a change chat's projection shows, with its
// projected values.
type chatVisibleChange struct {
	Path string `json:"path"`
	Old  string `json:"old,omitempty"`
	New  string `json:"new,omitempty"`
	Note string `json:"note,omitempty"` // added | removed | changed (encrypted)
}

// chatHiddenChange is a raw change the projection does not show.
type chatHiddenChange struct {
	Path   string `json:"path"`
	Change string `json:"change"` // changed (not shown) | added (not shown) | ...
}

type chatRenderChange struct {
	Group      string              `json:"group,omitempty"`
	Kind       string              `json:"kind"`
	Namespace  string              `json:"namespace,omitempty"`
	Name       string              `json:"name"`
	Change     string              `json:"change"` // changed | added | removed
	Visible    []chatVisibleChange `json:"visible,omitempty"`
	Hidden     []chatHiddenChange  `json:"hidden,omitempty"`
	PathsTotal int                 `json:"pathsTotal,omitempty"`
}

type renderKey struct{ group, kind, ns, name string }

func keyOf(u *unstructured.Unstructured) renderKey {
	gv, _ := schema.ParseGroupVersion(u.GetAPIVersion())
	return renderKey{gv.Group, u.GetKind(), u.GetNamespace(), u.GetName()}
}

// chatResolve names a rendered kind's resource, for chat's projection.
type chatResolve func(gv schema.GroupVersion, kind string) schema.GroupVersionResource

// projectedTree is chat's projection of a rendered object, sanitised first,
// as a plain tree: what chat would send of it.
func projectedTree(u *unstructured.Unstructured, resolve chatResolve, p *projector) any {
	gv, _ := schema.ParseGroupVersion(u.GetAPIVersion())
	gvr := resolve(gv, u.GetKind())
	s := &unstructured.Unstructured{Object: crd.SanitizeEmbedded(gvr.Group, u.GetKind(), u.Object).Object}
	o := chatProject(gvr, s)
	p.redacted += o.Redacted
	tree := map[string]any{}
	b, err := json.Marshal(struct {
		Labels      map[string]string `json:"labels,omitempty"`
		Annotations map[string]string `json:"annotations,omitempty"`
		Spec        any               `json:"spec,omitempty"`
		Status      any               `json:"status,omitempty"`
	}{o.Labels, o.Annotations, o.Spec, o.Status})
	if err == nil {
		_ = json.Unmarshal(b, &tree)
	}
	return tree
}

func visibleValue(v any, ok bool) string {
	if !ok {
		return ""
	}
	var s string
	if str, isStr := v.(string); isStr {
		s = str
	} else {
		b, _ := json.Marshal(v)
		s = string(b)
	}
	if r := []rune(s); len(r) > renderValueMax {
		s = string(r[:renderValueMax]) + "…"
	}
	return s
}

// compareRenders compares two renders object by object. The result lists
// at most renderObjectsMax objects; total counts every changed one.
func compareRenders(old, new []*unstructured.Unstructured, resolve chatResolve, p *projector) ([]chatRenderChange, int) {
	olds, news := map[renderKey]*unstructured.Unstructured{}, map[renderKey]*unstructured.Unstructured{}
	var keys []renderKey
	for _, u := range old {
		k := keyOf(u)
		if _, dup := olds[k]; !dup {
			keys = append(keys, k)
		}
		olds[k] = u
	}
	for _, u := range new {
		k := keyOf(u)
		if _, inOld := olds[k]; !inOld {
			if _, dup := news[k]; !dup {
				keys = append(keys, k)
			}
		}
		news[k] = u
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.ns != b.ns {
			return a.ns < b.ns
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return a.group < b.group
	})
	var out []chatRenderChange
	total := 0
	for _, k := range keys {
		a, b := olds[k], news[k]
		c := chatRenderChange{Group: k.group, Kind: p.str(k.kind, chatCheckReasonMax), Namespace: p.str(k.ns, chatStringMax), Name: p.str(k.name, chatStringMax)}
		switch {
		case a == nil:
			c.Change = "added"
		case b == nil:
			c.Change = "removed"
		case reflect.DeepEqual(a.Object, b.Object):
			continue
		default:
			c.Change = "changed"
			c.Visible, c.Hidden, c.PathsTotal = objectChanges(a, b, k, resolve, p)
		}
		total++
		if len(out) < renderObjectsMax {
			out = append(out, c)
		}
	}
	return out, total
}

// objectChanges lists one object's visible changes and its hidden markers.
func objectChanges(a, b *unstructured.Unstructured, k renderKey, resolve chatResolve, p *projector) ([]chatVisibleChange, []chatHiddenChange, int) {
	pa, pb := projectedTree(a, resolve, p), projectedTree(b, resolve, p)
	rules := alignRules(k.group, k.kind)
	vis := &treeDiff{max: renderRawPathsMax}
	vis.walk(pa, pb, nil)
	var visible []chatVisibleChange
	for _, path := range vis.paths {
		va, inA := lookup(pa, path)
		vb, inB := lookup(pb, path)
		c := chatVisibleChange{Path: chatScrub(renderPath(unalign(path, rules)), &p.redacted)}
		switch {
		case hasEncrypted(va) || hasEncrypted(vb):
			c.Note = "changed (encrypted)"
		case !inA:
			c.Note, c.New = "added", visibleValue(vb, inB)
		case !inB:
			c.Note, c.Old = "removed", visibleValue(va, inA)
		default:
			c.Old, c.New = visibleValue(va, inA), visibleValue(vb, inB)
		}
		visible = append(visible, c)
	}

	coreData := k.group == "" && (k.kind == "Secret" || k.kind == "ConfigMap")
	raw := &treeDiff{max: renderRawPathsMax}
	raw.walk(a.Object, b.Object, nil)
	seen := map[string]bool{}
	var hidden []chatHiddenChange
	for _, r := range raw.paths {
		ap := align(r, rules)
		explained := false
		for _, v := range vis.paths {
			if segsHavePrefix(ap, v) || segsHavePrefix(v, ap) {
				explained = true
				break
			}
		}
		if explained {
			continue
		}
		m := markerPath(r, rules, pa, pb, coreData)
		key := renderPath(m)
		if seen[key] {
			continue
		}
		seen[key] = true
		va, inA := lookup(a.Object, m)
		vb, inB := lookup(b.Object, m)
		change := "changed"
		switch {
		case !inA:
			change = "added"
		case !inB:
			change = "removed"
		}
		if hasEncrypted(va) || hasEncrypted(vb) {
			change += " (encrypted)"
		} else {
			change += " (not shown)"
		}
		hidden = append(hidden, chatHiddenChange{Path: chatScrub(key, &p.redacted), Change: change})
	}
	if raw.total > len(raw.paths) {
		// the walk stopped: say the rest is not shown, never that it is equal
		hidden = append(hidden, chatHiddenChange{Path: "(further fields)", Change: "not compared (bound)"})
	}
	if len(visible) == 0 && len(hidden) == 0 {
		hidden = append(hidden, chatHiddenChange{Path: "(object)", Change: "changed (not shown)"})
	}
	// at most renderPathsMax paths, with room kept for hidden ones: a
	// visible change never crowds every marker out
	n := len(visible) + len(hidden)
	keep := min(len(hidden), 5)
	if len(visible) > renderPathsMax-keep {
		visible = visible[:renderPathsMax-keep]
	}
	if room := renderPathsMax - len(visible); len(hidden) > room {
		hidden = hidden[:room]
	}
	return visible, hidden, n
}

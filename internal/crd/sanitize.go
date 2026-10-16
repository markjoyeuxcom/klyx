package crd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/moomora/klyx/internal/urlscrub"
)

// The embedded-payload sanitiser (coverage S0b, accepted at 30147ae). Klyx
// masks Secrets by kind; these kinds carry Secret material inside their spec
// or status instead - templates, patches, inline values, embedded Secret
// objects - and URLs anywhere can carry credentials in their user info. One
// backend function sanitises a copy before any DTO, string, cell, prompt or
// diff is made. It is non-mutating and idempotent, and on a depth or type
// failure it withholds rather than returns raw content.

// Placeholders. None carries a length: a length is information, and a second
// run must leave a placeholder as it is.
const (
	OmittedText   = "<omitted: may carry secrets>"
	OmittedValue  = "<omitted>"
	WithheldValue = "<withheld>"
	MaskedValue   = "<masked>"
)

// Describe modes (S0b §4).
const (
	DescribeWithheld    = "withheld"
	DescribeURLScrubbed = "url-scrubbed"
)

// sanitizeMaxDepth bounds every walk; deeper content is withheld.
const sanitizeMaxDepth = 64

// Sanitized is a sanitised copy and its policy metadata.
type Sanitized struct {
	Object map[string]any
	// Paths are the masked, omitted, withheld or URL-scrubbed paths, sorted.
	Paths []string
	// EditEligible is false when the sanitiser changed anything that a
	// round-trip edit could write back (a payload or a URL's user info). The
	// last-applied snapshot's removal does not count: an apply never sends it.
	EditEligible bool
	// DescribeMode is DescribeWithheld for a registered payload kind,
	// DescribeURLScrubbed for every other kind.
	DescribeMode string
}

type shape int

const (
	shapeEmbedded   shape = iota // objects; a core/v1 Secret among them is masked
	shapeOpaque                  // text that is omitted whole
	shapeFreeForm                // keys kept, leaf values omitted
	shapeSource                  // an Argo ApplicationSource
	shapeTemplate                // an ApplicationSet template (its spec's sources)
	shapeGenerators              // ApplicationSet generators, at any nesting
)

// surface is one registered payload location. A "[]" segment is every item
// of a list.
type surface struct {
	path  []string
	shape shape
}

func p(path string, s shape) surface { return surface{strings.Split(path, "."), s} }

// payloadKinds is the registry (S0b §1), keyed by "group/Kind", whatever
// version is served.
var payloadKinds = map[string][]surface{
	"fluxcd.controlplane.io/ResourceSet": {
		p("spec.resources.[]", shapeEmbedded),
		p("spec.steps.[].resources.[]", shapeEmbedded),
		p("spec.resourcesTemplate", shapeOpaque),
		p("spec.steps.[].resourcesTemplate", shapeOpaque),
		p("spec.inputs", shapeFreeForm),
	},
	"fluxcd.controlplane.io/ResourceSetInputProvider": {
		p("spec.defaultValues", shapeFreeForm),
		p("status.exportedInputs", shapeFreeForm),
	},
	"fluxcd.controlplane.io/FluxInstance": {
		p("spec.kustomize.patches.[].patch", shapeOpaque),
	},
	"kustomize.toolkit.fluxcd.io/Kustomization": {
		p("spec.patches.[].patch", shapeOpaque),
		p("spec.patchesStrategicMerge", shapeOpaque),
		p("spec.patchesJson6902.[].patch", shapeOpaque),
		p("spec.postBuild.substitute", shapeFreeForm),
	},
	"helm.toolkit.fluxcd.io/HelmRelease": {
		p("spec.values", shapeFreeForm),
		p("spec.postRenderers.[].kustomize.patches.[].patch", shapeOpaque),
		p("spec.postRenderers.[].kustomize.patchesStrategicMerge", shapeOpaque),
		p("spec.postRenderers.[].kustomize.patchesJson6902.[].patch", shapeOpaque),
	},
	"argoproj.io/Application": {
		p("spec.source", shapeSource),
		p("spec.sources.[]", shapeSource),
		p("status.history.[].source", shapeSource),
		p("status.history.[].sources.[]", shapeSource),
		p("status.sync.comparedTo.source", shapeSource),
		p("status.sync.comparedTo.sources.[]", shapeSource),
		p("status.operationState.operation.sync.source", shapeSource),
		p("status.operationState.operation.sync.sources.[]", shapeSource),
		p("status.operationState.syncResult.source", shapeSource),
		p("status.operationState.syncResult.sources.[]", shapeSource),
		p("operation.sync.source", shapeSource),
		p("operation.sync.sources.[]", shapeSource),
		p("operation.sync.manifests", shapeOpaque),
		p("status.operationState.operation.sync.manifests", shapeOpaque),
	},
	"argoproj.io/ApplicationSet": {
		p("spec.template", shapeTemplate),
		p("spec.templatePatch", shapeOpaque),
		p("spec.generators", shapeGenerators),
	},
}

// IsPayloadKind reports whether group/kind is in the registry.
func IsPayloadKind(group, kind string) bool {
	_, ok := payloadKinds[group+"/"+kind]
	return ok
}

// sanitizer carries one run's changes.
type sanitizer struct {
	paths map[string]bool
	edit  bool // a change that blocks a round-trip edit
}

func (s *sanitizer) mark(path string, blocksEdit bool) {
	if s.paths == nil {
		s.paths = map[string]bool{}
	}
	s.paths[path] = true
	if blocksEdit {
		s.edit = true
	}
}

// SanitizeEmbedded returns a sanitised copy of obj (group and kind name the
// object's own type) with its policy metadata. obj is never mutated.
func SanitizeEmbedded(group, kind string, obj map[string]any) Sanitized {
	s := &sanitizer{}
	out, _ := deepCopyBounded(obj, 0, s, "").(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	out = s.object(group, kind, out, "", 0)
	paths := make([]string, 0, len(s.paths))
	for k := range s.paths {
		paths = append(paths, k)
	}
	sort.Strings(paths)
	mode := DescribeURLScrubbed
	if IsPayloadKind(group, kind) {
		mode = DescribeWithheld
	}
	return Sanitized{Object: out, Paths: paths, EditEligible: !s.edit, DescribeMode: mode}
}

// object sanitises one object in place (a copy already): its registered
// surfaces, its last-applied snapshot when registered, then the URL rule
// over every string leaf.
func (s *sanitizer) object(group, kind string, o map[string]any, at string, depth int) map[string]any {
	if group == "" && kind == "Secret" {
		masked, _ := MaskSecretData(o)
		for _, f := range []string{"data", "stringData"} {
			if m, ok := o[f].(map[string]any); ok && len(m) > 0 && !allMasked(m) {
				s.mark(join(at, f), true)
			}
		}
		o = masked
	}
	if surfaces, ok := payloadKinds[group+"/"+kind]; ok {
		if hasLastApplied(o) {
			o = stripLastAppliedConfig(o)
			s.mark(join(join(at, "metadata"), "annotations")+"["+strconv.Quote(lastAppliedConfigAnnotation)+"]", false)
		}
		for _, sf := range surfaces {
			s.apply(o, sf.path, sf.shape, at, depth)
		}
	}
	s.urls(o, at, 0)
	return o
}

func allMasked(m map[string]any) bool {
	for _, v := range m {
		if v != MaskedValue {
			return false
		}
	}
	return true
}

func hasLastApplied(o map[string]any) bool {
	md, _ := o["metadata"].(map[string]any)
	ann, _ := md["annotations"].(map[string]any)
	_, ok := ann[lastAppliedConfigAnnotation]
	return ok
}

// apply walks path inside v and transforms what it reaches by shape.
func (s *sanitizer) apply(v any, path []string, sh shape, at string, depth int) {
	if depth > sanitizeMaxDepth {
		return
	}
	switch len(path) {
	case 0:
		return
	case 1:
		parent, ok := v.(map[string]any)
		if !ok {
			return
		}
		child, present := parent[path[0]]
		if !present || child == nil {
			return
		}
		parent[path[0]] = s.transform(child, sh, join(at, path[0]), depth+1)
		return
	}
	if path[0] == "[]" {
		list, ok := v.([]any)
		if !ok {
			return
		}
		for i, item := range list {
			s.apply(item, path[1:], sh, fmt.Sprintf("%s[%d]", at, i), depth+1)
		}
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if path[1] == "[]" && len(path) == 2 {
		// every item of the list at path[0]
		list, ok := m[path[0]].([]any)
		if !ok {
			return
		}
		for i, item := range list {
			list[i] = s.transform(item, sh, fmt.Sprintf("%s[%d]", join(at, path[0]), i), depth+1)
		}
		return
	}
	s.apply(m[path[0]], path[1:], sh, join(at, path[0]), depth+1)
}

// transform applies one shape to a value.
func (s *sanitizer) transform(v any, sh shape, at string, depth int) any {
	if depth > sanitizeMaxDepth {
		s.mark(at, true)
		return WithheldValue
	}
	switch sh {
	case shapeOpaque:
		if isPlaceholder(v) || empty(v) {
			return v
		}
		s.mark(at, true)
		return OmittedText
	case shapeFreeForm:
		return s.freeForm(v, at, depth)
	case shapeEmbedded:
		o, ok := v.(map[string]any)
		if !ok {
			if empty(v) || isPlaceholder(v) {
				return v
			}
			s.mark(at, true)
			return WithheldValue
		}
		g, k := groupKindOf(o)
		return s.object(g, k, o, at, depth+1)
	case shapeSource:
		o, ok := v.(map[string]any)
		if !ok {
			return v
		}
		s.source(o, at, depth+1)
		return o
	case shapeTemplate:
		o, ok := v.(map[string]any)
		if !ok {
			return v
		}
		s.template(o, at, depth+1)
		return o
	case shapeGenerators:
		list, ok := v.([]any)
		if !ok {
			return v
		}
		for i, g := range list {
			if gm, ok := g.(map[string]any); ok {
				s.generator(gm, fmt.Sprintf("%s[%d]", at, i), depth+1)
			}
		}
		return list
	}
	return v
}

// freeForm keeps the key structure and omits every leaf value. A list of
// scalars keeps only its length.
func (s *sanitizer) freeForm(v any, at string, depth int) any {
	if depth > sanitizeMaxDepth {
		s.mark(at, true)
		return WithheldValue
	}
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]any:
		for k, child := range t {
			t[k] = s.freeForm(child, join(at, k), depth+1)
		}
		return t
	case []any:
		scalars := true
		for _, c := range t {
			switch c.(type) {
			case map[string]any, []any:
				scalars = false
			}
		}
		if scalars {
			if len(t) == 1 && isPlaceholder(t[0]) {
				return t
			}
			if len(t) == 0 {
				return t
			}
			s.mark(at, true)
			return []any{fmt.Sprintf("%s × %d", OmittedValue, len(t))}
		}
		for i, c := range t {
			t[i] = s.freeForm(c, fmt.Sprintf("%s[%d]", at, i), depth+1)
		}
		return t
	default:
		if isPlaceholder(v) {
			return v
		}
		s.mark(at, true)
		return OmittedValue
	}
}

// source sanitises one Argo ApplicationSource (S0b §1.1). Reference fields
// (repoURL, path, chart, targetRevision, valueFiles, fileParameters paths,
// jsonnet libs) stay readable.
func (s *sanitizer) source(o map[string]any, at string, depth int) {
	if h, ok := o["helm"].(map[string]any); ok {
		ha := join(at, "helm")
		if v, ok := h["values"]; ok && !empty(v) && !isPlaceholder(v) {
			h["values"] = OmittedText
			s.mark(join(ha, "values"), true)
		}
		if v, ok := h["valuesObject"]; ok && v != nil {
			h["valuesObject"] = s.freeForm(v, join(ha, "valuesObject"), depth+1)
		}
		omitItemField(s, h, "parameters", "value", ha, depth)
	}
	if pl, ok := o["plugin"].(map[string]any); ok {
		pa := join(at, "plugin")
		omitItemField(s, pl, "env", "value", pa, depth)
		if params, ok := pl["parameters"].([]any); ok {
			for i, it := range params {
				if m, ok := it.(map[string]any); ok {
					for _, f := range []string{"string", "array", "map"} {
						if v, ok := m[f]; ok && v != nil {
							m[f] = s.freeForm(v, fmt.Sprintf("%s.parameters[%d].%s", pa, i, f), depth+1)
						}
					}
				}
			}
		}
	}
	if d, ok := o["directory"].(map[string]any); ok {
		if j, ok := d["jsonnet"].(map[string]any); ok {
			ja := join(at, "directory.jsonnet")
			omitItemField(s, j, "extVars", "value", ja, depth)
			omitItemField(s, j, "tlas", "value", ja, depth)
		}
	}
	if k, ok := o["kustomize"].(map[string]any); ok {
		if patches, ok := k["patches"].([]any); ok {
			for i, it := range patches {
				if m, ok := it.(map[string]any); ok {
					if v, ok := m["patch"]; ok && !empty(v) && !isPlaceholder(v) {
						m["patch"] = OmittedText
						s.mark(fmt.Sprintf("%s.kustomize.patches[%d].patch", at, i), true)
					}
				}
			}
		}
	}
}

// omitItemField omits field f of every item of the list at key.
func omitItemField(s *sanitizer, parent map[string]any, key, f, at string, depth int) {
	list, ok := parent[key].([]any)
	if !ok {
		return
	}
	for i, it := range list {
		if m, ok := it.(map[string]any); ok {
			if v, ok := m[f]; ok && v != nil {
				m[f] = s.freeForm(v, fmt.Sprintf("%s.%s[%d].%s", at, key, i, f), depth+1)
			}
		}
	}
}

// template sanitises an ApplicationSet template: its spec's sources.
func (s *sanitizer) template(o map[string]any, at string, depth int) {
	spec, ok := o["spec"].(map[string]any)
	if !ok {
		return
	}
	sa := join(at, "spec")
	if src, ok := spec["source"].(map[string]any); ok {
		s.source(src, join(sa, "source"), depth+1)
	}
	if list, ok := spec["sources"].([]any); ok {
		for i, it := range list {
			if src, ok := it.(map[string]any); ok {
				s.source(src, fmt.Sprintf("%s.sources[%d]", sa, i), depth+1)
			}
		}
	}
}

// generator sanitises one ApplicationSet generator, of whatever type, and
// recurses into matrix and merge generators.
func (s *sanitizer) generator(g map[string]any, at string, depth int) {
	if depth > sanitizeMaxDepth {
		return
	}
	for typ, body := range g {
		b, ok := body.(map[string]any)
		if !ok {
			continue
		}
		ba := join(at, typ)
		for _, f := range []string{"elements", "values"} {
			if v, ok := b[f]; ok && v != nil {
				b[f] = s.freeForm(v, join(ba, f), depth+1)
			}
		}
		if v, ok := b["elementsYaml"]; ok && !empty(v) && !isPlaceholder(v) {
			b["elementsYaml"] = OmittedText
			s.mark(join(ba, "elementsYaml"), true)
		}
		if in, ok := b["input"].(map[string]any); ok {
			if v, ok := in["parameters"]; ok && v != nil {
				in["parameters"] = s.freeForm(v, join(ba, "input.parameters"), depth+1)
			}
		}
		if t, ok := b["template"].(map[string]any); ok {
			s.template(t, join(ba, "template"), depth+1)
		}
		if list, ok := b["generators"].([]any); ok {
			for i, it := range list {
				if gm, ok := it.(map[string]any); ok {
					s.generator(gm, fmt.Sprintf("%s.generators[%d]", ba, i), depth+1)
				}
			}
		}
	}
}

// urls applies the URL rule to every string leaf.
func (s *sanitizer) urls(v any, at string, depth int) {
	if depth > sanitizeMaxDepth {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if str, ok := child.(string); ok {
				if r := urlscrub.Text(str); r.Changed() {
					t[k] = r.Text
					s.mark(join(at, k), true)
				}
				continue
			}
			s.urls(child, join(at, k), depth+1)
		}
	case []any:
		for i, child := range t {
			if str, ok := child.(string); ok {
				if r := urlscrub.Text(str); r.Changed() {
					t[i] = r.Text
					s.mark(fmt.Sprintf("%s[%d]", at, i), true)
				}
				continue
			}
			s.urls(child, fmt.Sprintf("%s[%d]", at, i), depth+1)
		}
	}
}

func groupKindOf(o map[string]any) (string, string) {
	av, _ := o["apiVersion"].(string)
	k, _ := o["kind"].(string)
	g := ""
	if i := strings.Index(av, "/"); i >= 0 {
		g = av[:i]
	}
	return g, k
}

func isPlaceholder(v any) bool {
	str, ok := v.(string)
	if !ok {
		return false
	}
	return str == OmittedText || str == OmittedValue || str == WithheldValue || str == MaskedValue ||
		strings.HasPrefix(str, OmittedValue+" × ")
}

func empty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// join appends a key to a path. A key with a dot, bracket or quote is
// written ["quoted"], so ValueAt can read the path back unambiguously.
func join(at, k string) string {
	if strings.ContainsAny(k, ".[]\"") {
		return at + "[" + strconv.Quote(k) + "]"
	}
	if at == "" {
		return k
	}
	return at + "." + k
}

// ValueAt reads the value at a path written by the sanitiser: dotted keys,
// [n] indexes and ["quoted"] keys.
func ValueAt(obj map[string]any, path string) (any, bool) {
	var cur any = obj
	for len(path) > 0 {
		switch {
		case path[0] == '.':
			path = path[1:]
		case strings.HasPrefix(path, "[\""):
			end := strings.Index(path, "\"]")
			for end > 0 && path[end-1] == '\\' {
				next := strings.Index(path[end+1:], "\"]")
				if next < 0 {
					return nil, false
				}
				end += next + 1
			}
			if end < 0 {
				return nil, false
			}
			key, err := strconv.Unquote(path[1 : end+1])
			if err != nil {
				return nil, false
			}
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = m[key]; !ok {
				return nil, false
			}
			path = path[end+2:]
		case path[0] == '[':
			end := strings.IndexByte(path, ']')
			if end < 0 {
				return nil, false
			}
			i, err := strconv.Atoi(path[1:end])
			l, ok := cur.([]any)
			if err != nil || !ok || i < 0 || i >= len(l) {
				return nil, false
			}
			cur, path = l[i], path[end+1:]
		default:
			end := strings.IndexAny(path, ".[")
			if end < 0 {
				end = len(path)
			}
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = m[path[:end]]; !ok {
				return nil, false
			}
			path = path[end:]
		}
	}
	return cur, true
}

// deepCopyBounded copies maps and lists; beyond the depth bound it withholds.
func deepCopyBounded(v any, depth int, s *sanitizer, at string) any {
	if depth > sanitizeMaxDepth {
		s.mark(at, true)
		return WithheldValue
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, c := range t {
			out[k] = deepCopyBounded(c, depth+1, s, join(at, k))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, c := range t {
			out[i] = deepCopyBounded(c, depth+1, s, fmt.Sprintf("%s[%d]", at, i))
		}
		return out
	default:
		return v
	}
}

// HasPlaceholder reports whether a document carries any sanitiser
// placeholder: such a document must never be applied (S0b §5).
func HasPlaceholder(doc string) bool {
	for _, ph := range []string{OmittedText, OmittedValue, WithheldValue, MaskedValue, urlscrub.Redacted, urlscrub.WithheldLine, urlscrub.WithheldURL} {
		if strings.Contains(doc, ph) {
			return true
		}
	}
	return false
}

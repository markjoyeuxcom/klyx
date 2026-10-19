package crd

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/moomora/klyx/internal/urlscrub"
	"github.com/moomora/klyx/internal/velero"
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

// sanitizeMaxSurface bounds one protected surface: a larger one is withheld
// whole, measured by a walk that stops as soon as it is over (S0b §2; Codex's
// implementation review, finding 8). The deep copy of the whole object is
// linear in what the caller already holds, which the API server bounds by
// its request size limit; nothing beyond one copy is allocated.
const sanitizeMaxSurface = 1 << 20

// Sanitized is a sanitised copy and its policy metadata.
type Sanitized struct {
	Object map[string]any
	// Paths are the masked, omitted, withheld or URL-scrubbed paths, sorted.
	Paths []string
	// EditEligible is false when the sanitiser changed anything that a
	// round-trip edit could write back (a payload or a URL's user info). The
	// last-applied snapshot's removal does not count: an apply never sends it.
	EditEligible bool
	// Payload is true when a registered payload surface was non-empty
	// (masked, omitted or withheld) - URL user info and the last-applied
	// snapshot do not count. Describe of a payload kind is released only
	// for a version without one (S0b revision 4).
	Payload bool
	// Messages is true when a kind's message rule changed a condition
	// message (S1b §6): kubectl would print the raw text, so Describe is
	// withheld for this version.
	Messages bool
	// DescribeMode is DescribeWithheld when the object carries a payload or
	// a message its kind's rule withholds, DescribeURLScrubbed otherwise.
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
	shapeOrigin                  // a URL shown only as its scheme and host
	// Velero's free text (coverage S2a §6): each message becomes its
	// class's fixed placeholder; the shape names the field's forms.
	shapeVeleroLocation
	shapeVeleroBackupFailure
	shapeVeleroBackupValidation
	shapeVeleroScheduleValidation
	shapeVeleroRestoreFailure
	shapeVeleroRestoreValidation
)

// veleroFields maps the Velero message shapes onto velero's fields.
var veleroFields = map[shape]string{
	shapeVeleroLocation:           velero.FieldLocationMessage,
	shapeVeleroBackupFailure:      velero.FieldBackupFailure,
	shapeVeleroBackupValidation:   velero.FieldBackupValidation,
	shapeVeleroScheduleValidation: velero.FieldScheduleValidation,
	shapeVeleroRestoreFailure:     velero.FieldRestoreFailure,
	shapeVeleroRestoreValidation:  velero.FieldRestoreValidation,
}

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
	// S1c §6: a Receiver's path is its bearer credential (a generic
	// Receiver authenticates nothing else); a Provider's address carries a
	// webhook secret in its path for most types, and its deprecated proxy
	// can carry user info; an ExternalArtifact's URL can be signed and its
	// metadata is the producer's arbitrary text.
	"notification.toolkit.fluxcd.io/Receiver": {
		p("status.webhookPath", shapeOpaque),
	},
	"notification.toolkit.fluxcd.io/Provider": {
		p("spec.address", shapeOpaque),
		p("spec.proxy", shapeOpaque),
	},
	"source.toolkit.fluxcd.io/ExternalArtifact": {
		p("status.artifact.url", shapeOrigin),
		p("status.artifact.metadata", shapeFreeForm),
	},
	// S1b §6: the commit message template, its values and the push options
	// can carry tokens; values are omitted, keys kept.
	"image.toolkit.fluxcd.io/ImageUpdateAutomation": {
		p("spec.git.commit.messageTemplate", shapeOpaque),
		p("spec.git.commit.messageTemplateValues", shapeFreeForm),
		p("spec.git.push.options", shapeFreeForm),
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
	// S2a §6: hooks carry exec commands (a Restore's init hooks embed whole
	// containers); provider config is arbitrary and can embed credentials
	// in a URL; Velero's messages pass a plugin's error text through.
	"velero.io/Schedule": {
		p("spec.template.hooks", shapeOpaque),
		p("status.validationErrors.[]", shapeVeleroScheduleValidation),
	},
	"velero.io/Backup": {
		p("spec.hooks", shapeOpaque),
		p("status.failureReason", shapeVeleroBackupFailure),
		p("status.validationErrors.[]", shapeVeleroBackupValidation),
	},
	"velero.io/Restore": {
		p("spec.hooks", shapeOpaque),
		p("status.failureReason", shapeVeleroRestoreFailure),
		p("status.validationErrors.[]", shapeVeleroRestoreValidation),
	},
	"velero.io/BackupStorageLocation": {
		p("spec.config", shapeFreeForm),
		p("spec.objectStorage.caCert", shapeOpaque),
		p("status.message", shapeVeleroLocation),
	},
	"velero.io/VolumeSnapshotLocation": {
		p("spec.config", shapeFreeForm),
	},
}

// IsPayloadKind reports whether group/kind is in the registry.
func IsPayloadKind(group, kind string) bool {
	_, ok := payloadKinds[group+"/"+kind]
	return ok
}

// IsPayloadKindName reports whether kind names a registered payload kind in
// any group. It serves text that prints a kind without its group (flux and
// kubectl subjects read Kind/namespace/name), so it errs towards withholding.
func IsPayloadKindName(kind string) bool {
	for k := range payloadKinds {
		if strings.HasSuffix(k, "/"+kind) {
			return true
		}
	}
	return false
}

// sanitizer carries one run's changes.
type sanitizer struct {
	paths    map[string]bool
	edit     bool // a change that blocks a round-trip edit
	payload  bool // a registered surface was non-empty
	messages bool // a kind's message rule changed a condition message
	inURLs   bool // marks made by the URL rule
}

func (s *sanitizer) mark(path string, blocksEdit bool) {
	if s.paths == nil {
		s.paths = map[string]bool{}
	}
	s.paths[path] = true
	if blocksEdit {
		s.edit = true
		if !s.inURLs {
			s.payload = true
		}
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
	if s.payload || s.messages {
		mode = DescribeWithheld
	}
	return Sanitized{Object: out, Paths: paths, EditEligible: !s.edit, Payload: s.payload, Messages: s.messages, DescribeMode: mode}
}

// object sanitises one object in place (a copy already): its registered
// surfaces, its last-applied snapshot when registered, its kind's message
// rule over its condition messages, then the URL rule over every string
// leaf.
func (s *sanitizer) object(group, kind string, o map[string]any, at string, depth int) map[string]any {
	if group == "" && kind == "Secret" {
		masked, _ := MaskSecretData(o)
		for _, f := range []string{"data", "stringData"} {
			switch v := o[f].(type) {
			case nil:
			case map[string]any:
				if len(v) > 0 && !allMasked(v) {
					s.mark(join(at, f), true)
				}
			default:
				// an unexpected container: MaskSecretData withheld it
				if !empty(v) && !isPlaceholder(v) {
					s.mark(join(at, f), true)
				}
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
	if IsEventKind(group, kind) {
		s.eventMessages(o, at)
	}
	s.conditionMessages(kind, o, at)
	if OriginsKind(kind) {
		s.originsHold(o, at, 0)
	}
	s.urls(o, at, 0)
	return o
}

// emptyValue is empty for MaskSecretData's callers.
func emptyValue(v any) bool { return empty(v) || isPlaceholder(v) }

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

// apply walks path inside v and transforms what it reaches by shape. A
// present, non-empty value of an unexpected type on the way is withheld in
// place, never returned raw (S0b §2; Codex's implementation review,
// finding 8).
func (s *sanitizer) apply(v any, path []string, sh shape, at string, depth int) {
	if depth > sanitizeMaxDepth || len(path) == 0 {
		return
	}
	if path[0] == "[]" {
		list, ok := v.([]any)
		if !ok {
			return // the caller checked the type before descending
		}
		for i, item := range list {
			ia := fmt.Sprintf("%s[%d]", at, i)
			if len(path) > 1 && path[1] != "[]" {
				if _, ok := item.(map[string]any); !ok {
					list[i] = s.withhold(item, ia)
					continue
				}
			}
			s.apply(item, path[1:], sh, ia, depth+1)
		}
		return
	}
	parent, ok := v.(map[string]any)
	if !ok {
		return // the root is a map, and the caller checked the rest
	}
	child, present := parent[path[0]]
	if !present || child == nil {
		return
	}
	ca := join(at, path[0])
	if len(path) == 1 {
		parent[path[0]] = s.transform(child, sh, ca, depth+1)
		return
	}
	if path[1] == "[]" {
		list, ok := child.([]any)
		if !ok {
			parent[path[0]] = s.withhold(child, ca)
			return
		}
		if len(path) == 2 {
			// every item of the list at path[0]
			for i, item := range list {
				list[i] = s.transform(item, sh, fmt.Sprintf("%s[%d]", ca, i), depth+1)
			}
			return
		}
		s.apply(list, path[1:], sh, ca, depth+1)
		return
	}
	if _, ok := child.(map[string]any); !ok {
		parent[path[0]] = s.withhold(child, ca)
		return
	}
	s.apply(child, path[1:], sh, ca, depth+1)
}

// withhold replaces a value of an unexpected type at a protected surface.
// Empty values and placeholders stay as they are.
func (s *sanitizer) withhold(v any, at string) any {
	if empty(v) || isPlaceholder(v) {
		return v
	}
	s.mark(at, true)
	return WithheldValue
}

// mapAt returns parent[key] as a map. A present, non-empty value of another
// type is withheld in place.
func (s *sanitizer) mapAt(parent map[string]any, key, at string) (map[string]any, bool) {
	v, ok := parent[key]
	if !ok || v == nil {
		return nil, false
	}
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	parent[key] = s.withhold(v, join(at, key))
	return nil, false
}

// listAt returns parent[key] as a list. A present, non-empty value of
// another type is withheld in place.
func (s *sanitizer) listAt(parent map[string]any, key, at string) ([]any, bool) {
	v, ok := parent[key]
	if !ok || v == nil {
		return nil, false
	}
	if l, ok := v.([]any); ok {
		return l, true
	}
	parent[key] = s.withhold(v, join(at, key))
	return nil, false
}

// sizeOver reports whether v's encoded size exceeds limit. It stops walking
// as soon as it does.
func sizeOver(v any, limit int) bool {
	n := 0
	var walk func(any) bool
	walk = func(v any) bool {
		switch t := v.(type) {
		case string:
			n += len(t) + 2
		case map[string]any:
			n += 2
			for k, c := range t {
				n += len(k) + 3
				if n > limit || walk(c) {
					return true
				}
			}
		case []any:
			n += 2
			for _, c := range t {
				n++
				if n > limit || walk(c) {
					return true
				}
			}
		default:
			n += 8
		}
		return n > limit
	}
	return walk(v)
}

// transform applies one shape to a value.
func (s *sanitizer) transform(v any, sh shape, at string, depth int) any {
	if depth > sanitizeMaxDepth {
		s.mark(at, true)
		return WithheldValue
	}
	if !isPlaceholder(v) && sizeOver(v, sanitizeMaxSurface) {
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
	case shapeOrigin:
		// S1c §6: a producer may sign its artifact URL in the path or query
		str, ok := v.(string)
		if !ok {
			return s.withhold(v, at)
		}
		if isPlaceholder(v) || str == "" {
			return v
		}
		s.mark(at, true)
		out, _ := urlscrub.Origins(str)
		if !strings.Contains(str, "://") {
			out = WithheldValue
		}
		return out
	case shapeVeleroLocation, shapeVeleroBackupFailure, shapeVeleroBackupValidation,
		shapeVeleroScheduleValidation, shapeVeleroRestoreFailure, shapeVeleroRestoreValidation:
		str, ok := v.(string)
		if !ok {
			return s.withhold(v, at)
		}
		if isPlaceholder(v) || str == "" {
			return v
		}
		s.mark(at, true)
		return velero.Placeholder(veleroFields[sh], str)
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
			return s.withhold(v, at)
		}
		s.source(o, at, depth+1)
		return o
	case shapeTemplate:
		o, ok := v.(map[string]any)
		if !ok {
			return s.withhold(v, at)
		}
		s.template(o, at, depth+1)
		return o
	case shapeGenerators:
		list, ok := v.([]any)
		if !ok {
			return s.withhold(v, at)
		}
		for i, g := range list {
			ga := fmt.Sprintf("%s[%d]", at, i)
			gm, ok := g.(map[string]any)
			if !ok {
				list[i] = s.withhold(g, ga)
				continue
			}
			s.generator(gm, ga, depth+1)
		}
		return list
	}
	return s.withhold(v, at)
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
	if h, ok := s.mapAt(o, "helm", at); ok {
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
	if pl, ok := s.mapAt(o, "plugin", at); ok {
		pa := join(at, "plugin")
		omitItemField(s, pl, "env", "value", pa, depth)
		if params, ok := s.listAt(pl, "parameters", pa); ok {
			for i, it := range params {
				ia := fmt.Sprintf("%s.parameters[%d]", pa, i)
				m, ok := it.(map[string]any)
				if !ok {
					params[i] = s.withhold(it, ia)
					continue
				}
				for _, f := range []string{"string", "array", "map"} {
					if v, ok := m[f]; ok && v != nil {
						m[f] = s.freeForm(v, join(ia, f), depth+1)
					}
				}
			}
		}
	}
	if d, ok := s.mapAt(o, "directory", at); ok {
		da := join(at, "directory")
		if j, ok := s.mapAt(d, "jsonnet", da); ok {
			ja := join(da, "jsonnet")
			omitItemField(s, j, "extVars", "value", ja, depth)
			omitItemField(s, j, "tlas", "value", ja, depth)
		}
	}
	if k, ok := s.mapAt(o, "kustomize", at); ok {
		ka := join(at, "kustomize")
		if patches, ok := s.listAt(k, "patches", ka); ok {
			for i, it := range patches {
				ia := fmt.Sprintf("%s.patches[%d]", ka, i)
				m, ok := it.(map[string]any)
				if !ok {
					patches[i] = s.withhold(it, ia)
					continue
				}
				if v, ok := m["patch"]; ok && !empty(v) && !isPlaceholder(v) {
					m["patch"] = OmittedText
					s.mark(join(ia, "patch"), true)
				}
			}
		}
	}
}

// omitItemField omits field f of every item of the list at key.
func omitItemField(s *sanitizer, parent map[string]any, key, f, at string, depth int) {
	list, ok := s.listAt(parent, key, at)
	if !ok {
		return
	}
	for i, it := range list {
		ia := fmt.Sprintf("%s.%s[%d]", at, key, i)
		m, ok := it.(map[string]any)
		if !ok {
			list[i] = s.withhold(it, ia)
			continue
		}
		if v, ok := m[f]; ok && v != nil {
			m[f] = s.freeForm(v, join(ia, f), depth+1)
		}
	}
}

// template sanitises an ApplicationSet template: its spec's sources.
func (s *sanitizer) template(o map[string]any, at string, depth int) {
	spec, ok := s.mapAt(o, "spec", at)
	if !ok {
		return
	}
	sa := join(at, "spec")
	if src, ok := s.mapAt(spec, "source", sa); ok {
		s.source(src, join(sa, "source"), depth+1)
	}
	if list, ok := s.listAt(spec, "sources", sa); ok {
		for i, it := range list {
			ia := fmt.Sprintf("%s.sources[%d]", sa, i)
			src, ok := it.(map[string]any)
			if !ok {
				list[i] = s.withhold(it, ia)
				continue
			}
			s.source(src, ia, depth+1)
		}
	}
}

// generator sanitises one ApplicationSet generator, of whatever type, and
// recurses into matrix and merge generators.
func (s *sanitizer) generator(g map[string]any, at string, depth int) {
	if depth > sanitizeMaxDepth {
		return
	}
	for typ := range g {
		b, ok := s.mapAt(g, typ, at)
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
		if in, ok := s.mapAt(b, "input", ba); ok {
			if v, ok := in["parameters"]; ok && v != nil {
				in["parameters"] = s.freeForm(v, join(join(ba, "input"), "parameters"), depth+1)
			}
		}
		if t, ok := s.mapAt(b, "template", ba); ok {
			s.template(t, join(ba, "template"), depth+1)
		}
		if list, ok := s.listAt(b, "generators", ba); ok {
			for i, it := range list {
				ia := fmt.Sprintf("%s.generators[%d]", ba, i)
				gm, ok := it.(map[string]any)
				if !ok {
					list[i] = s.withhold(it, ia)
					continue
				}
				s.generator(gm, ia, depth+1)
			}
		}
	}
}

// urls applies the URL rule to every string leaf. Its marks block an edit
// but are not a payload.
func (s *sanitizer) urls(v any, at string, depth int) {
	if depth > sanitizeMaxDepth {
		return
	}
	prev := s.inURLs
	s.inURLs = true
	defer func() { s.inURLs = prev }()
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
		omittedCount.MatchString(str) || velero.IsPlaceholder(str)
}

// omittedCount is freeForm's placeholder for a list of scalars, exactly:
// a prefix alone would let any text that begins with it pass as a
// placeholder (Codex's S2a implementation review, finding 1).
var omittedCount = regexp.MustCompile(`^` + regexp.QuoteMeta(OmittedValue) + ` × [0-9]{1,9}$`)

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

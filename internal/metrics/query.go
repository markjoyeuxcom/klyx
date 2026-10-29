package metrics

import (
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Settings S3b §3.3: the closed query surface. Every data query Klyx sends
// to Prometheus is a registered template, rendered by the pinned reader
// with the scope of its own terms - the member's effective match, or none
// when those terms say so - never a scope a caller chose. A query exists
// only as a Query, which the registry alone builds and which carries no
// scope; a sink takes a Query, never a string, so an unregistered query
// does not compile. The liveness probe is the one exception, outside data
// admission.

// Scope is a reader's effective match: equality matchers, in label order.
type Scope struct{ pairs [][2]string }

// ScopeOf is the scope of an effective match; empty for none.
func ScopeOf(match map[string]string) Scope {
	keys := make([]string, 0, len(match))
	for k := range match {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := Scope{}
	for _, k := range keys {
		s.pairs = append(s.pairs, [2]string{k, match[k]})
	}
	return s
}

// Empty is a scope with no matcher: a tenant-wide or unfiltered reader's.
func (s Scope) Empty() bool { return len(s.pairs) == 0 }

// Words is the scope as a sentence says it: label=value, in label order.
func (s Scope) Words() string {
	parts := make([]string, len(s.pairs))
	for i, p := range s.pairs {
		parts[i] = p[0] + "=" + p[1]
	}
	return strings.Join(parts, ", ")
}

// valid: every label of the scope is a label name, so no scope writes
// PromQL of its own, and none is one Prometheus reserves (a "__" prefix,
// __name__ among them): a scope matches series labels, and each query names
// its own metric (Codex's review of S3b-2a, R3).
func (s Scope) valid() bool {
	for _, p := range s.pairs {
		if !isLabelName(p[0]) || strings.HasPrefix(p[0], "__") {
			return false
		}
	}
	return true
}

// isLabelName is Prometheus's label name syntax.
func isLabelName(s string) bool { return labelNameRE.MatchString(s) }

var labelNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// matchers are the scope's equality matchers, each a PromQL string literal.
func (s Scope) matchers() string {
	parts := make([]string, len(s.pairs))
	for i, p := range s.pairs {
		parts[i] = Equals(p[0], p[1])
	}
	return strings.Join(parts, ",")
}

// Sel writes a vector selector: the metric with its own matchers and the
// scope's, conjoined (Prometheus requires every matcher of a selector, so a
// scope matcher on a label the template also matches never replaces it). A
// template writes every selector through Sel.
func Sel(metric, own, scope string) string {
	var ms []string
	if own != "" {
		ms = append(ms, own)
	}
	if scope != "" {
		ms = append(ms, scope)
	}
	if len(ms) == 0 {
		return metric
	}
	return metric + "{" + strings.Join(ms, ",") + "}"
}

// Query is one registered template with its parameters: the reader renders
// it with its own scope. A zero Query names no template and is refused
// before any request.
type Query struct {
	name   string
	render func(scope string) string
}

// Name is the template's name; "" for a zero Query.
func (q Query) Name() string { return q.name }

// ErrUnregisteredQuery: a query no template built, refused before any
// request.
var ErrUnregisteredQuery = errors.New("an unregistered metrics query is refused")

// ErrInvalidScope: a scope whose label is not a label name, or is one
// Prometheus reserves, refused before any request (the configuration admits
// none).
var ErrInvalidScope = errors.New("a metrics scope whose label is not a label name, or is reserved by Prometheus, is refused")

// Text is q rendered under scope, as a sink would send it; a zero Query is
// refused. It sends nothing: tests and display read it.
func (q Query) Text(scope Scope) (string, error) { return q.text(scope) }

// text renders q under scope; a zero Query is refused.
func (q Query) text(scope Scope) (string, error) {
	if q.render == nil || q.name == "" {
		return "", ErrUnregisteredQuery
	}
	if !scope.valid() {
		return "", ErrInvalidScope
	}
	return q.render(scope.matchers()), nil
}

// Template is a registered query: render writes it from its parameters and
// the scope's matchers, every selector through Sel. shape names a
// parameter's structural form (how many operands an "or" has, say); a
// parameter whose shape no declared case has is refused, so the proof that
// renders every declared case covers every query sent. clone copies a
// parameter deeply: a Query keeps its own copy, so a caller changing a
// slice it passed never changes a query admitted (Codex's review of
// S3b-2a, R2).
type Template[P any] struct {
	name   string
	render func(p P, scope string) string
	shape  func(P) string
	clone  func(P) P
	shapes map[string]bool
}

// registered is a template as the proof sees it: its declared cases,
// rendered under a scope.
type registered struct {
	name  string
	cases func(scope string) []string
}

var (
	regMu    sync.Mutex
	registry []registered
)

// define registers a template with the cases that cover its shapes; shape
// may be nil for a template whose structure never varies, and clone for a
// parameter type holding no slice, map or pointer. A template with no case
// is never defined: the proof could not see it. Only this package defines
// templates, so the proof's registry is every template there is (Codex's
// review of S3b-2a, R1).
func define[P any](name string, render func(p P, scope string) string, shape func(P) string, clone func(P) P, cases ...P) *Template[P] {
	if len(cases) == 0 {
		panic("metrics: template " + name + " declares no case")
	}
	if shape == nil {
		shape = func(P) string { return "" }
	}
	if clone == nil {
		var zero P
		if holdsReferences(reflect.TypeOf(zero)) {
			panic("metrics: template " + name + "'s parameters hold references, so it needs a clone")
		}
		clone = func(p P) P { return p }
	}
	t := &Template[P]{name: name, render: render, shape: shape, clone: clone, shapes: map[string]bool{}}
	for _, c := range cases {
		t.shapes[shape(c)] = true
	}
	regMu.Lock()
	defer regMu.Unlock()
	for _, r := range registry {
		if r.name == name {
			panic("metrics: template " + name + " defined twice")
		}
	}
	registry = append(registry, registered{name: name, cases: func(scope string) []string {
		out := make([]string, len(cases))
		for i, c := range cases {
			out[i] = render(c, scope)
		}
		return out
	}})
	return t
}

// With is the template with its own copy of these parameters; a zero Query
// when their shape is not one the template declared.
func (t *Template[P]) With(p P) Query {
	p = t.clone(p)
	if !t.shapes[t.shape(p)] {
		return Query{}
	}
	return Query{name: t.name, render: func(scope string) string { return t.render(p, scope) }}
}

// holdsReferences: a value of t shares memory with its copies (a slice, a
// map, a pointer, an interface, a channel or a function, at any depth).
func holdsReferences(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return true
	case reflect.Array:
		return holdsReferences(t.Elem())
	case reflect.Struct:
		for i := range t.NumField() {
			if holdsReferences(t.Field(i).Type) {
				return true
			}
		}
	}
	return false
}

// Rendered is every registered template's declared cases rendered under a
// scope, by name: the proof's input (Settings S3b §3.3). A scope a reader
// refuses renders nothing: nil.
func Rendered(match map[string]string) map[string][]string {
	scope := ScopeOf(match)
	if !scope.valid() {
		return nil
	}
	sc := scope.matchers()
	regMu.Lock()
	defer regMu.Unlock()
	out := make(map[string][]string, len(registry))
	for _, r := range registry {
		out[r.name] = r.cases(sc)
	}
	return out
}

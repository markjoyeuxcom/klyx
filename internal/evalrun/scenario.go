package evalrun

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/moomora/klyx/internal/evalrun/record"
)

// Scenario is one directory's scenario.yaml (eval design §2): the anchor,
// the plant and its witnesses, the scenario's subjects and lookalikes, the
// evidence predicates, and the lexical proxies. Nothing in it is ever sent
// to the model.
type Scenario struct {
	Name    string `json:"name"`
	Control bool   `json:"control,omitempty"`
	Anchor  Ref    `json:"anchor"`
	// Repos are the scenario's fixture repositories, the only ones the
	// preflight lets the config approve.
	Repos      []string        `json:"repos,omitempty"`
	Subjects   []Ref           `json:"subjects,omitempty"`
	Lookalikes []Ref           `json:"lookalikes,omitempty"`
	Invariants []Invariant     `json:"invariants"`
	Witnesses  []Witness       `json:"witnesses,omitempty"`
	Predicates []PredicateSpec `json:"predicates,omitempty"`
	Proxies    Proxies         `json:"proxies,omitempty"`

	dir string
}

// Ref names an object. Group, Version and Resource select it; Kind is how
// findings and checks name it. NamePrefix matches a family (a workload's
// pods) where the name is not fixed.
type Ref struct {
	Group      string `json:"group,omitempty"`
	Version    string `json:"version"`
	Resource   string `json:"resource"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
	NamePrefix string `json:"namePrefix,omitempty"`
}

// GVR is the reference's resource.
func (r Ref) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: r.Group, Version: r.Version, Resource: r.Resource}
}

func (r Ref) String() string {
	return strings.TrimPrefix(r.Kind+" "+strings.TrimPrefix(r.Namespace+"/"+r.Name+r.NamePrefix, "/"), " ")
}

// Names says whether a finding's or check's subject is this reference.
func (r Ref) Names(kind, namespace, name string) bool {
	if !strings.EqualFold(kind, r.Kind) || namespace != r.Namespace {
		return false
	}
	if r.NamePrefix != "" {
		return strings.HasPrefix(name, r.NamePrefix)
	}
	return name == r.Name
}

// Invariant is one plant check. Exactly one of its forms is set.
type Invariant struct {
	Object Ref `json:"object"`
	// Absent: the object does not exist.
	Absent bool `json:"absent,omitempty"`
	// Path and Matches: the field at Path matches the expression.
	Path    string `json:"path,omitempty"`
	Matches string `json:"matches,omitempty"`
	// Condition: the object's condition of this type has this status and
	// reason.
	Condition *ConditionSpec `json:"condition,omitempty"`
	// Pods: the pods of the workload's newest revision.
	Pods *PodsSpec `json:"pods,omitempty"`
	// Event: a recent event about the object.
	Event *EventSpec `json:"event,omitempty"`
}

// ConditionSpec selects a condition.
type ConditionSpec struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// PodsSpec is what every pod of a Deployment's newest ReplicaSet shows.
type PodsSpec struct {
	// Waiting: each pod has a container waiting for this reason with at
	// least MinRestarts restarts.
	Waiting string `json:"waiting,omitempty"`
	// Crashing: each pod is crash looping now (crashing, plant.go): not
	// Ready, a container failed at least MinRestarts times with its last
	// failure recent, and waiting, failed again or barely running since.
	Crashing    bool  `json:"crashing,omitempty"`
	MinRestarts int32 `json:"minRestarts,omitempty"`
	// Ready: each pod is Ready with no restarts.
	Ready bool `json:"ready,omitempty"`
	// Min is how many pods there must be at least (default 1).
	Min int `json:"min,omitempty"`
}

// EventSpec is an event about the object within a window.
type EventSpec struct {
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
	Within  string `json:"within,omitempty"` // a Go duration, default 1h
}

// Witness is a value recorded at the first plant check and compared at the
// second (eval design §2). UID is metadata.uid.
type Witness struct {
	Object Ref    `json:"object"`
	Path   string `json:"path"`
}

// PredicateSpec is a delivered-evidence predicate (eval design §5) as the
// manifest states it; the plant check fills in what only the lab knows.
type PredicateSpec struct {
	record.Predicate `json:",inline"`
	// SubjectRef: the predicate's subject, whose UID the plant check reads.
	SubjectRef *Ref `json:"subjectRef,omitempty"`
	// PodsOf: a log predicate's pods are this Deployment's newest
	// ReplicaSet's, by UID.
	PodsOf *Ref `json:"podsOf,omitempty"`
}

// Proxies are the lexical patterns reported as proxies, never as accuracy
// (eval design §6).
type Proxies struct {
	Cause     string `json:"cause,omitempty"`
	Lookalike string `json:"lookalike,omitempty"`
}

// LoadScenarios reads every scenario.yaml under dir, or only the named
// ones, in name order.
func LoadScenarios(dir string, names []string) ([]*Scenario, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*", "scenario.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []*Scenario
	found := map[string]bool{}
	for _, p := range paths {
		s, err := loadScenario(p)
		if err != nil {
			return nil, err
		}
		if len(want) == 0 || want[s.Name] {
			out = append(out, s)
			found[s.Name] = true
		}
	}
	for n := range want {
		if !found[n] {
			return nil, fmt.Errorf("no scenario %q under %s", n, dir)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no scenarios under %s", dir)
	}
	return out, nil
}

func loadScenario(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Scenario
	if err := yaml.UnmarshalStrict(raw, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.dir = filepath.Dir(path)
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

func (s *Scenario) validate() error {
	if s.Name == "" || filepath.Base(s.dir) != s.Name {
		return fmt.Errorf("a scenario's name is its directory's (%q)", filepath.Base(s.dir))
	}
	if s.Anchor.Name == "" || s.Anchor.Resource == "" || s.Anchor.Version == "" || s.Anchor.Kind == "" {
		return fmt.Errorf("the anchor needs its version, resource, kind and name")
	}
	if len(s.Invariants) == 0 {
		return fmt.Errorf("a scenario needs plant invariants")
	}
	for i, inv := range s.Invariants {
		n := 0
		for _, set := range []bool{inv.Absent, inv.Path != "", inv.Condition != nil, inv.Pods != nil, inv.Event != nil} {
			if set {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("invariant %d: exactly one form", i+1)
		}
		if inv.Path != "" {
			if _, err := regexp.Compile(inv.Matches); err != nil {
				return fmt.Errorf("invariant %d: %w", i+1, err)
			}
		}
		if e := inv.Event; e != nil {
			if _, err := regexp.Compile(e.Message); err != nil {
				return fmt.Errorf("invariant %d: %w", i+1, err)
			}
			if e.Within != "" {
				if _, err := time.ParseDuration(e.Within); err != nil {
					return fmt.Errorf("invariant %d: %w", i+1, err)
				}
			}
		}
	}
	if s.Control && len(s.Predicates) > 0 {
		return fmt.Errorf("a control plants no evidence")
	}
	if !s.Control && len(s.Predicates) == 0 {
		return fmt.Errorf("a scenario names the planted evidence")
	}
	for _, p := range []string{s.Proxies.Cause, s.Proxies.Lookalike} {
		if _, err := regexp.Compile(p); err != nil {
			return err
		}
	}
	return nil
}

// Script is a scenario's setup or teardown script.
func (s *Scenario) Script(name string) string { return filepath.Join(s.dir, name) }

// lookup follows a dotted path into an object: a segment is a key, a key
// with [N] for a list item, a key with [k=v] for the item whose k is v, or
// a key with ['name'] for a map's member whose name holds dots or slashes
// (an annotation). Dots inside brackets do not split the path.
func lookup(obj map[string]any, path string) (any, bool) {
	var cur any = obj
	for _, seg := range splitPath(path) {
		key, sel, hasSel := strings.Cut(seg, "[")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[key]; !ok {
			return nil, false
		}
		if !hasSel {
			continue
		}
		sel = strings.TrimSuffix(sel, "]")
		if len(sel) >= 2 && sel[0] == '\'' && sel[len(sel)-1] == '\'' {
			mm, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = mm[sel[1:len(sel)-1]]; !ok {
				return nil, false
			}
			continue
		}
		l, ok := cur.([]any)
		if !ok {
			return nil, false
		}
		if k, v, isKV := strings.Cut(sel, "="); isKV {
			found := false
			for _, it := range l {
				if im, ok := it.(map[string]any); ok && fmt.Sprint(im[k]) == v {
					cur, found = it, true
					break
				}
			}
			if !found {
				return nil, false
			}
			continue
		}
		n, err := strconv.Atoi(sel)
		if err != nil || n < 0 || n >= len(l) {
			return nil, false
		}
		cur = l[n]
	}
	return cur, true
}

// splitPath splits a path on the dots outside brackets.
func splitPath(path string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range path {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
		case '.':
			if depth == 0 {
				out = append(out, path[start:i])
				start = i + 1
			}
		}
	}
	return append(out, path[start:])
}

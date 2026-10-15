// Package flux parses Flux CRDs (read as unstructured) into vocabulary-correct
// reconciliation resources. No Flux Go API dependency: tolerant of version drift.
package flux

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Kind string

const (
	KustomizationKind Kind = "Kustomization"
	HelmReleaseKind   Kind = "HelmRelease"
)

// Source kinds Klyx watches + acts on (source.toolkit.fluxcd.io).
const (
	GitRepositoryKind  Kind = "GitRepository"
	OCIRepositoryKind  Kind = "OCIRepository"
	BucketKind         Kind = "Bucket"
	HelmRepositoryKind Kind = "HelmRepository"
	HelmChartKind      Kind = "HelmChart"
)

type ReadyState string

const (
	Ready       ReadyState = "Ready"
	Reconciling ReadyState = "Reconciling"
	Failed      ReadyState = "Failed"
	Unknown     ReadyState = "Unknown"
)

// Resource is a Flux-managed object's reconciliation state.
type Resource struct {
	Kind      Kind
	Namespace string
	Name      string
	Ready     ReadyState
	Reason    string
	Message   string
	Revision  string
	// ReadySince is the Ready condition's lastTransitionTime: when the
	// resource last CHANGED Ready state. It is NOT a per-apply timestamp -
	// Flux status carries none. A resource reconciling cleanly for weeks
	// keeps the timestamp of that weeks-old flip; read it as "ready/failed
	// since", never "applied N ago".
	ReadySince time.Time
	Suspended  bool
	SourceKind string
	SourceName string
	// SourceNamespace is the resolved namespace of the bound source:
	// sourceRef.namespace when set (an explicit cross-namespace ref), else the
	// object's own namespace (Flux's default). Same-named sources in different
	// namespaces are different sources - a consumer join needs all three.
	SourceNamespace string
	DependsOn       []DependencyRef
	// Stale reports that the spec changed and the controller has not observed
	// it yet (metadata.generation > status.observedGeneration). While stale,
	// Ready reflects the PREVIOUS spec - the UI must present the row as
	// transitioning, never as confidently green. ObservedGeneration/Generation
	// feed the explanatory tooltip.
	Stale              bool
	Generation         int64
	ObservedGeneration int64
	// LastAttemptedRevision is status.lastAttemptedRevision: the revision the
	// controller last tried to apply, successful or not (The Line §3.2's
	// "attempted" fact, kept apart from the applied Revision).
	LastAttemptedRevision string
	// History is a HelmRelease's status.history, newest (highest version)
	// first, at most HistoryMax entries; nil for a Kustomization. Only an
	// entry whose Status is "deployed" is deployment evidence.
	History []HelmHistory
	// Chart is a HelmRelease's spec.chart.spec.chart (empty for a chartRef
	// release, whose source names the chart): The Line's chart key.
	Chart string
	// ReadyObservedGeneration is the Ready condition's own observedGeneration:
	// the generation the Ready verdict was computed for. The top-level
	// observedGeneration can advance without a reconcile (a suspension).
	ReadyObservedGeneration int64
}

// HelmHistory is one HelmRelease status.history snapshot.
type HelmHistory struct {
	Version      int64
	ChartName    string
	ChartVersion string
	ConfigDigest string
	Status       string // deployed | superseded | failed | ...
	LastDeployed time.Time
}

// HistoryMax bounds the history kept per object (The Line §8.1).
const HistoryMax = 5

// staleness compares metadata.generation with status.observedGeneration. found
// is false when status carries no observedGeneration (brand-new object, or a
// controller that does not stamp it) - callers treat that as not-stale rather
// than guessing.
func staleness(u *unstructured.Unstructured) (stale bool, gen, observed int64) {
	gen = u.GetGeneration()
	observed, found, _ := unstructured.NestedInt64(u.Object, "status", "observedGeneration")
	if !found || gen == 0 {
		return false, gen, observed
	}
	return gen > observed, gen, observed
}

// DependencyRef is a spec.dependsOn entry (another Kustomization/HelmRelease the
// resource waits on). Namespace defaults to the resource's own when omitted.
type DependencyRef struct {
	Namespace string
	Name      string
}

// Flux's reasons for a reconcile blocked upstream (kustomize-controller
// v1.9.6, helm-controller v1.6.5): DependencyNotReady and ArtifactFailed in
// both; SourceNotReady in helm-controller, when its isReady judges the
// source not ready (generation not reconciled, Stalled, or no artifact).
const (
	ReasonDependencyNotReady = "DependencyNotReady"
	ReasonArtifactFailed     = "ArtifactFailed"
	ReasonSourceNotReady     = "SourceNotReady"
)

var sourceName = regexp.MustCompile(`^(\w+) '([^'/]+)/([^']+)' is not ready`)

// SourceNamed reads the source a SourceNotReady message names, as
// helm-controller words it: "<Kind> '<ns>/<name>' is not ready: <why>".
func SourceNamed(msg string) (kind, ns, name string, ok bool) {
	if m := sourceName.FindStringSubmatch(msg); m != nil {
		return m[1], m[2], m[3], true
	}
	return "", "", "", false
}

var dependencyName = regexp.MustCompile(`dependency '([^']+)'`)

// DependencyNamed reads the dependency a DependencyNotReady message names,
// as both controllers word it: "dependency '<ns>/<name>' is not ready", "...
// not found: ..." or "... is not ready according to readyExpr eval".
func DependencyNamed(msg string) (string, bool) {
	if m := dependencyName.FindStringSubmatch(msg); m != nil {
		return m[1], true
	}
	return "", false
}

// parseDependsOn reads spec.dependsOn; namespace defaults to the object's own.
func parseDependsOn(u *unstructured.Unstructured) []DependencyRef {
	raw, _, _ := unstructured.NestedSlice(u.Object, "spec", "dependsOn")
	var out []DependencyRef
	for _, e := range raw {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := em["name"].(string)
		if name == "" {
			continue
		}
		ns, _ := em["namespace"].(string)
		if ns == "" {
			ns = u.GetNamespace()
		}
		out = append(out, DependencyRef{Namespace: ns, Name: name})
	}
	return out
}

func ParseKustomization(u *unstructured.Unstructured) Resource {
	r := common(u, KustomizationKind)
	r.Revision, _, _ = unstructured.NestedString(u.Object, "status", "lastAppliedRevision")
	return r
}

// parseHelmHistory reads status.history newest-first by version, at most
// HistoryMax entries.
func parseHelmHistory(u *unstructured.Unstructured) []HelmHistory {
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "history")
	var out []HelmHistory
	for _, h := range raw {
		hm, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		e := HelmHistory{}
		switch v := hm["version"].(type) {
		case int64:
			e.Version = v
		case float64:
			e.Version = int64(v)
		}
		e.ChartName, _ = hm["chartName"].(string)
		e.ChartVersion, _ = hm["chartVersion"].(string)
		e.ConfigDigest, _ = hm["configDigest"].(string)
		e.Status, _ = hm["status"].(string)
		if t, ok := hm["lastDeployed"].(string); ok {
			e.LastDeployed, _ = time.Parse(time.RFC3339, t)
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Version > out[b].Version })
	if len(out) > HistoryMax {
		out = out[:HistoryMax]
	}
	return out
}

func ParseHelmRelease(u *unstructured.Unstructured) Resource {
	r := common(u, HelmReleaseKind)
	if rev, ok, _ := unstructured.NestedString(u.Object, "status", "lastAppliedRevision"); ok && rev != "" {
		r.Revision = rev
	} else if cv := currentHelmChartVersion(u); cv != "" {
		r.Revision = cv
	}
	r.History = parseHelmHistory(u)
	r.Chart, _, _ = unstructured.NestedString(u.Object, "spec", "chart", "spec", "chart")
	if r.SourceName == "" {
		if n, ok, _ := unstructured.NestedString(u.Object, "spec", "chart", "spec", "sourceRef", "name"); ok && n != "" {
			r.SourceName = n
			r.SourceKind, _, _ = unstructured.NestedString(u.Object, "spec", "chart", "spec", "sourceRef", "kind")
			r.SourceNamespace = refNamespace(u, "spec", "chart", "spec", "sourceRef", "namespace")
		} else if n, ok, _ := unstructured.NestedString(u.Object, "spec", "chartRef", "name"); ok && n != "" {
			r.SourceName = n
			r.SourceKind, _, _ = unstructured.NestedString(u.Object, "spec", "chartRef", "kind")
			r.SourceNamespace = refNamespace(u, "spec", "chartRef", "namespace")
		}
	}
	return r
}

// currentHelmChartVersion returns the chart version of a HelmRelease's current
// release from status.history. Flux maintains history newest-first (Latest() ==
// history[0]) and each Snapshot carries an integer `version` (the Helm release
// revision). We pick the entry with the highest version so the result is correct
// regardless of slice ordering; falling back to the first entry's chartVersion.
func currentHelmChartVersion(u *unstructured.Unstructured) string {
	hist, ok, _ := unstructured.NestedSlice(u.Object, "status", "history")
	if !ok || len(hist) == 0 {
		return ""
	}
	bestVer := int64(-1)
	var bestChart, firstChart string
	for i, h := range hist {
		hm, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		cv, _ := hm["chartVersion"].(string)
		if i == 0 {
			firstChart = cv
		}
		var ver int64 = -1
		switch v := hm["version"].(type) {
		case int64:
			ver = v
		case float64:
			ver = int64(v)
		}
		if cv != "" && ver > bestVer {
			bestVer = ver
			bestChart = cv
		}
	}
	if bestChart != "" {
		return bestChart
	}
	return firstChart
}

// refNamespace reads a sourceRef namespace field, defaulting to the object's
// own namespace as Flux does.
func refNamespace(u *unstructured.Unstructured, path ...string) string {
	if ns, ok, _ := unstructured.NestedString(u.Object, path...); ok && ns != "" {
		return ns
	}
	return u.GetNamespace()
}

func common(u *unstructured.Unstructured, kind Kind) Resource {
	r := Resource{Kind: kind, Name: u.GetName(), Namespace: u.GetNamespace()}
	if susp, ok, _ := unstructured.NestedBool(u.Object, "spec", "suspend"); ok {
		r.Suspended = susp
	}
	if k, ok, _ := unstructured.NestedString(u.Object, "spec", "sourceRef", "kind"); ok {
		r.SourceKind = k
	}
	if n, ok, _ := unstructured.NestedString(u.Object, "spec", "sourceRef", "name"); ok {
		r.SourceName = n
		r.SourceNamespace = refNamespace(u, "spec", "sourceRef", "namespace")
	}
	r.Ready, r.Reason, r.Message = readyFromConditions(u)
	r.DependsOn = parseDependsOn(u)
	r.Stale, r.Generation, r.ObservedGeneration = staleness(u)
	r.LastAttemptedRevision, _, _ = unstructured.NestedString(u.Object, "status", "lastAttemptedRevision")
	// ReadySince <- the Ready condition's lastTransitionTime, i.e. the moment
	// the ready STATE last flipped - see the field doc for why that is the
	// honest best Flux offers (no per-apply timestamp exists in status).
	r.ReadySince = readySince(u)
	r.ReadyObservedGeneration = conditionObservedGeneration(u, "Ready")
	return r
}

// conditionObservedGeneration is the named condition's own
// observedGeneration, 0 when absent.
func conditionObservedGeneration(u *unstructured.Unstructured, condType string) int64 {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t != condType {
			continue
		}
		switch og := cm["observedGeneration"].(type) {
		case int64:
			return og
		case float64:
			return int64(og)
		}
	}
	return 0
}

// readySince is the Ready condition's lastTransitionTime, zero when absent.
func readySince(u *unstructured.Unstructured) time.Time {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == "Ready" {
			if lt, ok := cm["lastTransitionTime"].(string); ok {
				if ts, err := time.Parse(time.RFC3339, lt); err == nil {
					return ts
				}
			}
		}
	}
	return time.Time{}
}

// readyFromConditions derives the aggregate Ready state, the Ready condition's
// reason, and its message from status.conditions. Reconciling overrides Ready
// unless Ready is Failed. Shared by common() and ParseSource.
func readyFromConditions(u *unstructured.Unstructured) (ReadyState, string, string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	state := Unknown
	reconciling := false
	var reason, message string
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		ctype, _ := cm["type"].(string)
		cstatus, _ := cm["status"].(string)
		switch ctype {
		case "Ready":
			switch cstatus {
			case "True":
				state = Ready
			case "False":
				state = Failed
			}
			reason, _ = cm["reason"].(string)
			message, _ = cm["message"].(string)
		case "Reconciling":
			if cstatus == "True" {
				reconciling = true
			}
		}
	}
	if reconciling && state != Failed {
		state = Reconciling
	}
	return state, reason, message
}

// Source is a Flux source object's fetch state (status.artifact + Ready).
type Source struct {
	Kind      Kind
	Namespace string
	Name      string
	Ready     ReadyState
	Reason    string
	Message   string
	Revision  string
	URL       string
	Suspended bool
	// RefKind/RefName are the tracked reference (The Line §3.2's comparison
	// key): a GitRepository's spec.ref (commit > name > semver > tag >
	// branch, Flux's precedence) or an OCIRepository's (digest > semver >
	// tag); empty when the kind tracks none or spec.ref is unset.
	RefKind string // branch | tag | semver | commit | name | digest
	RefName string
	// ArtifactUpdated is status.artifact.lastUpdateTime (a disclosed
	// tie-break only, never commit order); ReadySince the Ready condition's
	// lastTransitionTime. Zero when absent.
	ArtifactUpdated time.Time
	ReadySince      time.Time
	// Generation and ObservedGeneration: whether the source's current spec
	// (its URL among it) has been fetched, and whether it was ever edited.
	Generation, ObservedGeneration int64
}

// refPrecedence is the order a source's spec.ref fields win in, per kind.
var refPrecedence = map[Kind][]string{
	"GitRepository": {"commit", "name", "semver", "tag", "branch"},
	"OCIRepository": {"digest", "semver", "tag"},
}

// ParseSource extracts a source's fetch state from a watched source CR.
func ParseSource(u *unstructured.Unstructured) Source {
	s := Source{Kind: Kind(u.GetKind()), Namespace: u.GetNamespace(), Name: u.GetName()}
	s.Suspended, _, _ = unstructured.NestedBool(u.Object, "spec", "suspend")
	s.URL, _, _ = unstructured.NestedString(u.Object, "spec", "url")
	s.Revision, _, _ = unstructured.NestedString(u.Object, "status", "artifact", "revision")
	s.Ready, s.Reason, s.Message = readyFromConditions(u)
	for _, f := range refPrecedence[s.Kind] {
		if v, ok, _ := unstructured.NestedString(u.Object, "spec", "ref", f); ok && v != "" {
			s.RefKind, s.RefName = f, v
			break
		}
	}
	if t, ok, _ := unstructured.NestedString(u.Object, "status", "artifact", "lastUpdateTime"); ok {
		s.ArtifactUpdated, _ = time.Parse(time.RFC3339, t)
	}
	s.ReadySince = readySince(u)
	_, s.Generation, s.ObservedGeneration = staleness(u)
	return s
}

// SourceRef points at a source object bound to a Kustomization/HelmRelease.
type SourceRef struct {
	Kind      string
	Name      string
	Namespace string
}

// BoundSource resolves the source a Kustomization/HelmRelease reconciles from:
// spec.sourceRef for Kustomization, spec.chartRef or spec.chart.spec.sourceRef
// for HelmRelease. Namespace defaults to the resource's own namespace.
func BoundSource(u *unstructured.Unstructured) (SourceRef, bool) {
	candidates := [][]string{
		{"spec", "sourceRef"},                  // Kustomization
		{"spec", "chartRef"},                   // HelmRelease (newer)
		{"spec", "chart", "spec", "sourceRef"}, // HelmRelease (chart template)
	}
	for _, p := range candidates {
		name, _, _ := unstructured.NestedString(u.Object, append(append([]string{}, p...), "name")...)
		if name == "" {
			continue
		}
		kind, _, _ := unstructured.NestedString(u.Object, append(append([]string{}, p...), "kind")...)
		ns, _, _ := unstructured.NestedString(u.Object, append(append([]string{}, p...), "namespace")...)
		if ns == "" {
			ns = u.GetNamespace()
		}
		return SourceRef{Kind: kind, Name: name, Namespace: ns}, true
	}
	return SourceRef{}, false
}

type Condition struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

type InventoryEntry struct {
	Group     string
	Version   string
	Kind      string
	Namespace string
	Name      string
}

type Detail struct {
	Kind              Kind
	Namespace         string
	Name              string
	Suspended         bool
	Reason            string
	AppliedRevision   string
	AttemptedRevision string
	Conditions        []Condition
	Inventory         []InventoryEntry
	DependsOn         []DependencyRef
}

// ParseDetail extracts the detail view from a watched Flux CR. Inventory is
// parsed only for Kustomizations (HelmRelease CRs carry none).
func ParseDetail(u *unstructured.Unstructured) Detail {
	d := Detail{Kind: Kind(u.GetKind()), Namespace: u.GetNamespace(), Name: u.GetName()}
	d.AppliedRevision, _, _ = unstructured.NestedString(u.Object, "status", "lastAppliedRevision")
	d.AttemptedRevision, _, _ = unstructured.NestedString(u.Object, "status", "lastAttemptedRevision")
	d.Suspended, _, _ = unstructured.NestedBool(u.Object, "spec", "suspend")
	_, d.Reason, _ = readyFromConditions(u)
	d.DependsOn = parseDependsOn(u)

	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		cond := Condition{}
		cond.Type, _ = cm["type"].(string)
		cond.Status, _ = cm["status"].(string)
		cond.Reason, _ = cm["reason"].(string)
		cond.Message, _ = cm["message"].(string)
		d.Conditions = append(d.Conditions, cond)
	}

	entries, _, _ := unstructured.NestedSlice(u.Object, "status", "inventory", "entries")
	for _, e := range entries {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		id, _ := em["id"].(string)
		v, _ := em["v"].(string)
		if ie, ok := parseInventoryID(id, v); ok {
			d.Inventory = append(d.Inventory, ie)
		}
	}
	return d
}

// parseInventoryID parses Flux's inventory id "<namespace>_<name>_<group>_<kind>".
// Namespace is empty for cluster-scoped objects; group is empty for core kinds.
// k8s names/namespaces/groups/kinds contain no underscore, so a 4-way split is safe.
func parseInventoryID(id, version string) (InventoryEntry, bool) {
	parts := strings.SplitN(id, "_", 4)
	if len(parts) != 4 {
		return InventoryEntry{}, false
	}
	return InventoryEntry{
		Namespace: parts[0],
		Name:      parts[1],
		Group:     parts[2],
		Kind:      parts[3],
		Version:   version,
	}, true
}

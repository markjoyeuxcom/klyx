// Package flux parses Flux CRDs (read as unstructured) into vocabulary-correct
// reconciliation resources. No Flux Go API dependency: tolerant of version drift.
package flux

import (
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
	// LastApplied is NOT a per-apply timestamp - Flux status carries none.
	// It is the Ready condition's lastTransitionTime: when the resource last
	// CHANGED Ready state. A resource reconciling cleanly for weeks keeps the
	// timestamp of that weeks-old flip. Read it as "ready/failed since", never
	// "applied N ago". The field keeps its legacy name because appbridge maps
	// it to the lastAppliedAgeSeconds DTO field; renaming both (ReadyTransition
	// / readySinceSeconds) plus the UI copy is the follow-up that finishes
	// this fix.
	LastApplied time.Time
	Suspended   bool
	SourceKind  string
	SourceName  string
	DependsOn   []DependencyRef
}

// DependencyRef is a spec.dependsOn entry (another Kustomization/HelmRelease the
// resource waits on). Namespace defaults to the resource's own when omitted.
type DependencyRef struct {
	Namespace string
	Name      string
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

func ParseHelmRelease(u *unstructured.Unstructured) Resource {
	r := common(u, HelmReleaseKind)
	if rev, ok, _ := unstructured.NestedString(u.Object, "status", "lastAppliedRevision"); ok && rev != "" {
		r.Revision = rev
	} else if cv := currentHelmChartVersion(u); cv != "" {
		r.Revision = cv
	}
	if r.SourceName == "" {
		if n, ok, _ := unstructured.NestedString(u.Object, "spec", "chart", "spec", "sourceRef", "name"); ok && n != "" {
			r.SourceName = n
			r.SourceKind, _, _ = unstructured.NestedString(u.Object, "spec", "chart", "spec", "sourceRef", "kind")
		} else if n, ok, _ := unstructured.NestedString(u.Object, "spec", "chartRef", "name"); ok && n != "" {
			r.SourceName = n
			r.SourceKind, _, _ = unstructured.NestedString(u.Object, "spec", "chartRef", "kind")
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
	}
	r.Ready, r.Reason, r.Message = readyFromConditions(u)
	r.DependsOn = parseDependsOn(u)
	// LastApplied <- the Ready condition's lastTransitionTime, i.e. the moment
	// the ready STATE last flipped - see the field doc for why that is the
	// honest best Flux offers (no per-apply timestamp exists in status).
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == "Ready" {
			if lt, ok := cm["lastTransitionTime"].(string); ok {
				if ts, err := time.Parse(time.RFC3339, lt); err == nil {
					r.LastApplied = ts
				}
			}
		}
	}
	return r
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
}

// ParseSource extracts a source's fetch state from a watched source CR.
func ParseSource(u *unstructured.Unstructured) Source {
	s := Source{Kind: Kind(u.GetKind()), Namespace: u.GetNamespace(), Name: u.GetName()}
	s.Suspended, _, _ = unstructured.NestedBool(u.Object, "spec", "suspend")
	s.URL, _, _ = unstructured.NestedString(u.Object, "spec", "url")
	s.Revision, _, _ = unstructured.NestedString(u.Object, "status", "artifact", "revision")
	s.Ready, s.Reason, s.Message = readyFromConditions(u)
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

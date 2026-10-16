// Package crd parses CustomResourceDefinition objects (read as unstructured) into
// a vocabulary-correct model, with best-effort operator/category attribution and
// a hybrid instance-count display. No apiextensions Go API dependency: tolerant
// of version drift.
package crd

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GVR is the dynamic resource for listing CRDs.
var GVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// Cap bounds an instance count: a single metadata list page of this size. A full
// page plus a continue token means "more than Cap", rendered as "<Cap>+".
const Cap = 500

// Info is a parsed CRD: identity, scope, the version to count against, and a
// best-effort owning operator.
type Info struct {
	Group      string
	Kind       string
	Plural     string
	ShortNames []string
	Scope      string // "Namespaced" | "Cluster"
	Version    string // storage (else first served, else first) version
	Operator   string // best-effort from metadata.labels; "" when unknown
}

// InstanceMeta is the metadata-only view of one custom-resource instance.
type InstanceMeta struct {
	Namespace string
	Name      string
	Created   time.Time
	Fields    map[string]string
	// Cells are the server's printer-column values, in its page's Columns
	// order; nil for a page read without a table (slice 15a).
	Cells []string
}

// ParseCRD maps a CRD unstructured to Info. ok=false when group/kind/plural are
// missing (an object we cannot meaningfully browse).
func ParseCRD(u *unstructured.Unstructured) (Info, bool) {
	group, _, _ := unstructured.NestedString(u.Object, "spec", "group")
	kind, _, _ := unstructured.NestedString(u.Object, "spec", "names", "kind")
	plural, _, _ := unstructured.NestedString(u.Object, "spec", "names", "plural")
	if group == "" || kind == "" || plural == "" {
		return Info{}, false
	}
	scope, _, _ := unstructured.NestedString(u.Object, "spec", "scope")
	short, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "names", "shortNames")
	versions, _, _ := unstructured.NestedSlice(u.Object, "spec", "versions")

	return Info{
		Group:      group,
		Kind:       kind,
		Plural:     plural,
		ShortNames: short,
		Scope:      scope,
		Version:    storageVersion(versions),
		Operator:   Operator(u.GetLabels()),
	}, true
}

// storageVersion returns the storage version name, else the first served, else
// the first listed, else "".
func storageVersion(versions []interface{}) string {
	var firstServed, firstAny string
	for _, v := range versions {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		if firstAny == "" {
			firstAny = name
		}
		if storage, _ := m["storage"].(bool); storage {
			return name
		}
		if served, _ := m["served"].(bool); served && firstServed == "" {
			firstServed = name
		}
	}
	if firstServed != "" {
		return firstServed
	}
	return firstAny
}

// CountDisplay maps a single metadata-list page to a display count. A non-empty
// continue token means there are more than Cap items, so report Cap as a floor
// and flag capped.
func CountDisplay(items int, continueToken string) (count int, capped bool) {
	if continueToken != "" {
		return Cap, true
	}
	return items, false
}

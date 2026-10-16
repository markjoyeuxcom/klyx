package crd

import (
	"encoding/json"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The definition view (The Line slice 15c): one CRD read in full - its names,
// versions, storage, conversion, printer columns, recorded conditions and
// the schema of one version, flattened for a navigable tree.

// Schema bounds: the flattening stops at these and says so.
const (
	SchemaMaxNodes    = 3000
	SchemaMaxDepth    = 12
	schemaEnumMax     = 20
	schemaDefaultMax  = 200
	schemaDescribeMax = 1000
)

// PathSeg is one step of a schema path. Kind "p" is a property (Name holds
// it, whatever characters it carries), "i" an array's items, "m" an
// additionalProperties value - so dots and brackets in names are never
// ambiguous (design review 1).
type PathSeg struct {
	Kind string `json:"k"`
	Name string `json:"n,omitempty"`
}

// SchemaNode is one field of the flattened schema.
type SchemaNode struct {
	Path                 []PathSeg `json:"path"`
	Type                 string    `json:"type,omitempty"`
	Format               string    `json:"format,omitempty"`
	Required             bool      `json:"required,omitempty"`
	Enum                 []string  `json:"enum,omitempty"`
	EnumMore             int       `json:"enumMore,omitempty"`
	Default              string    `json:"default,omitempty"`
	DefaultTruncated     bool      `json:"defaultTruncated,omitempty"`
	Description          string    `json:"description,omitempty"`
	DescriptionTruncated bool      `json:"descriptionTruncated,omitempty"`
	PreserveUnknown      bool      `json:"preserveUnknown,omitempty"`
	IntOrString          bool      `json:"intOrString,omitempty"`
	EmbeddedResource     bool      `json:"embeddedResource,omitempty"`
	// Constraints names what the tree does not draw: allOf, anyOf, oneOf,
	// not, pattern, bounds - named, never flattened into fields.
	Constraints []string `json:"constraints,omitempty"`
	// Deeper: the node has children past SchemaMaxDepth, not drawn.
	Deeper bool `json:"deeper,omitempty"`
}

// PrinterColumn is one additionalPrinterColumn of a version.
type PrinterColumn struct {
	Name, Type, Format, JSONPath, Description string
	Priority                                  int64
}

// DefVersion is one version of the CRD.
type DefVersion struct {
	Name               string
	Served, Storage    bool
	Deprecated         bool
	DeprecationWarning string
	Columns            []PrinterColumn
}

// Conversion is spec.conversion as configured.
type Conversion struct {
	Strategy       string // "None" or "Webhook"
	URL            string
	ServiceNS      string
	ServiceName    string
	ServicePort    int64 // 443 when unset
	ServicePath    string
	ReviewVersions []string
}

// Definition is one CRD read in full, with one version's schema.
type Definition struct {
	Name, Group, Kind, Plural, Singular, ListKind, Scope string
	ShortNames, Categories                               []string
	Operator                                             string
	UID                                                  string
	Created                                              time.Time
	Versions                                             []DefVersion
	StoredVersions                                       []string
	Conversion                                           Conversion
	Conditions                                           []Condition
	SchemaVersion                                        string
	// SchemaRecorded: the version records an openAPIV3Schema at all - a
	// schema with no properties is not "no schema" (implementation review 1).
	SchemaRecorded bool
	// Schema starts with the root (an empty path), then its fields.
	Schema []SchemaNode
	// SchemaTruncated: the flattening stopped at SchemaMaxNodes.
	SchemaTruncated bool
}

// ParseDefinition reads a CRD. version picks the schema to flatten; "" picks
// the storage version (else the first served, else the first).
func ParseDefinition(u *unstructured.Unstructured, version string) Definition {
	o := u.Object
	str := func(path ...string) string { s, _, _ := unstructured.NestedString(o, path...); return s }
	strs := func(path ...string) []string { s, _, _ := unstructured.NestedStringSlice(o, path...); return s }
	d := Definition{
		Name: u.GetName(), UID: string(u.GetUID()), Created: u.GetCreationTimestamp().Time,
		Group: str("spec", "group"), Kind: str("spec", "names", "kind"), Plural: str("spec", "names", "plural"),
		Singular: str("spec", "names", "singular"), ListKind: str("spec", "names", "listKind"), Scope: str("spec", "scope"),
		ShortNames: strs("spec", "names", "shortNames"), Categories: strs("spec", "names", "categories"),
		Operator: Operator(u.GetLabels()), StoredVersions: strs("status", "storedVersions"),
		Conditions: ParseConditions(o),
	}
	versions, _, _ := unstructured.NestedSlice(o, "spec", "versions")
	schemas := map[string]map[string]interface{}{}
	for _, v := range versions {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		dv := DefVersion{}
		dv.Name, _ = m["name"].(string)
		dv.Served, _ = m["served"].(bool)
		dv.Storage, _ = m["storage"].(bool)
		dv.Deprecated, _ = m["deprecated"].(bool)
		dv.DeprecationWarning, _ = m["deprecationWarning"].(string)
		cols, _, _ := unstructured.NestedSlice(m, "additionalPrinterColumns")
		for _, c := range cols {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			pc := PrinterColumn{}
			pc.Name, _ = cm["name"].(string)
			pc.Type, _ = cm["type"].(string)
			pc.Format, _ = cm["format"].(string)
			pc.JSONPath, _ = cm["jsonPath"].(string)
			pc.Description, _ = cm["description"].(string)
			switch p := cm["priority"].(type) {
			case int64:
				pc.Priority = p
			case float64:
				pc.Priority = int64(p)
			}
			dv.Columns = append(dv.Columns, pc)
		}
		if s, ok, _ := unstructured.NestedMap(m, "schema", "openAPIV3Schema"); ok {
			schemas[dv.Name] = s
		}
		d.Versions = append(d.Versions, dv)
	}
	d.Conversion = parseConversion(o)
	if version == "" {
		version = storageVersion(versions)
	}
	d.SchemaVersion = version
	if s, ok := schemas[version]; ok {
		d.SchemaRecorded = true
		d.Schema, d.SchemaTruncated = FlattenSchema(s)
	}
	return d
}

func parseConversion(o map[string]interface{}) Conversion {
	c := Conversion{Strategy: "None"}
	if s, ok, _ := unstructured.NestedString(o, "spec", "conversion", "strategy"); ok && s != "" {
		c.Strategy = s
	}
	if c.Strategy != "Webhook" {
		return c
	}
	cc, _, _ := unstructured.NestedMap(o, "spec", "conversion", "webhook", "clientConfig")
	c.URL, _ = cc["url"].(string)
	if svc, ok := cc["service"].(map[string]interface{}); ok {
		c.ServiceNS, _ = svc["namespace"].(string)
		c.ServiceName, _ = svc["name"].(string)
		c.ServicePath, _ = svc["path"].(string)
		c.ServicePort = 443
		switch p := svc["port"].(type) {
		case int64:
			c.ServicePort = p
		case float64:
			c.ServicePort = int64(p)
		}
	}
	c.ReviewVersions, _, _ = unstructured.NestedStringSlice(o, "spec", "conversion", "webhook", "conversionReviewVersions")
	return c
}

// unsupported names the schema keywords the tree does not draw.
var unsupported = []string{"allOf", "anyOf", "oneOf", "not", "pattern", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
	"minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties", "multipleOf", "uniqueItems", "x-kubernetes-validations"}

// FlattenSchema walks an openAPIV3Schema depth first, properties in name
// order, into nodes - the root first, with an empty path, so its own facts
// (type, description, preserve-unknown, constraints) are never lost. It
// stops at SchemaMaxNodes (truncated=true) and does not descend past
// SchemaMaxDepth (the node says Deeper).
func FlattenSchema(root map[string]interface{}) ([]SchemaNode, bool) {
	var out []SchemaNode
	truncated := false
	var walk func(s map[string]interface{}, path []PathSeg, required bool)
	walk = func(s map[string]interface{}, path []PathSeg, required bool) {
		if truncated {
			return
		}
		if len(out) >= SchemaMaxNodes {
			truncated = true
			return
		}
		out = append(out, nodeOf(s, path, required))
		children := childrenOf(s)
		if len(children) == 0 {
			return
		}
		if len(path) >= SchemaMaxDepth {
			out[len(out)-1].Deeper = true
			return
		}
		req := map[string]bool{}
		if r, ok := s["required"].([]interface{}); ok {
			for _, x := range r {
				if name, ok := x.(string); ok {
					req[name] = true
				}
			}
		}
		for _, ch := range children {
			p := append(append([]PathSeg{}, path...), ch.seg)
			walk(ch.schema, p, ch.seg.Kind == "p" && req[ch.seg.Name])
		}
	}
	walk(root, nil, false)
	return out, truncated
}

type child struct {
	seg    PathSeg
	schema map[string]interface{}
}

func childrenOf(s map[string]interface{}) []child {
	var out []child
	if props, ok := s["properties"].(map[string]interface{}); ok {
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if m, ok := props[n].(map[string]interface{}); ok {
				out = append(out, child{PathSeg{Kind: "p", Name: n}, m})
			}
		}
	}
	if items, ok := s["items"].(map[string]interface{}); ok {
		out = append(out, child{PathSeg{Kind: "i"}, items})
	}
	if ap, ok := s["additionalProperties"].(map[string]interface{}); ok {
		out = append(out, child{PathSeg{Kind: "m"}, ap})
	}
	return out
}

func nodeOf(s map[string]interface{}, path []PathSeg, required bool) SchemaNode {
	n := SchemaNode{Path: path, Required: required}
	n.Type, _ = s["type"].(string)
	n.Format, _ = s["format"].(string)
	n.PreserveUnknown, _ = s["x-kubernetes-preserve-unknown-fields"].(bool)
	n.IntOrString, _ = s["x-kubernetes-int-or-string"].(bool)
	n.EmbeddedResource, _ = s["x-kubernetes-embedded-resource"].(bool)
	if desc, ok := s["description"].(string); ok {
		n.Description, n.DescriptionTruncated = clip(desc, schemaDescribeMax)
	}
	if enum, ok := s["enum"].([]interface{}); ok {
		for i, e := range enum {
			if i >= schemaEnumMax {
				n.EnumMore = len(enum) - schemaEnumMax
				break
			}
			b, _ := json.Marshal(e)
			n.Enum = append(n.Enum, string(b))
		}
	}
	if def, ok := s["default"]; ok {
		b, _ := json.Marshal(def)
		n.Default, n.DefaultTruncated = clip(string(b), schemaDefaultMax)
	}
	for _, k := range unsupported {
		if _, ok := s[k]; ok {
			n.Constraints = append(n.Constraints, k)
		}
	}
	return n
}

// clip cuts s to at most n runes.
func clip(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}

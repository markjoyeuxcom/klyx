package crd

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func defCRD(obj map[string]interface{}) *unstructured.Unstructured {
	base := map[string]interface{}{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]interface{}{"name": "widgets.example.com", "uid": "crd-1"},
		"spec": map[string]interface{}{
			"group": "example.com", "scope": "Namespaced",
			"names": map[string]interface{}{"kind": "Widget", "plural": "widgets", "singular": "widget", "listKind": "WidgetList", "shortNames": []interface{}{"wd"}, "categories": []interface{}{"all"}},
			"versions": []interface{}{
				map[string]interface{}{"name": "v1beta1", "served": true, "storage": false, "deprecated": true, "deprecationWarning": "use v1",
					"schema": map[string]interface{}{"openAPIV3Schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{"spec": map[string]interface{}{"type": "object"}}}}},
				map[string]interface{}{"name": "v1", "served": true, "storage": true,
					"additionalPrinterColumns": []interface{}{map[string]interface{}{"name": "Ready", "type": "string", "jsonPath": `.status.conditions[?(@.type=="Ready")].status`, "priority": int64(0)}},
					"schema": map[string]interface{}{"openAPIV3Schema": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"spec": map[string]interface{}{"type": "object", "required": []interface{}{"mode"}, "properties": map[string]interface{}{
							"mode":     map[string]interface{}{"type": "string", "enum": []interface{}{"fast", "slow"}, "default": "fast", "description": "How fast."},
							"a.b[c]":   map[string]interface{}{"type": "string"},
							"ports":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "integer", "minimum": int64(1)}},
							"labels":   map[string]interface{}{"type": "object", "additionalProperties": map[string]interface{}{"type": "string"}},
							"template": map[string]interface{}{"type": "object", "x-kubernetes-preserve-unknown-fields": true, "oneOf": []interface{}{}},
							"port":     map[string]interface{}{"x-kubernetes-int-or-string": true},
						}},
					}}}},
			},
		},
		"status": map[string]interface{}{"storedVersions": []interface{}{"v1beta1", "v1"}},
	}
	for k, v := range obj {
		base[k] = v
	}
	return &unstructured.Unstructured{Object: base}
}

func TestParseDefinition(t *testing.T) {
	d := ParseDefinition(defCRD(nil), "")
	if d.Kind != "Widget" || d.Singular != "widget" || d.ListKind != "WidgetList" || d.ShortNames[0] != "wd" || d.Categories[0] != "all" {
		t.Fatalf("names %+v", d)
	}
	if len(d.Versions) != 2 || !d.Versions[0].Deprecated || d.Versions[0].DeprecationWarning != "use v1" || !d.Versions[1].Storage {
		t.Fatalf("versions %+v", d.Versions)
	}
	if d.Versions[1].Columns[0].JSONPath == "" || d.SchemaVersion != "v1" || len(d.StoredVersions) != 2 {
		t.Fatalf("columns/schema version %+v", d)
	}
	if d.Conversion.Strategy != "None" {
		t.Errorf("conversion %+v", d.Conversion)
	}
	byPath := map[string]SchemaNode{}
	for _, n := range d.Schema {
		parts := []string{}
		for _, s := range n.Path {
			parts = append(parts, s.Kind+":"+s.Name)
		}
		byPath[strings.Join(parts, "/")] = n
	}
	mode := byPath["p:spec/p:mode"]
	if !mode.Required || mode.Default != `"fast"` || strings.Join(mode.Enum, ",") != `"fast","slow"` || mode.Description != "How fast." {
		t.Errorf("mode %+v", mode)
	}
	if _, ok := byPath["p:spec/p:a.b[c]"]; !ok {
		t.Error("a property named with dots and brackets keeps its name in one segment")
	}
	if n := byPath["p:spec/p:ports/i:"]; n.Type != "integer" || strings.Join(n.Constraints, ",") != "minimum" {
		t.Errorf("array items %+v", n)
	}
	if n := byPath["p:spec/p:labels/m:"]; n.Type != "string" {
		t.Errorf("map values %+v", n)
	}
	if n := byPath["p:spec/p:template"]; !n.PreserveUnknown || strings.Join(n.Constraints, ",") != "oneOf" {
		t.Errorf("template %+v", n)
	}
	if !byPath["p:spec/p:port"].IntOrString {
		t.Error("int-or-string")
	}
	if old := ParseDefinition(defCRD(nil), "v1beta1"); old.SchemaVersion != "v1beta1" || len(old.Schema) != 1 {
		t.Errorf("the requested version's schema: %+v", old.Schema)
	}
}

func TestParseDefinition_Conversion(t *testing.T) {
	svc := defCRD(nil)
	_ = unstructured.SetNestedMap(svc.Object, map[string]interface{}{"strategy": "Webhook", "webhook": map[string]interface{}{
		"clientConfig":             map[string]interface{}{"service": map[string]interface{}{"namespace": "ops", "name": "conv"}},
		"conversionReviewVersions": []interface{}{"v1"}}}, "spec", "conversion")
	c := ParseDefinition(svc, "").Conversion
	if c.Strategy != "Webhook" || c.ServiceNS != "ops" || c.ServiceName != "conv" || c.ServicePort != 443 || c.ServicePath != "" || c.ReviewVersions[0] != "v1" {
		t.Fatalf("service webhook %+v", c)
	}
	url := defCRD(nil)
	_ = unstructured.SetNestedMap(url.Object, map[string]interface{}{"strategy": "Webhook", "webhook": map[string]interface{}{
		"clientConfig": map[string]interface{}{"url": "https://conv.example.com/convert"}}}, "spec", "conversion")
	if c := ParseDefinition(url, "").Conversion; c.URL != "https://conv.example.com/convert" || c.ServiceName != "" {
		t.Fatalf("url webhook %+v", c)
	}
}

func TestFlattenSchema_Bounds(t *testing.T) {
	props := map[string]interface{}{}
	for i := 0; i < SchemaMaxNodes+50; i++ {
		props[strings.Repeat("f", 1+i%7)+string(rune('a'+i%26))+strings.Repeat("x", i/26)] = map[string]interface{}{"type": "string"}
	}
	nodes, truncated := FlattenSchema(map[string]interface{}{"type": "object", "properties": props})
	if len(nodes) != SchemaMaxNodes || !truncated {
		t.Fatalf("node cap: %d truncated=%v", len(nodes), truncated)
	}
	deep := map[string]interface{}{"type": "string"}
	for i := 0; i < SchemaMaxDepth+5; i++ {
		deep = map[string]interface{}{"type": "object", "properties": map[string]interface{}{"n": deep}}
	}
	nodes, _ = FlattenSchema(deep)
	if len(nodes) != SchemaMaxDepth || !nodes[len(nodes)-1].Deeper {
		t.Fatalf("depth cap: %d nodes, last %+v", len(nodes), nodes[len(nodes)-1])
	}
	long := strings.Repeat("d", schemaDescribeMax+10)
	nodes, _ = FlattenSchema(map[string]interface{}{"properties": map[string]interface{}{"x": map[string]interface{}{"description": long, "enum": make([]interface{}, schemaEnumMax+3)}}})
	if !nodes[0].DescriptionTruncated || nodes[0].EnumMore != 3 || len(nodes[0].Enum) != schemaEnumMax {
		t.Fatalf("truncation markers %+v", nodes[0])
	}
}

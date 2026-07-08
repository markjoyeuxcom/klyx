package crd

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func factByLabel(facts []Fact, label string) (Fact, bool) {
	for _, f := range facts {
		if f.Label == label {
			return f, true
		}
	}
	return Fact{}, false
}

func TestBuildFacts_HTTPRoute(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"spec": map[string]interface{}{
			"hostnames":  []interface{}{"ollama.example.com"},
			"parentRefs": []interface{}{map[string]interface{}{"name": "eg", "namespace": "envoy-gateway-system"}},
			"rules": []interface{}{
				map[string]interface{}{
					"backendRefs": []interface{}{
						map[string]interface{}{"name": "ollama", "port": int64(80), "weight": int64(100)},
					},
				},
			},
		},
	}}
	facts := BuildFacts("gateway.networking.k8s.io", "httproutes", u)
	if f, ok := factByLabel(facts, "hostnames"); !ok || f.Value != "ollama.example.com" {
		t.Fatalf("hostnames fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "parents"); !ok || f.Value != "envoy-gateway-system/eg" {
		t.Fatalf("parents fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "rules"); !ok || f.Value != "1" {
		t.Fatalf("rules fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "backends"); !ok || f.Value != "ollama:80 (w100)" {
		t.Fatalf("backends fact wrong: %+v", f)
	}
}

func TestBuildFacts_Gateway(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Gateway",
		"spec": map[string]interface{}{
			"gatewayClassName": "eg",
			"listeners": []interface{}{
				map[string]interface{}{"name": "http", "port": int64(80), "protocol": "HTTP"},
				map[string]interface{}{"name": "https", "port": int64(443), "protocol": "HTTPS", "hostname": "*.example.com"},
			},
		},
		"status": map[string]interface{}{
			"addresses": []interface{}{map[string]interface{}{"value": "203.0.113.10"}},
			"listeners": []interface{}{
				map[string]interface{}{"name": "http", "attachedRoutes": int64(2)},
				map[string]interface{}{"name": "https", "attachedRoutes": int64(3)},
			},
		},
	}}
	facts := BuildFacts("gateway.networking.k8s.io", "gateways", u)
	if f, ok := factByLabel(facts, "class"); !ok || f.Value != "eg" {
		t.Fatalf("class fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "address"); !ok || f.Value != "203.0.113.10" {
		t.Fatalf("address fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "listeners"); !ok || f.Value != "2" {
		t.Fatalf("listeners fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "attached routes"); !ok || f.Value != "5" {
		t.Fatalf("attached routes fact wrong (want 5): %+v", f)
	}
}

func TestBuildFacts_ConfigMap_NoValueLeak(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":      "ConfigMap",
		"immutable": true,
		"data": map[string]interface{}{
			"app.conf":  "secret-looking-value-should-not-appear",
			"other.yml": "x",
		},
	}}
	facts := BuildFacts("", "configmaps", u)
	if f, ok := factByLabel(facts, "keys"); !ok || f.Value != "2" {
		t.Fatalf("keys fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "immutable"); !ok || f.Value != "yes" {
		t.Fatalf("immutable fact wrong: %+v", f)
	}
	// Per-key rows carry name + SIZE, never the value.
	if f, ok := factByLabel(facts, "app.conf"); !ok || f.Value != "38 B" {
		t.Fatalf("data key size fact wrong: %+v", f)
	}
	for _, f := range facts {
		if f.Value == "secret-looking-value-should-not-appear" {
			t.Fatalf("a data VALUE leaked into a fact: %+v", f)
		}
	}
}

func TestBuildFacts_Unregistered(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{"kind": "Widget"}}
	if facts := BuildFacts("example.com", "widgets", u); facts != nil {
		t.Fatalf("unregistered kind must return nil facts, got %v", facts)
	}
}

func TestParseConditionsDeep_HTTPRouteParents(t *testing.T) {
	obj := map[string]interface{}{
		"status": map[string]interface{}{
			"parents": []interface{}{
				map[string]interface{}{
					"parentRef":  map[string]interface{}{"name": "eg"},
					"conditions": []interface{}{map[string]interface{}{"type": "Accepted", "status": "True", "reason": "Accepted"}},
				},
			},
		},
	}
	conds := ParseConditionsDeep("gateway.networking.k8s.io", "httproutes", obj)
	if len(conds) != 1 {
		t.Fatalf("want 1 condition from parents, got %d", len(conds))
	}
	if conds[0].Type != "Accepted" || conds[0].Scope != "parent eg" {
		t.Fatalf("parent condition wrong: %+v", conds[0])
	}
}

func TestParseConditionsDeep_GatewayTopLevelPlusListeners(t *testing.T) {
	obj := map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{map[string]interface{}{"type": "Programmed", "status": "True"}},
			"listeners": []interface{}{
				map[string]interface{}{
					"name":       "https",
					"conditions": []interface{}{map[string]interface{}{"type": "Programmed", "status": "True"}},
				},
			},
		},
	}
	conds := ParseConditionsDeep("gateway.networking.k8s.io", "gateways", obj)
	if len(conds) != 2 {
		t.Fatalf("want top-level + per-listener condition (2), got %d: %+v", len(conds), conds)
	}
	// top-level has no scope; listener one carries scope, so React keys never collide.
	if conds[0].Scope != "" || conds[1].Scope != "listener https" {
		t.Fatalf("scopes wrong: %+v", conds)
	}
}

func TestParseConditionsDeep_StandardKindUnaffected(t *testing.T) {
	obj := map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
		},
	}
	conds := ParseConditionsDeep("", "configmaps", obj)
	if len(conds) != 1 || conds[0].Type != "Ready" || conds[0].Scope != "" {
		t.Fatalf("standard kind must behave like ParseConditions: %+v", conds)
	}
}

package gwapi

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func fpRoute(rules ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "api", "namespace": "shop"}, "spec": map[string]any{"rules": rules}}}
}

func fpRule(path string, backends ...any) map[string]any {
	return map[string]any{"matches": []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": path}}},
		"backendRefs": backends}
}

func fpBackendRef(name string, weight int64) map[string]any {
	b := map[string]any{"name": name, "port": int64(8080)}
	if weight >= 0 {
		b["weight"] = weight
	}
	return b
}

// The Line slice 7b: a rule's fingerprint is its matches and its backends
// with weights, in rule order, defaults applied.
func TestRuleFingerprints(t *testing.T) {
	base := RuleFingerprints(fpRoute(fpRule("/api", fpBackendRef("api", -1)), fpRule("/v2", fpBackendRef("v2", 1))))
	if len(base) != 2 || base[0] == base[1] || len(base[0]) != 64 {
		t.Fatalf("fingerprints %v", base)
	}
	// Defaults: weight 1, kind Service, core group, the route's namespace.
	explicit := RuleFingerprints(fpRoute(fpRule("/api", map[string]any{"name": "api", "port": int64(8080), "weight": int64(1),
		"kind": "Service", "group": "", "namespace": "shop"}), fpRule("/v2", fpBackendRef("v2", 1))))
	if explicit[0] != base[0] {
		t.Fatal("an explicit default moved the fingerprint")
	}
	// A weight change moves only that rule's fingerprint.
	w := RuleFingerprints(fpRoute(fpRule("/api", fpBackendRef("api", 5)), fpRule("/v2", fpBackendRef("v2", 1))))
	if w[0] == base[0] || w[1] != base[1] {
		t.Fatalf("a weight change: %v vs %v", w, base)
	}
	// A match change, and a backend change.
	if m := RuleFingerprints(fpRoute(fpRule("/apix", fpBackendRef("api", -1)), fpRule("/v2", fpBackendRef("v2", 1)))); m[0] == base[0] || m[1] != base[1] {
		t.Fatal("a match change")
	}
	if b := RuleFingerprints(fpRoute(fpRule("/api", fpBackendRef("api2", -1)), fpRule("/v2", fpBackendRef("v2", 1)))); b[0] == base[0] {
		t.Fatal("a backend change")
	}
	// A reorder moves both indexes.
	r := RuleFingerprints(fpRoute(fpRule("/v2", fpBackendRef("v2", 1)), fpRule("/api", fpBackendRef("api", -1))))
	if r[0] != base[1] || r[1] != base[0] {
		t.Fatal("a reorder")
	}
	// A filter is not part of it.
	f := fpRule("/api", fpBackendRef("api", -1))
	f["filters"] = []any{map[string]any{"type": "RequestHeaderModifier"}}
	if RuleFingerprints(fpRoute(f, fpRule("/v2", fpBackendRef("v2", 1))))[0] != base[0] {
		t.Fatal("a filter moved the fingerprint")
	}
}

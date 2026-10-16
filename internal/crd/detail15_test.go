package crd

import (
	"strings"
	"testing"
)

// The Line slice 15b: the Object block's facts and the Fields tree's input.
func TestManagersOf_NewestFirstWithoutFieldSets(t *testing.T) {
	obj := map[string]interface{}{"metadata": map[string]interface{}{"managedFields": []interface{}{
		map[string]interface{}{"manager": "kubectl-client-side-apply", "operation": "Update", "time": "2026-10-10T10:00:00Z", "fieldsV1": map[string]interface{}{"f:spec": map[string]interface{}{}}},
		map[string]interface{}{"manager": "kustomize-controller", "operation": "Apply", "time": "2026-10-12T10:00:00Z"},
		map[string]interface{}{"manager": "source-controller", "operation": "Update", "subresource": "status", "time": "2026-10-11T10:00:00Z"},
	}}}
	ms := ManagersOf(obj)
	if len(ms) != 3 || ms[0].Manager != "kustomize-controller" || ms[1].Subresource != "status" || ms[2].Operation != "Update" {
		t.Fatalf("managers %+v", ms)
	}
}

func TestFieldsJSONOf(t *testing.T) {
	obj := map[string]interface{}{"apiVersion": "v1", "kind": "K", "metadata": map[string]interface{}{"name": "x"},
		"spec": map[string]interface{}{"replicas": int64(3)}, "status": map[string]interface{}{"phase": "Ready"}, "data": map[string]interface{}{"a": "b"}}
	js, why := FieldsJSONOf(obj, false)
	if why != "" || !strings.Contains(js, `"spec":{"replicas":3}`) || strings.Contains(js, "metadata") || strings.Contains(js, "apiVersion") {
		t.Fatalf("fields %q (%s)", js, why)
	}
	if js, why := FieldsJSONOf(obj, true); js != "" || why != "secret" {
		t.Errorf("a Secret never sends its fields: %q %q", js, why)
	}
	big := map[string]interface{}{"spec": map[string]interface{}{"blob": strings.Repeat("x", FieldsCap)}}
	if js, why := FieldsJSONOf(big, false); js != "" || why != "too large" {
		t.Errorf("the cap: %d bytes, %q", len(js), why)
	}
	if js, why := FieldsJSONOf(map[string]interface{}{"metadata": map[string]interface{}{}}, false); js != "" || why != "" {
		t.Errorf("nothing to draw: %q %q", js, why)
	}
}

func TestObservedGenerationAndConditionTimes(t *testing.T) {
	obj := map[string]interface{}{"status": map[string]interface{}{"observedGeneration": int64(3), "conditions": []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True", "lastTransitionTime": "2026-10-10T10:00:00Z", "observedGeneration": int64(2)},
		map[string]interface{}{"type": "Other", "status": "False"},
	}}}
	if ObservedGenerationOf(obj) != 3 || ObservedGenerationOf(map[string]interface{}{}) != -1 {
		t.Fatal("observedGeneration")
	}
	cs := ParseConditions(obj)
	if cs[0].ObservedGeneration != 2 || cs[0].LastTransition.IsZero() || cs[1].ObservedGeneration != -1 || !cs[1].LastTransition.IsZero() {
		t.Fatalf("conditions %+v", cs)
	}
}

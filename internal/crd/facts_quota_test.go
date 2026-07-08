package crd

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFacts_ResourceQuota(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ResourceQuota",
		"spec": map[string]interface{}{
			"scopes": []interface{}{"NotTerminating"},
		},
		"status": map[string]interface{}{
			"hard": map[string]interface{}{
				"requests.cpu":    "10",
				"requests.memory": "20Gi",
				"pods":            "50",
			},
			"used": map[string]interface{}{
				"requests.cpu":    "10",   // 100% -> danger, over budget
				"requests.memory": "19Gi", // 95% -> warning
				// pods used absent -> treated as 0 -> 0%
			},
		},
	}}
	facts := BuildFacts("", "resourcequotas", u)

	if f, ok := factByLabel(facts, "resources"); !ok || f.Value != "3" {
		t.Fatalf("resources well wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "over budget"); !ok || f.Value != "requests.cpu" || f.Tone != "danger" {
		t.Fatalf("over budget well wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "scopes"); !ok || f.Value != "NotTerminating" {
		t.Fatalf("scopes row wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "requests.cpu"); !ok || f.Value != "10 / 10 (100%)" || f.Tone != "danger" {
		t.Fatalf("requests.cpu quota row wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "requests.memory"); !ok || f.Tone != "warning" {
		t.Fatalf("requests.memory quota row should be warning at ~95%%: %+v", f)
	}
	if f, ok := factByLabel(facts, "pods"); !ok || f.Value != "0 / 50 (0%)" || f.Tone != "" {
		t.Fatalf("pods quota row (used absent -> 0) wrong: %+v", f)
	}
}

func TestBuildFacts_ResourceQuota_PressureFirst(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ResourceQuota",
		"status": map[string]interface{}{
			"hard": map[string]interface{}{"a": "10", "b": "10", "c": "10"},
			"used": map[string]interface{}{"a": "1", "b": "9", "c": "5"},
		},
	}}
	facts := BuildFacts("", "resourcequotas", u)
	// Collect quota rows in emitted order: expect b (90%), c (50%), a (10%).
	var order []string
	for _, f := range facts {
		if f.Group == "Quota" {
			order = append(order, f.Label)
		}
	}
	if len(order) != 3 || order[0] != "b" || order[1] != "c" || order[2] != "a" {
		t.Fatalf("quota rows not sorted pressure-first: %v", order)
	}
}

func TestBuildFacts_ResourceQuota_NoOverBudget(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ResourceQuota",
		"status": map[string]interface{}{
			"hard": map[string]interface{}{"pods": "50"},
			"used": map[string]interface{}{"pods": "10"},
		},
	}}
	facts := BuildFacts("", "resourcequotas", u)
	if _, ok := factByLabel(facts, "over budget"); ok {
		t.Fatalf("over budget well must not appear when nothing is at >=100%%")
	}
}

func TestBuildFacts_LimitRange(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "LimitRange",
		"spec": map[string]interface{}{
			"limits": []interface{}{
				map[string]interface{}{
					"type":           "Container",
					"default":        map[string]interface{}{"cpu": "500m", "memory": "512Mi"},
					"defaultRequest": map[string]interface{}{"cpu": "100m"},
					"max":            map[string]interface{}{"cpu": "2"},
					"min":            map[string]interface{}{"cpu": "50m"},
				},
			},
		},
	}}
	facts := BuildFacts("", "limitranges", u)
	if f, ok := factByLabel(facts, "Container cpu"); !ok || f.Value != "default 500m, request 100m, max 2, min 50m" {
		t.Fatalf("Container cpu limit row wrong: %+v", f)
	}
	// memory only has a default set -> only that field appears.
	if f, ok := factByLabel(facts, "Container memory"); !ok || f.Value != "default 512Mi" {
		t.Fatalf("Container memory limit row wrong: %+v", f)
	}
	// Informational only: no glow.
	for _, f := range facts {
		if f.Tone != "" {
			t.Fatalf("LimitRange facts must be tone-free: %+v", f)
		}
	}
}

func TestBuildFacts_ResourceQuota_SubOnePercent(t *testing.T) {
	// 3 / 1000 = 0.3% rounds to "0%", which reads as untouched; a quota with
	// real (small) usage must show "<1%" instead, and stay untoned.
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ResourceQuota",
		"status": map[string]interface{}{
			"hard": map[string]interface{}{"pods": "1k"},
			"used": map[string]interface{}{"pods": "3"},
		},
	}}
	facts := BuildFacts("", "resourcequotas", u)
	f, ok := factByLabel(facts, "pods")
	if !ok || f.Value != "3 / 1k (<1%)" {
		t.Fatalf("sub-1%% usage should render <1%%, got %+v", f)
	}
	if f.Tone != "" {
		t.Fatalf("sub-1%% usage is not a problem; tone must be empty: %+v", f)
	}
}

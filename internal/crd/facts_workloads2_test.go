package crd

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFacts_Job_Completed(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"spec": map[string]interface{}{
			"completions":    int64(3),
			"parallelism":    int64(2),
			"backoffLimit":   int64(6),
			"completionMode": "Indexed",
		},
		"status": map[string]interface{}{
			"succeeded":      int64(3),
			"active":         int64(0),
			"failed":         int64(1),
			"startTime":      "2026-07-08T10:00:00Z",
			"completionTime": "2026-07-08T10:03:12Z",
		},
	}}
	facts := BuildFacts("batch", "jobs", u)
	if f, ok := factByLabel(facts, "completions"); !ok || f.Value != "3/3" {
		t.Fatalf("completions fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "active"); !ok || f.Value != "0" {
		t.Fatalf("active fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "failed"); !ok || f.Value != "1" || f.Tone != "danger" {
		t.Fatalf("failed fact wrong (want 1/danger): %+v", f)
	}
	if f, ok := factByLabel(facts, "parallelism"); !ok || f.Value != "2" {
		t.Fatalf("parallelism fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "backoff"); !ok || f.Value != "6" {
		t.Fatalf("backoff fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "mode"); !ok || f.Value != "Indexed" {
		t.Fatalf("mode fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "duration"); !ok || f.Value != "3m12s" {
		t.Fatalf("duration fact wrong (want 3m12s): %+v", f)
	}
}

func TestBuildFacts_Job_NoCompletionsAndRunning(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Job",
		"spec": map[string]interface{}{},
		"status": map[string]interface{}{
			"succeeded": int64(1),
			"active":    int64(2),
			"startTime": "2026-07-08T10:00:00Z",
		},
	}}
	facts := BuildFacts("batch", "jobs", u)
	// No spec.completions -> succeeded-only value, no slash.
	if f, ok := factByLabel(facts, "completions"); !ok || f.Value != "1" {
		t.Fatalf("completions fact wrong (want bare 1): %+v", f)
	}
	// failed defaults to 0 with no danger tone.
	if f, ok := factByLabel(facts, "failed"); !ok || f.Value != "0" || f.Tone != "" {
		t.Fatalf("failed fact wrong (want 0/no tone): %+v", f)
	}
	// Running: startTime set, no completionTime.
	if f, ok := factByLabel(facts, "duration"); !ok || !strings.HasPrefix(f.Value, "running ") {
		t.Fatalf("duration fact wrong (want running prefix): %+v", f)
	}
	// completionMode absent -> no mode row.
	if _, ok := factByLabel(facts, "mode"); ok {
		t.Fatalf("mode fact should be absent when completionMode unset")
	}
}

func TestBuildFacts_PDB_DrainBlocked(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "policy/v1",
		"kind":       "PodDisruptionBudget",
		"spec": map[string]interface{}{
			"minAvailable": int64(2),
		},
		"status": map[string]interface{}{
			"disruptionsAllowed": int64(0),
			"currentHealthy":     int64(1),
			"desiredHealthy":     int64(2),
			"expectedPods":       int64(3),
			"disruptedPods": map[string]interface{}{
				"pod-a": "2026-07-08T10:00:00Z",
			},
		},
	}}
	facts := BuildFacts("policy", "poddisruptionbudgets", u)
	if f, ok := factByLabel(facts, "disruptions allowed"); !ok || f.Value != "0" || f.Tone != "warning" {
		t.Fatalf("disruptions allowed fact wrong (want 0/warning): %+v", f)
	}
	if f, ok := factByLabel(facts, "healthy"); !ok || f.Value != "1" {
		t.Fatalf("healthy fact wrong: %+v", f)
	}
	// currentHealthy(1) < desiredHealthy(2) -> warning.
	if f, ok := factByLabel(facts, "desired"); !ok || f.Value != "2" || f.Tone != "warning" {
		t.Fatalf("desired fact wrong (want 2/warning): %+v", f)
	}
	if f, ok := factByLabel(facts, "expected"); !ok || f.Value != "3" {
		t.Fatalf("expected fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "min available"); !ok || f.Value != "2" {
		t.Fatalf("min available fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "disrupted"); !ok || f.Value != "1" {
		t.Fatalf("disrupted fact wrong: %+v", f)
	}
}

func TestBuildFacts_PDB_HealthyPercentString(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "PodDisruptionBudget",
		"spec": map[string]interface{}{
			"maxUnavailable": "25%",
		},
		"status": map[string]interface{}{
			"disruptionsAllowed": int64(3),
			"currentHealthy":     int64(5),
			"desiredHealthy":     int64(4),
			"expectedPods":       int64(5),
		},
	}}
	facts := BuildFacts("policy", "poddisruptionbudgets", u)
	// Healthy PDB: disruptions allowed > 0 carries no warning tone.
	if f, ok := factByLabel(facts, "disruptions allowed"); !ok || f.Value != "3" || f.Tone != "" {
		t.Fatalf("disruptions allowed fact wrong (want 3/no tone): %+v", f)
	}
	// currentHealthy(5) >= desiredHealthy(4) -> no warning.
	if f, ok := factByLabel(facts, "desired"); !ok || f.Value != "4" || f.Tone != "" {
		t.Fatalf("desired fact wrong (want 4/no tone): %+v", f)
	}
	// IntOrString percentage read as its string form.
	if f, ok := factByLabel(facts, "max unavailable"); !ok || f.Value != "25%" {
		t.Fatalf("max unavailable fact wrong (want 25%%): %+v", f)
	}
	// No disruptedPods -> no disrupted well.
	if _, ok := factByLabel(facts, "disrupted"); ok {
		t.Fatalf("disrupted fact should be absent with no disruptedPods")
	}
}

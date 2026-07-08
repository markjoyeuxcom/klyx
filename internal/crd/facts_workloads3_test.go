package crd

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFacts_ReplicaSet_Healthy(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"spec": map[string]interface{}{
			"replicas": int64(3),
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{"name": "app", "image": "ghcr.io/acme/app:1.4.2"},
						map[string]interface{}{"name": "sidecar", "image": "envoyproxy/envoy:v1.29"},
					},
				},
			},
		},
		"status": map[string]interface{}{
			"replicas":          int64(3),
			"readyReplicas":     int64(3),
			"availableReplicas": int64(3),
		},
	}}
	facts := BuildFacts("apps", "replicasets", u)

	if f, ok := factByLabel(facts, "ready"); !ok || f.Value != "3/3" || f.Tone != "success" {
		t.Fatalf("ready fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "available"); !ok || f.Value != "3" {
		t.Fatalf("available fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "current"); !ok || f.Value != "3" {
		t.Fatalf("current fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "desired"); !ok || f.Value != "3" {
		t.Fatalf("desired fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "image"); !ok || f.Value != "ghcr.io/acme/app:1.4.2, envoyproxy/envoy:v1.29" {
		t.Fatalf("image fact wrong: %+v", f)
	}
}

func TestBuildFacts_ReplicaSet_Degraded(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ReplicaSet",
		"spec": map[string]interface{}{"replicas": int64(3)},
		"status": map[string]interface{}{
			"replicas":      int64(3),
			"readyReplicas": int64(1),
		},
	}}
	facts := BuildFacts("apps", "replicasets", u)
	if f, ok := factByLabel(facts, "ready"); !ok || f.Value != "1/3" || f.Tone != "warning" {
		t.Fatalf("degraded ready fact wrong: %+v", f)
	}
}

func TestBuildFacts_ReplicaSet_ScaledDownNotAFault(t *testing.T) {
	// An old revision scaled to zero is intentional, not broken - no warning tone.
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind":   "ReplicaSet",
		"spec":   map[string]interface{}{"replicas": int64(0)},
		"status": map[string]interface{}{"replicas": int64(0)},
	}}
	facts := BuildFacts("apps", "replicasets", u)
	if f, ok := factByLabel(facts, "ready"); !ok || f.Value != "0/0" || f.Tone != "" {
		t.Fatalf("scaled-down ready fact must be tone-neutral: %+v", f)
	}
}

func TestBuildFacts_ReplicaSet_Empty(t *testing.T) {
	// nil-tolerance: an object with no spec/status must not panic and still emits
	// the ready headline as 0/0.
	u := &unstructured.Unstructured{Object: map[string]interface{}{"kind": "ReplicaSet"}}
	facts := BuildFacts("apps", "replicasets", u)
	if f, ok := factByLabel(facts, "ready"); !ok || f.Value != "0/0" || f.Tone != "" {
		t.Fatalf("empty ready fact wrong: %+v", f)
	}
	if _, ok := factByLabel(facts, "image"); ok {
		t.Fatalf("no containers -> no image row expected")
	}
}

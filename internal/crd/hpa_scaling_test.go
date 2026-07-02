package crd

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// hpaObj builds a minimal HPA unstructured object for testing.
func hpaObj(spec, status map[string]interface{}) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "autoscaling/v2",
		"kind":       "HorizontalPodAutoscaler",
		"metadata":   map[string]interface{}{"name": "web-hpa", "namespace": "default"},
		"spec":       spec,
		"status":     status,
	}
	return &unstructured.Unstructured{Object: obj}
}

// TestBuildHPAScalingCPUUtilization verifies a Resource/cpu metric with
// averageUtilization target and a matching current value.
func TestBuildHPAScalingCPUUtilization(t *testing.T) {
	u := hpaObj(
		map[string]interface{}{
			"maxReplicas": int64(10),
			"minReplicas": int64(2),
			"scaleTargetRef": map[string]interface{}{
				"kind": "Deployment",
				"name": "web",
			},
			"metrics": []interface{}{
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name": "cpu",
						"target": map[string]interface{}{
							"type":               "Utilization",
							"averageUtilization": int64(70),
						},
					},
				},
			},
		},
		map[string]interface{}{
			"currentReplicas": int64(4),
			"desiredReplicas": int64(4),
			"currentMetrics": []interface{}{
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name": "cpu",
						"current": map[string]interface{}{
							"averageUtilization": int64(43),
							"averageValue":       "430m",
						},
					},
				},
			},
		},
	)

	s, err := BuildHPAScaling(u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.MinReplicas != 2 {
		t.Errorf("MinReplicas: want 2, got %d", s.MinReplicas)
	}
	if s.MaxReplicas != 10 {
		t.Errorf("MaxReplicas: want 10, got %d", s.MaxReplicas)
	}
	if s.CurrentReplicas != 4 {
		t.Errorf("CurrentReplicas: want 4, got %d", s.CurrentReplicas)
	}
	if s.TargetKind != "Deployment" || s.TargetName != "web" {
		t.Errorf("scaleTargetRef: kind=%q name=%q", s.TargetKind, s.TargetName)
	}
	if len(s.Metrics) != 1 {
		t.Fatalf("Metrics count: want 1, got %d", len(s.Metrics))
	}
	m := s.Metrics[0]
	if m.Name != "cpu" {
		t.Errorf("metric name: want cpu, got %q", m.Name)
	}
	if m.Target != "70%" {
		t.Errorf("metric target: want 70%%, got %q", m.Target)
	}
	if m.Current != "43%" {
		t.Errorf("metric current: want 43%%, got %q", m.Current)
	}
}

// TestBuildHPAScalingMemoryAverageValue verifies a Resource/memory metric with
// averageValue target and current.
func TestBuildHPAScalingMemoryAverageValue(t *testing.T) {
	u := hpaObj(
		map[string]interface{}{
			"maxReplicas": int64(5),
			"scaleTargetRef": map[string]interface{}{
				"kind": "Deployment",
				"name": "api",
			},
			"metrics": []interface{}{
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name": "memory",
						"target": map[string]interface{}{
							"type":         "AverageValue",
							"averageValue": "256Mi",
						},
					},
				},
			},
		},
		map[string]interface{}{
			"currentReplicas": int64(2),
			"desiredReplicas": int64(2),
			"currentMetrics": []interface{}{
				// Deliberately the retired v2beta1 FLAT shape - this pins the
				// defensive fallback in statusCurrentStr.
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name":                "memory",
						"currentAverageValue": "180Mi",
					},
				},
			},
		},
	)

	s, err := BuildHPAScaling(u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Metrics) != 1 {
		t.Fatalf("Metrics count: want 1, got %d", len(s.Metrics))
	}
	m := s.Metrics[0]
	if m.Name != "memory" {
		t.Errorf("metric name: want memory, got %q", m.Name)
	}
	if m.Target != "256Mi" {
		t.Errorf("metric target: want 256Mi, got %q", m.Target)
	}
	if m.Current != "180Mi" {
		t.Errorf("metric current: want 180Mi, got %q", m.Current)
	}
}

// TestBuildHPAScalingMissingCurrentMetrics verifies that an absent
// status.currentMetrics entry results in Current="" (honest, not invented).
func TestBuildHPAScalingMissingCurrentMetrics(t *testing.T) {
	u := hpaObj(
		map[string]interface{}{
			"maxReplicas": int64(8),
			"scaleTargetRef": map[string]interface{}{
				"kind": "StatefulSet",
				"name": "db",
			},
			"metrics": []interface{}{
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name": "cpu",
						"target": map[string]interface{}{
							"type":               "Utilization",
							"averageUtilization": int64(60),
						},
					},
				},
			},
		},
		map[string]interface{}{
			"currentReplicas": int64(1),
			"desiredReplicas": int64(1),
			// No currentMetrics entry at all
		},
	)

	s, err := BuildHPAScaling(u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Metrics) != 1 {
		t.Fatalf("Metrics count: want 1, got %d", len(s.Metrics))
	}
	if s.Metrics[0].Current != "" {
		t.Errorf("Current must be empty when no currentMetrics match, got %q", s.Metrics[0].Current)
	}
}

// TestBuildHPAScalingMinReplicasAbsent verifies that a missing spec.minReplicas
// defaults to 1 (the Kubernetes API default).
func TestBuildHPAScalingMinReplicasAbsent(t *testing.T) {
	u := hpaObj(
		map[string]interface{}{
			"maxReplicas": int64(3),
			"scaleTargetRef": map[string]interface{}{
				"kind": "Deployment",
				"name": "app",
			},
		},
		map[string]interface{}{
			"currentReplicas": int64(1),
			"desiredReplicas": int64(1),
		},
	)

	s, err := BuildHPAScaling(u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.MinReplicas != 1 {
		t.Errorf("MinReplicas must default to 1 when absent, got %d", s.MinReplicas)
	}
}

// TestBuildHPAScalingMinReplicasExplicitZero verifies that an explicit
// spec.minReplicas: 0 (legal under the HPAScaleToZero feature gate) is
// preserved, not conflated with "absent" and bumped to 1.
func TestBuildHPAScalingMinReplicasExplicitZero(t *testing.T) {
	u := hpaObj(
		map[string]interface{}{
			"minReplicas": int64(0),
			"maxReplicas": int64(3),
			"scaleTargetRef": map[string]interface{}{
				"kind": "Deployment",
				"name": "app",
			},
		},
		map[string]interface{}{
			"currentReplicas": int64(0),
			"desiredReplicas": int64(0),
		},
	)

	s, err := BuildHPAScaling(u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.MinReplicas != 0 {
		t.Errorf("MinReplicas must stay 0 when explicitly set, got %d", s.MinReplicas)
	}
}

// TestBuildHPAScalingLastScaleTimeAbsent verifies that an absent
// status.lastScaleTime yields LastScaleUnix=0.
func TestBuildHPAScalingLastScaleTimeAbsent(t *testing.T) {
	u := hpaObj(
		map[string]interface{}{
			"maxReplicas": int64(5),
			"scaleTargetRef": map[string]interface{}{
				"kind": "Deployment",
				"name": "svc",
			},
		},
		map[string]interface{}{
			"currentReplicas": int64(2),
			"desiredReplicas": int64(2),
			// No lastScaleTime
		},
	)

	s, err := BuildHPAScaling(u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.LastScaleUnix != 0 {
		t.Errorf("LastScaleUnix must be 0 when absent, got %d", s.LastScaleUnix)
	}
}

// TestBuildHPAScalingLastScaleTimeParsed verifies that a present
// status.lastScaleTime is correctly parsed to a Unix timestamp.
func TestBuildHPAScalingLastScaleTimeParsed(t *testing.T) {
	u := hpaObj(
		map[string]interface{}{
			"maxReplicas":    int64(5),
			"scaleTargetRef": map[string]interface{}{"kind": "Deployment", "name": "x"},
		},
		map[string]interface{}{
			"currentReplicas": int64(1),
			"desiredReplicas": int64(1),
			"lastScaleTime":   "2026-06-01T10:00:00Z",
		},
	)

	s, err := BuildHPAScaling(u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.LastScaleUnix == 0 {
		t.Error("LastScaleUnix must be non-zero when lastScaleTime is present")
	}
	// 2026-06-01T10:00:00Z = 1780308000
	if s.LastScaleUnix != 1780308000 {
		t.Errorf("LastScaleUnix: want 1780308000, got %d", s.LastScaleUnix)
	}
}

// TestBuildHPAScalingTypeNameMatchingNotIndex verifies the spec guarantees
// correct type+name matching even when status.currentMetrics[] is reordered
// relative to spec.metrics[]. A naive index-based match would produce wrong
// pairings here.
func TestBuildHPAScalingTypeNameMatchingNotIndex(t *testing.T) {
	u := hpaObj(
		map[string]interface{}{
			"maxReplicas": int64(10),
			"scaleTargetRef": map[string]interface{}{
				"kind": "Deployment",
				"name": "app",
			},
			// spec order: cpu first, memory second
			"metrics": []interface{}{
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name": "cpu",
						"target": map[string]interface{}{
							"type":               "Utilization",
							"averageUtilization": int64(70),
						},
					},
				},
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name": "memory",
						"target": map[string]interface{}{
							"type":               "Utilization",
							"averageUtilization": int64(80),
						},
					},
				},
			},
		},
		map[string]interface{}{
			"currentReplicas": int64(3),
			"desiredReplicas": int64(3),
			// status order: memory first, cpu second (deliberately reordered)
			"currentMetrics": []interface{}{
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name": "memory",
						"current": map[string]interface{}{
							"averageUtilization": int64(55),
						},
					},
				},
				map[string]interface{}{
					"type": "Resource",
					"resource": map[string]interface{}{
						"name": "cpu",
						"current": map[string]interface{}{
							"averageUtilization": int64(42),
						},
					},
				},
			},
		},
	)

	s, err := BuildHPAScaling(u)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.Metrics) != 2 {
		t.Fatalf("Metrics count: want 2, got %d", len(s.Metrics))
	}

	// spec order is preserved: cpu first, memory second
	cpu := s.Metrics[0]
	mem := s.Metrics[1]

	if cpu.Name != "cpu" {
		t.Errorf("Metrics[0].Name: want cpu, got %q", cpu.Name)
	}
	if cpu.Current != "42%" {
		t.Errorf("cpu current: want 42%%, got %q (index-based would give 55%%)", cpu.Current)
	}
	if mem.Name != "memory" {
		t.Errorf("Metrics[1].Name: want memory, got %q", mem.Name)
	}
	if mem.Current != "55%" {
		t.Errorf("memory current: want 55%%, got %q (index-based would give 42%%)", mem.Current)
	}
}

// TestBuildHPAScalingMissingMaxReplicas verifies that an absent maxReplicas
// returns an error.
func TestBuildHPAScalingMissingMaxReplicas(t *testing.T) {
	u := hpaObj(
		map[string]interface{}{
			"scaleTargetRef": map[string]interface{}{"kind": "Deployment", "name": "app"},
			// no maxReplicas
		},
		map[string]interface{}{},
	)

	_, err := BuildHPAScaling(u)
	if err == nil {
		t.Error("expected error for missing maxReplicas, got nil")
	}
}

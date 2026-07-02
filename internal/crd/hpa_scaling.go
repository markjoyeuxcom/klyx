package crd

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// HPAScaling is a pure summary of an autoscaling/v2 HorizontalPodAutoscaler.
// It carries what the user cares about: replica band, current state, scale
// target, and per-metric current/target pairs.
type HPAScaling struct {
	MinReplicas     int32
	MaxReplicas     int32
	CurrentReplicas int32
	DesiredReplicas int32
	TargetKind      string // scaleTargetRef.kind
	TargetName      string // scaleTargetRef.name
	LastScaleUnix   int64  // 0 when never scaled (status.lastScaleTime absent)
	Metrics         []HPAMetric
}

// HPAMetric is one entry from spec.metrics[], matched with the corresponding
// status.currentMetrics[] entry when possible.
type HPAMetric struct {
	Name    string // resource name ("cpu"/"memory"), or object/pods/external metric name
	Type    string // "Resource" | "Pods" | "Object" | "External" | "ContainerResource"
	Target  string // human string: "70%" (averageUtilization), "100m" (averageValue/value)
	Current string // matched current value, "" when unknown (never invented)
}

// BuildHPAScaling builds an HPAScaling from an unstructured HPA object. It
// returns an error only when the spec is unparseable (e.g., maxReplicas
// missing). Non-fatal gaps (unknown current, absent lastScaleTime, absent
// minReplicas) are silently defaulted and documented in-line.
func BuildHPAScaling(u *unstructured.Unstructured) (*HPAScaling, error) {
	obj := u.Object

	// spec.maxReplicas is required by the API; its absence is an error.
	maxRaw, found, err := unstructured.NestedInt64(obj, "spec", "maxReplicas")
	if err != nil || !found {
		return nil, fmt.Errorf("hpa_scaling: spec.maxReplicas missing or invalid")
	}

	// spec.minReplicas: defaults to 1 only when ABSENT — that IS the Kubernetes
	// API default (HPA spec says "defaults to 1 if not set"). An explicit 0 is
	// legal under the HPAScaleToZero feature gate and must be preserved, so we
	// key off NestedInt64's found flag, never the zero value.
	minRaw, minFound, _ := unstructured.NestedInt64(obj, "spec", "minReplicas")
	min := int32(minRaw)
	if !minFound {
		min = 1
	}

	// scaleTargetRef
	targetKind, _, _ := unstructured.NestedString(obj, "spec", "scaleTargetRef", "kind")
	targetName, _, _ := unstructured.NestedString(obj, "spec", "scaleTargetRef", "name")

	// status replicas
	currentRaw, _, _ := unstructured.NestedInt64(obj, "status", "currentReplicas")
	desiredRaw, _, _ := unstructured.NestedInt64(obj, "status", "desiredReplicas")

	// status.lastScaleTime: RFC3339 string; absent → 0
	var lastScaleUnix int64
	if lsRaw, found, _ := unstructured.NestedString(obj, "status", "lastScaleTime"); found && lsRaw != "" {
		if t, err := time.Parse(time.RFC3339, lsRaw); err == nil {
			lastScaleUnix = t.Unix()
		}
	}

	// Build the current-metrics lookup by type+name.
	// We match by type+name rather than by array index because the Kubernetes
	// API does not guarantee that status.currentMetrics[] preserves the same
	// order as spec.metrics[]. Matching by index would produce silently
	// wrong pairings whenever the controller reorders entries.
	currentMap := buildCurrentMetricsMap(obj)

	// Parse spec.metrics[]
	specMetrics, _, _ := unstructured.NestedSlice(obj, "spec", "metrics")
	metrics := make([]HPAMetric, 0, len(specMetrics))
	for _, raw := range specMetrics {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		met := parseSpecMetric(m, currentMap)
		metrics = append(metrics, met)
	}

	return &HPAScaling{
		MinReplicas:     min,
		MaxReplicas:     int32(maxRaw),
		CurrentReplicas: int32(currentRaw),
		DesiredReplicas: int32(desiredRaw),
		TargetKind:      targetKind,
		TargetName:      targetName,
		LastScaleUnix:   lastScaleUnix,
		Metrics:         metrics,
	}, nil
}

// currentMetricKey is the map key used to match spec.metrics with
// status.currentMetrics by type+name (not index).
type currentMetricKey struct {
	metricType string
	name       string
}

// buildCurrentMetricsMap indexes status.currentMetrics[] by {type, name} so
// that parseSpecMetric can do an O(1) lookup regardless of ordering.
func buildCurrentMetricsMap(obj map[string]interface{}) map[currentMetricKey]map[string]interface{} {
	raw, _, _ := unstructured.NestedSlice(obj, "status", "currentMetrics")
	out := make(map[currentMetricKey]map[string]interface{}, len(raw))
	for _, r := range raw {
		cm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		metType, _ := cm["type"].(string)
		name := currentMetricName(metType, cm)
		out[currentMetricKey{metricType: metType, name: name}] = cm
	}
	return out
}

// currentMetricName extracts the metric's identifying name from a
// status.currentMetrics[] entry: the resource name for Resource/ContainerResource,
// the metric name for Pods/Object/External.
func currentMetricName(metType string, cm map[string]interface{}) string {
	switch metType {
	case "Resource":
		if res, ok := cm["resource"].(map[string]interface{}); ok {
			n, _ := res["name"].(string)
			return n
		}
	case "ContainerResource":
		if cr, ok := cm["containerResource"].(map[string]interface{}); ok {
			n, _ := cr["name"].(string)
			return n
		}
	case "Pods":
		if p, ok := cm["pods"].(map[string]interface{}); ok {
			if met, ok := p["metric"].(map[string]interface{}); ok {
				n, _ := met["name"].(string)
				return n
			}
		}
	case "Object":
		if o, ok := cm["object"].(map[string]interface{}); ok {
			if met, ok := o["metric"].(map[string]interface{}); ok {
				n, _ := met["name"].(string)
				return n
			}
		}
	case "External":
		if e, ok := cm["external"].(map[string]interface{}); ok {
			if met, ok := e["metric"].(map[string]interface{}); ok {
				n, _ := met["name"].(string)
				return n
			}
		}
	}
	return ""
}

// parseSpecMetric converts one spec.metrics[] entry to an HPAMetric, filling
// Current from the pre-built currentMap lookup.
func parseSpecMetric(m map[string]interface{}, currentMap map[currentMetricKey]map[string]interface{}) HPAMetric {
	metType, _ := m["type"].(string)

	switch metType {
	case "Resource":
		res, _ := m["resource"].(map[string]interface{})
		name, _ := res["name"].(string)
		target := resourceTargetStr(res)
		current := ""
		if cm, ok := currentMap[currentMetricKey{metricType: "Resource", name: name}]; ok {
			current = statusCurrentStr(cm["resource"])
		}
		return HPAMetric{Name: name, Type: metType, Target: target, Current: current}

	case "ContainerResource":
		cr, _ := m["containerResource"].(map[string]interface{})
		name, _ := cr["name"].(string)
		target := resourceTargetStr(cr)
		current := ""
		if cm, ok := currentMap[currentMetricKey{metricType: "ContainerResource", name: name}]; ok {
			current = statusCurrentStr(cm["containerResource"])
		}
		return HPAMetric{Name: name, Type: metType, Target: target, Current: current}

	case "Pods":
		p, _ := m["pods"].(map[string]interface{})
		met, _ := p["metric"].(map[string]interface{})
		name, _ := met["name"].(string)
		target := podsTargetStr(p)
		current := ""
		if cm, ok := currentMap[currentMetricKey{metricType: "Pods", name: name}]; ok {
			current = statusCurrentStr(cm["pods"])
		}
		return HPAMetric{Name: name, Type: metType, Target: target, Current: current}

	case "Object":
		o, _ := m["object"].(map[string]interface{})
		met, _ := o["metric"].(map[string]interface{})
		name, _ := met["name"].(string)
		target := objectTargetStr(o)
		current := ""
		if cm, ok := currentMap[currentMetricKey{metricType: "Object", name: name}]; ok {
			current = statusCurrentStr(cm["object"])
		}
		return HPAMetric{Name: name, Type: metType, Target: target, Current: current}

	case "External":
		e, _ := m["external"].(map[string]interface{})
		met, _ := e["metric"].(map[string]interface{})
		name, _ := met["name"].(string)
		target := externalTargetStr(e)
		current := ""
		if cm, ok := currentMap[currentMetricKey{metricType: "External", name: name}]; ok {
			current = statusCurrentStr(cm["external"])
		}
		return HPAMetric{Name: name, Type: metType, Target: target, Current: current}

	default:
		return HPAMetric{Type: metType}
	}
}

// ---- Target string helpers (spec) -------------------------------------------

func resourceTargetStr(res map[string]interface{}) string {
	if res == nil {
		return ""
	}
	tgt, _ := res["target"].(map[string]interface{})
	if tgt == nil {
		return ""
	}
	// averageUtilization → "N%"
	if u, ok := tgt["averageUtilization"]; ok {
		if ui, ok := toInt64(u); ok {
			return fmt.Sprintf("%d%%", ui)
		}
	}
	// averageValue → quantity string verbatim
	if av, ok := tgt["averageValue"].(string); ok && av != "" {
		return av
	}
	// value → quantity string verbatim
	if v, ok := tgt["value"].(string); ok && v != "" {
		return v
	}
	return ""
}

func podsTargetStr(p map[string]interface{}) string {
	if p == nil {
		return ""
	}
	tgt, _ := p["target"].(map[string]interface{})
	if tgt == nil {
		return ""
	}
	if av, ok := tgt["averageValue"].(string); ok && av != "" {
		return av
	}
	return ""
}

func objectTargetStr(o map[string]interface{}) string {
	if o == nil {
		return ""
	}
	tgt, _ := o["target"].(map[string]interface{})
	if tgt == nil {
		return ""
	}
	if av, ok := tgt["averageValue"].(string); ok && av != "" {
		return av
	}
	if v, ok := tgt["value"].(string); ok && v != "" {
		return v
	}
	return ""
}

func externalTargetStr(e map[string]interface{}) string {
	if e == nil {
		return ""
	}
	tgt, _ := e["target"].(map[string]interface{})
	if tgt == nil {
		return ""
	}
	if av, ok := tgt["averageValue"].(string); ok && av != "" {
		return av
	}
	if v, ok := tgt["value"].(string); ok && v != "" {
		return v
	}
	return ""
}

// ---- Current string helpers (status) ----------------------------------------

// currentValueStr extracts a human value from a status current{} block:
// averageUtilization → "N%", else averageValue / value quantity verbatim.
func currentValueStr(cur map[string]interface{}) string {
	if u, ok := cur["averageUtilization"]; ok {
		if ui, ok := toInt64(u); ok {
			return fmt.Sprintf("%d%%", ui)
		}
	}
	if av, ok := cur["averageValue"].(string); ok && av != "" {
		return av
	}
	if v, ok := cur["value"].(string); ok && v != "" {
		return v
	}
	return ""
}

// statusCurrentStr reads a status.currentMetrics[].<type> block. The
// autoscaling/v2 (and v2beta2) shape nests values under current{} -
// e.g. resource.current.averageUtilization. The flat currentAverage*
// keys are the retired v2beta1 shape, kept as a defensive fallback only
// (reading the flat keys FIRST was the bug that rendered "-" for every
// live v2 HPA while the v2beta1-shaped fixtures passed).
func statusCurrentStr(raw interface{}) string {
	m, ok := raw.(map[string]interface{})
	if !ok || m == nil {
		return ""
	}
	if cur, ok := m["current"].(map[string]interface{}); ok {
		if s := currentValueStr(cur); s != "" {
			return s
		}
	}
	// v2beta1 flat fallback
	if u, ok := m["currentAverageUtilization"]; ok {
		if ui, ok := toInt64(u); ok {
			return fmt.Sprintf("%d%%", ui)
		}
	}
	if av, ok := m["currentAverageValue"].(string); ok && av != "" {
		return av
	}
	if v, ok := m["currentValue"].(string); ok && v != "" {
		return v
	}
	return ""
}

// toInt64 converts a JSON-decoded number (float64) or int64 to int64.
func toInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case int:
		return int64(n), true
	}
	return 0, false
}

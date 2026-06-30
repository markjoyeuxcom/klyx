package crd

import (
	"encoding/base64"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Condition is one status.conditions entry.
type Condition struct {
	Type    string
	Status  string
	Reason  string
	Message string
}

// Event is a describe-style event for an instance.
type Event struct {
	Type    string // Normal | Warning
	Reason  string
	Message string
	Count   int32
	Last    time.Time
}

// SecretKeyInfo describes one key in a Secret's data map: name and decoded
// byte-length only. The value never leaves the Go side until RevealSecretKey is
// called explicitly.
type SecretKeyInfo struct {
	Key   string
	Bytes int
}

// RelatedRef is a navigable object relation discovered from a detail object's
// spec/status. It intentionally carries the GVR because Kind alone is not
// enough for generic resource drill-in.
type RelatedRef struct {
	Kind      string
	Namespace string
	Name      string
	Group     string
	Version   string
	Plural    string
	Scope     string
	Relation  string
}

// InstanceDetail is the full per-instance detail: header, conditions, events, YAML.
type InstanceDetail struct {
	Kind        string
	Namespace   string
	Name        string
	Created     time.Time
	Labels      map[string]string
	Annotations map[string]string
	Conditions  []Condition
	Events      []Event
	YAML        string
	// SecretKeys is populated only for v1 Secrets. The YAML has values replaced
	// with "<masked>"; this list gives key names + decoded byte-lengths so the
	// frontend can render a key list without any value data crossing the bridge.
	SecretKeys []SecretKeyInfo
	// ServiceBacking is populated only for v1 Services. Nil for all other kinds.
	ServiceBacking *ServiceBacking
	// HPAScaling is populated only for autoscaling HorizontalPodAutoscalers
	// (any version; v2 is what we list). Nil for all other kinds.
	HPAScaling *HPAScaling
	// Related contains direct object relationships for drill-in tabs.
	Related []RelatedRef
}

// ParseConditions maps status.conditions[] (a near-universal convention). Empty
// when the field is absent or not a list of objects.
func ParseConditions(obj map[string]interface{}) []Condition {
	raw, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	out := make([]Condition, 0, len(raw))
	for _, c := range raw {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		cond := Condition{}
		cond.Type, _ = m["type"].(string)
		cond.Status, _ = m["status"].(string)
		cond.Reason, _ = m["reason"].(string)
		cond.Message, _ = m["message"].(string)
		out = append(out, cond)
	}
	return out
}

// ToYAML marshals an unstructured Object map to kubectl-style YAML.
func ToYAML(obj map[string]interface{}) (string, error) {
	b, err := yaml.Marshal(withoutManagedFields(obj))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// MaskSecretData returns a shallow-cloned object map with all values in the
// "data" and "stringData" fields replaced by the literal string "<masked>", and
// a sorted slice of SecretKeyInfo carrying key names + decoded byte-lengths.
// The input is never mutated. Call this only when group=="" && kind=="Secret".
func MaskSecretData(obj map[string]interface{}) (map[string]interface{}, []SecretKeyInfo) {
	out := make(map[string]interface{}, len(obj))
	for k, v := range obj {
		out[k] = v
	}

	var keys []SecretKeyInfo

	// data: base64-encoded byte values (the standard Secret.data field)
	if raw, ok := obj["data"].(map[string]interface{}); ok && len(raw) > 0 {
		masked := make(map[string]interface{}, len(raw))
		for k, v := range raw {
			// Decode byte-length from the base64 string; fall back to 0 on any error.
			n := 0
			if s, ok := v.(string); ok && s != "" {
				if b, err := base64.StdEncoding.DecodeString(s); err == nil {
					n = len(b)
				}
			}
			keys = append(keys, SecretKeyInfo{Key: k, Bytes: n})
			masked[k] = "<masked>"
		}
		out["data"] = masked
	}

	// stringData: plain-text values; defense-in-depth masking.
	if raw, ok := obj["stringData"].(map[string]interface{}); ok && len(raw) > 0 {
		masked := make(map[string]interface{}, len(raw))
		for k, v := range raw {
			n := 0
			if s, ok := v.(string); ok {
				n = len(s)
			}
			// Only add to keys list if not already present from data.
			found := false
			for _, ki := range keys {
				if ki.Key == k {
					found = true
					break
				}
			}
			if !found {
				keys = append(keys, SecretKeyInfo{Key: k, Bytes: n})
			}
			masked[k] = "<masked>"
		}
		out["stringData"] = masked
	}

	// Sort for deterministic output.
	sortSecretKeys(keys)
	return out, keys
}

func sortSecretKeys(ks []SecretKeyInfo) {
	for i := 1; i < len(ks); i++ {
		for j := i; j > 0 && ks[j].Key < ks[j-1].Key; j-- {
			ks[j], ks[j-1] = ks[j-1], ks[j]
		}
	}
}

// withoutManagedFields returns obj with metadata.managedFields removed, matching
// the modern `kubectl get -o yaml` default (managedFields hidden since 1.21).
// The server-side-apply ownership tree is pure noise in a detail view and buries
// the real spec. Does not mutate the input (shallow-clones the touched maps).
func withoutManagedFields(obj map[string]interface{}) map[string]interface{} {
	md, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		return obj
	}
	if _, has := md["managedFields"]; !has {
		return obj
	}
	out := make(map[string]interface{}, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	nmd := make(map[string]interface{}, len(md))
	for k, v := range md {
		if k == "managedFields" {
			continue
		}
		nmd[k] = v
	}
	out["metadata"] = nmd
	return out
}

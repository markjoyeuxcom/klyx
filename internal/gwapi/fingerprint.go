package gwapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// RuleFingerprints hashes each rule of an HTTPRoute, by rule index (The
// Line parent §8.5, slice 7 note §3): its matches and its backends with
// their weights - nothing else, so a filter, timeout or session change
// keeps a rule's history. Backends are defaulted as the Gateway API does:
// core group, kind Service, the route's namespace, weight 1. Matches are
// taken as the API server stored them, which already carries its defaults.
func RuleFingerprints(u *unstructured.Unstructured) []string {
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		out = append(out, ruleFingerprint(rm, u.GetNamespace()))
	}
	return out
}

type fpBackend struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      any    `json:"port"`
	Weight    int64  `json:"weight"`
}

func ruleFingerprint(rule map[string]any, routeNS string) string {
	matches, _, _ := unstructured.NestedSlice(rule, "matches")
	refs, _, _ := unstructured.NestedSlice(rule, "backendRefs")
	backends := make([]fpBackend, 0, len(refs))
	for _, r := range refs {
		m, _ := r.(map[string]any)
		b := fpBackend{Kind: "Service", Namespace: routeNS, Weight: 1, Port: m["port"]}
		b.Group, _ = m["group"].(string)
		if k, _ := m["kind"].(string); k != "" {
			b.Kind = k
		}
		if ns, _ := m["namespace"].(string); ns != "" {
			b.Namespace = ns
		}
		b.Name, _ = m["name"].(string)
		if w, ok, _ := unstructured.NestedInt64(m, "weight"); ok {
			b.Weight = w
		}
		backends = append(backends, b)
	}
	// encoding/json sorts map keys, so a match is canonical whatever its
	// key order.
	data, _ := json.Marshal(struct {
		Matches  []any       `json:"matches"`
		Backends []fpBackend `json:"backends"`
	}{matches, backends})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

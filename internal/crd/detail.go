package crd

import (
	"encoding/base64"
	"encoding/json"
	"sort"
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
	// Scope identifies the sub-object a nested condition came from (e.g.
	// "parent envoy-gw/eg", "listener https"); "" for a standard
	// status.conditions[] entry. The real condition Type is never flattened
	// into this - "Accepted" stays "Accepted".
	Scope string
	// LastTransition and ObservedGeneration as recorded (slice 15b); zero
	// and -1 when the condition records none.
	LastTransition     time.Time
	ObservedGeneration int64
}

// conditionTimes reads a condition's lastTransitionTime and observedGeneration.
func conditionTimes(cond *Condition, m map[string]interface{}) {
	cond.ObservedGeneration = -1
	if ts, ok := m["lastTransitionTime"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			cond.LastTransition = t
		}
	}
	switch g := m["observedGeneration"].(type) {
	case int64:
		cond.ObservedGeneration = g
	case float64:
		cond.ObservedGeneration = int64(g)
	}
}

// Fact is one salient, human-readable summary value pulled from a resource's
// spec/status - the data path the resource describes, surfaced without opening
// the YAML tab. Group is an optional section header ("Routing", "Data"; "" =
// headline facts, no header). Kind is "well" for short scalars/counts (rendered
// as inset counter wells), "row" for prose/lists (labeled rows), or "meter" for
// a used/total ratio (rendered as a bar; Pct drives the fill, Value keeps the
// legible "used / hard (pct%)" text). Tone is "" | success | warning | danger |
// info | neutral.
type Fact struct {
	Group string
	Label string
	Value string
	Tone  string
	Kind  string
	// Pct is the fill percentage for Kind=="meter" (0 for every other kind).
	Pct float64
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
	Kind      string
	Namespace string
	Name      string
	// UID and Generation identify the exact object read, so a caller can
	// fence against a same-name replacement (spec 2026-10-13 §4.1).
	UID         string
	Generation  int64
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
	// Facts are per-kind salient summary values (the data path) for kinds with a
	// registered builder (see facts.go). Empty for kinds without one, which then
	// render metadata-only exactly as before.
	Facts []Fact

	// The Line slice 15b. Summary is the object's own table row at every
	// priority, read in the same response as the object; nil without a table.
	Summary []SummaryCell
	// ObservedGeneration is status.observedGeneration; -1 when the object
	// records none.
	ObservedGeneration int64
	// Deleting is metadata.deletionTimestamp; zero when not being deleted.
	Deleting   time.Time
	Finalizers []string
	// Owners are metadata.ownerReferences as recorded; the inspector links
	// those Related resolves and names the rest.
	Owners []OwnerRef
	// Managers summarise metadata.managedFields: who wrote, how and when.
	// Their field sets never leave Go.
	Managers []FieldManager
	// FieldsJSON is the sanitised object without apiVersion, kind and
	// metadata; "" with FieldsOmitted saying why.
	FieldsJSON    string
	FieldsOmitted string
}

// OwnerRef is one recorded owner reference.
type OwnerRef struct {
	APIVersion string
	Kind       string
	Name       string
	Controller bool
}

// SummaryCell is one printer column's value for the object.
type SummaryCell struct {
	Column
	Value string
}

// FieldManager is one entry of metadata.managedFields, without its fields.
type FieldManager struct {
	Manager     string
	Operation   string
	Subresource string
	Time        time.Time
}

// FieldsCap bounds the fields sent to the inspector's tree (slice 15b).
const FieldsCap = 256 << 10

// ManagersOf reads metadata.managedFields' entries, newest first.
func ManagersOf(obj map[string]interface{}) []FieldManager {
	raw, _, _ := unstructured.NestedSlice(obj, "metadata", "managedFields")
	out := make([]FieldManager, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		fm := FieldManager{}
		fm.Manager, _ = m["manager"].(string)
		fm.Operation, _ = m["operation"].(string)
		fm.Subresource, _ = m["subresource"].(string)
		if ts, ok := m["time"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				fm.Time = t
			}
		}
		out = append(out, fm)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// FieldsJSONOf renders the object's own fields for the inspector's tree: the
// SANITISED object without apiVersion, kind and metadata. Never for a Secret
// (its masked section stands), and never past FieldsCap.
func FieldsJSONOf(obj map[string]interface{}, secret bool) (string, string) {
	if secret {
		return "", "secret"
	}
	rest := make(map[string]interface{}, len(obj))
	for k, v := range obj {
		if k == "apiVersion" || k == "kind" || k == "metadata" {
			continue
		}
		rest[k] = v
	}
	if len(rest) == 0 {
		return "", ""
	}
	b, err := json.Marshal(rest)
	if err != nil {
		return "", "unreadable"
	}
	if len(b) > FieldsCap {
		return "", "too large"
	}
	return string(b), ""
}

// ObservedGenerationOf is status.observedGeneration, or -1.
func ObservedGenerationOf(obj map[string]interface{}) int64 {
	if g, ok, _ := unstructured.NestedInt64(obj, "status", "observedGeneration"); ok {
		return g
	}
	return -1
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
		conditionTimes(&cond, m)
		out = append(out, cond)
	}
	return out
}

// condPath describes where a kind stores nested per-sub-object conditions when
// they are NOT in the standard status.conditions[]. slice is the path to a list
// of sub-objects (e.g. status.parents), nameKey the field inside each carrying
// its identity (e.g. parentRef.name), and label the scope prefix ("parent").
type condPath struct {
	slice   []string
	nameKey []string
	label   string
}

// condPaths maps group/plural to a nested-condition location. Absent = standard
// status.conditions[] only, so the hundreds of ordinary kinds are unaffected.
// Gateway API routes report health per attached parent; Gateways per listener
// (in addition to a top-level status.conditions).
var condPaths = map[string]condPath{
	"gateway.networking.k8s.io/httproutes": {slice: []string{"status", "parents"}, nameKey: []string{"parentRef", "name"}, label: "parent"},
	"gateway.networking.k8s.io/grpcroutes": {slice: []string{"status", "parents"}, nameKey: []string{"parentRef", "name"}, label: "parent"},
	"gateway.networking.k8s.io/gateways":   {slice: []string{"status", "listeners"}, nameKey: []string{"name"}, label: "listener"},
	// Gateway API / Envoy Gateway policies report per-target health under
	// status.ancestors[].conditions[] (the attached Gateway/Route is the ancestor).
	"gateway.networking.k8s.io/backendtlspolicies": {slice: []string{"status", "ancestors"}, nameKey: []string{"ancestorRef", "name"}, label: "ancestor"},
	"gateway.envoyproxy.io/securitypolicies":       {slice: []string{"status", "ancestors"}, nameKey: []string{"ancestorRef", "name"}, label: "ancestor"},
	"gateway.envoyproxy.io/backendtrafficpolicies": {slice: []string{"status", "ancestors"}, nameKey: []string{"ancestorRef", "name"}, label: "ancestor"},
	"gateway.envoyproxy.io/clienttrafficpolicies":  {slice: []string{"status", "ancestors"}, nameKey: []string{"ancestorRef", "name"}, label: "ancestor"},
	"gateway.envoyproxy.io/envoyextensionpolicies": {slice: []string{"status", "ancestors"}, nameKey: []string{"ancestorRef", "name"}, label: "ancestor"},
}

// ParseConditionsDeep returns status.conditions[] PLUS any nested per-sub-object
// conditions the kind reports elsewhere (Gateway API routes/gateways), each
// tagged with a Scope so the frontend can attribute and de-collide them. Kinds
// with no nested-path entry behave exactly like ParseConditions.
func ParseConditionsDeep(group, plural string, obj map[string]interface{}) []Condition {
	out := ParseConditions(obj)
	cp, ok := condPaths[group+"/"+plural]
	if !ok {
		return out
	}
	subs, _, _ := unstructured.NestedSlice(obj, cp.slice...)
	for _, s := range subs {
		m, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := nestedStringPath(m, cp.nameKey...)
		scope := cp.label
		if name != "" {
			scope = cp.label + " " + name
		}
		conds, _, _ := unstructured.NestedSlice(m, "conditions")
		for _, c := range conds {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			cond := Condition{Scope: scope}
			cond.Type, _ = cm["type"].(string)
			cond.Status, _ = cm["status"].(string)
			cond.Reason, _ = cm["reason"].(string)
			cond.Message, _ = cm["message"].(string)
			conditionTimes(&cond, cm)
			out = append(out, cond)
		}
	}
	return out
}

// nestedStringPath reads a dotted path off a plain map (unstructured.NestedString
// needs the top-level object; this works from an arbitrary sub-map).
func nestedStringPath(m map[string]interface{}, path ...string) (string, bool) {
	cur := m
	for i, k := range path {
		v, ok := cur[k]
		if !ok {
			return "", false
		}
		if i == len(path)-1 {
			s, ok := v.(string)
			return s, ok
		}
		cur, ok = v.(map[string]interface{})
		if !ok {
			return "", false
		}
	}
	return "", false
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

	// Strip the kubectl.kubernetes.io/last-applied-configuration annotation: for a
	// Secret written with `kubectl apply` it holds a full JSON snapshot of the
	// object INCLUDING the base64 data values, so masking data/stringData below is
	// not enough to keep secret bytes out of the rendered YAML (and any egress).
	// metadata is deep-copied here so the shared informer object is never mutated.
	out = stripLastAppliedConfig(out)

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
// lastAppliedConfigAnnotation is the client-side apply snapshot annotation. For a
// Secret it carries base64 data values; it is noise everywhere else.
const lastAppliedConfigAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// stripLastAppliedConfig returns obj with the last-applied-configuration
// annotation removed, deep-copying metadata + annotations so the input (a shared
// informer object) is never mutated. A no-op (returns obj as-is) when absent.
func stripLastAppliedConfig(obj map[string]interface{}) map[string]interface{} {
	md, ok := obj["metadata"].(map[string]interface{})
	if !ok {
		return obj
	}
	ann, ok := md["annotations"].(map[string]interface{})
	if !ok {
		return obj
	}
	if _, has := ann[lastAppliedConfigAnnotation]; !has {
		return obj
	}
	nann := make(map[string]interface{}, len(ann))
	for k, v := range ann {
		if k == lastAppliedConfigAnnotation {
			continue
		}
		nann[k] = v
	}
	nmd := make(map[string]interface{}, len(md))
	for k, v := range md {
		nmd[k] = v
	}
	if len(nann) > 0 {
		nmd["annotations"] = nann
	} else {
		delete(nmd, "annotations")
	}
	out := make(map[string]interface{}, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	out["metadata"] = nmd
	return out
}

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

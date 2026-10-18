package gwapi

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// condTrue reports whether status.conditions has an entry of the given type with
// status "True".
func condTrue(obj map[string]interface{}, condType string) bool {
	conds, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t == condType {
			s, _ := m["status"].(string)
			return s == "True"
		}
	}
	return false
}

// condOf returns the named condition as observed; an absent condition has
// Status "".
func condOf(conds []interface{}, condType string) Cond {
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != condType {
			continue
		}
		out := Cond{}
		out.Status, _ = m["status"].(string)
		out.Reason, _ = m["reason"].(string)
		out.Message, _ = m["message"].(string)
		switch og := m["observedGeneration"].(type) {
		case int64:
			out.ObservedGeneration = og
		case float64:
			out.ObservedGeneration = int64(og)
		}
		if lt, ok := m["lastTransitionTime"].(string); ok {
			if ts, err := time.Parse(time.RFC3339, lt); err == nil {
				out.LastTransitionTime = ts
			}
		}
		return out
	}
	return Cond{}
}

func statusConds(obj map[string]interface{}) []interface{} {
	conds, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	return conds
}

// ParseGateway maps a Gateway unstructured to a GatewayNode (no routes).
func ParseGateway(u *unstructured.Unstructured) GatewayNode {
	g := GatewayNode{Namespace: u.GetNamespace(), Name: u.GetName()}
	g.ClassName, _, _ = unstructured.NestedString(u.Object, "spec", "gatewayClassName")
	g.Accepted = condTrue(u.Object, "Accepted")
	g.Programmed = condTrue(u.Object, "Programmed")
	g.AcceptedCond = condOf(statusConds(u.Object), "Accepted")
	g.ProgrammedCond = condOf(statusConds(u.Object), "Programmed")
	g.Generation = u.GetGeneration()
	g.Listeners = parseListeners(u.Object)
	g.Addresses = parseAddresses(u.Object)
	g.AllowedListeners, g.AttachedListenerSets = parseListenerAllowance(u.Object)
	return g
}

func parseListeners(obj map[string]interface{}) []Listener {
	ls, _, _ := unstructured.NestedSlice(obj, "spec", "listeners")
	out := make([]Listener, 0, len(ls))
	gwNS, _, _ := unstructured.NestedString(obj, "metadata", "namespace")
	status := parseListenerStatus(obj)
	for _, l := range ls {
		m, ok := l.(map[string]interface{})
		if !ok {
			continue
		}
		lis := Listener{}
		lis.Name, _ = m["name"].(string)
		lis.Protocol, _ = m["protocol"].(string)
		lis.Hostname, _ = m["hostname"].(string)
		if p, ok, _ := unstructured.NestedNumberAsFloat64(m, "port"); ok {
			lis.Port = int32(p)
		}
		lis.CertificateRefs = parseCertificateRefs(m, gwNS)
		lis.TLSMode, _, _ = unstructured.NestedString(m, "tls", "mode")
		if ar, ok := m["allowedRoutes"].(map[string]interface{}); ok {
			lis.AllowedRoutes = &AllowedRoutes{Namespaces: parseAllowance(ar), Kinds: parseRouteKinds(ar["kinds"])}
		}
		lis.Status = status[lis.Name]
		out = append(out, lis)
	}
	return out
}

// parseListenerStatus reads status.listeners by name (S7 §3). A name
// reported twice keeps its first entry, as a list map keyed by name has one.
func parseListenerStatus(obj map[string]interface{}) map[string]*ListenerStatus {
	ls, _, _ := unstructured.NestedSlice(obj, "status", "listeners")
	out := make(map[string]*ListenerStatus, len(ls))
	for _, l := range ls {
		m, ok := l.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if _, seen := out[name]; seen || name == "" {
			continue
		}
		st := &ListenerStatus{SupportedKinds: parseRouteKinds(m["supportedKinds"])}
		if n, ok, _ := unstructured.NestedNumberAsFloat64(m, "attachedRoutes"); ok {
			st.AttachedRoutes = int32(n)
		}
		conds, _ := m["conditions"].([]interface{})
		st.Conditions = typedConds(conds)
		out[name] = st
	}
	return out
}

// typedConds reads every condition in the order reported.
func typedConds(conds []interface{}) []TypedCond {
	var out []TypedCond
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		if t == "" {
			continue
		}
		out = append(out, TypedCond{Type: t, Cond: condOf(conds, t)})
	}
	return out
}

func parseRouteKinds(v interface{}) []RouteKind {
	ks, _ := v.([]interface{})
	var out []RouteKind
	for _, k := range ks {
		m, ok := k.(map[string]interface{})
		if !ok {
			continue
		}
		rk := RouteKind{}
		rk.Group, _ = m["group"].(string)
		rk.Kind, _ = m["kind"].(string)
		if rk.Kind != "" {
			out = append(out, rk)
		}
	}
	return out
}

// parseAllowance reads a namespaces allowance from the map holding it (an
// allowedRoutes or allowedListeners object).
func parseAllowance(holder map[string]interface{}) NamespaceAllowance {
	ns, ok := holder["namespaces"].(map[string]interface{})
	if !ok {
		return NamespaceAllowance{}
	}
	a := NamespaceAllowance{Set: true}
	a.From, _ = ns["from"].(string)
	if raw, ok := ns["selector"].(map[string]interface{}); ok {
		var sel metav1.LabelSelector
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &sel); err != nil {
			a.SelectorInvalid = true
		} else {
			a.Selector = &sel
		}
	}
	return a
}

// parseListenerAllowance reads a Gateway's allowedListeners and its
// attachedListenerSets count (S7 §4).
func parseListenerAllowance(obj map[string]interface{}) (NamespaceAllowance, *int32) {
	var a NamespaceAllowance
	if al, ok, _ := unstructured.NestedMap(obj, "spec", "allowedListeners"); ok {
		a = parseAllowance(al)
		if !a.Set {
			a.Set = true // allowedListeners written, its namespaces defaulted
		}
	}
	var attached *int32
	if n, ok, _ := unstructured.NestedNumberAsFloat64(obj, "status", "attachedListenerSets"); ok {
		v := int32(n)
		attached = &v
	}
	return a, attached
}

func parseCertificateRefs(listener map[string]interface{}, gwNS string) []CertificateRef {
	refs, _, _ := unstructured.NestedSlice(listener, "tls", "certificateRefs")
	out := make([]CertificateRef, 0, len(refs))
	for _, r := range refs {
		m, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		ref := CertificateRef{}
		ref.Group, _ = m["group"].(string)
		ref.Kind, _ = m["kind"].(string)
		if ref.Kind == "" {
			ref.Kind = "Secret"
		}
		ref.Namespace, _ = m["namespace"].(string)
		if ref.Namespace == "" {
			ref.Namespace = gwNS
		}
		ref.Name, _ = m["name"].(string)
		if ref.Name == "" {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func parseAddresses(obj map[string]interface{}) []GatewayAddress {
	addrs, _, _ := unstructured.NestedSlice(obj, "status", "addresses")
	out := make([]GatewayAddress, 0, len(addrs))
	for _, a := range addrs {
		m, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		value, _ := m["value"].(string)
		if value == "" {
			continue
		}
		addrType, _ := m["type"].(string)
		out = append(out, GatewayAddress{Type: addrType, Value: value})
	}
	return out
}

// ParseGatewayRef maps a Gateway to the lightweight list item.
func ParseGatewayRef(u *unstructured.Unstructured) GatewayRef {
	cls, _, _ := unstructured.NestedString(u.Object, "spec", "gatewayClassName")
	stale, gen, observed := gatewayStaleness(u)
	allowed, attached := parseListenerAllowance(u.Object)
	return GatewayRef{
		Namespace: u.GetNamespace(), Name: u.GetName(), ClassName: cls,
		Accepted: condTrue(u.Object, "Accepted"), Programmed: condTrue(u.Object, "Programmed"),
		Addresses: parseAddresses(u.Object),
		Listeners: parseListeners(u.Object),
		Stale:     stale, Generation: gen, ObservedGeneration: observed,
		AcceptedCond: condOf(statusConds(u.Object), "Accepted"), ProgrammedCond: condOf(statusConds(u.Object), "Programmed"),
		AllowedListeners: allowed, AttachedListenerSets: attached,
	}
}

// gatewayStaleness compares metadata.generation with the Programmed (falling
// back to Accepted) condition's observedGeneration - Gateway API stamps it per
// condition (metav1.Condition). Missing conditions read as not-stale rather
// than guessing.
func gatewayStaleness(u *unstructured.Unstructured) (stale bool, gen, observed int64) {
	gen = u.GetGeneration()
	if gen == 0 {
		return false, gen, 0
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	byType := map[string]int64{}
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		switch og := m["observedGeneration"].(type) {
		case int64:
			byType[t] = og
		case float64:
			byType[t] = int64(og)
		}
	}
	for _, t := range []string{"Programmed", "Accepted"} {
		if og, ok := byType[t]; ok {
			return gen > og, gen, og
		}
	}
	return false, gen, 0
}

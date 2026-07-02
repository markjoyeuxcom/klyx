package gwapi

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// RouteForGateway parses an HTTPRoute and, if it attaches to the given Gateway
// (by parentRef, namespace defaulting to the route's), returns the RouteNode with
// Accepted/ResolvedRefs scoped to THAT parent. ok=false when it does not attach.
// Services/Pods are filled later by the fleet layer.
//
// Known limitation: a route that attaches to the same Gateway via multiple
// parentRefs with different sectionNames collapses to a single lane scoped to
// the first matching parentRef. Per-listener lanes are out of scope for M5-a.
func RouteForGateway(u *unstructured.Unstructured, gwNamespace, gwName string) (RouteNode, bool) {
	routeNS := u.GetNamespace()

	parents, _, _ := unstructured.NestedSlice(u.Object, "spec", "parentRefs")
	attached := false
	var sectionName string
	for _, p := range parents {
		m, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		ns, _ := m["namespace"].(string)
		if ns == "" {
			ns = routeNS
		}
		// group/kind default to gateway.networking.k8s.io / Gateway.
		if g, _ := m["group"].(string); g != "" && g != "gateway.networking.k8s.io" {
			continue
		}
		if kind, _ := m["kind"].(string); kind != "" && kind != "Gateway" {
			continue
		}
		if name == gwName && ns == gwNamespace {
			attached = true
			sectionName, _ = m["sectionName"].(string)
			break
		}
	}
	if !attached {
		return RouteNode{}, false
	}

	rn := RouteNode{Namespace: routeNS, Name: u.GetName(), ParentSectionName: sectionName}
	rn.Hostnames, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "hostnames")

	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
	for _, r := range rules {
		rm, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		matches, _, _ := unstructured.NestedSlice(rm, "matches")
		for _, mt := range matches {
			mm, ok := mt.(map[string]interface{})
			if !ok {
				continue
			}
			match := Match{}
			match.Method, _ = mm["method"].(string)
			if pth, ok := mm["path"].(map[string]interface{}); ok {
				match.PathType, _ = pth["type"].(string)
				match.PathValue, _ = pth["value"].(string)
			}
			rn.Matches = append(rn.Matches, match)
		}
		brefs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
		for _, b := range brefs {
			bm, ok := b.(map[string]interface{})
			if !ok {
				continue
			}
			be := Backend{}
			be.Name, _ = bm["name"].(string)
			be.Kind, _ = bm["kind"].(string)
			if be.Kind == "" {
				be.Kind = "Service"
			}
			be.Namespace, _ = bm["namespace"].(string)
			if be.Namespace == "" {
				be.Namespace = routeNS
			}
			if p, ok, _ := unstructured.NestedNumberAsFloat64(bm, "port"); ok {
				be.Port = int32(p)
			}
			if w, ok, _ := unstructured.NestedNumberAsFloat64(bm, "weight"); ok {
				be.Weight = int32(w)
			}
			rn.Backends = append(rn.Backends, be)
		}
	}

	// Scope status to this Gateway's parent entry (and listener, if pinned).
	rn.Accepted, rn.ResolvedRefs = parentStatus(u.Object, gwNamespace, gwName, routeNS, sectionName)
	return rn, true
}

// parentStatus reads status.parents[] and returns Accepted/ResolvedRefs for the
// entry whose parentRef matches the given Gateway (namespace defaulting to the
// route's). When wantSection is non-empty, the status entry must be pinned to
// the same listener via sectionName; an empty wantSection matches the first
// entry regardless of its sectionName. Returns false,false when no matching
// parent status exists yet.
func parentStatus(obj map[string]interface{}, gwNamespace, gwName, routeNS, wantSection string) (accepted, resolved bool) {
	// Returns the first status entry matching the parentRef; controllerName is
	// not disambiguated (single Gateway controller assumed).
	sps, _, _ := unstructured.NestedSlice(obj, "status", "parents")
	for _, sp := range sps {
		spm, ok := sp.(map[string]interface{})
		if !ok {
			continue
		}
		pr, ok := spm["parentRef"].(map[string]interface{})
		if !ok {
			continue
		}
		if g, _ := pr["group"].(string); g != "" && g != "gateway.networking.k8s.io" {
			continue
		}
		if kind, _ := pr["kind"].(string); kind != "" && kind != "Gateway" {
			continue
		}
		name, _ := pr["name"].(string)
		ns, _ := pr["namespace"].(string)
		if ns == "" {
			ns = routeNS
		}
		if name != gwName || ns != gwNamespace {
			continue
		}
		if wantSection != "" {
			gotSection, _ := pr["sectionName"].(string)
			if gotSection != wantSection {
				continue
			}
		}
		conds, _, _ := unstructured.NestedSlice(spm, "conditions")
		for _, c := range conds {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			t, _ := cm["type"].(string)
			s, _ := cm["status"].(string)
			switch t {
			case "Accepted":
				accepted = s == "True"
			case "ResolvedRefs":
				resolved = s == "True"
			}
		}
		return accepted, resolved
	}
	return false, false
}

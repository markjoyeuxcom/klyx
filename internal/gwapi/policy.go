package gwapi

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// PolicyTargets reads spec.targetRefs[] plus the legacy singular spec.targetRef.
// Namespace holds the raw targetRef.namespace (empty when omitted).
func PolicyTargets(u *unstructured.Unstructured) []TargetRef {
	var out []TargetRef
	add := func(m map[string]interface{}) {
		t := TargetRef{}
		t.Group, _ = m["group"].(string)
		t.Kind, _ = m["kind"].(string)
		t.Name, _ = m["name"].(string)
		t.Namespace, _ = m["namespace"].(string)
		t.SectionName, _ = m["sectionName"].(string)
		out = append(out, t)
	}
	refs, _, _ := unstructured.NestedSlice(u.Object, "spec", "targetRefs")
	for _, r := range refs {
		if m, ok := r.(map[string]interface{}); ok {
			add(m)
		}
	}
	if single, ok, _ := unstructured.NestedMap(u.Object, "spec", "targetRef"); ok {
		add(single)
	}
	return out
}

// BuildPolicyRefs fans a policy into one PolicyRef per targetRef, sharing the
// decoded summary/details. targetRef namespace defaults to the policy's namespace
// when omitted.
func BuildPolicyRefs(u *unstructured.Unstructured, kind string) []PolicyRef {
	dec := Decode(kind, u)
	polNS := u.GetNamespace()
	var out []PolicyRef
	for _, t := range PolicyTargets(u) {
		tns := t.Namespace
		if tns == "" {
			tns = polNS
		}
		details := append([]PolicyDetail(nil), dec.Details...) // own copy per fanned-out ref
		out = append(out, PolicyRef{
			Kind: kind, Namespace: polNS, Name: u.GetName(),
			TargetKind: t.Kind, TargetNamespace: tns, TargetName: t.Name, TargetSectionName: t.SectionName,
			Summary: dec.Summary, Details: details,
		})
	}
	return out
}

// AttachPolicies places each PolicyRef on the node its resolved target names.
// A ref whose target matches nothing in this (single-Gateway) topology is dropped.
// Matching is by TargetKind + namespace + name; targetRef.group is intentionally
// not matched - the in-scope kinds (Gateway, the five route kinds, Service) are
// unambiguous in this environment, so group adds no disambiguation.
func AttachPolicies(topo *Topology, refs []PolicyRef) {
	for _, p := range refs {
		switch p.TargetKind {
		case "Gateway":
			if p.TargetNamespace == topo.Gateway.Namespace && p.TargetName == topo.Gateway.Name {
				topo.Gateway.Policies = append(topo.Gateway.Policies, p)
			}
		case KindHTTPRoute, KindGRPCRoute, KindTLSRoute, KindTCPRoute, KindUDPRoute:
			// a route target names a lane of its own kind only: a TLSRoute of
			// the same name as an HTTPRoute is another route (S7b §5)
			for i := range topo.Routes {
				k := topo.Routes[i].Kind
				if k == "" {
					k = KindHTTPRoute
				}
				if k != p.TargetKind {
					continue
				}
				if topo.Routes[i].Namespace == p.TargetNamespace && topo.Routes[i].Name == p.TargetName {
					topo.Routes[i].Policies = append(topo.Routes[i].Policies, p)
				}
			}
		case "Service":
			for i := range topo.Routes {
				for j := range topo.Routes[i].Services {
					s := &topo.Routes[i].Services[j]
					if s.Namespace == p.TargetNamespace && s.Name == p.TargetName {
						s.Policies = append(s.Policies, p)
					}
				}
			}
		}
	}
}

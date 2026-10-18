package gwapi

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func polObj(kind, ns, name string, targetRefs []interface{}, singleTargetRef map[string]interface{}, spec map[string]interface{}) *unstructured.Unstructured {
	s := map[string]interface{}{}
	for k, v := range spec {
		s[k] = v
	}
	if targetRefs != nil {
		s["targetRefs"] = targetRefs
	}
	if singleTargetRef != nil {
		s["targetRef"] = singleTargetRef
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.envoyproxy.io/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]interface{}{"namespace": ns, "name": name},
		"spec":       s,
	}}
}

func tref(kind, name, ns, section string) map[string]interface{} {
	m := map[string]interface{}{"kind": kind, "name": name, "group": "gateway.networking.k8s.io"}
	if ns != "" {
		m["namespace"] = ns
	}
	if section != "" {
		m["sectionName"] = section
	}
	return m
}

func TestPolicyTargetsPluralAndSingular(t *testing.T) {
	u := polObj("BackendTrafficPolicy", "apps", "btp", []interface{}{
		tref("HTTPRoute", "r1", "", "https"),
		tref("HTTPRoute", "r2", "other", ""),
	}, nil, nil)
	ts := PolicyTargets(u)
	if len(ts) != 2 || ts[0].Kind != "HTTPRoute" || ts[0].Name != "r1" || ts[0].SectionName != "https" {
		t.Fatalf("targets[0]: %+v", ts)
	}
	if ts[1].Namespace != "other" {
		t.Fatalf("targets[1] ns: %+v", ts[1])
	}

	// Legacy singular targetRef.
	u2 := polObj("ClientTrafficPolicy", "infra", "ctp", nil, tref("Gateway", "eg", "", ""), nil)
	ts2 := PolicyTargets(u2)
	if len(ts2) != 1 || ts2[0].Kind != "Gateway" || ts2[0].Name != "eg" {
		t.Fatalf("singular: %+v", ts2)
	}
}

func TestBuildPolicyRefsFanOutAndNamespaceDefault(t *testing.T) {
	// One BTP targeting two routes; first target omits namespace (defaults to the
	// policy's "apps"), second sets it explicitly.
	u := polObj("BackendTrafficPolicy", "apps", "btp", []interface{}{
		tref("HTTPRoute", "r1", "", ""),
		tref("HTTPRoute", "r2", "other", ""),
	}, nil, map[string]interface{}{"retry": map[string]interface{}{"numRetries": int64(2)}})

	refs := BuildPolicyRefs(u, "BackendTrafficPolicy")
	if len(refs) != 2 {
		t.Fatalf("fan-out: %+v", refs)
	}
	if refs[0].TargetNamespace != "apps" {
		t.Fatalf("target ns default: %+v", refs[0])
	}
	if refs[1].TargetNamespace != "other" {
		t.Fatalf("explicit target ns: %+v", refs[1])
	}
	// Decoded summary/details shared across fan-out.
	if refs[0].Summary != "retries" || refs[0].Name != "btp" || refs[0].Namespace != "apps" {
		t.Fatalf("ref[0]: %+v", refs[0])
	}
}

func TestAttachPoliciesPlacesByTarget(t *testing.T) {
	topo := &Topology{
		Gateway: GatewayNode{Namespace: "infra", Name: "eg"},
		Routes: []RouteNode{{
			Namespace: "apps", Name: "share",
			Services: []ServiceNode{{Namespace: "apps", Name: "share-api"}},
		}},
	}
	refs := []PolicyRef{
		{Kind: "ClientTrafficPolicy", Name: "ctp", TargetKind: "Gateway", TargetNamespace: "infra", TargetName: "eg"},
		{Kind: "BackendTrafficPolicy", Name: "btp", TargetKind: "HTTPRoute", TargetNamespace: "apps", TargetName: "share"},
		{Kind: "BackendTLSPolicy", Name: "btls", TargetKind: "Service", TargetNamespace: "apps", TargetName: "share-api"},
		{Kind: "SecurityPolicy", Name: "ghost", TargetKind: "HTTPRoute", TargetNamespace: "apps", TargetName: "nope"},
	}
	AttachPolicies(topo, refs)

	if len(topo.Gateway.Policies) != 1 || topo.Gateway.Policies[0].Name != "ctp" {
		t.Fatalf("gateway: %+v", topo.Gateway.Policies)
	}
	if len(topo.Routes[0].Policies) != 1 || topo.Routes[0].Policies[0].Name != "btp" {
		t.Fatalf("route: %+v", topo.Routes[0].Policies)
	}
	if len(topo.Routes[0].Services[0].Policies) != 1 || topo.Routes[0].Services[0].Policies[0].Name != "btls" {
		t.Fatalf("service: %+v", topo.Routes[0].Services[0].Policies)
	}
	// The non-matching SecurityPolicy is dropped silently (belongs elsewhere).
}

// S7b: a route target names the lane of its own kind; an HTTPRoute and a
// TCPRoute of one name are two routes.
func TestAttachPoliciesMatchesTheRouteKind(t *testing.T) {
	topo := &Topology{Routes: []RouteNode{
		{Kind: KindHTTPRoute, Namespace: "apps", Name: "db"},
		{Kind: KindTCPRoute, Namespace: "apps", Name: "db"},
		{Namespace: "apps", Name: "legacy"}, // a lane from before S7b is an HTTPRoute
	}}
	AttachPolicies(topo, []PolicyRef{
		{Kind: "BackendTrafficPolicy", Name: "tcp-btp", TargetKind: KindTCPRoute, TargetNamespace: "apps", TargetName: "db"},
		{Kind: "SecurityPolicy", Name: "http-sp", TargetKind: KindHTTPRoute, TargetNamespace: "apps", TargetName: "db"},
		{Kind: "SecurityPolicy", Name: "legacy-sp", TargetKind: KindHTTPRoute, TargetNamespace: "apps", TargetName: "legacy"},
	})
	if p := topo.Routes[0].Policies; len(p) != 1 || p[0].Name != "http-sp" {
		t.Fatalf("HTTPRoute lane: %+v", p)
	}
	if p := topo.Routes[1].Policies; len(p) != 1 || p[0].Name != "tcp-btp" {
		t.Fatalf("TCPRoute lane: %+v", p)
	}
	if p := topo.Routes[2].Policies; len(p) != 1 || p[0].Name != "legacy-sp" {
		t.Fatalf("a kindless lane reads as HTTPRoute: %+v", p)
	}
}

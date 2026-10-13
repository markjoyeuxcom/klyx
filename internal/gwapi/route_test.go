package gwapi

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

func hrObj(name, ns string, parentRefs, rules, statusParents, hostnames []interface{}) *unstructured.Unstructured {
	spec := map[string]interface{}{"parentRefs": parentRefs, "rules": rules}
	if hostnames != nil {
		spec["hostnames"] = hostnames
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]interface{}{"name": name, "namespace": ns},
		"spec":       spec,
		"status":     map[string]interface{}{"parents": statusParents},
	}}
}

func parentRef(name, ns, section string) map[string]interface{} {
	m := map[string]interface{}{"name": name}
	if ns != "" {
		m["namespace"] = ns
	}
	if section != "" {
		m["sectionName"] = section
	}
	return m
}

// parentRefGroup builds a parentRef with an explicit group (and optional kind).
func parentRefGroup(name, ns, group, kind string) map[string]interface{} {
	m := map[string]interface{}{"name": name}
	if ns != "" {
		m["namespace"] = ns
	}
	if group != "" {
		m["group"] = group
	}
	if kind != "" {
		m["kind"] = kind
	}
	return m
}

func statusParent(name, ns string, accepted, resolved string) map[string]interface{} {
	pr := map[string]interface{}{"name": name}
	if ns != "" {
		pr["namespace"] = ns
	}
	return map[string]interface{}{
		"parentRef": pr,
		"conditions": []interface{}{
			map[string]interface{}{"type": "Accepted", "status": accepted},
			map[string]interface{}{"type": "ResolvedRefs", "status": resolved},
		},
	}
}

// statusParentSection builds a status.parents[] entry whose parentRef is pinned
// to a specific listener via sectionName.
func statusParentSection(name, ns, section string, accepted, resolved string) map[string]interface{} {
	pr := map[string]interface{}{"name": name}
	if ns != "" {
		pr["namespace"] = ns
	}
	if section != "" {
		pr["sectionName"] = section
	}
	return map[string]interface{}{
		"parentRef": pr,
		"conditions": []interface{}{
			map[string]interface{}{"type": "Accepted", "status": accepted},
			map[string]interface{}{"type": "ResolvedRefs", "status": resolved},
		},
	}
}

func rule(path, method, backend string, port, weight int64) map[string]interface{} {
	return map[string]interface{}{
		"matches": []interface{}{map[string]interface{}{
			"path":   map[string]interface{}{"type": "PathPrefix", "value": path},
			"method": method,
		}},
		"backendRefs": []interface{}{map[string]interface{}{
			"name": backend, "port": port, "weight": weight,
		}},
	}
}

func TestRouteForGatewayAttachesAndScopesStatus(t *testing.T) {
	// Route in ns "apps" attaches to Gateway "eg" in ns "infra" (cross-namespace),
	// accepted by THIS gateway but rejected by another parent.
	u := hrObj("share", "apps",
		[]interface{}{
			parentRef("eg", "infra", ""),
			parentRef("other-gw", "infra", ""),
		},
		[]interface{}{rule("/api/share", "GET", "share-api", 8080, 100)},
		[]interface{}{
			statusParent("eg", "infra", "True", "True"),
			statusParent("other-gw", "infra", "False", "True"), // rejected by the other
		},
		[]interface{}{"share.example.com"})

	rn, ok := RouteForGateway(u, "infra", "eg")
	if !ok {
		t.Fatal("route should attach to infra/eg")
	}
	if !rn.Accepted || !rn.ResolvedRefs {
		t.Fatalf("status must be scoped to infra/eg (accepted): %+v", rn)
	}
	if rn.Name != "share" || rn.Namespace != "apps" {
		t.Fatalf("ids: %+v", rn)
	}
	if len(rn.Hostnames) != 1 || rn.Hostnames[0] != "share.example.com" {
		t.Fatalf("hostnames: %+v", rn.Hostnames)
	}
	if len(rn.Matches) != 1 || rn.Matches[0].PathValue != "/api/share" || rn.Matches[0].Method != "GET" {
		t.Fatalf("matches: %+v", rn.Matches)
	}
	if len(rn.Backends) != 1 || rn.Backends[0].Name != "share-api" || rn.Backends[0].Port != 8080 || rn.Backends[0].Weight != 100 {
		t.Fatalf("backends: %+v", rn.Backends)
	}
	// Backend namespace defaults to the route's namespace when omitted.
	if rn.Backends[0].Namespace != "apps" {
		t.Fatalf("backend ns default: %+v", rn.Backends[0])
	}
}

func TestRouteForGatewayParentRefNamespaceDefaultsToRoute(t *testing.T) {
	// parentRef without a namespace defaults to the route's namespace.
	u := hrObj("r", "infra",
		[]interface{}{parentRef("eg", "", "")}, // no namespace
		[]interface{}{rule("/", "", "svc", 80, 0)},
		[]interface{}{statusParent("eg", "", "True", "True")},
		nil)
	if _, ok := RouteForGateway(u, "infra", "eg"); !ok {
		t.Fatal("parentRef ns should default to route ns (infra)")
	}
	if _, ok := RouteForGateway(u, "other", "eg"); ok {
		t.Fatal("must not attach to a gateway in a different namespace")
	}
}

func TestRouteForGatewayNotAttached(t *testing.T) {
	u := hrObj("r", "apps",
		[]interface{}{parentRef("some-other", "apps", "")},
		[]interface{}{rule("/", "", "svc", 80, 0)},
		nil, nil)
	if _, ok := RouteForGateway(u, "apps", "eg"); ok {
		t.Fatal("should not attach")
	}
}

func TestRouteForGatewaySectionNameScopesStatus(t *testing.T) {
	// Route pins sectionName "https"; status has TWO entries for infra/eg:
	// http (Accepted=False) and https (Accepted=True). Must pick the https one.
	u := hrObj("pinned", "apps",
		[]interface{}{parentRef("eg", "infra", "https")},
		[]interface{}{rule("/", "", "svc", 443, 0)},
		[]interface{}{
			statusParentSection("eg", "infra", "http", "False", "True"),
			statusParentSection("eg", "infra", "https", "True", "True"),
		},
		nil)
	rn, ok := RouteForGateway(u, "infra", "eg")
	if !ok {
		t.Fatal("route should attach to infra/eg")
	}
	if !rn.Accepted {
		t.Fatalf("status must be scoped to the https listener (accepted): %+v", rn)
	}
	if rn.ParentSectionName != "https" {
		t.Fatalf("parent section should be projected: %+v", rn)
	}
}

func TestRouteForGatewayFloat64Ports(t *testing.T) {
	// json.Unmarshal into interface{} yields float64 for numeric fields.
	u := hrObj("f", "apps",
		[]interface{}{parentRef("eg", "infra", "")},
		[]interface{}{map[string]interface{}{
			"backendRefs": []interface{}{map[string]interface{}{
				"name": "svc", "port": float64(8080), "weight": float64(50),
			}},
		}},
		[]interface{}{statusParent("eg", "infra", "True", "True")},
		nil)
	rn, ok := RouteForGateway(u, "infra", "eg")
	if !ok {
		t.Fatal("route should attach to infra/eg")
	}
	if len(rn.Backends) != 1 || rn.Backends[0].Port != 8080 || rn.Backends[0].Weight != 50 || !rn.Backends[0].WeightSet {
		t.Fatalf("float64 port/weight must decode: %+v", rn.Backends)
	}
}

// An unset weight is the Gateway API default of 1 (the backend carries
// traffic); an explicit 0 carries none.
func TestRouteForGatewayWeightSet(t *testing.T) {
	u := hrObj("w", "apps",
		[]interface{}{parentRef("eg", "infra", "")},
		[]interface{}{map[string]interface{}{
			"backendRefs": []interface{}{
				map[string]interface{}{"name": "unset", "port": float64(80)},
				map[string]interface{}{"name": "zero", "port": float64(80), "weight": float64(0)},
			},
		}},
		[]interface{}{statusParent("eg", "infra", "True", "True")},
		nil)
	rn, ok := RouteForGateway(u, "infra", "eg")
	if !ok || len(rn.Backends) != 2 {
		t.Fatalf("route should attach with two backends: %+v", rn)
	}
	if rn.Backends[0].WeightSet || !rn.Backends[0].CarriesTraffic() {
		t.Fatalf("an unset weight carries traffic: %+v", rn.Backends[0])
	}
	if !rn.Backends[1].WeightSet || rn.Backends[1].CarriesTraffic() {
		t.Fatalf("an explicit weight 0 carries none: %+v", rn.Backends[1])
	}
}

func TestRouteForGatewayWrongGroupParentDoesNotMatch(t *testing.T) {
	u := hrObj("r", "infra",
		[]interface{}{parentRefGroup("eg", "infra", "example.com", "Gateway")},
		[]interface{}{rule("/", "", "svc", 80, 0)},
		nil, nil)
	if _, ok := RouteForGateway(u, "infra", "eg"); ok {
		t.Fatal("parentRef with non-default group must not attach")
	}
}

// Parser to rule (Codex review of afef050): each backend keeps the rule that
// lists it, so a /dead rule whose only backend has weight 0 is a fault even
// beside a healthy /healthy rule.
func TestRouteForGatewayKeepsRuleBoundaries(t *testing.T) {
	u := hrObj("split", "apps",
		[]interface{}{parentRef("eg", "infra", "")},
		[]interface{}{
			map[string]interface{}{"backendRefs": []interface{}{map[string]interface{}{"name": "dead", "port": float64(80), "weight": float64(0)}}},
			map[string]interface{}{"backendRefs": []interface{}{map[string]interface{}{"name": "healthy", "port": float64(80), "weight": float64(1)}}},
		},
		[]interface{}{statusParent("eg", "infra", "True", "True")},
		nil)
	rn, ok := RouteForGateway(u, "infra", "eg")
	if !ok || len(rn.Backends) != 2 || rn.Backends[0].Rule != 0 || rn.Backends[1].Rule != 1 {
		t.Fatalf("each backend keeps its rule: %+v", rn.Backends)
	}
	rn.Backends[0].State, rn.Backends[0].Endpoints = BackendResolved, &EndpointCount{}
	rn.Backends[1].State, rn.Backends[1].Endpoints = BackendResolved, &EndpointCount{Ready: 2, Total: 2}
	got := RouteRule(rn, nil)
	if got.Role != health.Fault || got.Label != "rule 1: every backend has weight 0" || !got.Included {
		t.Fatalf("a dead rule is never hidden by a healthy one: %+v", got)
	}
}

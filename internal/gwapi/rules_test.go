package gwapi

import (
	"strings"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/moomora/klyx/internal/health"
)

// Codex review 2026-10-13 #7: the EndpointSlice API says a nil Ready is an
// unknown state consumers should treat as ready.
func TestCountEndpointsNilReadyIsReady(t *testing.T) {
	f := false
	got := CountEndpoints([]discoveryv1.EndpointSlice{{Endpoints: []discoveryv1.Endpoint{
		{Addresses: []string{"10.0.0.1"}},
		{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &f}},
	}}})
	if got.Ready != 1 || got.Total != 2 {
		t.Fatalf("nil readiness counts as ready, false does not: %+v", got)
	}
}

func acceptedRoute(bs ...Backend) RouteNode {
	return RouteNode{ParentFound: true, AcceptedCond: Cond{Status: "True"}, ResolvedRefsCond: Cond{Status: "True"}, Backends: bs}
}

func svc(name string, ready, total int) Backend {
	return Backend{Kind: "Service", Name: name, Namespace: "shop", State: BackendResolved, Endpoints: &EndpointCount{Ready: ready, Total: total}}
}

func inRule(b Backend, rule int) Backend {
	b.Rule = rule
	return b
}

func weighted(b Backend, w int32) Backend {
	b.Weight, b.WeightSet = w, true
	return b
}

// Codex review 2026-10-13 #6: every backend that carries traffic is
// diagnosed, not only the first.
func TestRouteRuleDiagnosesEveryCarryingBackend(t *testing.T) {
	cases := []struct {
		name  string
		rn    RouteNode
		role  health.Kind
		label string
	}{
		{"second of two equal backends has no endpoints", acceptedRoute(svc("a", 1, 1), svc("b", 0, 0)), health.Degraded, "b: no endpoints"},
		{"the same with explicit equal weights", acceptedRoute(weighted(svc("a", 1, 1), 1), weighted(svc("b", 0, 0), 1)), health.Degraded, "b: no endpoints"},
		{"every carrying backend faults", acceptedRoute(svc("a", 0, 0), svc("b", 0, 2)), health.Fault, "a: no endpoints"},
		{"a weight-0 backend is not diagnosed", acceptedRoute(svc("a", 2, 2), weighted(svc("canary", 0, 0), 0)), health.OK, "resolved"},
		{"the only carrying backend faults beside a weight-0 one", acceptedRoute(svc("a", 0, 0), weighted(svc("canary", 2, 2), 0)), health.Fault, "no endpoints"},
		{"every backend has weight 0", acceptedRoute(weighted(svc("a", 2, 2), 0), weighted(svc("b", 2, 2), 0)), health.Fault, "every backend has weight 0"},
		{"a partly ready second backend", acceptedRoute(svc("a", 2, 2), svc("b", 1, 3)), health.Degraded, "b: 1/3 endpoints ready"},
		{"all carrying backends healthy", acceptedRoute(svc("a", 2, 2), svc("b", 3, 3)), health.OK, "resolved"},
		// Weights split traffic within one rule (Codex review of afef050).
		{"a zero-weight-only rule beside a healthy rule", acceptedRoute(weighted(svc("dead", 0, 0), 0), inRule(svc("healthy", 2, 2), 1)), health.Fault, "rule 1: every backend has weight 0"},
		{"the same with the dead rule second", acceptedRoute(svc("healthy", 2, 2), inRule(weighted(svc("dead", 0, 0), 0), 1)), health.Fault, "rule 2: every backend has weight 0"},
		{"a sole backend failing in its own rule is a fault", acceptedRoute(svc("healthy", 2, 2), inRule(svc("empty", 0, 0), 1)), health.Fault, "empty: no endpoints"},
		{"a backend failing beside a healthy one in its rule is degraded", acceptedRoute(svc("other", 2, 2), inRule(svc("b1", 2, 2), 1), inRule(svc("b2", 0, 0), 1)), health.Degraded, "b2: no endpoints"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RouteRule(c.rn, nil)
			if got.Role != c.role || got.Label != c.label {
				t.Fatalf("want %s %q, got %s %q", c.role, c.label, got.Role, got.Label)
			}
		})
	}
}

// The Line slice 8d: each rule judged on its own, each backend with its own
// verdict and configured weight, and a rule with no backendRefs still a rule.
func TestRuleVerdicts(t *testing.T) {
	rn := acceptedRoute(
		inRule(weighted(svc("web", 0, 2), 90), 0), inRule(weighted(svc("web-canary", 1, 1), 10), 0),
		inRule(weighted(svc("api", 1, 1), 0), 1), inRule(weighted(svc("api-old", 1, 1), 0), 1),
		inRule(svc("docs", 2, 2), 3), inRule(weighted(svc("docs-off", 0, 1), 0), 3),
	)
	rn.RuleCount = 4 // rule 2 is a direct response: no backendRefs
	got := RuleVerdicts(rn)
	if len(got) != 4 {
		t.Fatalf("%d rules", len(got))
	}
	want := []struct {
		role  health.Kind
		label string
		bs    []string
	}{
		{health.Degraded, "web: no ready endpoints", []string{"degraded no ready endpoints", "ok resolved"}},
		{health.Fault, "every backend has weight 0", []string{"ok no traffic (weight 0)", "ok no traffic (weight 0)"}},
		{health.Unknown, "no backendRefs", nil},
		{health.OK, "resolved", []string{"ok resolved", "ok no traffic (weight 0)"}},
	}
	for i, w := range want {
		r := got[i]
		if r.Index != i || r.Eval.Role != w.role || r.Eval.Label != w.label {
			t.Errorf("rule %d: %+v, want %v %q", i, r.Eval, w.role, w.label)
		}
		var bs []string
		for _, b := range r.Backends {
			bs = append(bs, b.Eval.Role.String()+" "+b.Eval.Label)
		}
		if strings.Join(bs, ",") != strings.Join(w.bs, ",") {
			t.Errorf("rule %d backends: %q, want %q", i, bs, w.bs)
		}
	}
	if !got[0].Eval.Included || !got[1].Eval.Included || got[3].Eval.Included {
		t.Fatal("included follows fault and degraded")
	}
	// The route's verdict reads the same judgments.
	if e := RouteRule(rn, nil); e.Role != health.Fault || e.Label != "rule 2: every backend has weight 0" {
		t.Fatalf("route: %+v", e)
	}
}

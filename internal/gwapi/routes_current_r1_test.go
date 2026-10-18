package gwapi

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

// Codex's S7b re-review (revision 1), its probes adopted: a ListenerSet's
// own current refusal is a Fault whatever its Gateway's read; an omitted
// RouteGroupKind group is the Gateway API's, an explicit "" the core group;
// current retained objects in a capped read still prove their facts.

func TestFreshListenerSetRefusalSurvivesAGatewayGap(t *testing.T) {
	for _, state := range []string{"stale", "not retained"} {
		t.Run(state, func(t *testing.T) {
			ref := refTo("extra", "kind", "ListenerSet")
			ix := indexWith(gwRef("eg", "https"))
			ix.ListenerSets = []ListenerSet{{Namespace: "infra", Name: "extra", Parent: ParentRef{Group: Group, Kind: "Gateway", Namespace: "infra", Name: "eg"}, Generation: 1, AcceptedCond: Cond{Status: "False", Reason: "NotAllowed", ObservedGeneration: 1}}}
			ix.GatewaysRead = ParentRead{}
			if state == "not retained" {
				ix.Gateways = nil
				ix.GatewaysRead = ParentRead{Current: true}
			}
			r := ParseRoute(KindHTTPRoute, routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{ref}, "rules": backendSpec("web", 80)["rules"]}, parentEntry(ref, egCtl, "True", "True", 1)))
			okBackends(&r)
			got := r.Attachments(ix)[0]
			if got.Eval.Role != health.Fault || got.Eval.Label != "parent ListenerSet not accepted" {
				t.Fatalf("fresh ListenerSet refusal was hidden by Gateway %s: %+v (%s)", state, got.Eval, got.Parent.ControllerWhy)
			}
		})
	}
}

func TestExplicitCoreGroupIsNotTheDefault(t *testing.T) {
	for _, source := range []string{"supportedKinds", "declaredKinds"} {
		for _, tc := range []struct {
			name    string
			group   any
			present bool
			want    string
		}{
			{"omitted", nil, false, AllowedYes},
			{"Gateway API", Group, true, AllowedYes},
			{"explicit core", "", true, AllowedNo},
			{"foreign", "example.test", true, AllowedNo},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				rk := map[string]any{"kind": KindHTTPRoute}
				if tc.present {
					rk["group"] = tc.group
				}
				lis := map[string]any{"name": "http", "protocol": "HTTP", "port": int64(80)}
				status := map[string]any{}
				if source == "supportedKinds" {
					status["listeners"] = []any{map[string]any{"name": "http", "supportedKinds": []any{rk}}}
				} else {
					lis["allowedRoutes"] = map[string]any{"kinds": []any{rk}}
				}
				u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": Group + "/v1", "kind": "Gateway", "metadata": map[string]any{"name": "eg", "namespace": "shop"}, "spec": map[string]any{"listeners": []any{lis}}, "status": status}}
				gw := ParseGatewayRef(u)
				ae := AttachmentEval{Ref: RouteParentRef{Group: Group, Kind: "Gateway", Namespace: "shop", Name: "eg"}, Parent: AttachmentParent{State: ParentFound, Current: true, Listeners: gw.Listeners}}
				got := RouteAllowedBy(KindHTTPRoute, "shop", ae, NamespaceLabels{})
				if got.Allowed != tc.want {
					t.Fatalf("group present=%v raw=%q: wanted %s, got %+v", tc.present, tc.group, tc.want, got)
				}
			})
		}
	}
}

func TestRetainedCurrentObjectsStillProveTheirFacts(t *testing.T) {
	ref := refTo("eg", "sectionName", "http")
	g := gwRef("eg", "http")
	g.Namespace = "shop"
	g.Listeners[0].Protocol = "HTTP"
	ix := indexWith(g)
	ix.GatewaysRead = ParentRead{Current: true}
	ix.ClassesRead = ParentRead{Current: true}
	ref["namespace"] = "shop"
	r := ParseRoute(KindHTTPRoute, routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{ref}, "rules": backendSpec("web", 80)["rules"]}, parentEntry(ref, egCtl, "True", "True", 1)))
	okBackends(&r)
	ae := r.Attachments(ix)[0]
	if ae.Eval.Role != health.OK || ae.Parent.Controller != egCtl {
		t.Fatalf("a current retained parent/class under a cap lost its proof: %+v", ae)
	}
	if got := RouteAllowedBy(KindHTTPRoute, "shop", ae, NamespaceLabels{}); got.Allowed != AllowedYes {
		t.Fatalf("retained current listener: %+v", got)
	}
	gr := GrantsRead{Read: ParentRead{Current: true}, Grants: []ReferenceGrant{{Namespace: "other", Name: "allow", From: []GrantFrom{{Group: Group, Kind: KindHTTPRoute, Namespace: "shop"}}, To: []GrantTo{{Group: "", Kind: "Service", Name: "web"}}}}}
	if got := GrantFor(gr, GrantFrom{Group: Group, Kind: KindHTTPRoute, Namespace: "shop"}, "other", GrantTo{Group: "", Kind: "Service", Name: "web"}); got.Allowed != AllowedYes {
		t.Fatalf("current retained grant: %+v", got)
	}
}

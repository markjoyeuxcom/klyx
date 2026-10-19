package gwapi

import (
	"strings"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/moomora/klyx/internal/health"
)

// Codex's S7b implementation review, findings 1 and 5 (its probes, adopted):
// a dependency retained from a read that is not current establishes no
// controller, status or grant; a supported kind is its group and kind.

func TestRetainedStaleParentChainEstablishesNoController(t *testing.T) {
	for _, dep := range []string{"Gateway", "GatewayClass", "ListenerSet"} {
		t.Run(dep, func(t *testing.T) {
			ref := refTo("eg")
			ix := indexWith(gwRef("eg", "https"))
			if dep == "ListenerSet" {
				ref = refTo("extra", "kind", "ListenerSet")
				ix.ListenerSets = []ListenerSet{{Namespace: "infra", Name: "extra", Parent: ParentRef{Group: Group, Kind: "Gateway", Namespace: "infra", Name: "eg"}, AcceptedCond: Cond{Status: "True", ObservedGeneration: 1}, Generation: 1}}
				ix.ListenerSetsRead = ParentRead{}
			} else if dep == "Gateway" {
				ix.GatewaysRead = ParentRead{}
			} else {
				ix.ClassesRead = ParentRead{}
			}
			r := ParseRoute(KindHTTPRoute, routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{ref}, "rules": backendSpec("web", 80)["rules"]}, parentEntry(ref, egCtl, "True", "True", 1)))
			okBackends(&r)
			a := r.Attachments(ix)[0]
			if a.Eval.Role != health.Unknown || a.Status != nil {
				t.Fatalf("retained stale %s established current controller/status: role=%v label=%q controller=%q status=%+v", dep, a.Eval.Role, a.Eval.Label, a.Parent.Controller, a.Status)
			}
		})
	}
}

func TestRetainedStaleGrantCannotDisagree(t *testing.T) {
	ref := refTo("eg")
	raw := routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{ref}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "web", "namespace": "other", "port": int64(80)}}}}}, parentEntry(ref, egCtl, "True", "False", 1))
	r := ParseRoute(KindHTTPRoute, raw)
	r.Parents[0].ResolvedRefs.Reason = "RefNotPermitted"
	okBackends(&r)
	ix := indexWith(gwRef("eg"))
	ae := r.Attachments(ix)[0]
	ev := Evidence(r, ae, ix, GrantsRead{Read: ParentRead{}, Grants: []ReferenceGrant{{Namespace: "other", Name: "old", From: []GrantFrom{{Group: Group, Kind: KindHTTPRoute, Namespace: "shop"}}, To: []GrantTo{{Group: "", Kind: "Service", Name: "web"}}}}}, NamespaceLabels{})
	if len(ev.Grants) != 1 || ev.Grants[0].Allowed != "" || len(ev.Disagreements) != 0 {
		t.Fatalf("stale grant is not current evidence: grants=%+v disagreements=%v", ev.Grants, ev.Disagreements)
	}
}

func TestForeignGroupKindDoesNotAdmitAGatewayAPIRoute(t *testing.T) {
	ref := RouteParentRef{Group: Group, Kind: "Gateway", Namespace: "shop", Name: "eg"}
	listener := func(ks ...RouteKind) []Listener {
		return []Listener{{Name: "http", Protocol: "HTTP", Status: &ListenerStatus{SupportedKinds: ks}}}
	}
	ae := AttachmentEval{Ref: ref, Parent: AttachmentParent{State: ParentFound, Current: true, Listeners: listener(RouteKind{Group: "example.test", Kind: KindHTTPRoute})}}
	got := RouteAllowedBy(KindHTTPRoute, "shop", ae, NamespaceLabels{})
	if got.Allowed != AllowedNo || got.Text != "listener http: HTTPRoute is not among the kinds its controller reports (example.test/HTTPRoute)" {
		t.Fatalf("foreign group's HTTPRoute admitted the Gateway API HTTPRoute: %+v", got)
	}
	// the API's own group admits it; the core group (written as "") does not
	ae.Parent.Listeners = listener(RouteKind{Group: Group, Kind: KindHTTPRoute})
	if got := RouteAllowedBy(KindHTTPRoute, "shop", ae, NamespaceLabels{}); got.Allowed != AllowedYes {
		t.Fatalf("the Gateway API's group: %+v", got)
	}
	ae.Parent.Listeners = listener(RouteKind{Group: "", Kind: KindHTTPRoute})
	if got := RouteAllowedBy(KindHTTPRoute, "shop", ae, NamespaceLabels{}); got.Allowed != AllowedNo || !strings.Contains(got.Text, "core/HTTPRoute") {
		t.Fatalf("the core group: %+v", got)
	}
	// a parent from a read that is not current establishes no allowance
	ae.Parent.Current = false
	if got := RouteAllowedBy(KindHTTPRoute, "shop", ae, NamespaceLabels{}); got.Allowed != "" {
		t.Fatalf("a last observed parent: %+v", got)
	}
}

// A ListenerSet's own refusal is a Fault only from a current read; a stale
// one is last observed.
func TestStaleListenerSetRefusalIsNotAFault(t *testing.T) {
	ref := refTo("extra", "kind", "ListenerSet")
	ix := indexWith(gwRef("eg", "https"))
	ix.ListenerSets = []ListenerSet{{Namespace: "infra", Name: "extra", Parent: ParentRef{Group: Group, Kind: "Gateway", Namespace: "infra", Name: "eg"},
		AcceptedCond: Cond{Status: "False", Reason: "NotAllowed", ObservedGeneration: 1}, Generation: 1}}
	r := ParseRoute(KindHTTPRoute, routeObj(KindHTTPRoute, "infra", "web", 1, map[string]any{"parentRefs": []any{ref}, "rules": backendSpec("web", 80)["rules"]}, parentEntry(ref, egCtl, "True", "True", 1)))
	okBackends(&r)
	if a := r.Attachments(ix)[0]; a.Eval.Role != health.Fault || a.Eval.Label != "parent ListenerSet not accepted" {
		t.Fatalf("a current refusal is a fault: %+v", a.Eval)
	}
	ix.ListenerSetsRead = ParentRead{}
	a := r.Attachments(ix)[0]
	if a.Eval.Role != health.Unknown || !strings.Contains(a.Parent.ControllerWhy, "ListenerSet infra/extra is last observed") {
		t.Fatalf("a stale refusal is not current evidence: %+v %q", a.Eval, a.Parent.ControllerWhy)
	}
}

// A Service retained from a read that is not current proves neither its
// port nor a missing one.
func TestStaleServiceIsNotAResolvedBackend(t *testing.T) {
	for _, port := range []int32{80, 6432} {
		b := Backend{Kind: "Service", Name: "web", Namespace: "shop", Port: port}
		ResolveBackend(&b, KindHTTPRoute, BackendReads{
			Service: func(string, string) ([]ServicePort, bool) {
				return []ServicePort{{Name: "http", Port: 80, Protocol: "TCP"}}, true
			},
			ServicesComplete: true, ServicesCurrent: false, SlicesComplete: true, SlicesCurrent: true,
			Slices: func(string, string) []discoveryv1.EndpointSlice { return nil }})
		if b.State != BackendUnreadable || !strings.Contains(b.Error, "last observed") {
			t.Fatalf("port %d from a stale Services read: %+v", port, b)
		}
	}
}

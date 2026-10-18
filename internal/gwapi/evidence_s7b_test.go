package gwapi

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/health"
)

var fullRead = ParentRead{Current: true, Complete: true}

// Coverage S7b §6: ReferenceGrants explain cross-namespace references; a
// grant to one kind never covers another; only a complete, current read
// says none allows it.
func TestReferenceGrantEvidence(t *testing.T) {
	grant := ReferenceGrant{Namespace: "backends", Name: "from-shop", From: []GrantFrom{{Group: Group, Kind: KindHTTPRoute, Namespace: "shop"}}, To: []GrantTo{{Kind: "Service"}}}
	gwGrant := ReferenceGrant{Namespace: "certs", Name: "gw", From: []GrantFrom{{Group: Group, Kind: "Gateway", Namespace: "infra"}}, To: []GrantTo{{Kind: "Secret", Name: "wild"}}}
	gr := GrantsRead{Grants: []ReferenceGrant{grant, gwGrant}, Read: fullRead}
	if ev := GrantFor(gr, GrantFrom{Group: Group, Kind: KindHTTPRoute, Namespace: "shop"}, "backends", GrantTo{Kind: "Service", Name: "api"}); ev.Allowed != AllowedYes || ev.By != "backends/from-shop" {
		t.Errorf("granted: %+v", ev)
	}
	if ev := GrantFor(gr, GrantFrom{Group: Group, Kind: KindTCPRoute, Namespace: "shop"}, "backends", GrantTo{Kind: "Service", Name: "api"}); ev.Allowed != AllowedNo {
		t.Errorf("a grant for HTTPRoutes is not a TCPRoute's: %+v", ev)
	}
	// non-inheritance (S7 §1): a Gateway's grant is not its ListenerSet's
	gwLs := []Listener{{Name: "https", CertificateRefs: []CertificateRef{{Kind: "Secret", Namespace: "certs", Name: "wild"}}}}
	if n := CertificateGrants("Gateway", "infra", gwLs, gr); len(n) != 1 || !strings.Contains(n[0], "granted by ReferenceGrant certs/gw") {
		t.Errorf("gateway cert: %v", n)
	}
	if n := CertificateGrants("ListenerSet", "infra", gwLs, gr); len(n) != 1 || !strings.Contains(n[0], "no ReferenceGrant in certs allows it") {
		t.Errorf("the Gateway's grant must not cover a ListenerSet: %v", n)
	}
	for name, read := range map[string]ParentRead{"truncated": {Current: true}, "stale": {}} {
		if ev := GrantFor(GrantsRead{Read: read}, GrantFrom{Group: Group, Kind: KindHTTPRoute, Namespace: "shop"}, "backends", GrantTo{Kind: "Service", Name: "api"}); ev.Allowed != "" {
			t.Errorf("%s: %+v", name, ev)
		}
	}
	if ev := GrantFor(GrantsRead{Read: ParentRead{Absent: true, Complete: true}}, GrantFrom{}, "x", GrantTo{}); ev.Allowed != "" || !strings.Contains(ev.Text, "not served") {
		t.Errorf("not served: %+v", ev)
	}
}

func crossRoute(resolved string, reason string) Route {
	u := routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{refTo("eg")},
		"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "api", "namespace": "backends", "port": int64(80)}}}}},
		map[string]any{"parentRef": refTo("eg"), "controllerName": egCtl, "conditions": []any{cond("Accepted", "True", "Accepted", 1), cond("ResolvedRefs", resolved, reason, 1)}})
	r := ParseRoute(KindHTTPRoute, u)
	okBackends(&r)
	return r
}

// S7 §6: the controller's ResolvedRefs stays authoritative; a disagreement
// with the grants is said only on a complete, current grants read.
func TestGrantDisagreements(t *testing.T) {
	ix := indexWith(gwRef("eg"))
	ix.Gateways[0].Listeners = []Listener{{Name: "http", Protocol: "HTTP", AllowedRoutes: &AllowedRoutes{Namespaces: NamespaceAllowance{Set: true, From: "All"}}}}
	none := GrantsRead{Read: fullRead}
	r := crossRoute("True", "ResolvedRefs")
	ae := r.Attachments(ix)[0]
	if ev := Evidence(r, ae, ix, none, NamespaceLabels{}); len(ev.Disagreements) != 1 || !strings.Contains(ev.Disagreements[0], "ResolvedRefs is True, but no ReferenceGrant allows 1 cross-namespace backend") {
		t.Errorf("disagreement: %+v", ev)
	}
	if ev := Evidence(r, ae, ix, GrantsRead{Read: ParentRead{Current: true}}, NamespaceLabels{}); len(ev.Disagreements) != 0 {
		t.Errorf("a truncated grants read disagrees with nothing: %+v", ev.Disagreements)
	}
	granted := GrantsRead{Grants: []ReferenceGrant{{Namespace: "backends", Name: "g", From: []GrantFrom{{Group: Group, Kind: KindHTTPRoute, Namespace: "shop"}}, To: []GrantTo{{Kind: "Service"}}}}, Read: fullRead}
	r = crossRoute("False", "RefNotPermitted")
	ae = r.Attachments(ix)[0]
	if ev := Evidence(r, ae, ix, granted, NamespaceLabels{}); len(ev.Disagreements) != 1 || !strings.Contains(ev.Disagreements[0], "RefNotPermitted although a ReferenceGrant allows") {
		t.Errorf("the converse: %+v", ev)
	}
	if ae.Eval.Role != health.Fault || ae.Eval.Label != "unresolved refs" {
		t.Errorf("the verdict stays the controller's: %+v", ae.Eval)
	}
}

// S7 §6: allowedRoutes per target listener - namespaces Same, All or
// Selector (labels only from a complete, current read), kinds from
// supportedKinds, then the declared kinds, then the protocol's convention.
func TestRouteAllowedBy(t *testing.T) {
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"team": "shop"}}
	listeners := []Listener{
		{Name: "same", Protocol: "HTTP"},
		{Name: "all-tcp", Protocol: "TCP", AllowedRoutes: &AllowedRoutes{Namespaces: NamespaceAllowance{Set: true, From: "All"}}},
		{Name: "selected", Protocol: "HTTPS", AllowedRoutes: &AllowedRoutes{Namespaces: NamespaceAllowance{Set: true, From: "Selector", Selector: sel}}},
		{Name: "grpc-only", Protocol: "HTTP", AllowedRoutes: &AllowedRoutes{Namespaces: NamespaceAllowance{Set: true, From: "All"}, Kinds: []RouteKind{{Group: Group, Kind: KindGRPCRoute}}}},
		{Name: "reported", Protocol: "HTTP", AllowedRoutes: &AllowedRoutes{Namespaces: NamespaceAllowance{Set: true, From: "All"}}, Status: &ListenerStatus{SupportedKinds: []RouteKind{{Group: Group, Kind: KindHTTPRoute}}}},
	}
	ae := func(section string) AttachmentEval {
		return AttachmentEval{Ref: RouteParentRef{Group: Group, Kind: "Gateway", Namespace: "infra", Name: "eg", SectionName: section}, Parent: AttachmentParent{State: ParentFound, Current: true, Listeners: listeners}}
	}
	known := NamespaceLabels{Known: true, Labels: map[string]string{"team": "shop"}}
	for _, tc := range []struct {
		name, kind, section string
		ns                  NamespaceLabels
		allowed, text       string
	}{
		{"Same, another namespace", KindHTTPRoute, "same", known, AllowedNo, "routes from infra only (Same); this one is in shop"},
		{"All, a TCP listener's convention", KindTCPRoute, "all-tcp", known, AllowedYes, "TCPRoute is among the kinds its protocol TCP conventionally admits"},
		{"All, the wrong kind by convention", KindUDPRoute, "all-tcp", known, AllowedNo, "UDPRoute is not among the kinds its protocol TCP conventionally admits"},
		{"Selector matching", KindHTTPRoute, "selected", known, AllowedYes, "matches its selector (team=shop)"},
		{"Selector, labels not established", KindHTTPRoute, "selected", NamespaceLabels{}, "", "are not established"},
		{"declared kinds", KindHTTPRoute, "grpc-only", known, AllowedNo, "not among the kinds it declares (GRPCRoute)"},
		{"reported kinds win", KindHTTPRoute, "reported", known, AllowedYes, "among the kinds its controller reports"},
		{"no such section", KindHTTPRoute, "nope", known, AllowedNo, "no listener of the parent matches the reference"},
	} {
		got := RouteAllowedBy(tc.kind, "shop", ae(tc.section), tc.ns)
		if got.Allowed != tc.allowed || !strings.Contains(got.Text, tc.text) {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
	// no section: any listener admitting it
	if got := RouteAllowedBy(KindTCPRoute, "shop", ae(""), known); got.Allowed != AllowedYes {
		t.Errorf("any listener: %+v", got)
	}
}

// S7 §6: features a class lists - a class listing none says nothing.
func TestFeatureNotes(t *testing.T) {
	listed := &GatewayClass{Name: "eg", SupportedFeatures: []string{"Gateway", "HTTPRoute", "TCPRoute"}, FeaturesReported: true}
	if n := FeatureNote(listed, KindUDPRoute); n != "the class's controller does not declare support for UDPRoute" {
		t.Errorf("udp: %q", n)
	}
	if FeatureNote(listed, KindTCPRoute) != "" || FeatureNote(&GatewayClass{Name: "eg"}, KindUDPRoute) != "" || FeatureNote(nil, "x") != "" {
		t.Error("a listed feature, or a class listing none, says nothing")
	}
	if n := ListenerFeatureNotes(listed, []Listener{{Name: "tls", Protocol: "TLS", TLSMode: "Terminate"}, {Name: "pt", Protocol: "TLS", TLSMode: "Passthrough"}}); len(n) != 1 || !strings.Contains(n[0], "TLSRouteModeTerminate") {
		t.Errorf("tls terminate: %v", n)
	}
}

package gwapi

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

const egCtl = "gateway.envoyproxy.io/gatewayclass-controller"

func routeObj(kind, ns, name string, gen int64, spec map[string]any, parents ...any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns, "uid": kind + "-" + name, "generation": gen},
		"spec":     spec,
		"status":   map[string]any{"parents": parents},
	}}
}

func parentEntry(ref map[string]any, controller string, accepted, resolved string, og int64) map[string]any {
	return map[string]any{"parentRef": ref, "controllerName": controller,
		"conditions": []any{cond("Accepted", accepted, "R", og), cond("ResolvedRefs", resolved, "R", og)}}
}

func refTo(name string, extra ...any) map[string]any {
	m := map[string]any{"name": name, "namespace": "infra"}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

func backendSpec(name string, port int64) map[string]any {
	return map[string]any{"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": name, "port": port}}}}}
}

func classOf(name, controller string) GatewayClass {
	return GatewayClass{Name: name, ControllerName: controller}
}

func indexWith(gws ...GatewayRef) RouteIndex {
	full := ParentRead{Current: true, Complete: true}
	return RouteIndex{Gateways: gws, GatewaysRead: full, ListenerSetsRead: full, Classes: []GatewayClass{classOf("eg", egCtl)}, ClassesRead: full}
}

func gwRef(name string, listeners ...string) GatewayRef {
	g := GatewayRef{Namespace: "infra", Name: name, ClassName: "eg", Generation: 1,
		AcceptedCond: Cond{Status: "True", ObservedGeneration: 1}, ProgrammedCond: Cond{Status: "True", ObservedGeneration: 1}}
	for _, l := range listeners {
		g.Listeners = append(g.Listeners, Listener{Name: l})
	}
	return g
}

func okBackends(r *Route) {
	ResolveBackends(r, BackendReads{
		Service: func(string, string) ([]ServicePort, bool) {
			return []ServicePort{{Name: "p", Port: 80, Protocol: "TCP"}, {Name: "q", Port: 5353, Protocol: "UDP"}}, true
		},
		ServicesComplete: true, ServicesCurrent: true, SlicesComplete: true, SlicesCurrent: true,
		Slices: func(string, string) []discoveryv1.EndpointSlice {
			return []discoveryv1.EndpointSlice{slice("p", corev1.ProtocolTCP, true), slice("q", corev1.ProtocolUDP, true)}
		}})
}

func slice(port string, proto corev1.Protocol, ready bool) discoveryv1.EndpointSlice {
	var name *string
	if port != "" {
		name = &port
	}
	r := ready
	return discoveryv1.EndpointSlice{Ports: []discoveryv1.EndpointPort{{Name: name, Protocol: &proto}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &r}}}}
}

// Coverage S7b §5: one attachment per parentRef, its identity the whole
// normalised reference - two sections, or two ports, to one Gateway are two
// attachments, each judged on its own status entry.
func TestAttachmentsPerParentRef(t *testing.T) {
	u := routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{
		"parentRefs": []any{refTo("eg", "sectionName", "https"), refTo("eg", "sectionName", "http"), refTo("eg", "port", int64(8443)), refTo("eg", "port", int64(9443))},
		"rules":      backendSpec("web", 80)["rules"],
	},
		parentEntry(refTo("eg", "sectionName", "https"), egCtl, "True", "True", 1),
		parentEntry(refTo("eg", "sectionName", "http"), egCtl, "False", "True", 1),
		parentEntry(refTo("eg", "port", int64(8443)), egCtl, "True", "True", 1))
	r := ParseRoute(KindHTTPRoute, u)
	okBackends(&r)
	atts := r.Attachments(indexWith(gwRef("eg", "https", "http")))
	if len(atts) != 4 {
		t.Fatalf("attachments: %d", len(atts))
	}
	keys := map[string]health.Kind{}
	for _, a := range atts {
		keys[a.Ref.Label()] = a.Eval.Role
	}
	want := map[string]health.Kind{"infra/eg · https": health.OK, "infra/eg · http": health.Fault, "infra/eg · :8443": health.OK, "infra/eg · :9443": health.Unknown}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("per-attachment roles: %v", keys)
	}
	if atts[0].Ref.Key() == atts[1].Ref.Key() || atts[2].Ref.Key() == atts[3].Ref.Key() {
		t.Error("section-only and port-only references must have distinct identities")
	}
	if atts[3].Eval.Label != "no status for this Gateway" {
		t.Errorf("an attachment no controller reported on: %q", atts[3].Eval.Label)
	}
}

// Codex's design review finding 1: route status is keyed by parentRef and
// controllerName - only the expected controller's entry is judged.
func TestParentStatusIsTheExpectedControllers(t *testing.T) {
	retired := "example.dev/retired"
	u := routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{refTo("eg")}, "rules": backendSpec("web", 80)["rules"]},
		parentEntry(refTo("eg"), retired, "True", "True", 1),
		parentEntry(refTo("eg"), egCtl, "False", "True", 1))
	r := ParseRoute(KindHTTPRoute, u)
	okBackends(&r)
	a := r.Attachments(indexWith(gwRef("eg")))[0]
	if a.Eval.Role != health.Fault || a.Eval.Label != "not accepted" || a.Status == nil || a.Status.ControllerName != egCtl {
		t.Errorf("the current controller's False must decide: %+v", a.Eval)
	}
	if len(a.Others) != 1 || a.Others[0].ControllerName != retired {
		t.Errorf("the retired controller's entry is listed, not judged: %+v", a.Others)
	}
	// the class unread: the controller is not established, nothing chosen
	ix := indexWith(gwRef("eg"))
	ix.Classes, ix.ClassesRead = nil, ParentRead{}
	a = r.Attachments(ix)[0]
	if a.Eval.Role != health.Unknown || a.Eval.Label != "the controller judging this parent is not established" || a.Status != nil || len(a.Others) != 2 {
		t.Errorf("class unread: %+v status %v others %d", a.Eval, a.Status, len(a.Others))
	}
	// a class naming no controller
	ix = indexWith(gwRef("eg"))
	ix.Classes = []GatewayClass{{Name: "eg"}}
	if a := r.Attachments(ix)[0]; a.Eval.Role != health.Unknown || a.Parent.ControllerWhy != "the GatewayClass eg names no controller" {
		t.Errorf("no controllerName: %+v %q", a.Eval, a.Parent.ControllerWhy)
	}
}

// Codex's design review finding 2: only a complete, current read proves a
// parent missing; a parent kind not served is not established.
func TestParentCompleteness(t *testing.T) {
	u := routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{refTo("gone")}, "rules": backendSpec("web", 80)["rules"]})
	r := ParseRoute(KindHTTPRoute, u)
	okBackends(&r)
	for name, tc := range map[string]struct {
		read  ParentRead
		role  health.Kind
		label string
	}{
		"complete and current": {ParentRead{Current: true, Complete: true}, health.Fault, "parent Gateway not found"},
		"truncated":            {ParentRead{Current: true}, health.Unknown, "parent Gateway not established"},
		"stale":                {ParentRead{}, health.Unknown, "parent Gateway not established"},
		"not served":           {ParentRead{Absent: true, Complete: true}, health.Unknown, "parent Gateway not established"},
	} {
		ix := indexWith(gwRef("eg"))
		ix.GatewaysRead = tc.read
		if a := r.Attachments(ix)[0]; a.Eval.Role != tc.role || a.Eval.Label != tc.label {
			t.Errorf("%s: %v %q", name, a.Eval.Role, a.Eval.Label)
		}
	}
	// a controller-reported failure is judged whatever the backends' reads
	u = routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{refTo("eg")}, "rules": backendSpec("web", 80)["rules"]},
		parentEntry(refTo("eg"), egCtl, "False", "True", 1))
	r = ParseRoute(KindHTTPRoute, u)
	ResolveBackends(&r, BackendReads{Service: func(string, string) ([]ServicePort, bool) { return nil, false }})
	if a := r.Attachments(indexWith(gwRef("eg")))[0]; a.Eval.Role != health.Fault || a.Eval.Label != "not accepted" {
		t.Errorf("controller failure with unread backends: %+v", a.Eval)
	}
	if b := r.Rules[0].Backends[0]; b.State != BackendUnreadable {
		t.Errorf("a Service absent from an incomplete read is unreadable, never not found: %+v", b)
	}
}

// S7 §4/§5: a route attached to a ListenerSet - its section is one of the
// ListenerSet's listeners, its controller the parent Gateway's class's, and
// a ListenerSet not accepted is the attachment's fault.
func TestListenerSetParents(t *testing.T) {
	ls := ListenerSet{Namespace: "infra", Name: "extra", Generation: 1, Parent: ParentRef{Group: Group, Kind: "Gateway", Namespace: "infra", Name: "eg"},
		Listeners: []Listener{{Name: "tenant-a"}}, AcceptedCond: Cond{Status: "True", ObservedGeneration: 1}, ProgrammedCond: Cond{Status: "True", ObservedGeneration: 1}}
	ix := indexWith(gwRef("eg"))
	ix.ListenerSets = []ListenerSet{ls}
	lref := func(section string) map[string]any {
		return refTo("extra", "kind", "ListenerSet", "sectionName", section)
	}
	u := routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{lref("tenant-a"), lref("tenant-b")}, "rules": backendSpec("web", 80)["rules"]},
		parentEntry(lref("tenant-a"), egCtl, "True", "True", 1), parentEntry(lref("tenant-b"), egCtl, "True", "True", 1))
	r := ParseRoute(KindHTTPRoute, u)
	okBackends(&r)
	atts := r.Attachments(ix)
	if atts[0].Eval.Role != health.OK || atts[0].Parent.Controller != egCtl || atts[0].Parent.Gateway == nil {
		t.Errorf("through a ListenerSet: %+v", atts[0].Eval)
	}
	if atts[1].Eval.Role != health.Fault || atts[1].Eval.Label != "listener missing" {
		t.Errorf("a section the ListenerSet lacks: %+v", atts[1].Eval)
	}
	ix.ListenerSets[0].AcceptedCond = Cond{Status: "False", Reason: "NotAllowed", ObservedGeneration: 1}
	if a := r.Attachments(ix)[0]; a.Eval.Role != health.Fault || a.Eval.Label != "parent ListenerSet not accepted" {
		t.Errorf("a ListenerSet not accepted: %+v", a.Eval)
	}
	// a parent of a kind Klyx does not read is no attachment of the task
	u = routeObj(KindHTTPRoute, "shop", "mesh", 1, map[string]any{"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "web"}}, "rules": backendSpec("web", 80)["rules"]})
	if atts := ParseRoute(KindHTTPRoute, u).Attachments(ix); len(atts) != 0 {
		t.Errorf("a mesh parentRef: %d attachments", len(atts))
	}
}

// Codex's design review finding 3, revision 2: a backend resolves through
// the Service port of its number AND the route's transport, then the
// EndpointSlices carrying that port's name and protocol.
func TestBackendsArePortAndProtocolQualified(t *testing.T) {
	dns := func(tcpFirst bool) []ServicePort {
		tcp, udp := ServicePort{Name: "dns-tcp", Port: 53, Protocol: "TCP"}, ServicePort{Name: "dns-udp", Port: 53, Protocol: "UDP"}
		if tcpFirst {
			return []ServicePort{tcp, udp}
		}
		return []ServicePort{udp, tcp}
	}
	resolve := func(kind string, ports []ServicePort, slices ...discoveryv1.EndpointSlice) Backend {
		b := Backend{Kind: "Service", Name: "dns", Namespace: "kube-system", Port: 53}
		ResolveBackend(&b, kind, BackendReads{Service: func(string, string) ([]ServicePort, bool) { return ports, true }, ServicesComplete: true, ServicesCurrent: true,
			Slices: func(string, string) []discoveryv1.EndpointSlice { return slices }, SlicesComplete: true, SlicesCurrent: true})
		return b
	}
	for _, tcpFirst := range []bool{true, false} {
		tcpOnly := slice("dns-tcp", corev1.ProtocolTCP, true)
		udpOnly := slice("dns-udp", corev1.ProtocolUDP, true)
		if b := resolve(KindUDPRoute, dns(tcpFirst), tcpOnly); b.State != BackendResolved || b.Endpoints.Total != 0 {
			t.Errorf("tcpFirst=%v: a UDPRoute backend with only TCP endpoints: %+v %+v", tcpFirst, b, b.Endpoints)
		}
		if b := resolve(KindTCPRoute, dns(tcpFirst), tcpOnly); b.Endpoints.Ready != 1 {
			t.Errorf("tcpFirst=%v: a TCPRoute backend with TCP endpoints: %+v", tcpFirst, b.Endpoints)
		}
		if b := resolve(KindUDPRoute, dns(tcpFirst), udpOnly); b.Endpoints.Ready != 1 {
			t.Errorf("tcpFirst=%v: a UDPRoute backend with UDP endpoints: %+v", tcpFirst, b.Endpoints)
		}
		if b := resolve(KindTCPRoute, dns(tcpFirst), udpOnly); b.Endpoints.Total != 0 {
			t.Errorf("tcpFirst=%v: a TCPRoute backend with only UDP endpoints: %+v", tcpFirst, b.Endpoints)
		}
	}
	// UDP/53 with TCP/80: only TCP endpoints never make the UDP backend available
	b := resolve(KindUDPRoute, []ServicePort{{Name: "dns", Port: 53, Protocol: "UDP"}, {Name: "http", Port: 80}}, slice("http", corev1.ProtocolTCP, true))
	if r, s := BackendIssue(b); r != health.Fault || s != "no endpoints" {
		t.Errorf("UDP/53 beside TCP/80: %v %q", r, s)
	}
	// no port of the transport: named, with the other protocol's
	b = resolve(KindUDPRoute, []ServicePort{{Name: "dns-tcp", Port: 53, Protocol: "TCP"}})
	if r, s := BackendIssue(b); b.State != BackendNoPort || r != health.Fault || s != "no UDP port 53" || b.Error != "Service has no UDP port 53 (port 53 exists for TCP only)" {
		t.Errorf("no UDP port: %v %q %+v", r, s, b)
	}
	// an unnamed Service port matches the slice's unnamed port of its protocol
	ub := Backend{Kind: "Service", Name: "web", Namespace: "shop", Port: 80}
	ResolveBackend(&ub, KindHTTPRoute, BackendReads{Service: func(string, string) ([]ServicePort, bool) { return []ServicePort{{Port: 80}}, true }, ServicesComplete: true, ServicesCurrent: true,
		Slices: func(string, string) []discoveryv1.EndpointSlice {
			return []discoveryv1.EndpointSlice{slice("", corev1.ProtocolTCP, true)}
		}, SlicesComplete: true, SlicesCurrent: true})
	if ub.State != BackendResolved || ub.Endpoints.Ready != 1 {
		t.Errorf("unnamed port: %+v %+v", ub, ub.Endpoints)
	}
	// a Service port from a read that is not current proves no missing port
	nb := Backend{Kind: "Service", Name: "dns", Namespace: "x", Port: 53}
	ResolveBackend(&nb, KindUDPRoute, BackendReads{Service: func(string, string) ([]ServicePort, bool) { return []ServicePort{{Port: 53}}, true }, ServicesComplete: true})
	if nb.State != BackendUnreadable {
		t.Errorf("a stale Service: %+v", nb)
	}
}

// S7 §1, revision 1: TLSRoute's v1alpha2 shape, read through v1 - no
// hostnames and several rules - keeps every rule.
func TestTLSRouteAlphaShapeKeepsEveryRule(t *testing.T) {
	u := routeObj(KindTLSRoute, "shop", "tls", 1, map[string]any{"parentRefs": []any{refTo("eg")},
		"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "a", "port": int64(443)}}}, map[string]any{"name": "second", "backendRefs": []any{map[string]any{"name": "b", "port": int64(443)}}}}})
	r := ParseRoute(KindTLSRoute, u)
	if len(r.Rules) != 2 || r.HostnameCount != 0 || r.Rules[1].Name != "second" || len(r.Fingerprints) != 2 || r.Fingerprints[0] == r.Fingerprints[1] {
		t.Errorf("tls alpha shape: %+v", r)
	}
}

// Fingerprints: an HTTPRoute's are unchanged (its metric history joins on
// them); another kind's carry the kind.
func TestKindRuleFingerprints(t *testing.T) {
	spec := backendSpec("web", 443)
	h := routeObj(KindHTTPRoute, "shop", "same", 1, spec)
	tls := routeObj(KindTLSRoute, "shop", "same", 1, spec)
	tcp := routeObj(KindTCPRoute, "shop", "same", 1, spec)
	if !reflect.DeepEqual(KindRuleFingerprints(KindHTTPRoute, h), RuleFingerprints(h)) {
		t.Error("an HTTPRoute's fingerprints changed")
	}
	a, b := KindRuleFingerprints(KindTLSRoute, tls)[0], KindRuleFingerprints(KindTCPRoute, tcp)[0]
	if a == b || a == RuleFingerprints(h)[0] {
		t.Error("two kinds' identical rules must fingerprint apart")
	}
	g := routeObj(KindGRPCRoute, "shop", "grpc", 1, map[string]any{"rules": []any{map[string]any{
		"matches":     []any{map[string]any{"method": map[string]any{"service": "shop.Cart", "method": "Add"}}},
		"backendRefs": []any{map[string]any{"name": "cart", "port": int64(9000)}}}}})
	r := ParseRoute(KindGRPCRoute, g)
	if m := r.Rules[0].Matches[0]; m.GRPCService != "shop.Cart" || m.GRPCMethod != "Add" || m.PathValue != "" {
		t.Errorf("grpc match: %+v", m)
	}
}

func TestRouteParentRefDefaultsAndLabels(t *testing.T) {
	r := ParseRoute(KindHTTPRoute, routeObj(KindHTTPRoute, "shop", "web", 1, map[string]any{"parentRefs": []any{
		map[string]any{"name": "eg"},
		map[string]any{"group": "", "kind": "Service", "name": "web"},
		map[string]any{"name": "extra", "namespace": "infra", "kind": "ListenerSet", "sectionName": "a", "port": int64(8443)},
	}}))
	if p := r.ParentRefs[0]; p != (RouteParentRef{Group: Group, Kind: "Gateway", Namespace: "shop", Name: "eg"}) || p.Label() != "shop/eg" || !p.IsGateway() {
		t.Errorf("defaults: %+v %q", p, p.Label())
	}
	if p := r.ParentRefs[1]; p.Group != "" || p.IsGateway() {
		t.Errorf("an explicit empty group is the core group: %+v", p)
	}
	if p := r.ParentRefs[2]; p.Label() != "ListenerSet infra/extra · a:8443" || !p.IsListenerSet() {
		t.Errorf("label: %q", p.Label())
	}
	if RouteKindOf("udproutes") != KindUDPRoute || RouteResource(KindGRPCRoute) != "grpcroutes" || Transport(KindUDPRoute) != "UDP" || Transport(KindTLSRoute) != "TCP" {
		t.Error("kind table")
	}
}

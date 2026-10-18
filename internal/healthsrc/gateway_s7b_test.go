package healthsrc

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// l4Route is a route of kind with one backend at port.
func l4Route(kind, name string, parents []any, backend string, port int64, status ...any) *unstructured.Unstructured {
	u := cr("gateway.networking.k8s.io/v1", kind, "shop", name, map[string]any{"parentRefs": parents,
		"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": backend, "port": port}}}}}, nil, nil)
	u.Object["metadata"].(map[string]any)["uid"] = kind + "-" + name
	u.Object["status"] = map[string]any{"parents": status}
	return u
}

func parentOK(ref map[string]any) map[string]any {
	return map[string]any{"parentRef": ref, "controllerName": egController, "conditions": []any{cond("Accepted", "True", "Accepted"), cond("ResolvedRefs", "True", "ResolvedRefs")}}
}

func dnsService() *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "shop", UID: types.UID("svc-dns")},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "dns-tcp", Port: 53, Protocol: corev1.ProtocolTCP}, {Name: "dns-udp", Port: 53, Protocol: corev1.ProtocolUDP}}}}
}

func dnsSlice(port string, proto corev1.Protocol) *discoveryv1.EndpointSlice {
	ready := true
	return &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "dns-" + port, Namespace: "shop", Labels: map[string]string{discoveryv1.LabelServiceName: "dns"}},
		Ports:     []discoveryv1.EndpointPort{{Name: &port, Protocol: &proto}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.0.0.9"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}}}}
}

// Coverage S7b §5: every route kind counts, identity is kind/namespace/name
// (an HTTPRoute and a TLSRoute of one name are two routes), each attachment
// is one parentRef, and a UDP backend counts only UDP endpoints.
func TestRoutesPartReadsEveryKind(t *testing.T) {
	gwRef := map[string]any{"name": "eg", "namespace": "infra"}
	c := routesCapture(t, nil, gw("eg", "True"))
	add := func(resource string, u *unstructured.Unstructured) {
		c.Objects[resource] = append(c.Objects[resource], projected(t, resource, u))
		c.Complete[resource], c.Current[resource] = true, true
	}
	add(KindHTTPRoutes, l4Route("HTTPRoute", "same", []any{gwRef}, "web", 80, parentOK(gwRef)))
	add(KindTLSRoutes, l4Route("TLSRoute", "same", []any{gwRef}, "web", 80, parentOK(gwRef)))
	add(KindTCPRoutes, l4Route("TCPRoute", "dns-tcp", []any{gwRef}, "dns", 53, parentOK(gwRef)))
	add(KindUDPRoutes, l4Route("UDPRoute", "dns-udp", []any{gwRef}, "dns", 53, parentOK(gwRef)))
	// two port-only references to one Gateway: two attachments
	ports := []any{map[string]any{"name": "eg", "namespace": "infra", "port": int64(8443)}, map[string]any{"name": "eg", "namespace": "infra", "port": int64(9443)}}
	add(KindGRPCRoutes, l4Route("GRPCRoute", "cart", ports, "web", 80, parentOK(ports[0].(map[string]any))))
	c.Objects[KindGatewayServices] = append(c.Objects[KindGatewayServices], dnsService())
	c.Objects[KindGatewayEndpoints] = append(c.Objects[KindGatewayEndpoints], dnsSlice("dns-tcp", corev1.ProtocolTCP)) // only TCP endpoints

	res := evalRoutes("c1")(c)
	rows := map[string]Row[RouteRow]{}
	for _, r := range res.Rows.(RowSet[RouteRow]).Rows {
		rows[r.Data.Kind+"/"+r.Data.Name] = r
	}
	if res.Native.Total != 5 || len(rows) != 5 {
		t.Fatalf("five routes, kind-qualified: %d %v", res.Native.Total, rows)
	}
	if rows["HTTPRoute/same"].Ref.UID == rows["TLSRoute/same"].Ref.UID || rows["TLSRoute/same"].Ref.GVR != "gateway.networking.k8s.io/v1/tlsroutes" {
		t.Errorf("same name, two kinds: %+v %+v", rows["HTTPRoute/same"].Ref, rows["TLSRoute/same"].Ref)
	}
	if r := rows["TCPRoute/dns-tcp"]; r.Data.Role != "ok" {
		t.Errorf("a TCPRoute over TCP endpoints: %s %q", r.Data.Role, r.Data.Label)
	}
	if r := rows["UDPRoute/dns-udp"]; r.Data.Role != "fault" || r.Data.Label != "no endpoints" {
		t.Errorf("a UDPRoute with only TCP endpoints: %s %q", r.Data.Role, r.Data.Label)
	}
	g := rows["GRPCRoute/cart"]
	if len(g.Data.Attachments) != 2 || g.Data.Attachments[0].Parent == g.Data.Attachments[1].Parent || len(g.Data.Evidence) != 2 {
		t.Fatalf("port-only references: %+v", g.Data.Attachments)
	}
	if g.Data.Role != "unknown" || g.Data.ParentLabel != "infra/eg · :9443" {
		t.Errorf("the attachment no controller reported on decides: %s %q", g.Data.Role, g.Data.ParentLabel)
	}
	if ev := g.Data.Evidence[0]; ev.Controller != egController {
		t.Errorf("evidence names the expected controller: %+v", ev)
	}
	// the ledger partitions by kind
	sources := map[string]bool{}
	for _, r := range res.Roles {
		sources[r.Source] = true
	}
	for _, k := range []string{KindHTTPRoutes, KindTLSRoutes, KindTCPRoutes, KindUDPRoutes, KindGRPCRoutes} {
		if !sources[k] {
			t.Errorf("no role partitioned under %s: %v", k, sources)
		}
	}
	// a Top ref names the Gateway the worst attachment lands on
	for _, ref := range res.Top {
		if ref.Parent != "infra/eg" {
			t.Errorf("top parent: %+v", ref)
		}
	}
}

// S7b §5: a route attached to a ListenerSet lands on the ListenerSet's
// parent Gateway, judged by its class's controller.
func TestRoutesThroughAListenerSet(t *testing.T) {
	c := routesCapture(t, nil, gwAllowing("eg", "Same"))
	ls := listenerSet("infra", "extra", "eg", s7t0, "True", "Accepted")
	c.Objects[KindListenerSets] = []any{projected(t, KindListenerSets, ls)}
	lref := map[string]any{"name": "extra", "namespace": "infra", "kind": "ListenerSet", "sectionName": "extra"}
	c.Objects[KindHTTPRoutes] = []any{projected(t, KindHTTPRoutes, l4Route("HTTPRoute", "via-ls", []any{lref}, "web", 80, parentOK(lref)))}
	row := evalRoutes("c1")(c).Rows.(RowSet[RouteRow]).Rows[0]
	if row.Data.Role != "ok" || row.Data.Gateway != "infra/eg" || row.Data.ParentRef == nil || row.Data.ParentRef.Kind != "ListenerSet" {
		t.Errorf("through a ListenerSet: %+v", row.Data.Attached)
	}
	if row.Data.ParentLabel != "ListenerSet infra/extra · extra" {
		t.Errorf("label: %q", row.Data.ParentLabel)
	}
}

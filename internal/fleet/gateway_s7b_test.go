package fleet

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/health"
)

// Coverage S7b at the fleet layer: the on-demand topology draws one lane per
// attachment of every route kind, judged by the parent's controller, with
// backends selected by port number and the route's transport.

func gwRes(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: resource}
}

// l4Gateway listens for HTTP, TCP and UDP, so every kind has a section.
func l4Gateway() *unstructured.Unstructured {
	g := gw("eg", "infra")
	_ = unstructured.SetNestedSlice(g.Object, []interface{}{
		map[string]interface{}{"name": "http", "protocol": "HTTP", "port": int64(80)},
		map[string]interface{}{"name": "pg", "protocol": "TCP", "port": int64(5432)},
		map[string]interface{}{"name": "dns", "protocol": "UDP", "port": int64(53)},
	}, "spec", "listeners")
	return g
}

// routeOf builds a route of any kind: parents are parentRef maps, each rule
// a list of backendRef maps; every parent gets an accepted status from the
// fixtures' controller.
func routeOf(kind, ns, name string, parents []map[string]interface{}, rules ...[]map[string]interface{}) *unstructured.Unstructured {
	refs := make([]interface{}, 0, len(parents))
	statuses := make([]interface{}, 0, len(parents))
	for _, p := range parents {
		refs = append(refs, p)
		statuses = append(statuses, map[string]interface{}{"parentRef": p, "controllerName": egController, "conditions": []interface{}{
			map[string]interface{}{"type": "Accepted", "status": "True"}, map[string]interface{}{"type": "ResolvedRefs", "status": "True"}}})
	}
	rs := make([]interface{}, 0, len(rules))
	for _, backends := range rules {
		bs := make([]interface{}, 0, len(backends))
		for _, b := range backends {
			bs = append(bs, b)
		}
		rs = append(rs, map[string]interface{}{"backendRefs": bs})
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": kind,
		"metadata": map[string]interface{}{"name": name, "namespace": ns, "uid": ns + "-" + name},
		"spec":     map[string]interface{}{"parentRefs": refs, "rules": rs},
		"status":   map[string]interface{}{"parents": statuses},
	}}
}

func toEG(section string) map[string]interface{} {
	p := map[string]interface{}{"name": "eg", "namespace": "infra"}
	if section != "" {
		p["sectionName"] = section
	}
	return p
}

func backendRef(name string, port int64) map[string]interface{} {
	return map[string]interface{}{"name": name, "port": port}
}

// slicePort is one named port at one protocol, as an EndpointSlice lists it.
func slicePort(name string, proto corev1.Protocol, port int32) []discoveryv1.EndpointPort {
	return []discoveryv1.EndpointPort{{Name: &name, Protocol: &proto, Port: &port}}
}

func readySlice(svc, name string, ports []discoveryv1.EndpointPort, pods ...string) *discoveryv1.EndpointSlice {
	yes := true
	s := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "apps", Labels: map[string]string{"kubernetes.io/service-name": svc}},
		Ports:      ports,
	}
	for _, p := range pods {
		s.Endpoints = append(s.Endpoints, discoveryv1.Endpoint{Conditions: discoveryv1.EndpointConditions{Ready: &yes},
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: "apps", Name: p, UID: types.UID("uid-" + p)}})
	}
	return s
}

func topologyOf(t *testing.T, dyn dynamic.Interface, objs ...runtime.Object) gwapi.Topology {
	t.Helper()
	c := NewClusterConn("x", typedfake.NewSimpleClientset(objs...), nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	return topo
}

func laneOf(t *testing.T, topo gwapi.Topology, kind, name string) gwapi.RouteNode {
	t.Helper()
	for _, r := range topo.Routes {
		if r.Kind == kind && r.Name == name {
			return r
		}
	}
	t.Fatalf("no %s %s lane in %d lanes", kind, name, len(topo.Routes))
	return gwapi.RouteNode{}
}

// A TCPRoute and a UDPRoute each draw their own lane; a UDP backend counts
// only the endpoints of the slice carrying its UDP port, never the TCP one.
func TestTopologyL4RoutesDrawTheirOwnLanes(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR():                  {l4Gateway()},
		gwRes("tcproutes"):       {routeOf("TCPRoute", "apps", "pg", []map[string]interface{}{toEG("pg")}, []map[string]interface{}{backendRef("db", 5432)})},
		gwRes("udproutes"):       {routeOf("UDPRoute", "apps", "dns", []map[string]interface{}{toEG("dns")}, []map[string]interface{}{backendRef("dns", 53)})},
		gwRes("httproutes"):      nil,
		gwRes("referencegrants"): nil,
	})
	db := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "apps"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Name: "pg", Port: 5432, Protocol: corev1.ProtocolTCP}}}}
	dns := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "apps"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{
			{Name: "dns-tcp", Port: 53, Protocol: corev1.ProtocolTCP}, {Name: "dns", Port: 53, Protocol: corev1.ProtocolUDP}}}}
	topo := topologyOf(t, dyn, db, dns,
		readySlice("db", "db-1", slicePort("pg", corev1.ProtocolTCP, 5432), "db-0"),
		readySlice("dns", "dns-udp", slicePort("dns", corev1.ProtocolUDP, 53), "dns-0"),
		readySlice("dns", "dns-tcp", slicePort("dns-tcp", corev1.ProtocolTCP, 53), "dns-0", "dns-1", "dns-2"))

	if !topo.RoutesComplete || len(topo.Routes) != 2 {
		t.Fatalf("two L4 lanes, complete: %v %+v", topo.RoutesComplete, topo.Routes)
	}
	pg := laneOf(t, topo, "TCPRoute", "pg")
	if pg.Backends[0].Transport != "TCP" || pg.Backends[0].State != gwapi.BackendResolved || pg.Backends[0].Endpoints.Ready != 1 {
		t.Fatalf("TCP backend: %+v %+v", pg.Backends[0], pg.Backends[0].Endpoints)
	}
	if pg.Verdict == nil || pg.Verdict.Role != health.OK || pg.Controller != egController {
		t.Fatalf("TCPRoute judged by its parent's controller: %+v %q", pg.Verdict, pg.Controller)
	}
	if pg.Attachment != "gateway.networking.k8s.io/Gateway/infra/eg#pg:0" || pg.AttachmentLabel != "infra/eg · pg" {
		t.Fatalf("attachment identity: %q %q", pg.Attachment, pg.AttachmentLabel)
	}
	d := laneOf(t, topo, "UDPRoute", "dns")
	if b := d.Backends[0]; b.Transport != "UDP" || b.State != gwapi.BackendResolved || b.Endpoints.Ready != 1 || b.Endpoints.Total != 1 {
		t.Fatalf("UDP backend counts only the UDP slice: %+v %+v", b, b.Endpoints)
	}
}

// A UDPRoute to a Service that publishes port 53 for TCP only has no port:
// a Fault naming what the Service does publish.
func TestTopologyUDPBackendWithOnlyTCPPortIsNoPort(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR():            {l4Gateway()},
		gwRes("udproutes"): {routeOf("UDPRoute", "apps", "dns", []map[string]interface{}{toEG("dns")}, []map[string]interface{}{backendRef("dns", 53)})},
	})
	dns := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "apps"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 53, Protocol: corev1.ProtocolTCP}}}}
	topo := topologyOf(t, dyn, dns)

	d := laneOf(t, topo, "UDPRoute", "dns")
	b := d.Backends[0]
	if b.State != gwapi.BackendNoPort || b.Endpoints != nil || !strings.Contains(b.Error, "no UDP port 53") || !strings.Contains(b.Error, "exists for TCP only") {
		t.Fatalf("no-port backend: %+v", b)
	}
	if d.Verdict == nil || d.Verdict.Role != health.Fault {
		t.Fatalf("a backend with no port is a fault: %+v", d.Verdict)
	}
	var warned bool
	for _, w := range topo.Warnings {
		warned = warned || strings.HasPrefix(w, "route UDPRoute apps/dns: ")
	}
	if !warned {
		t.Fatalf("the warning names the route by kind: %q", topo.Warnings)
	}
}

// Two parentRefs to two sections of one Gateway are two attachments, so two
// lanes keyed apart; a parentRef to another Gateway draws none here.
func TestTopologyEachAttachmentIsALane(t *testing.T) {
	r := routeOf("HTTPRoute", "apps", "web", []map[string]interface{}{toEG("http"), toEG("pg"),
		{"name": "other", "namespace": "infra"}}, []map[string]interface{}{backendRef("web", 80)})
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{gwGVR(): {l4Gateway()}, hrGVR(): {r}})
	web := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}}}
	topo := topologyOf(t, dyn, web, readySlice("web", "web-1", tcp80(), "web-0"))

	if len(topo.Routes) != 2 {
		t.Fatalf("one lane per landing attachment: %+v", topo.Routes)
	}
	if topo.Routes[0].Attachment == topo.Routes[1].Attachment {
		t.Fatalf("the lanes must be keyed by attachment: %q", topo.Routes[0].Attachment)
	}
	for _, l := range topo.Routes {
		if l.Kind != "HTTPRoute" || l.Name != "web" || l.ParentRef.Name != "eg" {
			t.Fatalf("lane: %+v", l)
		}
	}
}

func listenerSet(name, parent string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "ListenerSet",
		"metadata": map[string]interface{}{"name": name, "namespace": "infra", "generation": int64(1)},
		"spec": map[string]interface{}{"parentRef": map[string]interface{}{"name": parent},
			"listeners": []interface{}{map[string]interface{}{"name": "extra", "protocol": "HTTP", "port": int64(8080)}}},
		"status": map[string]interface{}{"conditions": []interface{}{
			map[string]interface{}{"type": "Accepted", "status": "True", "observedGeneration": int64(1)},
			map[string]interface{}{"type": "Programmed", "status": "True", "observedGeneration": int64(1)}}},
	}}
}

func toLS(name string) map[string]interface{} {
	return map[string]interface{}{"group": "gateway.networking.k8s.io", "kind": "ListenerSet", "name": name, "namespace": "infra"}
}

// A route attached through this Gateway's ListenerSet lands here; one
// attached through another Gateway's ListenerSet does not.
func TestTopologyListenerSetAttachmentLands(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR():               {l4Gateway()},
		gwRes("listenersets"): {listenerSet("mine", "eg"), listenerSet("theirs", "other")},
		hrGVR(): {
			routeOf("HTTPRoute", "apps", "via-mine", []map[string]interface{}{toLS("mine")}, []map[string]interface{}{backendRef("web", 80)}),
			routeOf("HTTPRoute", "apps", "via-theirs", []map[string]interface{}{toLS("theirs")}, []map[string]interface{}{backendRef("web", 80)}),
		},
	})
	web := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}}}
	topo := topologyOf(t, dyn, web, readySlice("web", "web-1", tcp80(), "web-0"))

	if len(topo.Routes) != 1 {
		t.Fatalf("only the route through this Gateway's ListenerSet lands: %+v", topo.Routes)
	}
	l := topo.Routes[0]
	if l.Name != "via-mine" || !l.ParentRef.IsListenerSet() || l.Controller != egController || l.Verdict == nil || l.Verdict.Role != health.OK {
		t.Fatalf("ListenerSet lane judged through its parent Gateway's controller: %+v %+v", l, l.Verdict)
	}
	if l.AttachmentLabel != "ListenerSet infra/mine" {
		t.Fatalf("label: %q", l.AttachmentLabel)
	}
}

// With ListenerSets unreadable, a route attached through one may land here:
// the routes are incomplete and say why, never silently short.
func TestTopologyUnreadableListenerSetsAreCoverage(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR(): {l4Gateway()},
		hrGVR(): {routeOf("HTTPRoute", "apps", "via-set", []map[string]interface{}{toLS("mine")}, []map[string]interface{}{backendRef("web", 80)})},
	})
	dyn.(interface {
		PrependReactor(string, string, k8stesting.ReactionFunc)
	}).PrependReactor("list", "listenersets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "listenersets"}, "", nil)
	})
	topo := topologyOf(t, dyn)

	if topo.RoutesComplete || !strings.Contains(topo.RoutesError, "ListenerSet that could not be read") || len(topo.Routes) != 0 {
		t.Fatalf("coverage must be named: %v %q %+v", topo.RoutesComplete, topo.RoutesError, topo.Routes)
	}
}

// A failed list of one route kind leaves the routes incomplete, naming it;
// the kinds that were read still draw.
func TestTopologyRouteKindListFailureIsNamed(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR(): {l4Gateway()},
		hrGVR(): {hr("share", "apps", "eg", "infra", "share-api")},
	})
	dyn.(interface {
		PrependReactor(string, string, k8stesting.ReactionFunc)
	}).PrependReactor("list", "tcproutes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "tcproutes"}, "", nil)
	})
	topo := topologyOf(t, dyn)

	if topo.RoutesComplete || !strings.Contains(topo.RoutesError, "TCPRoutes") || len(topo.Routes) != 1 {
		t.Fatalf("the failed kind is named, the others draw: %v %q %d", topo.RoutesComplete, topo.RoutesError, len(topo.Routes))
	}
}

// Without the parent's GatewayClass the controller is not established: the
// lane is Unknown with the reason, never a Fault and never accepted.
func TestTopologyMissingClassLeavesTheControllerUnknown(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR(): {gw("eg", "infra")},
		gcGVR(): nil,
		hrGVR(): {hr("share", "apps", "eg", "infra", "share-api")},
	})
	topo := topologyOf(t, dyn)

	l := laneOf(t, topo, "HTTPRoute", "share")
	if l.Verdict == nil || l.Verdict.Role != health.Unknown || l.Verdict.Included || !strings.Contains(l.ControllerWhy, "envoy-gateway does not exist") {
		t.Fatalf("an unestablished controller is unknown: %+v %q", l.Verdict, l.ControllerWhy)
	}
	if l.Accepted {
		t.Fatal("no controller judged this attachment, so it is not shown accepted")
	}
	if len(l.OtherControllers) != 1 || l.OtherControllers[0].ControllerName != egController {
		t.Fatalf("the status that exists is kept as another controller's: %+v", l.OtherControllers)
	}
}

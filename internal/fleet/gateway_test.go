package fleet

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func gwGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
}
func hrGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
}

// gwListKinds registers every Gateway API resource the topology reads, at
// its first candidate version, plus extra: a fake dynamic client panics on
// a list it was not told about.
func gwListKinds(extra map[schema.GroupVersionResource]string) map[schema.GroupVersionResource]string {
	kinds := map[string]string{
		"gateways": "GatewayList", "httproutes": "HTTPRouteList", "gatewayclasses": "GatewayClassList",
		"listenersets": "ListenerSetList", "referencegrants": "ReferenceGrantList", "grpcroutes": "GRPCRouteList",
		"tlsroutes": "TLSRouteList", "tcproutes": "TCPRouteList", "udproutes": "UDPRouteList",
	}
	out := map[schema.GroupVersionResource]string{}
	for res, list := range kinds {
		out[schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: res}] = list
	}
	for gvr, list := range extra {
		out[gvr] = list
	}
	return out
}

// seedGW builds a fake dynamic client and seeds the given objects under their
// exact GVRs via Create. The default fake plural-guesser maps "Gateway" to
// "gatewaies", so constructor-varargs seeding would key the object under the
// wrong resource; Create on the explicit GVR avoids that.
func seedGW(t *testing.T, objs map[schema.GroupVersionResource][]*unstructured.Unstructured) dynamic.Interface {
	t.Helper()
	scheme := dynScheme()
	listKinds := gwListKinds(nil)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	if _, ok := objs[gcGVR()]; !ok {
		// Every fixture Gateway names envoy-gateway; its class must exist for a
		// route to be judged by a controller at all.
		objs[gcGVR()] = []*unstructured.Unstructured{egClass()}
	}
	for gvr, items := range objs {
		for _, o := range items {
			ns, _, _ := unstructured.NestedString(o.Object, "metadata", "namespace")
			if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
				t.Fatalf("seed %s: %v", gvr, err)
			}
		}
	}
	return dyn
}

// egController is the controller the fixtures' GatewayClass names and their
// route statuses report: S7b judges a route by its parent's controller.
const egController = "gateway.envoyproxy.io/gatewayclass-controller"

func egClass() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "GatewayClass",
		"metadata": map[string]interface{}{"name": "envoy-gateway"},
		"spec":     map[string]interface{}{"controllerName": egController},
	}}
}

// tcp80 is the port a Service on port 80 publishes in its EndpointSlices:
// S7b counts only the slices that carry the selected Service port.
func tcp80() []discoveryv1.EndpointPort {
	name, proto, port := "", corev1.ProtocolTCP, int32(80)
	return []discoveryv1.EndpointPort{{Name: &name, Protocol: &proto, Port: &port}}
}

func gw(name, ns string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway",
		"metadata": map[string]interface{}{"name": name, "namespace": ns},
		"spec":     map[string]interface{}{"gatewayClassName": "envoy-gateway", "listeners": []interface{}{map[string]interface{}{"name": "http", "protocol": "HTTP", "port": int64(80)}}},
		"status": map[string]interface{}{
			"addresses":  []interface{}{map[string]interface{}{"type": "IPAddress", "value": "192.0.2.10"}},
			"conditions": []interface{}{map[string]interface{}{"type": "Accepted", "status": "True"}, map[string]interface{}{"type": "Programmed", "status": "True"}},
		},
	}}
}

func hr(name, ns, gwName, gwNS, backend string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"parentRefs": []interface{}{map[string]interface{}{"name": gwName, "namespace": gwNS}},
			"rules":      []interface{}{map[string]interface{}{"backendRefs": []interface{}{map[string]interface{}{"name": backend, "port": int64(80), "weight": int64(100)}}}},
		},
		"status": map[string]interface{}{"parents": []interface{}{map[string]interface{}{"parentRef": map[string]interface{}{"name": gwName, "namespace": gwNS}, "controllerName": egController, "conditions": []interface{}{map[string]interface{}{"type": "Accepted", "status": "True"}, map[string]interface{}{"type": "ResolvedRefs", "status": "True"}}}}},
	}}
}

func TestGetGatewayTopology(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR(): {gw("eg", "infra")},
		hrGVR(): {hr("share", "apps", "eg", "infra", "share-api")},
	})

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "share-api", Namespace: "apps"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	ready := true
	eps := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "share-api-abc", Namespace: "apps", Labels: map[string]string{"kubernetes.io/service-name": "share-api"}},
		Ports:      tcp80(),
		Endpoints:  []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: &ready}}, {Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
	}
	typed := typedfake.NewSimpleClientset(svc, eps)

	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if topo.Gateway.Name != "eg" || !topo.Gateway.Programmed {
		t.Fatalf("gateway: %+v", topo.Gateway)
	}
	if len(topo.Routes) != 1 || topo.Routes[0].Name != "share" || !topo.Routes[0].Accepted {
		t.Fatalf("routes: %+v", topo.Routes)
	}
	r := topo.Routes[0]
	if len(r.Services) != 1 || r.Services[0].Name != "share-api" || !r.Services[0].Resolved {
		t.Fatalf("service: %+v", r.Services)
	}
	if r.Pods.Ready != 2 || r.Pods.Total != 2 || r.Pods.Unknown {
		t.Fatalf("pods: %+v", r.Pods)
	}
}

func TestGetGatewayTopologyUnresolvedBackendWarns(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR(): {gw("eg", "infra")},
		hrGVR(): {hr("share", "apps", "eg", "infra", "missing-svc")},
	})
	typed := typedfake.NewSimpleClientset() // no service

	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology must still render: %v", err)
	}
	if len(topo.Routes) != 1 || topo.Routes[0].Services[0].Resolved {
		t.Fatalf("backend should be unresolved: %+v", topo.Routes)
	}
	if len(topo.Warnings) == 0 {
		t.Fatal("an unresolved backend must produce a warning")
	}
}

// hrBackends builds an HTTPRoute with an arbitrary ordered list of backendRefs,
// each a {kind, name} pair, so a route can mix non-Service and Service kinds.
func hrBackends(name, ns, gwName, gwNS string, backends []map[string]interface{}) *unstructured.Unstructured {
	refs := make([]interface{}, 0, len(backends))
	for _, b := range backends {
		ref := map[string]interface{}{"name": b["name"], "port": int64(80), "weight": int64(100)}
		if k, ok := b["kind"]; ok {
			ref["kind"] = k
		}
		refs = append(refs, ref)
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]interface{}{"name": name, "namespace": ns},
		"spec": map[string]interface{}{
			"parentRefs": []interface{}{map[string]interface{}{"name": gwName, "namespace": gwNS}},
			"rules":      []interface{}{map[string]interface{}{"backendRefs": refs}},
		},
		"status": map[string]interface{}{"parents": []interface{}{map[string]interface{}{"parentRef": map[string]interface{}{"name": gwName, "namespace": gwNS}, "controllerName": egController, "conditions": []interface{}{map[string]interface{}{"type": "Accepted", "status": "True"}, map[string]interface{}{"type": "ResolvedRefs", "status": "True"}}}}},
	}}
}

// A route whose FIRST backendRef is a non-Service kind must still get a real pod
// count for its real (later) Service backend - the count follows the first
// resolved Service, not array position 0.
func TestGetGatewayTopologyNonServiceFirstBackendCounts(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR(): {gw("eg", "infra")},
		hrGVR(): {hrBackends("share", "apps", "eg", "infra", []map[string]interface{}{
			{"kind": "Backend", "name": "raw-backend"},
			{"kind": "Service", "name": "api"},
		})},
	})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	ready := true
	pod := &corev1.ObjectReference{Kind: "Pod", Namespace: "apps", Name: "api-1", UID: "uid-api-1"}
	eps := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "api-abc", Namespace: "apps", Labels: map[string]string{"kubernetes.io/service-name": "api"}},
		Ports:      tcp80(),
		Endpoints:  []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: &ready}, TargetRef: pod}},
	}
	typed := typedfake.NewSimpleClientset(svc, eps)
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	r := topo.Routes[0]
	if r.Pods.Ready != 1 || r.Pods.Total != 1 || r.Pods.Unknown {
		t.Fatalf("primary pod count must follow the Service backend, not array index 0: %+v", r.Pods)
	}
}

// A dual-stack Service lists the same pods in its IPv4 and IPv6 EndpointSlices;
// each pod must be counted once, not once per slice.
func TestGetGatewayTopologyDualStackCountsPodsOnce(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR(): {gw("eg", "infra")},
		hrGVR(): {hr("share", "apps", "eg", "infra", "share-api")},
	})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "share-api", Namespace: "apps"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	ready := true
	pod := &corev1.ObjectReference{Kind: "Pod", Namespace: "apps", Name: "share-api-1", UID: "uid-share-1"}
	v4 := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: "share-api-v4", Namespace: "apps", Labels: map[string]string{"kubernetes.io/service-name": "share-api"}},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports:       tcp80(),
		Endpoints:   []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: &ready}, TargetRef: pod}},
	}
	v6 := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: "share-api-v6", Namespace: "apps", Labels: map[string]string{"kubernetes.io/service-name": "share-api"}},
		AddressType: discoveryv1.AddressTypeIPv6,
		Ports:       tcp80(),
		Endpoints:   []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: &ready}, TargetRef: pod}},
	}
	typed := typedfake.NewSimpleClientset(svc, v4, v6)
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	r := topo.Routes[0]
	if r.Pods.Ready != 1 || r.Pods.Total != 1 {
		t.Fatalf("dual-stack pod must be counted once across slices: %+v", r.Pods)
	}
}

func TestGetGatewayTopologyGlobalService(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR(): {gw("eg", "infra")},
		hrGVR(): {hr("share", "apps", "eg", "infra", "share-api")},
	})
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "share-api", Namespace: "apps", Annotations: map[string]string{"service.cilium.io/global": "true"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	typed := typedfake.NewSimpleClientset(svc)
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if len(topo.Routes) != 1 || len(topo.Routes[0].Services) != 1 || !topo.Routes[0].Services[0].Global {
		t.Fatalf("service should be marked global: %+v", topo.Routes[0].Services)
	}
}

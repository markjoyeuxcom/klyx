package fleet

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gwapi"
)

// Redesign step 6 (Codex review): each backend carries its own evidence, so
// a lane is never diagnosed with another backend's endpoint numbers.
func TestGatewayBackendsCarryTheirOwnEvidence(t *testing.T) {
	dyn := seedGW(t, map[schema.GroupVersionResource][]*unstructured.Unstructured{
		gwGVR(): {gw("eg", "infra")},
		hrGVR(): {hrBackends("share", "apps", "eg", "infra", []map[string]interface{}{
			{"kind": "Service", "name": "missing"},
			{"kind": "Service", "name": "ready"},
			{"kind": "Service", "name": "locked"},
			{"kind": "Backend", "name": "raw"},
		})},
	})
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "ready", Namespace: "apps"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}}
	yes := true
	eps := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "ready-1", Namespace: "apps", Labels: map[string]string{"kubernetes.io/service-name": "ready"}},
		Endpoints: []discoveryv1.Endpoint{
			{Conditions: discoveryv1.EndpointConditions{Ready: &yes}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "p1", UID: "u1"}},
			{Conditions: discoveryv1.EndpointConditions{Ready: &yes}}, // an endpoint with no pod behind it
		},
	}
	typed := typedfake.NewSimpleClientset(svc, eps)
	typed.PrependReactor("get", "services", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.(k8stesting.GetAction).GetName() == "locked" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "locked", nil)
		}
		return false, nil, nil
	})
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})

	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("topology: %v", err)
	}
	if !topo.RoutesComplete || topo.RoutesError != "" {
		t.Fatalf("route coverage: %v %q", topo.RoutesComplete, topo.RoutesError)
	}
	b := topo.Routes[0].Backends
	if b[0].State != gwapi.BackendNotFound || b[0].Endpoints != nil {
		t.Fatalf("first backend must be not-found with no endpoints of its own: %+v", b[0])
	}
	if b[1].State != gwapi.BackendResolved || b[1].Endpoints == nil || b[1].Endpoints.Ready != 2 || b[1].Endpoints.Total != 2 || b[1].Endpoints.NonPod != 1 {
		t.Fatalf("second backend endpoints: %+v %+v", b[1], b[1].Endpoints)
	}
	if b[2].State != gwapi.BackendUnreadable || b[2].Error == "" {
		t.Fatalf("a forbidden GET is unreadable, never not-found: %+v", b[2])
	}
	if b[3].State != gwapi.BackendUnsupported {
		t.Fatalf("a non-Service kind is unsupported, not 'no backend': %+v", b[3])
	}
}

// A failed HTTPRoute list is coverage: no routes, RoutesComplete=false, named.
func TestGatewayRouteListFailureIsCoverage(t *testing.T) {
	scheme := dynScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{gwGVR(): "GatewayList", hrGVR(): "HTTPRouteList"})
	if _, err := dyn.Resource(gwGVR()).Namespace("infra").Create(context.Background(), gw("eg", "infra"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	dyn.PrependReactor("list", "httproutes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "gateway.networking.k8s.io", Resource: "httproutes"}, "", nil)
	})
	c := NewClusterConn("x", typedfake.NewSimpleClientset(), nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	topo, err := c.GetGatewayTopology(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatalf("the Gateway itself was readable: %v", err)
	}
	if topo.RoutesComplete || topo.RoutesError == "" || len(topo.Routes) != 0 {
		t.Fatalf("coverage must be named: complete=%v err=%q routes=%d", topo.RoutesComplete, topo.RoutesError, len(topo.Routes))
	}
}

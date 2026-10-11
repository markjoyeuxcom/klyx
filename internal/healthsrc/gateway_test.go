package healthsrc

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/observe"
)

func gw(name string, accepted string) *unstructured.Unstructured {
	u := cr("gateway.networking.k8s.io/v1", "Gateway", "infra", name, map[string]any{"gatewayClassName": "eg",
		"listeners": []any{map[string]any{"name": "https", "protocol": "HTTPS", "port": int64(443)}, map[string]any{"name": "http", "protocol": "HTTP", "port": int64(80)}}},
		[]any{cond("Accepted", accepted, "R"), cond("Programmed", "True", "R")}, nil)
	return u
}

func httpRoute(name string, parents []string, backend string, accepted map[string]string) *unstructured.Unstructured {
	var refs, status []any
	for _, p := range parents {
		refs = append(refs, map[string]any{"name": p, "namespace": "infra"})
		a := accepted[p]
		if a == "" {
			a = "True"
		}
		status = append(status, map[string]any{"parentRef": map[string]any{"name": p, "namespace": "infra"}, "controllerName": "gateway.envoyproxy.io/gatewayclass-controller",
			"conditions": []any{cond("Accepted", a, "R"), cond("ResolvedRefs", "True", "R")}})
	}
	u := cr("gateway.networking.k8s.io/v1", "HTTPRoute", "shop", name, map[string]any{"parentRefs": refs,
		"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": backend, "port": int64(80)}}}}}, nil, nil)
	u.Object["status"] = map[string]any{"parents": status}
	return u
}

func slice(svc string, ready ...bool) *discoveryv1.EndpointSlice {
	es := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: svc + "-abc", Namespace: "shop", UID: types.UID("es-" + svc),
		Labels: map[string]string{discoveryv1.LabelServiceName: svc, "endpointslice.kubernetes.io/managed-by": "endpointslice-controller.k8s.io"}},
		AddressType: discoveryv1.AddressTypeIPv4}
	for i, r := range ready {
		rr := r
		es.Endpoints = append(es.Endpoints, discoveryv1.Endpoint{Addresses: []string{"10.0.0.1"}, Conditions: discoveryv1.EndpointConditions{Ready: &rr},
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: "shop", Name: svc + "-pod", UID: types.UID(svc + "-pod-" + string(rune('a'+i)))}})
	}
	return es
}

func gatewayListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}: "GatewayClassList",
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}:       "GatewayList",
		{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}:     "HTTPRouteList",
	}
}

func startGateway(t *testing.T, served bool, dynObjs []runtime.Object, typed *fake.Clientset, metaObjs ...runtime.Object) (*sink, *observe.Observer) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// The fake guesses a kind's resource ("Gateway" -> "gatewaies"), so
	// objects go in through the client at their real resource.
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gatewayListKinds())
	for _, o := range dynObjs {
		u := o.(*unstructured.Unstructured)
		res := map[string]string{"Gateway": "gateways", "HTTPRoute": "httproutes", "GatewayClass": "gatewayclasses"}[u.GetKind()]
		gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: res}
		if _, err := dyn.Resource(gvr).Namespace(u.GetNamespace()).Create(context.Background(), u, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	probe := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		if group == "gateway.networking.k8s.io" && served {
			return observe.Probe{Served: true, Version: "v1"}, nil
		}
		return observe.Probe{Served: false}, nil
	}
	kinds, domains := Gateway(NativeDeps{Cluster: "c1", Dyn: dyn, Served: probe, Typed: typed, Meta: metafake.NewSimpleMetadataClient(scheme, metaObjs...)})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: domains, Throttle: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	return s, o
}

func serviceMeta(name string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("svc-" + name)}}
}

func TestGatewayTaskOnFixtures(t *testing.T) {
	dynObjs := []runtime.Object{
		gw("eg", "True"), gw("rejecting", "False"),
		httpRoute("web", []string{"eg"}, "web", nil),                                                   // healthy
		httpRoute("api", []string{"eg"}, "api", nil),                                                   // healthy Gateway, backend Service missing
		httpRoute("dual", []string{"eg", "rejecting"}, "web", map[string]string{"rejecting": "False"}), // one healthy, one failing parent
		httpRoute("orphan", []string{"gone"}, "web", nil),                                              // its only parent is not in the model
		httpRoute("thin", []string{"eg"}, "thin", nil),                                                 // 1 of 3 endpoints ready
	}
	typed := fake.NewClientset(slice("web", true, true, true), slice("thin", true, false, false))
	s, _ := startGateway(t, true, dynObjs, typed, serviceMeta("web"), serviceMeta("thin"))

	waitFor(t, "gateways: the rejecting Gateway is the one fault", func() bool {
		p, ok := nativePart(s, TaskGateway, "gateways")
		return ok && p.Coverage == observe.Live && p.Native.Total == 2 && p.Native.Included == 1 && p.Top[0].Name == "rejecting" && p.Top[0].Label == "not accepted"
	})
	waitFor(t, "routes: counted once each, worst attachment, orphan not counted", func() bool {
		p, ok := nativePart(s, TaskGateway, "routes")
		if !ok || p.Coverage != observe.Live || p.Native.Total != 4 || p.Native.Included != 3 {
			return false
		}
		byName := map[string]observe.Ref{}
		for _, r := range p.Top {
			byName[r.Name] = r
		}
		return byName["api"].Label == "backend not found" && byName["api"].Parent == "infra/eg" &&
			byName["dual"].Role == "fault" && byName["dual"].Parent == "infra/rejecting" && byName["dual"].Label == "not accepted" &&
			byName["thin"].Role == "degraded" && byName["thin"].Label == "1/3 endpoints ready" && byName["api"].UID == "uid-api"
	})
}

func TestGatewayEvidenceIsNotWatchedWithoutTheGatewayAPI(t *testing.T) {
	typed := fake.NewClientset(slice("web", true))
	s, _ := startGateway(t, false, nil, typed)
	waitFor(t, "the task is absent", func() bool {
		tk, ok := task(s, TaskGateway)
		return ok && tk.Coverage == observe.Absent
	})
	time.Sleep(50 * time.Millisecond)
	for _, a := range typed.Actions() {
		if a.GetResource().Resource == "endpointslices" {
			t.Fatalf("EndpointSlices are watched only where the Gateway API is served: %v", a)
		}
	}
}

func TestEndpointSliceProjectionParity(t *testing.T) {
	raw := []discoveryv1.EndpointSlice{*slice("web", true, false, true), *slice("web", true)}
	raw[1].Name, raw[1].UID = "web-v6", "es-web-v6"
	raw[1].Endpoints[0].TargetRef.UID = "web-pod-a" // dual-stack: the same pod in both slices
	var proj []discoveryv1.EndpointSlice
	for i := range raw {
		p, err := ProjectEndpointSlice(&raw[i])
		if err != nil {
			t.Fatal(err)
		}
		proj = append(proj, *p.(*discoveryv1.EndpointSlice))
	}
	if a, b := gwapi.CountEndpoints(raw), gwapi.CountEndpoints(proj); !reflect.DeepEqual(a, b) || a.Total != 3 {
		t.Fatalf("endpoint counts differ after projection: raw %+v proj %+v", a, b)
	}
	if proj[0].Endpoints[0].Addresses != nil {
		t.Fatal("the projection keeps addresses it should drop")
	}
}

package healthsrc

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	krt "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/observe"
)

// Coverage S7 §8's full-budget case: every budgeted Gateway API kind - the
// classes, Gateways, ListenerSets, the five route kinds and ReferenceGrants
// (S7b) - and the namespaces kind with its labels, filled past its budget,
// read through the real kinds, parts and rows. Retained is the heap the
// running Observer holds after its first complete lists with the gateways,
// listenersets and routes rows subscribed, over a baseline taken with the
// fakes already holding every object; it is reported beside the encoded
// budgets it is bounded by.
//
//	go test ./internal/healthsrc -run TestGatewayAPIObserverHeap -v
func TestGatewayAPIObserverHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("calibration")
	}
	const (
		classes    = 200  // 64 KiB budget
		gateways   = 200  // 1 MiB budget, ten listeners each
		sets       = 600  // 512 KiB budget, two listeners each
		namespaces = 3000 // 256 KiB budget
		// S7b: four-rule routes of two backends and two matches each
		// (about 1.5 KiB encoded), and grants of two froms
		httproutes = 1500 // 1 MiB budget
		grpcroutes = 400  // 256 KiB budget
		tlsroutes  = 400  // 256 KiB budget
		tcproutes  = 300  // 128 KiB budget
		udproutes  = 300  // 128 KiB budget
		grants     = 800  // 128 KiB budget
	)
	var dynObjs []krt.Object
	for i := 0; i < classes; i++ {
		u := cr("gateway.networking.k8s.io/v1", "GatewayClass", "", fmt.Sprintf("class-%03d", i), map[string]any{"controllerName": fmt.Sprintf("example.dev/controller-%03d", i)},
			[]any{cond("Accepted", "True", "Accepted")}, map[string]any{"supportedFeatures": []any{map[string]any{"name": "Gateway"}, map[string]any{"name": "HTTPRoute"}, map[string]any{"name": "ListenerSet"}}})
		dynObjs = append(dynObjs, u)
	}
	for i := 0; i < gateways; i++ {
		u := gw(fmt.Sprintf("gw-%03d", i), "True")
		u.SetUID(types.UID(fmt.Sprintf("gw-%03d", i)))
		loadListeners(u, 10)
		u.Object["spec"].(map[string]any)["allowedListeners"] = map[string]any{"namespaces": map[string]any{"from": "All"}}
		dynObjs = append(dynObjs, u)
	}
	for i := 0; i < sets; i++ {
		u := cr("gateway.networking.k8s.io/v1", "ListenerSet", fmt.Sprintf("team-%03d", i%100), fmt.Sprintf("ls-%03d", i),
			map[string]any{"parentRef": map[string]any{"name": fmt.Sprintf("gw-%03d", i%gateways), "namespace": "infra"}},
			[]any{cond("Accepted", "True", "Accepted"), cond("Programmed", "True", "Programmed")}, nil)
		u.SetUID(types.UID(fmt.Sprintf("ls-%03d", i)))
		u.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)))
		loadListeners(u, 2)
		dynObjs = append(dynObjs, u)
	}
	route := func(kind string, i int) *unstructured.Unstructured {
		var rules []any
		for r := 0; r < 4; r++ {
			rule := map[string]any{"backendRefs": []any{
				map[string]any{"name": fmt.Sprintf("svc-%03d-a", i%200), "port": int64(80 + r), "weight": int64(90)},
				map[string]any{"name": fmt.Sprintf("svc-%03d-b", i%200), "port": int64(80 + r), "weight": int64(10)}}}
			switch kind {
			case "HTTPRoute":
				rule["matches"] = []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": fmt.Sprintf("/api/v%d", r)}}, map[string]any{"path": map[string]any{"type": "Exact", "value": "/healthz"}}}
			case "GRPCRoute":
				rule["matches"] = []any{map[string]any{"method": map[string]any{"service": "shop.Catalog", "method": fmt.Sprintf("Get%d", r)}}}
			}
			rules = append(rules, rule)
		}
		spec := map[string]any{"parentRefs": []any{map[string]any{"name": fmt.Sprintf("gw-%03d", i%gateways), "namespace": "infra", "sectionName": "l0"}}, "rules": rules}
		if kind == "HTTPRoute" || kind == "GRPCRoute" || kind == "TLSRoute" {
			spec["hostnames"] = []any{fmt.Sprintf("r%04d.shop.example", i)}
		}
		u := cr("gateway.networking.k8s.io/v1", kind, fmt.Sprintf("team-%03d", i%100), fmt.Sprintf("%s-%04d", strings.ToLower(kind), i), spec, nil, nil)
		u.SetUID(types.UID(fmt.Sprintf("%s-%04d", kind, i)))
		u.Object["status"] = map[string]any{"parents": []any{map[string]any{"parentRef": spec["parentRefs"].([]any)[0], "controllerName": "example.dev/controller-000",
			"conditions": []any{cond("Accepted", "True", "Accepted"), cond("ResolvedRefs", "True", "ResolvedRefs")}}}}
		return u
	}
	for kind, n := range map[string]int{"HTTPRoute": httproutes, "GRPCRoute": grpcroutes, "TLSRoute": tlsroutes, "TCPRoute": tcproutes, "UDPRoute": udproutes} {
		for i := 0; i < n; i++ {
			dynObjs = append(dynObjs, route(kind, i))
		}
	}
	for i := 0; i < grants; i++ {
		u := cr("gateway.networking.k8s.io/v1", "ReferenceGrant", fmt.Sprintf("team-%03d", i%100), fmt.Sprintf("grant-%04d", i), map[string]any{
			"from": []any{map[string]any{"group": gwapi.Group, "kind": "HTTPRoute", "namespace": "shop"}, map[string]any{"group": gwapi.Group, "kind": "GRPCRoute", "namespace": "shop"}},
			"to":   []any{map[string]any{"group": "", "kind": "Service"}}}, nil, nil)
		u.SetUID(types.UID(fmt.Sprintf("grant-%04d", i)))
		dynObjs = append(dynObjs, u)
	}
	listKinds := gatewayListKinds()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(krt.NewScheme(), listKinds)
	res := map[string]string{"Gateway": "gateways", "GatewayClass": "gatewayclasses", "ListenerSet": "listenersets",
		"HTTPRoute": "httproutes", "GRPCRoute": "grpcroutes", "TLSRoute": "tlsroutes", "TCPRoute": "tcproutes", "UDPRoute": "udproutes", "ReferenceGrant": "referencegrants"}
	for _, o := range dynObjs {
		u := o.(*unstructured.Unstructured)
		gvr := schema.GroupVersionResource{Group: gwapi.Group, Version: "v1", Resource: res[u.GetKind()]}
		if _, err := dyn.Resource(gvr).Namespace(u.GetNamespace()).Create(context.Background(), u, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	scheme := krt.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var nsObjs []krt.Object
	for i := 0; i < namespaces; i++ {
		name := fmt.Sprintf("team-%04d", i)
		nsObjs = append(nsObjs, &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("ns-" + name), Labels: map[string]string{
				"kubernetes.io/metadata.name": name, "team": fmt.Sprintf("team-%d", i%20), "env": "prd", "cost-center": fmt.Sprintf("cc-%04d", i%300), "owner": "platform"}}})
	}
	meta := metafake.NewSimpleMetadataClient(scheme, nsObjs...)
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		return observe.Probe{Served: group == gwapi.Group, Version: "v1"}, nil
	}
	nd := NativeDeps{Cluster: "c1", Dyn: dyn, Served: served, Typed: fake.NewClientset(), Meta: meta}
	kinds, domains := Gateway(nd)
	kinds = append(append(SharedNetworkKinds(nd), namespacesKind(meta)), kinds...)
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: domains, NamespacesKind: KindNamespaces, Throttle: 10 * time.Millisecond})
	before := heapNow()
	ctx, cancel := context.WithCancel(context.Background())
	o.Start(ctx)
	o.SubscribeRows(TaskGateway, "gateways", observe.Owner{Kind: "lens", ID: "t"})
	o.SubscribeRows(TaskGateway, "listenersets", observe.Owner{Kind: "lens", ID: "t"})
	o.SubscribeRows(TaskGateway, "routes", observe.Owner{Kind: "lens", ID: "t"})
	budgeted := []string{KindGatewayClasses, KindGateways, KindListenerSets, KindNamespaces,
		KindHTTPRoutes, KindGRPCRoutes, KindTLSRoutes, KindTCPRoutes, KindUDPRoutes, KindReferenceGrants}
	held := map[string][2]int{}
	waitFor(t, "every budgeted kind's first complete list, truncated", func() bool {
		var parts []observe.Part
		for _, id := range []string{"gateways", "listenersets", "routes"} {
			p, ok := nativePart(s, TaskGateway, id)
			if !ok || p.Coverage != observe.Live {
				return false
			}
			parts = append(parts, p)
		}
		for _, p := range parts {
			for _, k := range p.Kinds {
				if k.Truncated {
					held[k.Kind] = [2]int{k.Objects, k.NotRetained}
				}
			}
		}
		return len(held) == len(budgeted)
	})
	for _, id := range []string{"listenersets", "routes"} {
		if _, ok := o.RowsNow(TaskGateway, id); !ok {
			t.Fatalf("%s rows not published", id)
		}
	}
	after := heapNow()
	retained := int64(after) - int64(before)
	budget := budgetGatewayClasses + budgetGateways + budgetListenerSets + budgetNamespaces +
		budgetHTTPRoutes + budgetGRPCRoutes + budgetTLSRoutes + budgetTCPRoutes + budgetUDPRoutes + budgetReferenceGrants
	for _, k := range budgeted {
		t.Logf("%s: %d retained, %d not retained", k, held[k][0], held[k][1])
	}
	t.Logf("the Observer holds %d heap bytes (%.2f MiB) with every S7 kind at its budget (%d encoded bytes, %.2f MiB): %.2f heap bytes per budget byte",
		retained, float64(retained)/(1<<20), budget, float64(budget)/(1<<20), float64(retained)/float64(budget))
	if s := summaryNamespaces(s); s != namespaces {
		t.Errorf("the namespace count past the budget: %d, want %d", s, namespaces)
	}
	cancel()
	<-o.Done()
	runtime.KeepAlive(dyn)
	runtime.KeepAlive(meta)
}

func summaryNamespaces(s *sink) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last.Namespaces == nil {
		return -1
	}
	return *s.last.Namespaces
}

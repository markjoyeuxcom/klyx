package fleet

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/routemetrics"
)

// The Line slice 7c at the fleet layer: the Gateway's EnvoyProxy settings,
// its attached routes and their rules, and the proxy pods, ahead of the
// per-rule reads (internal/routemetrics holds the series fixtures).

func gcGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
}
func epGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Resource: "envoyproxies"}
}

// ruleQ answers the liveness probe and the per-rule queries, recording them.
type ruleQ struct {
	mu      sync.Mutex
	queries []string
}

func (q *ruleQ) record(s string) {
	q.mu.Lock()
	q.queries = append(q.queries, s)
	q.mu.Unlock()
}

func (q *ruleQ) InstantQuery(_ context.Context, promql string) (int, []byte, error) {
	q.record(promql)
	if strings.HasPrefix(promql, "last_over_time(") {
		return 200, []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"envoy_cluster_name":"httproute/apps/web/rule/0",` +
			`"gateway_envoyproxy_io_owning_gateway_name":"eg","gateway_envoyproxy_io_owning_gateway_namespace":"infra"},"value":[1,"5"]}]}}`), nil
	}
	return 200, []byte(emptyOrLive(promql)), nil
}

func emptyOrLive(promql string) string {
	if strings.Contains(promql, "vector(1)") {
		return `{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"1"]}]}}`
	}
	return `{"status":"success","data":{"resultType":"vector","result":[]}}`
}

func (q *ruleQ) RangeQuery(_ context.Context, promql string, _, _ time.Time, _ time.Duration) (int, []byte, error) {
	q.record(promql)
	return 200, []byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"envoy_cluster_name":"httproute/apps/web/rule/0"},"values":[[100,"2"]]}]}}`), nil
}

func (q *ruleQ) prom() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []string
	for _, s := range q.queries {
		if !strings.Contains(s, "vector(1)") {
			out = append(out, s)
		}
	}
	return out
}

func ruleDiscovery() []*metav1.APIResourceList {
	return []*metav1.APIResourceList{
		{GroupVersion: "gateway.networking.k8s.io/v1", APIResources: []metav1.APIResource{
			{Name: "gateways", Namespaced: true, Kind: "Gateway"},
			{Name: "httproutes", Namespaced: true, Kind: "HTTPRoute"},
			{Name: "gatewayclasses", Namespaced: false, Kind: "GatewayClass"},
		}},
		{GroupVersion: "gateway.envoyproxy.io/v1alpha1", APIResources: []metav1.APIResource{
			{Name: "envoyproxies", Namespaced: true, Kind: "EnvoyProxy"},
		}},
	}
}

type ruleEnv struct {
	gwInfraRef, classRef map[string]any // parametersRefs, nil for none
	proxies              []*unstructured.Unstructured
	routes               []*unstructured.Unstructured
	hasEnvoy             bool
	q                    *ruleQ
}

func (e ruleEnv) conn(t *testing.T) *ClusterConn {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{
		gwGVR(): "GatewayList", hrGVR(): "HTTPRouteList", gcGVR(): "GatewayClassList", epGVR(): "EnvoyProxyList"})
	create := func(gvr schema.GroupVersionResource, o *unstructured.Unstructured) {
		if _, err := dyn.Resource(gvr).Namespace(o.GetNamespace()).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", gvr, err)
		}
	}
	g := gw("eg", "infra")
	if e.gwInfraRef != nil {
		_ = unstructured.SetNestedMap(g.Object, e.gwInfraRef, "spec", "infrastructure", "parametersRef")
	}
	create(gwGVR(), g)
	gc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "GatewayClass",
		"metadata": map[string]any{"name": "envoy-gateway"}, "spec": map[string]any{"controllerName": "gateway.envoyproxy.io/gatewayclass-controller"}}}
	if e.classRef != nil {
		_ = unstructured.SetNestedMap(gc.Object, e.classRef, "spec", "parametersRef")
	}
	create(gcGVR(), gc)
	for _, p := range e.proxies {
		create(epGVR(), p)
	}
	for _, r := range e.routes {
		create(hrGVR(), r)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "envoy-infra-eg-1", Namespace: "envoy-gateway-system",
		Labels: map[string]string{labelOwningGatewayName: "eg", labelOwningGatewayNamespace: "infra"}}}
	typed := typedfake.NewSimpleClientset(pod)
	typed.Resources = ruleDiscovery()
	c := NewClusterConn("prd", typed, nil, dyn, nil, clock.NewFake(time.Unix(1792080000, 500)), config.MetricsConfig{Endpoint: "http://prom"})
	c.metricsTF = fakeTF{q: e.q}
	c.caps = capability.Set{Network: capability.NetworkCapability{HasEnvoyProxy: e.hasEnvoy}}
	return c
}

func envoyProxy(ns, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "gateway.envoyproxy.io/v1alpha1", "kind": "EnvoyProxy",
		"metadata": map[string]any{"namespace": ns, "name": name}, "spec": spec}}
}

func epRef(ns, name string) map[string]any {
	m := map[string]any{"group": "gateway.envoyproxy.io", "kind": "EnvoyProxy", "name": name}
	if ns != "" {
		m["namespace"] = ns
	}
	return m
}

func ruleRoutes() []*unstructured.Unstructured {
	web := hr("web", "apps", "eg", "infra", "web")
	rules, _, _ := unstructured.NestedSlice(web.Object, "spec", "rules")
	_ = unstructured.SetNestedSlice(web.Object, append(rules, map[string]any{"backendRefs": []any{map[string]any{"name": "web-v2", "port": int64(80)}}}), "spec", "rules")
	web.SetUID("uid-web")
	twice := hr("twice", "apps", "eg", "infra", "api")
	_ = unstructured.SetNestedSlice(twice.Object, []any{
		map[string]any{"name": "eg", "namespace": "infra", "sectionName": "http"},
		map[string]any{"name": "eg", "namespace": "infra", "sectionName": "https"},
	}, "spec", "parentRefs")
	twice.SetUID("uid-twice")
	other := hr("other", "apps", "other-gw", "infra", "api")
	other.SetUID("uid-other")
	return []*unstructured.Unstructured{web, twice, other}
}

func TestRuleMetricsRoutesAndScope(t *testing.T) {
	q := &ruleQ{}
	e := ruleEnv{hasEnvoy: true, q: q, routes: ruleRoutes(),
		gwInfraRef: epRef("", "eg-proxy"), classRef: epRef("envoy-gateway-system", "default"),
		proxies: []*unstructured.Unstructured{
			envoyProxy("infra", "eg-proxy", map[string]any{"provider": map[string]any{"type": "Kubernetes"}}),
			envoyProxy("envoy-gateway-system", "default", map[string]any{"mergeGateways": false}),
		}}
	res, err := e.conn(t).RuleMetrics(context.Background(), "infra", "eg")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Status.Available || res.Unattributable != "" || res.Scope != routemetrics.ScopeOwningGateway {
		t.Fatalf("result: %+v", res)
	}
	// Whole seconds, so points and cut-offs compare exactly.
	if !res.End.Equal(time.Unix(1792080000, 0)) || !res.Status.UpdatedAt.Equal(res.End) {
		t.Fatalf("end %v updated %v", res.End, res.Status.UpdatedAt)
	}
	got := map[string]routemetrics.RuleSeries{}
	for _, r := range res.Rules {
		got[r.Name+"#"+strconv.Itoa(r.Rule)] = r
	}
	if len(got) != 3 || got["web#0"].UID != "uid-web" || got["web#1"].Fingerprint == "" || got["web#0"].Fingerprint == got["web#1"].Fingerprint {
		t.Fatalf("rules: %+v", res.Rules)
	}
	if got["twice#0"].Unattributable != routemetrics.ReasonSameGateway {
		t.Fatalf("twice: %+v", got["twice#0"])
	}
	if _, ok := got["other#0"]; ok {
		t.Fatal("a route of another Gateway")
	}
	if got["web#0"].Requests.Availability != routemetrics.MetricAvailable {
		t.Fatalf("web#0: %+v", got["web#0"])
	}
}

func TestRuleMetricsEnvoyProxy(t *testing.T) {
	cases := map[string]struct {
		env  ruleEnv
		want string
	}{
		"class merges backends": {ruleEnv{classRef: epRef("envoy-gateway-system", "default"),
			proxies: []*unstructured.Unstructured{envoyProxy("envoy-gateway-system", "default", map[string]any{"mergeBackends": map[string]any{}})}},
			"the EnvoyProxy envoy-gateway-system/default sets spec.mergeBackends"},
		"gateway renames clusters": {ruleEnv{gwInfraRef: epRef("", "eg-proxy"),
			proxies: []*unstructured.Unstructured{envoyProxy("infra", "eg-proxy", map[string]any{"telemetry": map[string]any{"metrics": map[string]any{"clusterStatName": "%ROUTE_NAME%"}}})}},
			"sets spec.telemetry.metrics.clusterStatName"},
		"class merges gateways": {ruleEnv{classRef: epRef("envoy-gateway-system", "default"),
			proxies: []*unstructured.Unstructured{envoyProxy("envoy-gateway-system", "default", map[string]any{"mergeGateways": true})}},
			"sets spec.mergeGateways"},
		"referenced but missing": {ruleEnv{gwInfraRef: epRef("", "gone")}, "the EnvoyProxy infra/gone could not be read"},
		"not an EnvoyProxy": {ruleEnv{classRef: map[string]any{"group": "example.com", "kind": "Config", "name": "x", "namespace": "y"}},
			"refers to Config y/x, not an EnvoyProxy"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			q := &ruleQ{}
			c.env.q, c.env.hasEnvoy, c.env.routes = q, true, ruleRoutes()
			res, err := c.env.conn(t).RuleMetrics(context.Background(), "infra", "eg")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(res.Unattributable, routemetrics.ReasonSharedClusters) || !strings.Contains(res.Unattributable, c.want) {
				t.Fatalf("reason %q, want %q", res.Unattributable, c.want)
			}
			for _, r := range res.Rules {
				if r.Unattributable != res.Unattributable {
					t.Fatalf("rule %+v", r)
				}
			}
			if p := q.prom(); len(p) != 0 {
				t.Fatalf("queried Prometheus: %v", p)
			}
		})
	}
}

func TestRuleMetricsGates(t *testing.T) {
	q := &ruleQ{}
	e := ruleEnv{q: q, routes: ruleRoutes()}
	res, err := e.conn(t).RuleMetrics(context.Background(), "infra", "eg")
	if err != nil || res.Status.Available || res.Status.Message != "Envoy Gateway not detected" {
		t.Fatalf("no Envoy: %+v %v", res.Status, err)
	}
	e.hasEnvoy = true
	if _, err := e.conn(t).RuleMetrics(context.Background(), "infra", "missing"); err == nil || !strings.Contains(err.Error(), "get gateway infra/missing") {
		t.Fatalf("an unreadable Gateway is an error: %v", err)
	}
	c := e.conn(t)
	c.metricsCfg = config.MetricsConfig{}
	c.metricsTF = nil
	res, err = c.RuleMetrics(context.Background(), "infra", "eg")
	if err != nil || res.Status.Available || len(res.Rules) != 3 {
		t.Fatalf("metrics unavailable keeps the rules: %+v %v", res, err)
	}
}

func TestFluxUnitOf(t *testing.T) {
	k := fluxUnitOf(map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"})
	if k == nil || k.Kind != "Kustomization" || k.Namespace != "flux-system" || k.Name != "apps" {
		t.Fatalf("kustomize labels: %+v", k)
	}
	h := fluxUnitOf(map[string]string{"helm.toolkit.fluxcd.io/name": "web", "helm.toolkit.fluxcd.io/namespace": "shop"})
	if h == nil || h.Kind != "HelmRelease" || h.Name != "web" {
		t.Fatalf("helm labels: %+v", h)
	}
	if fluxUnitOf(map[string]string{"kustomize.toolkit.fluxcd.io/name": "apps"}) != nil || fluxUnitOf(nil) != nil {
		t.Fatal("a half label or none names no unit")
	}
}

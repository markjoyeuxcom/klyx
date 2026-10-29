package metrics

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Settings S3b §3.3: the closed query surface.

// before are the queries Klyx sent before scopes, rendered by the code that
// sent them (copied from the history at a89d5876, its format strings
// unchanged), for each registered template's declared cases. With an empty
// scope, every template renders exactly these.
func before(name string) []string {
	cont := `container!="",container!="POD"`
	podSet := func(ns string, pods ...string) string {
		return Equals("namespace", ns) + "," + MatchAnyOf("pod", "", pods, "")
	}
	routes := MatchAnyOf("envoy_cluster_name", "httproute/", []string{"shop/web"}, "/rule/[0-9]+")
	orEach := func(scopes []string, f func(string) string) string {
		parts := make([]string, len(scopes))
		for i, sc := range scopes {
			parts[i] = f(sc)
		}
		return strings.Join(parts, " or ")
	}
	clusters := MatchAnyOf("envoy_cluster_name", "", []string{"httproute/shop/web/rule/0"}, "")
	one := Equals("namespace", "envoy-gateway-system") + "," + MatchAnyOf("pod", "", []string{"envoy-0"}, "")
	two := Equals("namespace", "edge") + "," + MatchAnyOf("pod", "", []string{"envoy-1"}, "")
	rangeOf := func(metric, extra, by string, scopes []string) string {
		inner := orEach(scopes, func(sc string) string {
			return fmt.Sprintf("rate(%s{%s,%s%s}[%s])", metric, clusters, sc, extra, "1m")
		})
		return fmt.Sprintf("sum by (%s%s) (%s)", "envoy_cluster_name", by, inner)
	}
	switch name {
	case "cluster-cpu":
		return []string{`1 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m]))`}
	case "cluster-memory":
		return []string{`1 - sum(node_memory_MemAvailable_bytes) / sum(node_memory_MemTotal_bytes)`}
	case "pod-cpu":
		q := `sum by (namespace,pod) (rate(container_cpu_usage_seconds_total{%scontainer!="",container!="POD"}[5m]))`
		return []string{fmt.Sprintf(q, ""), fmt.Sprintf(q, fmt.Sprintf("namespace=%q,", "shop"))}
	case "pod-memory":
		q := `sum by (namespace,pod) (container_memory_working_set_bytes{%scontainer!="",container!="POD"})`
		return []string{fmt.Sprintf(q, ""), fmt.Sprintf(q, fmt.Sprintf("namespace=%q,", "shop"))}
	case "container-cpu":
		return []string{fmt.Sprintf(`sum by (container) (rate(container_cpu_usage_seconds_total{namespace=%q,pod=%q,container!="",container!="POD"}[5m]))`, "shop", "web-0")}
	case "container-memory":
		return []string{fmt.Sprintf(`sum by (container) (container_memory_working_set_bytes{namespace=%q,pod=%q,container!="",container!="POD"})`, "shop", "web-0")}
	case "workload-cpu":
		return []string{fmt.Sprintf(`sum(rate(container_cpu_usage_seconds_total{%s}[5m]))`, podSet("shop", "web-0", "web-1")+","+cont)}
	case "workload-memory":
		return []string{fmt.Sprintf(`sum(container_memory_working_set_bytes{%s})`, podSet("shop", "web-0", "web-1")+","+cont)}
	case "pods-cpu-by-container":
		return []string{fmt.Sprintf(`sum by (container) (rate(container_cpu_usage_seconds_total{%s}[5m]))`, podSet("shop", "web-0")+","+cont)}
	case "pods-memory-by-container":
		return []string{fmt.Sprintf(`sum by (container) (container_memory_working_set_bytes{%s})`, podSet("shop", "web-0")+","+cont)}
	case "pods-restarts":
		return []string{fmt.Sprintf(`sum by (pod) (kube_pod_container_status_restarts_total{%s})`, podSet("shop", "web-0"))}
	case "route-rps":
		return []string{fmt.Sprintf("sum by (%s)(rate(%s{%s}[%s]))", "envoy_cluster_name", "envoy_cluster_upstream_rq_total", routes, "5m")}
	case "route-p50":
		return []string{fmt.Sprintf("histogram_quantile(0.50, sum by (%s, le)(rate(%s{%s}[%s])))", "envoy_cluster_name", "envoy_cluster_upstream_rq_time_bucket", routes, "5m")}
	case "route-p99":
		return []string{fmt.Sprintf("histogram_quantile(0.99, sum by (%s, le)(rate(%s{%s}[%s])))", "envoy_cluster_name", "envoy_cluster_upstream_rq_time_bucket", routes, "5m")}
	case "route-5xx":
		return []string{fmt.Sprintf("sum by (%s)(rate(%s{%s,%s=\"5\"}[%s]))", "envoy_cluster_name", "envoy_cluster_upstream_rq_xx", routes, "envoy_response_code_class", "5m")}
	case "route-all":
		return []string{fmt.Sprintf("sum by (%s)(rate(%s{%s}[%s]))", "envoy_cluster_name", "envoy_cluster_upstream_rq_xx", routes, "5m")}
	case "envoy-scraped":
		return []string{fmt.Sprintf("count(%s)", "envoy_cluster_upstream_rq_total")}
	case "rule-probe":
		var out []string
		for _, m := range []string{"envoy_cluster_upstream_rq_total", "envoy_cluster_upstream_rq_xx", "envoy_cluster_upstream_rq", "envoy_cluster_upstream_rq_retry"} {
			out = append(out, fmt.Sprintf("last_over_time(%s{%s}[%s])", m, clusters, "30m"))
		}
		return out
	case "rule-range":
		var out []string
		fams := []struct{ metric, extra, by string }{
			{"envoy_cluster_upstream_rq_total", "", ""},
			{"envoy_cluster_upstream_rq_xx", "", ", envoy_response_code_class"},
			{"envoy_cluster_upstream_rq", "," + Equals("envoy_response_code", "503"), ""},
			{"envoy_cluster_upstream_rq_retry", "", ""},
		}
		owning := Equals("gateway_envoyproxy_io_owning_gateway_name", "eg") + "," + Equals("gateway_envoyproxy_io_owning_gateway_namespace", "envoy-gateway-system")
		for _, f := range fams {
			out = append(out, rangeOf(f.metric, f.extra, f.by, []string{one}), rangeOf(f.metric, f.extra, f.by, []string{one, two}), rangeOf(f.metric, f.extra, f.by, []string{owning}))
		}
		return out
	case "rule-shared":
		by := []string{"gateway_envoyproxy_io_owning_gateway_name", "gateway_envoyproxy_io_owning_gateway_namespace", "pod", "namespace", "kubernetes_pod_name", "kubernetes_namespace"}
		inner := orEach([]string{"envoy_cluster_upstream_rq_total", "envoy_cluster_upstream_rq_xx", "envoy_cluster_upstream_rq", "envoy_cluster_upstream_rq_retry"}, func(m string) string {
			return fmt.Sprintf("last_over_time(%s{%s=~%s}[%s])", m, "envoy_cluster_name", strconv.Quote("^(service|serviceimport|backend)/[^/]+/[^/]+/[0-9]+(/[a-z0-9]+)?$"), "30m")
		})
		return []string{fmt.Sprintf("count by (%s) (%s)", strings.Join(by, ", "), inner)}
	case "hubble-verdicts":
		return []string{`sum by (verdict)(rate(hubble_flows_processed_total[5m]))`}
	case "hubble-drops":
		return []string{`sum by (reason, protocol)(rate(hubble_drop_total[5m]))`}
	case "hubble-dns":
		return []string{`sum by (rcode)(rate(hubble_dns_responses_total[5m]))`}
	case "hubble-http":
		return []string{`sum by (status)(rate(hubble_http_requests_total[5m]))`}
	}
	return nil
}

func TestAnEmptyScopeRendersTheQueriesSentBeforeScopes(t *testing.T) {
	rendered := Rendered(nil)
	if len(rendered) != 24 {
		t.Errorf("%d templates registered; this test knows 24: a new one needs its case here", len(rendered))
	}
	for name, cases := range rendered {
		want := before(name)
		if want == nil {
			t.Errorf("%s: no query before scopes to compare", name)
			continue
		}
		if len(want) != len(cases) {
			t.Errorf("%s: %d cases, %d before", name, len(cases), len(want))
			continue
		}
		for i := range cases {
			if cases[i] != want[i] {
				t.Errorf("%s case %d:\n got  %s\n want %s", name, i, cases[i], want[i])
			}
		}
	}
}

// recording is a querier that records what it was sent.
type recording struct{ sent []string }

func (r *recording) InstantQuery(_ context.Context, q string) (int, []byte, error) {
	r.sent = append(r.sent, q)
	return 200, []byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`), nil
}

func (r *recording) RangeQuery(_ context.Context, q string, _, _ time.Time, _ time.Duration) (int, []byte, error) {
	r.sent = append(r.sent, q)
	return 200, []byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`), nil
}

func TestTheReaderRendersWithItsOwnScope(t *testing.T) {
	rec := &recording{}
	c := NewClient(TransportOf(rec), ScopeOf(map[string]string{"team": "a b", "cluster": "dev"}))
	if _, err := c.InstantScalar(context.Background(), ClusterCPU.With(None{})); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RangeMatrix(context.Background(), WorkloadMem.With(PodSet{Namespace: "shop", Pods: []string{"web-0"}}), time.Unix(0, 0), time.Unix(60, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`1 - avg(rate(node_cpu_seconds_total{mode="idle",cluster="dev",team="a b"}[5m]))`,
		`sum(container_memory_working_set_bytes{namespace="shop",pod=~"^(web-0)$",container!="",container!="POD",cluster="dev",team="a b"})`,
	}
	if strings.Join(rec.sent, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sent:\n%s", strings.Join(rec.sent, "\n"))
	}
	// the same query under another reader's scope is that reader's: the query
	// carries none of its own
	other := &recording{}
	_, _ = NewClient(TransportOf(other), Scope{}).InstantScalar(context.Background(), ClusterCPU.With(None{}))
	if other.sent[0] != `1 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m]))` {
		t.Fatalf("unscoped: %s", other.sent[0])
	}
}

func TestAZeroOrUndeclaredQueryIsRefusedBeforeAnyRequest(t *testing.T) {
	rec := &recording{}
	c := NewClient(TransportOf(rec), Scope{})
	pod := ProxySet{Equal: [][2]string{{"pod", "a"}}}
	for _, q := range []Query{
		{},
		RuleRangeQuery.With(RuleRange{Family: "bogus", Clusters: []string{"x"}, Proxies: []ProxySet{pod}}),
		RuleRangeQuery.With(RuleRange{Family: "total", Clusters: []string{"x"}}),
		RuleProbeQuery.With(RuleProbe{Family: "up", Clusters: []string{"x"}}),
		// a proxy set that writes PromQL, or names nothing
		RuleRangeQuery.With(RuleRange{Family: "total", Clusters: []string{"x"}, Proxies: []ProxySet{{Equal: [][2]string{{`pod="a"} or vector(1) or up{a`, "b"}}}}}),
		RuleRangeQuery.With(RuleRange{Family: "total", Clusters: []string{"x"}, Proxies: []ProxySet{pod, {NameLabel: "pod{", Names: []string{"a"}}}}),
		RuleRangeQuery.With(RuleRange{Family: "total", Clusters: []string{"x"}, Proxies: []ProxySet{{}}}),
		RuleRangeQuery.With(RuleRange{Family: "total", Clusters: []string{"x"}, Proxies: []ProxySet{{Equal: [][2]string{{"namespace", "a"}}, Names: []string{"a"}}}}),
		RuleRangeQuery.With(RuleRange{Family: "total", Clusters: []string{"x"}, Proxies: []ProxySet{{NameLabel: "pod"}}}),
	} {
		if _, err := c.InstantVector(context.Background(), q); !errors.Is(err, ErrUnregisteredQuery) {
			t.Fatalf("%q: %v", q.Name(), err)
		}
		if _, err := c.RangeMatrix(context.Background(), q, time.Unix(0, 0), time.Unix(60, 0), time.Minute); !errors.Is(err, ErrUnregisteredQuery) {
			t.Fatalf("range %q: %v", q.Name(), err)
		}
	}
	if len(rec.sent) != 0 {
		t.Fatalf("a refused query was sent: %v", rec.sent)
	}
	// the controls: a declared shape is admitted
	if q := RuleRangeQuery.With(RuleRange{Family: "503", Clusters: []string{"x"}, Proxies: []ProxySet{pod, {Equal: [][2]string{{"namespace", "b"}}, NameLabel: "pod", Names: []string{"b"}}}}); q.Name() == "" {
		t.Fatal("a declared shape was refused")
	}
}

// A scope whose label is not a label name writes PromQL of its own: refused
// before any request. The configuration admits none; this is the reader's
// own check.
func TestAScopeWithAForeignLabelIsRefused(t *testing.T) {
	for _, label := range []string{`cluster="x"} or vector(1) or up{a`, "a-b", "0a", "", "__name__", "__meta_kubernetes_pod_name"} {
		rec := &recording{}
		_, err := NewClient(TransportOf(rec), ScopeOf(map[string]string{"cluster": "dev", label: "x"})).InstantScalar(context.Background(), ClusterCPU.With(None{}))
		if !errors.Is(err, ErrInvalidScope) || len(rec.sent) != 0 {
			t.Fatalf("%q: %v, sent %v", label, err, rec.sent)
		}
	}
}

func TestATemplateNeedsACaseAndItsOwnName(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    func()
	}{
		{"no case", func() { define("test-no-case", func(None, string) string { return "x" }, nil, nil) }},
		{"defined twice", func() { define("cluster-cpu", func(None, string) string { return "x" }, nil, nil, None{}) }},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: defined", tc.name)
				}
			}()
			tc.f()
		}()
	}
}

func TestSelConjoinsTheScope(t *testing.T) {
	for _, tc := range []struct{ metric, own, scope, want string }{
		{"up", "", "", "up"},
		{"up", `job="a"`, "", `up{job="a"}`},
		{"up", "", `cluster="dev"`, `up{cluster="dev"}`},
		{"up", `cluster="x"`, `cluster="dev"`, `up{cluster="x",cluster="dev"}`},
	} {
		if got := Sel(tc.metric, tc.own, tc.scope); got != tc.want {
			t.Errorf("%+v: %s", tc, got)
		}
	}
}

// The parameter forms Klyx's readers build are each a declared case's
// shape, so the proof over the declared cases covers what is sent: a case
// removed from a template fails here, never at a reader that would refuse
// its query (Settings S3b §3.3).
func TestEveryShapeTheReadersBuildIsDeclared(t *testing.T) {
	pods := ProxySet{Equal: [][2]string{{"namespace", "edge"}}, NameLabel: "pod", Names: []string{"a", "b"}}
	k8s := ProxySet{Equal: [][2]string{{"kubernetes_namespace", "edge"}}, NameLabel: "kubernetes_pod_name", Names: []string{"a"}}
	owning := ProxySet{Equal: [][2]string{{EnvoyOwnGatewayName, "gw"}, {EnvoyOwnGatewayNamespace, "edge"}}}
	built := map[string]Query{
		"pod-cpu every":    PodCPU.With(Namespace{}),
		"pod-cpu one":      PodCPU.With(Namespace{Name: "a"}),
		"pod-memory every": PodMem.With(Namespace{}),
		"pod-memory one":   PodMem.With(Namespace{Name: "a"}),
	}
	for _, fam := range ruleFamilies {
		built["rule-probe "+fam] = RuleProbeQuery.With(RuleProbe{Family: fam, Clusters: []string{"a", "b"}})
	}
	for _, fam := range []string{"total", "xx", "503", "retry"} {
		for name, proxies := range map[string][]ProxySet{
			"owning": {owning}, "pods": {pods}, "kubernetes": {k8s}, "pods in two namespaces": {pods, k8s},
		} {
			built["rule-range "+fam+" "+name] = RuleRangeQuery.With(RuleRange{Family: fam, Clusters: []string{"a"}, Proxies: proxies})
		}
	}
	for name, q := range built {
		if q.Name() == "" {
			t.Errorf("%s: refused: its shape is no declared case", name)
		}
	}
}

// Codex's review of S3b-2a, R2: a Query keeps its own copy of its
// parameters. Changing a slice the caller passed - a proxy's label, the
// proxies themselves, a name or a rule cluster - changes no query admitted.
func TestAQueryKeepsItsOwnParameters(t *testing.T) {
	scope := ScopeOf(map[string]string{"cluster": "dev"})
	equal := [][2]string{{"pod", "a"}}
	names := []string{"envoy-0"}
	clusters := []string{"x"}
	proxies := []ProxySet{{Equal: equal}, {Equal: [][2]string{{"namespace", "edge"}}, NameLabel: "pod", Names: names}}
	q := RuleRangeQuery.With(RuleRange{Family: "total", Clusters: clusters, Proxies: proxies})
	before, err := q.Text(scope)
	if err != nil {
		t.Fatal(err)
	}
	equal[0][0] = `pod="a",cluster="other"}[1m]) or rate(up{pod`
	names[0] = "other"
	clusters[0] = "y"
	proxies[1] = ProxySet{Equal: [][2]string{{"evil", "1"}}}
	after, err := q.Text(scope)
	if err != nil || after != before {
		t.Fatalf("the admitted query changed:\n%s\n%s (%v)", before, after, err)
	}
	// every template's parameters: those holding references are copied
	pods := []string{"web-0"}
	w := WorkloadCPU.With(PodSet{Namespace: "shop", Pods: pods})
	keys := []string{"shop/web"}
	r := RouteRPS.With(Routes{Keys: keys})
	probeClusters := []string{"c0"}
	p := RuleProbeQuery.With(RuleProbe{Family: EnvoyRqTotal, Clusters: probeClusters})
	w0, _ := w.Text(scope)
	r0, _ := r.Text(scope)
	p0, _ := p.Text(scope)
	pods[0], keys[0], probeClusters[0] = "x", "x/x", "x"
	if w1, _ := w.Text(scope); w1 != w0 {
		t.Fatal("pods")
	}
	if r1, _ := r.Text(scope); r1 != r0 {
		t.Fatal("routes")
	}
	if p1, _ := p.Text(scope); p1 != p0 {
		t.Fatal("rule clusters")
	}
}

// A template whose parameters hold references is never defined without a
// clone.
func TestATemplateHoldingReferencesNeedsAClone(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("defined without a clone")
		}
	}()
	define("test-no-clone", func(PodSet, string) string { return "x" }, nil, nil, PodSet{})
}

// Codex's review of S3b-2a, R3: a scope Prometheus reserves renders
// nothing for the proof, as the reader refuses it.
func TestAReservedScopeRendersNothing(t *testing.T) {
	if Rendered(map[string]string{"__name__": "node_cpu_seconds_total"}) != nil {
		t.Fatal("rendered under __name__")
	}
	if Rendered(map[string]string{"cluster": "dev"}) == nil {
		t.Fatal("the control rendered nothing")
	}
}

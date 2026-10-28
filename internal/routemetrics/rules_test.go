package routemetrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/metrics"
)

// The Line slice 7c: per-rule metrics over Prometheus API fixtures
// (testdata/rules/<case>/<query>.json). The kind lab has no Envoy or
// Prometheus, so the fixtures carry the label sets a Prometheus scrape of
// Envoy Gateway's proxies writes (kubernetes-pods job: labelmap of the pod's
// labels, namespace, pod), and the fake routes each query by its shape.

var fixEnd = time.Unix(1792080000, 0)

type fixtureQ struct {
	t       *testing.T
	dir     string
	fail    map[string]error // query kind -> error
	queries map[string]string
	ranges  map[string][2]time.Time
	steps   map[string]time.Duration
}

func newFixture(t *testing.T, name string) *fixtureQ {
	return &fixtureQ{t: t, dir: filepath.Join("testdata", "rules", name), fail: map[string]error{},
		queries: map[string]string{}, ranges: map[string][2]time.Time{}, steps: map[string]time.Duration{}}
}

// kindOf names a query by its shape; the order matters (the 503 query also
// names the per-code counter).
func kindOf(promql string) string {
	switch {
	case strings.HasPrefix(promql, "count by ("):
		return "merged"
	case strings.HasPrefix(promql, "last_over_time("):
		return "probe-" + promql[len("last_over_time("):strings.Index(promql, "{")]
	case strings.Contains(promql, `envoy_response_code="503"`):
		return "r503"
	case strings.Contains(promql, "envoy_response_code_class) ("):
		return "classes"
	case strings.Contains(promql, mRqRetry+"{"):
		return "retries"
	case strings.Contains(promql, mRqTotal+"{"):
		return "requests"
	}
	return "unknown"
}

func (f *fixtureQ) answer(promql, empty string) (int, []byte, error) {
	k := kindOf(promql)
	f.queries[k] = promql
	if err := f.fail[k]; err != nil {
		return 0, nil, err
	}
	b, err := os.ReadFile(filepath.Join(f.dir, k+".json"))
	if errors.Is(err, os.ErrNotExist) && strings.HasPrefix(k, "probe-") {
		b, err = os.ReadFile(filepath.Join(f.dir, "probe.json")) // one probe file answers every family
	}
	if errors.Is(err, os.ErrNotExist) {
		return 200, []byte(empty), nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return 200, b, nil
}

func (f *fixtureQ) InstantQuery(_ context.Context, promql string) (int, []byte, error) {
	return f.answer(promql, emptyVec)
}

func (f *fixtureQ) RangeQuery(_ context.Context, promql string, start, end time.Time, step time.Duration) (int, []byte, error) {
	k := kindOf(promql)
	f.ranges[k], f.steps[k] = [2]time.Time{start, end}, step
	return f.answer(promql, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
}

func pods(list ...ProxyPod) func(context.Context) ([]ProxyPod, error) {
	return func(context.Context) ([]ProxyPod, error) { return list, nil }
}

var (
	egPod1 = ProxyPod{Namespace: "envoy-gateway-system", Name: "envoy-infra-eg-5f6d7c9b8-abcde"}
	egPod2 = ProxyPod{Namespace: "infra", Name: "eg-6b7c8d9e0-pqrst"}
)

func egQuery(p func(context.Context) ([]ProxyPod, error)) RuleQuery {
	return RuleQuery{GatewayNamespace: "infra", GatewayName: "eg", End: fixEnd, Pods: p, Routes: []RouteRules{
		{Namespace: "default", Name: "web", UID: "uid-web", Fingerprints: []string{"fpW0", "fpW1"}},
		{Namespace: "default", Name: "api", UID: "uid-api", Fingerprints: []string{"fpA0"}},
	}}
}

func ruleOf(t *testing.T, g GatewayRules, name string, rule int) RuleSeries {
	t.Helper()
	for _, r := range g.Rules {
		if r.Name == name && r.Rule == rule {
			return r
		}
	}
	t.Fatalf("no rule %s#%d in %+v", name, rule, g.Rules)
	return RuleSeries{}
}

func TestParseRuleCluster(t *testing.T) {
	for in, want := range map[string]struct {
		ns, name string
		rule     int
		ok       bool
	}{
		"httproute/default/web/rule/0":         {"default", "web", 0, true},
		"httproute/team.a/api.example/rule/12": {"team.a", "api.example", 12, true},
		"httproute/default/web/rule/x":         {ok: false},
		"httproute/default/web/rule/":          {ok: false},
		"grpcroute/default/web/rule/0":         {ok: false},
		"service/default/web/8080":             {ok: false}, // a merged backend cluster
		"httproute/default/web/rule/0/extra":   {ok: false},
		"httproute//web/rule/0":                {ok: false},
	} {
		ns, name, rule, ok := ParseRuleCluster(in)
		if ok != want.ok || (ok && (ns != want.ns || name != want.name || rule != want.rule)) {
			t.Errorf("%q: got %s/%s#%d %v, want %+v", in, ns, name, rule, ok, want)
		}
	}
	if RuleCluster("default", "web", 3) != "httproute/default/web/rule/3" {
		t.Fatal("RuleCluster")
	}
}

// Dedicated clusters, the owner labels in the series: every rule from its
// own cluster on this Gateway's proxies, with requests, class shares, the 503
// share and retries apart; a direct-response rule has no cluster at all.
func TestRulesDedicated(t *testing.T) {
	f := newFixture(t, "dedicated")
	listed := false
	g := NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(func(context.Context) ([]ProxyPod, error) {
		listed = true
		return nil, nil
	}))
	if !g.Status.Available || g.Scope != ScopeOwningGateway || g.ScopeNote != "" || g.Unattributable != "" {
		t.Fatalf("gateway: %+v", g)
	}
	if listed {
		t.Fatal("the owner labels need no pod list")
	}
	if !g.Start.Equal(fixEnd.Add(-30*time.Minute)) || !g.End.Equal(fixEnd) {
		t.Fatalf("window %v..%v", g.Start, g.End)
	}
	if g.BackendSplit != NoteBackendSplit || len(g.Notes) != 2 || g.Notes[0] != NoteDirectResponses || g.Notes[1] != NoteUptake {
		t.Fatalf("notes: %q %q", g.BackendSplit, g.Notes)
	}
	// Every query is scoped to this Gateway's owner pair and its rules, at a
	// 1m rate, over the window at a 30s step.
	for _, k := range []string{"requests", "classes", "r503", "retries"} {
		q := f.queries[k]
		for _, want := range []string{`gateway_envoyproxy_io_owning_gateway_name="eg"`, `gateway_envoyproxy_io_owning_gateway_namespace="infra"`, `[1m]`,
			`envoy_cluster_name=~"^(httproute/default/web/rule/0|httproute/default/web/rule/1|httproute/default/api/rule/0)$"`} {
			if !strings.Contains(q, want) {
				t.Errorf("%s query lacks %s: %s", k, want, q)
			}
		}
		if f.ranges[k] != [2]time.Time{fixEnd.Add(-30 * time.Minute), fixEnd} || f.steps[k] != 30*time.Second {
			t.Errorf("%s range %v step %v", k, f.ranges[k], f.steps[k])
		}
	}
	if q := f.queries["requests"]; !strings.HasPrefix(q, "sum by (envoy_cluster_name) (rate(envoy_cluster_upstream_rq_total{") {
		t.Errorf("requests: %s", q)
	}
	if q := f.queries["merged"]; !strings.Contains(q, `envoy_cluster_name=~"^(service|serviceimport|backend)/`) || !strings.Contains(q, "[30m]") {
		t.Errorf("merged probe: %s", q)
	}

	web0 := ruleOf(t, g, "web", 0)
	if web0.UID != "uid-web" || web0.Fingerprint != "fpW0" || web0.Unattributable != "" {
		t.Fatalf("web#0 identity: %+v", web0)
	}
	// NaN stays a gap.
	if web0.Requests.Availability != MetricAvailable || len(web0.Requests.Points) != 3 || web0.Requests.Points[2] != (metrics.Point{Unix: fixEnd.Unix(), Value: 13}) {
		t.Fatalf("web#0 requests: %+v", web0.Requests)
	}
	// Class shares per point: 11.4/(11.4+0.6) = 0.95 at the first point; the
	// second point has 2xx only.
	c := web0.Classes
	if c.Availability != MetricAvailable || len(c.Classes) != 2 || len(c.Classes["2xx"]) != 4 {
		t.Fatalf("web#0 classes: %+v", c)
	}
	if v := c.Classes["2xx"][0].Value; v < 0.9499 || v > 0.9501 {
		t.Fatalf("2xx share %v", v)
	}
	if c.Classes["5xx"][1].Value != 0 || c.Classes["2xx"][1].Value != 1 {
		t.Fatalf("second point: %+v %+v", c.Classes["5xx"][1], c.Classes["2xx"][1])
	}
	if s := web0.Share503; s.Availability != MetricAvailable || len(s.Points) != 4 || s.Points[0].Value < 0.0499 || s.Points[0].Value > 0.0501 {
		t.Fatalf("web#0 503 share: %+v", s)
	}
	if r := web0.Retries; r.Availability != MetricAvailable || len(r.Points) != 4 || r.Points[2].Value != 0.3 {
		t.Fatalf("web#0 retries: %+v", r)
	}
	// No 503 series: no 503 share, never a measured zero.
	api0 := ruleOf(t, g, "api", 0)
	if s := api0.Share503; s.Availability != MetricAbsent || len(s.Points) != 0 {
		t.Fatalf("api#0 503 share: %+v", s)
	}
	if _, ok := api0.Classes.Classes["5xx"]; ok {
		t.Fatal("a class with no series")
	}
	// The direct-response rule has no upstream cluster: absent, never zero.
	web1 := ruleOf(t, g, "web", 1)
	for name, a := range map[string]Availability{"requests": web1.Requests.Availability, "classes": web1.Classes.Availability,
		"503": web1.Share503.Availability, "retries": web1.Retries.Availability} {
		if a != MetricAbsent {
			t.Errorf("web#1 %s: %s", name, a)
		}
	}
}

// Per-metric availability: one query failing fails that metric only (the
// 503 share needs the classes too).
func TestRulesMetricFails(t *testing.T) {
	f := newFixture(t, "dedicated")
	f.fail["retries"] = errors.New("query timed out")
	f.fail["r503"] = errors.New("bad gateway")
	g := NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(nil))
	web0 := ruleOf(t, g, "web", 0)
	if web0.Requests.Availability != MetricAvailable || web0.Classes.Availability != MetricAvailable {
		t.Fatalf("unaffected metrics: %+v %+v", web0.Requests, web0.Classes)
	}
	if web0.Retries.Availability != MetricFailed || !strings.Contains(web0.Retries.Error, "timed out") {
		t.Fatalf("retries: %+v", web0.Retries)
	}
	if web0.Share503.Availability != MetricFailed {
		t.Fatalf("503: %+v", web0.Share503)
	}
	f = newFixture(t, "dedicated")
	f.fail["classes"] = errors.New("boom")
	g = NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(nil))
	web0 = ruleOf(t, g, "web", 0)
	if web0.Classes.Availability != MetricFailed || web0.Share503.Availability != MetricFailed || web0.Requests.Availability != MetricAvailable {
		t.Fatalf("classes failing: %+v %+v", web0.Classes, web0.Share503)
	}
}

// A shared (merged) cluster on this Gateway's proxies makes every rule
// unattributable, and nothing else is read; on another Gateway's proxies it
// does not count.
func TestRulesSharedClusters(t *testing.T) {
	f := newFixture(t, "merged")
	g := NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(nil))
	if g.Unattributable != ReasonSharedClusters || !g.Status.Available {
		t.Fatalf("gateway: %+v", g)
	}
	for _, r := range g.Rules {
		if r.Unattributable != ReasonSharedClusters || r.Requests.Availability != "" {
			t.Fatalf("rule: %+v", r)
		}
	}
	if _, ok := f.queries["probe-"+mRqTotal]; ok {
		t.Fatal("read past a shared cluster")
	}
	// Known by its pod pair: the pod is a current proxy of this Gateway.
	f = newFixture(t, "mergedpods")
	g = NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(pods(egPod1)))
	if g.Unattributable != ReasonSharedClusters {
		t.Fatalf("by pod pair: %+v", g)
	}
	// The pod list failing cannot rule it out.
	g = NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(func(context.Context) ([]ProxyPod, error) {
		return nil, errors.New("forbidden")
	}))
	if !strings.Contains(g.Unattributable, "could not check for shared clusters") {
		t.Fatalf("pod list failing: %+v", g)
	}
	// A failing check is unattributable too.
	f = newFixture(t, "dedicated")
	f.fail["merged"] = errors.New("boom")
	g = NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(nil))
	if !strings.Contains(g.Unattributable, "could not check for shared clusters") {
		t.Fatalf("check failing: %+v", g)
	}
}

// The pod pair: each namespace's proxies matched by name inside it, joined
// by "or"; the result says only the current pods count.
func TestRulesPodScope(t *testing.T) {
	f := newFixture(t, "podform")
	g := NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(pods(egPod2, egPod1)))
	if !g.Status.Available || g.Scope != ScopePod || g.ScopeNote != NoteCurrentPods {
		t.Fatalf("gateway: %+v", g)
	}
	q := f.queries["requests"]
	want := `rate(envoy_cluster_upstream_rq_total{envoy_cluster_name=~"^(httproute/default/web/rule/0|httproute/default/web/rule/1|httproute/default/api/rule/0)$",namespace="envoy-gateway-system",pod=~"^(envoy-infra-eg-5f6d7c9b8-abcde)$"}[1m]) or ` +
		`rate(envoy_cluster_upstream_rq_total{envoy_cluster_name=~"^(httproute/default/web/rule/0|httproute/default/web/rule/1|httproute/default/api/rule/0)$",namespace="infra",pod=~"^(eg-6b7c8d9e0-pqrst)$"}[1m])`
	if !strings.Contains(q, want) {
		t.Fatalf("pod-scoped query:\n%s\nwant within it:\n%s", q, want)
	}
	if web0 := ruleOf(t, g, "web", 0); web0.Requests.Availability != MetricAvailable || web0.Share503.Availability != MetricAbsent {
		t.Fatalf("web#0: %+v", web0)
	}
	// No proxy pods: cannot scope.
	f = newFixture(t, "podform")
	g = NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(pods()))
	if g.Status.Available || !strings.HasPrefix(g.Status.Message, ReasonCannotScope) {
		t.Fatalf("no pods: %+v", g.Status)
	}
	if _, ok := f.queries["requests"]; ok {
		t.Fatal("queried without a scope")
	}
}

// A pod name with no namespace label never scopes; nor do mixed label sets.
func TestRulesCannotScope(t *testing.T) {
	f := newFixture(t, "podnameonly")
	g := NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(pods(egPod1)))
	if g.Status.Available || g.Status.Message != ReasonCannotScope {
		t.Fatalf("pod name only: %+v", g.Status)
	}
	probe := []metrics.LabeledSample{
		{Labels: map[string]string{"pod": "a", "namespace": "x"}},
		{Labels: map[string]string{"kubernetes_pod_name": "b", "kubernetes_namespace": "y"}},
	}
	if _, ok := detectForm(probe); ok {
		t.Fatal("mixed label sets scoped")
	}
	probe = append(probe[:1], metrics.LabeledSample{Labels: map[string]string{"pod": "b", "namespace": "y", lOwnGwName: "eg"}})
	if form, ok := detectForm(probe); !ok || form != ScopePod {
		t.Fatalf("the owner name alone is not the pair: %v %v", form, ok)
	}
}

// No series for any rule (an idle Gateway, or names that do not parse, as a
// custom stat name from the controller's defaults gives): absent, apart.
func TestRulesNoSeries(t *testing.T) {
	f := newFixture(t, "none")
	g := NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(nil))
	if !g.Status.Available || g.Scope != "" {
		t.Fatalf("gateway: %+v", g)
	}
	for _, r := range g.Rules {
		if r.Requests.Availability != MetricAbsent || r.Classes.Availability != MetricAbsent || r.Share503.Availability != MetricAbsent || r.Retries.Availability != MetricAbsent {
			t.Fatalf("rule: %+v", r)
		}
	}
	if _, ok := f.queries["requests"]; ok {
		t.Fatal("range queries with nothing to read")
	}
}

// A route attached to this Gateway twice is unattributable on its own; a
// Gateway-wide reason (the EnvoyProxy) reads nothing at all.
func TestRulesUnattributable(t *testing.T) {
	f := newFixture(t, "dedicated")
	q := egQuery(nil)
	q.Routes[1].SameGatewayTwice = true
	g := NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), q)
	if api0 := ruleOf(t, g, "api", 0); api0.Unattributable != ReasonSameGateway || api0.Requests.Availability != "" {
		t.Fatalf("api#0: %+v", api0)
	}
	if strings.Contains(f.queries["requests"], "httproute/default/api/rule/0") {
		t.Fatal("an unattributable rule was queried")
	}
	if ruleOf(t, g, "web", 0).Requests.Availability != MetricAvailable {
		t.Fatal("the other route lost its metrics")
	}

	f = newFixture(t, "dedicated")
	q = egQuery(nil)
	q.Unattributable = "shared or custom cluster names: the EnvoyProxy infra/eg sets spec.mergeBackends"
	g = NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), q)
	if len(f.queries) != 0 || g.Unattributable != q.Unattributable || ruleOf(t, g, "web", 0).Unattributable != q.Unattributable {
		t.Fatalf("gateway-wide: %+v queries %v", g, f.queries)
	}
	if u := Unavailable(egQuery(nil), "metrics unavailable"); u.Status.Available || len(u.Rules) != 3 {
		t.Fatalf("unavailable keeps the rules: %+v", u)
	}
}

func TestEnvoyProxySettings(t *testing.T) {
	ep := func(spec map[string]any) map[string]any { return map[string]any{"spec": spec} }
	for name, c := range map[string]struct {
		obj  map[string]any
		want []string
	}{
		"none":           {ep(map[string]any{"provider": map[string]any{"type": "Kubernetes"}}), nil},
		"stat name":      {ep(map[string]any{"telemetry": map[string]any{"metrics": map[string]any{"clusterStatName": "%ROUTE_NAME%"}}}), []string{"spec.telemetry.metrics.clusterStatName"}},
		"merge gateways": {ep(map[string]any{"mergeGateways": true}), []string{"spec.mergeGateways"}},
		"not merged":     {ep(map[string]any{"mergeGateways": false}), nil},
		"merge backends": {ep(map[string]any{"mergeBackends": map[string]any{}}), []string{"spec.mergeBackends"}},
	} {
		got := EnvoyProxySettings(c.obj)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// A proxy scraped twice would count twice, and one store holding two
// clusters would mix a Gateway named alike in both: both fail closed.
func TestRulesScrapedOnce(t *testing.T) {
	for name, want := range map[string]string{"scrapedtwice": ReasonScrapedTwice, "severalclusters": ReasonSeveralClusters} {
		f := newFixture(t, name)
		g := NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(pods(egPod1)))
		if g.Status.Available || g.Status.Message != want {
			t.Errorf("%s: %+v", name, g.Status)
		}
		if _, ok := f.queries["requests"]; ok {
			t.Errorf("%s: queried past the ambiguity", name)
		}
	}
	// The pod pair: the same pod twice.
	probe := []metrics.LabeledSample{
		{Labels: map[string]string{lClusterName: "httproute/default/web/rule/0", "pod": egPod1.Name, "namespace": egPod1.Namespace, "job": "a"}},
		{Labels: map[string]string{lClusterName: "httproute/default/web/rule/0", "pod": egPod1.Name, "namespace": egPod1.Namespace, "job": "b"}},
	}
	if r := scopedOnce(context.Background(), probe, ScopePod, egQuery(nil), onceList(pods(egPod1))); r != ReasonScrapedTwice {
		t.Fatalf("pod pair twice: %q", r)
	}
	// Another Gateway's pod is out of scope, so it never counts.
	if r := scopedOnce(context.Background(), probe, ScopePod, egQuery(nil), onceList(pods(egPod2))); r != "" {
		t.Fatalf("out of scope: %q", r)
	}
}

// Nothing missing is taken as zero (design review of a6d5c4e): a missing 503
// series is absent, a missing class sample is a gap for every class, and a
// 503 gap is a gap.
func TestSharesKeepGaps(t *testing.T) {
	cls := []metrics.LabeledSeries{{Labels: map[string]string{lRespClass: "2"}, Points: []metrics.Point{{Unix: 100, Value: 10}}}}
	if _, got := shares(cls, nil, nil, nil); got.Availability != MetricAbsent || len(got.Points) != 0 {
		t.Fatalf("a missing 503 series became a measured zero: %+v", got)
	}
	cls = []metrics.LabeledSeries{
		{Labels: map[string]string{lRespClass: "2"}, Points: []metrics.Point{{Unix: 100, Value: 10}, {Unix: 130, Value: 10}, {Unix: 160, Value: 10}}},
		{Labels: map[string]string{lRespClass: "5"}, Points: []metrics.Point{{Unix: 100, Value: 1}, {Unix: 160, Value: 1}}},
	}
	r503 := []metrics.LabeledSeries{{Points: []metrics.Point{{Unix: 160, Value: 1}}}}
	got, s503 := shares(cls, nil, r503, nil)
	for k, pts := range got.Classes {
		if len(pts) != 2 || pts[0].Unix != 100 || pts[1].Unix != 160 {
			t.Fatalf("%s: a missing class sample was filled: %+v", k, pts)
		}
	}
	if s503.Availability != MetricAvailable || len(s503.Points) != 1 || s503.Points[0].Unix != 160 || s503.Points[0].Value < 0.0908 || s503.Points[0].Value > 0.0910 {
		t.Fatalf("503 share: %+v", s503)
	}
	// Two series for one class cannot be divided honestly.
	cls = append(cls, metrics.LabeledSeries{Labels: map[string]string{lRespClass: "5"}})
	if c, s := shares(cls, nil, r503, nil); c.Availability != MetricFailed || s.Availability != MetricFailed {
		t.Fatalf("a repeated class: %+v %+v", c, s)
	}
}

// One family filtered out of the scrape, or failing, never hides the
// others (design review of a6d5c4e).
func TestRulesFamiliesApart(t *testing.T) {
	f := newFixture(t, "nototal")
	g := NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(nil))
	web0 := ruleOf(t, g, "web", 0)
	if !g.Status.Available || web0.Requests.Availability != MetricAbsent || web0.Classes.Availability != MetricAvailable || web0.Retries.Availability != MetricAvailable {
		t.Fatalf("no request total: %+v / %+v", g.Status, web0)
	}
	if _, ok := f.queries["requests"]; ok {
		t.Fatal("a range query for an absent family")
	}
	f = newFixture(t, "dedicated")
	f.fail["probe-"+mRqRetry] = errors.New("boom")
	g = NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(nil))
	web0 = ruleOf(t, g, "web", 0)
	if !g.Status.Available || web0.Retries.Availability != MetricFailed || web0.Requests.Availability != MetricAvailable {
		t.Fatalf("a failing probe: %+v / %+v", g.Status, web0)
	}
	// Every probe failing leaves nothing readable.
	f = newFixture(t, "dedicated")
	for _, m := range ruleMetrics {
		f.fail["probe-"+m] = errors.New("down")
	}
	if g = NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(nil)); g.Status.Available {
		t.Fatalf("every probe failing: %+v", g.Status)
	}
	// The merged check reads every family.
	if q := newFixtureQueries(t); !strings.Contains(q, mRqRetry+"{") || !strings.Contains(q, mRqXX+"{") || !strings.Contains(q, " or ") {
		t.Fatalf("merged check: %s", q)
	}
}

func newFixtureQueries(t *testing.T) string {
	f := newFixture(t, "dedicated")
	NewEnvoyRuleSource(metrics.NewClient(metrics.TransportOf(f), metrics.Scope{})).QueryRules(context.Background(), egQuery(nil))
	return f.queries["merged"]
}

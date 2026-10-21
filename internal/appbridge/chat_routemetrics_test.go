package appbridge

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/routemetrics"
)

// AI A1 §3.4: the route templates read each named Gateway's per-rule Envoy
// metrics, keep only the rules of the route as read now, and say every
// Gateway or rule they could not read.

var routesGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}

func routeChat(uid string) *fakeChatConn {
	f := newFakeChat()
	f.kinds["httproute"] = []fleet.ChatKind{{GVR: routesGVR, Kind: "HTTPRoute", Namespaced: true}}
	f.objects[key(routesGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "u-r"},
		"spec": map[string]any{"parentRefs": []any{
			map[string]any{"name": "gw", "namespace": "infra"}, map[string]any{"name": "gw2"},
			map[string]any{"group": "other.example", "kind": "Gateway", "name": "m"}, map[string]any{"name": "gw", "namespace": "infra"}}}})
	end := fixedNow()
	start := end.Add(-routemetrics.RuleWindow)
	step := routemetrics.RuleStep
	f.rules = map[string]routemetrics.GatewayRules{
		"infra/gw": {Status: routemetrics.Status{Available: true}, Start: start, End: end, Rules: []routemetrics.RuleSeries{
			{Namespace: "elsewhere", Name: "web", UID: "u-elsewhere", Rule: 0, Requests: routemetrics.Series{Availability: routemetrics.MetricAvailable, Points: pts(start, step, 77)}},
			{Namespace: "shop", Name: "web", UID: uid, Rule: 0,
				Requests: routemetrics.Series{Availability: routemetrics.MetricAvailable, Points: pts(start, step, 10, 12, gap, 11)},
				Classes: routemetrics.ClassSeries{Availability: routemetrics.MetricAvailable, Classes: map[string][]metrics.Point{
					"2xx": pts(start, step, 1, 0.96, 0.88), "5xx": pts(start, step, 0, 0.04, 0.12)}}},
			{Namespace: "shop", Name: "web", UID: uid, Rule: 1, Unattributable: routemetrics.ReasonSharedClusters},
			{Namespace: "shop", Name: "other", UID: "u-o", Rule: 0, Requests: routemetrics.Series{Availability: routemetrics.MetricAvailable, Points: pts(start, step, 99)}},
		}},
		"shop/gw2": {Status: routemetrics.Status{Available: false, Message: "Envoy Gateway not detected"}},
	}
	return f
}

func TestRouteMetricsPerRuleThroughEachGateway(t *testing.T) {
	f := routeChat("u-r")
	env, read := run(t, f, toolMetrics, `{"template":"route_5xx_ratio","namespace":"shop","name":"web"}`)
	d := metricsData(t, env)
	if strings.Join(f.ruleCalls, ",") != "infra/gw,shop/gw2" {
		t.Fatalf("each Gateway the route names, once: %v", f.ruleCalls)
	}
	if d.SeriesTotal != 1 || d.Series[0].Label != "Gateway infra/gw rule 0" || d.Series[0].Max != 0.12 || d.Window != "30m0s" || env.Complete {
		t.Fatalf("%+v", d)
	}
	errs := strings.Join(env.Errors, "|")
	if !strings.Contains(errs, "Gateway shop/gw2: Envoy Gateway not detected") || !strings.Contains(errs, "Gateway infra/gw rule 1: "+routemetrics.ReasonSharedClusters) {
		t.Fatalf("every Gateway and rule not read is said: %s", errs)
	}
	if len(env.Checks) != 1 || env.Checks[0].Subject.Identity != identityName || !strings.HasPrefix(env.Checks[0].Statement,
		"rule 0 of HTTPRoute shop/web (by name) through Gateway infra/gw served up to 12% 5xx responses in the last 30m0s, first past 5% at") {
		t.Fatalf("%+v", env.Checks)
	}
	if len(read.Refs) != 1 || read.Refs[0].UID != "u-r" {
		t.Fatalf("%+v", read.Refs)
	}

	env, _ = run(t, f, toolMetrics, `{"template":"route_requests","kind":"HTTPRoute","namespace":"shop","name":"web","window":"6h"}`)
	d = metricsData(t, env)
	if d.Series[0].Points != 3 || d.Series[0].Last != 11 || len(env.Checks) != 0 || !strings.Contains(d.Note, "6h, does not apply") {
		t.Fatalf("%+v", d)
	}
}

func TestRouteMetricsKeepOnlyTheRouteAsReadNow(t *testing.T) {
	f := routeChat("u-old")
	env, _ := run(t, f, toolMetrics, `{"template":"route_requests","namespace":"shop","name":"web"}`)
	if d := metricsData(t, env); d.SeriesTotal != 0 || !strings.Contains(strings.Join(env.Errors, "|"), "replaced between reads") {
		t.Fatalf("%+v %+v", d, env.Errors)
	}
	f = routeChat("u-r")
	gr := f.rules["infra/gw"]
	gr.Rules[1].Classes.Classes = map[string][]metrics.Point{"2xx": pts(fixedNow(), time.Minute, 1)}
	gr.Rules[1].Requests = routemetrics.Series{Availability: routemetrics.MetricAbsent}
	f.rules["infra/gw"] = gr
	env, _ = run(t, f, toolMetrics, `{"template":"route_5xx_ratio","namespace":"shop","name":"web"}`)
	if d := metricsData(t, env); d.SeriesTotal != 0 || !strings.Contains(d.Note, "rule 0: no 5xx responses in the window") {
		t.Fatalf("%+v", d)
	}
	env, _ = run(t, f, toolMetrics, `{"template":"route_requests","namespace":"shop","name":"web"}`)
	if d := metricsData(t, env); !strings.Contains(d.Note, "does not show that the rule took no traffic") {
		t.Fatalf("%+v", d)
	}
	if _, read := run(t, f, toolMetrics, `{"template":"route_requests","kind":"Pod","namespace":"shop","name":"web"}`); read.Outcome != "failed" {
		t.Fatal("a route template reads an HTTPRoute")
	}
}

package appbridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/observe"
)

// AI A1 §3.4 and §8: Klyx's templates only, escaped, at most 60 points per
// window with the inclusive end, summarised series with gaps, counter resets
// and a limit crossed, by pod name, and an empty result never read as an
// absent source.

func pts(start time.Time, step time.Duration, vals ...float64) []metrics.Point {
	var out []metrics.Point
	for i, v := range vals {
		if !isGap(v) {
			out = append(out, metrics.Point{Unix: start.Add(time.Duration(i) * step).Unix(), Value: v})
		}
	}
	return out
}

const gap = -1

func isGap(v float64) bool { return v == gap }

func metricsData(t *testing.T, env chatEnvelope) chatMetricsData {
	t.Helper()
	var d chatMetricsData
	b, _ := json.Marshal(env.Data)
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestMetricsStepKeepsEveryWindowWithinSixtyPoints(t *testing.T) {
	for w, d := range chatMetricWindows {
		s := stepFor(d)
		if n := int(d/s) + 1; n > chatSeriesPoints || n < chatSeriesPoints-3 || s%time.Second != 0 {
			t.Fatalf("%s: step %s gives %d points", w, s, n)
		}
	}
}

func TestMetricsPodMemoryAgainstItsLimit(t *testing.T) {
	f := newFakeChat()
	f.objects[key(podsGVR, "shop", "web.v2-7d9")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "web.v2-7d9", "namespace": "shop", "uid": "u-p"},
		"spec": map[string]any{"containers": []any{
			map[string]any{"name": "web", "resources": map[string]any{"limits": map[string]any{"memory": "512Mi"}}},
			map[string]any{"name": "sidecar"}}}})
	end := fixedNow().UTC()
	step := stepFor(time.Hour)
	start := end.Add(-time.Hour)
	q := `sum by (container) (container_memory_working_set_bytes{namespace="shop",pod=~"^(web\\.v2-7d9)$",container!="",container!="POD"})`
	mi := float64(1 << 20)
	f.series = map[string][]metrics.LabeledSeries{q: {
		{Labels: map[string]string{"container": "web"}, Points: pts(start, step, 300*mi, gap, 400*mi, 470*mi, 500*mi)},
		{Labels: map[string]string{"container": "sidecar"}, Points: pts(start, step, 10*mi, 11*mi)},
	}}
	env, read := run(t, f, toolMetrics, `{"template":"pod_memory","namespace":"shop","name":"web.v2-7d9"}`)
	if len(f.queries) != 1 || f.queries[0] != q || f.steps[0] != step {
		t.Fatalf("the template, escaped: %q", f.queries)
	}
	d := metricsData(t, env)
	if read.Outcome != "completed" || d.SeriesTotal != 2 || d.Unit != "bytes" || len(d.Series) != 2 || d.Series[0].Label != "sidecar" {
		t.Fatalf("%+v", d)
	}
	web := d.Series[1]
	if web.Points != 4 || web.Gaps != web.Expected-4 || web.Limit != "512Mi" || web.Threshold != round3(512*mi*0.9) ||
		web.CrossedAt != time.Unix(start.Add(3*step).Unix(), 0).UTC().Format(time.RFC3339) || web.Max != 500*mi {
		t.Fatalf("%+v", web)
	}
	b, _ := json.Marshal(env)
	if strings.Contains(string(b), `"points":[`) || !strings.Contains(d.Note, "as read now") || !strings.Contains(d.Note, "recreated inside the window is included") {
		t.Fatalf("summaries only, with their identity said: %s", b)
	}
	if len(env.Checks) != 1 || env.Checks[0].Class != checkMetric || env.Checks[0].Subject.Identity != identityName || env.Checks[0].Subject.UID != "" ||
		env.Checks[0].Statement != fmt.Sprintf("container web of the pod currently named shop/web.v2-7d9 (by name) reached 98%% of its memory limit (512Mi) in the last 1h, first past 90%% at %s", web.CrossedAt) {
		t.Fatalf("one check, for the container past its limit's threshold: %+v", env.Checks)
	}
}

func TestMetricsWorkloadRestartsByItsCurrentPods(t *testing.T) {
	f := alertsChat(observe.KindCoverage{})
	end := fixedNow().UTC()
	step := stepFor(15 * time.Minute)
	q := `sum by (pod) (kube_pod_container_status_restarts_total{namespace="shop",pod=~"^(web-1-a)$"})`
	f.series = map[string][]metrics.LabeledSeries{q: {{Labels: map[string]string{"pod": "web-1-a"}, Points: pts(end.Add(-15*time.Minute), step, 1, 2, 1, 3)}}}
	env, _ := run(t, f, toolMetrics, `{"template":"restarts","kind":"Deployment","namespace":"shop","name":"web","window":"15m"}`)
	if len(f.queries) != 1 || f.queries[0] != q {
		t.Fatalf("the workload's own pods by controller UID, never a lookalike's: %q", f.queries)
	}
	d := metricsData(t, env)
	s := d.Series[0]
	if s.Resets != 1 || s.Increase != 4 || d.PodsTotal != 1 || d.Pods[0] != "web-1-a" {
		t.Fatalf("a counter's rise counts across its reset: %+v", d)
	}
	if len(env.Checks) != 1 || env.Checks[0].Statement != "restarts of the pod currently named shop/web-1-a (by name) rose by 4 in the last 15m (the counter reset once)" {
		t.Fatalf("%+v", env.Checks)
	}
}

func TestMetricsEmptyIsNoDataNeverAbsent(t *testing.T) {
	f := alertsChat(observe.KindCoverage{})
	env, read := run(t, f, toolMetrics, `{"template":"workload_cpu","kind":"Deployment","namespace":"shop","name":"web"}`)
	d := metricsData(t, env)
	if read.Outcome != "completed" || d.SeriesTotal != 0 || len(env.Checks) != 0 || !strings.Contains(d.Note, "does not show that a source") {
		t.Fatalf("%+v", d)
	}
	f.seriesErr = fmt.Errorf("%w: no Prometheus found", fleet.ErrChatMetricsUnavailable)
	env, read = run(t, f, toolMetrics, `{"template":"workload_cpu","kind":"Deployment","namespace":"shop","name":"web"}`)
	if read.Outcome != "failed" || !strings.Contains(env.Errors[0], "metrics unavailable on this cluster") {
		t.Fatalf("%+v", env)
	}
}

func TestMetricsRefusesWhatIsNotATemplate(t *testing.T) {
	f := alertsChat(observe.KindCoverage{})
	for _, args := range []string{
		`{"template":"up","namespace":"shop","name":"web"}`,
		`{"template":"pod_cpu","namespace":"shop","name":"web","promql":"up"}`,
		`{"template":"pod_cpu","namespace":"shop","name":"web","window":"7d"}`,
		`{"template":"workload_cpu","namespace":"shop","name":"web"}`,
		`{"template":"pod_cpu","kind":"Deployment","namespace":"shop","name":"web"}`,
		`{"template":"pod_cpu","kind":"Job","namespace":"shop","name":"web"}`,
	} {
		if _, read := run(t, f, toolMetrics, args); read.Outcome != "failed" {
			t.Fatalf("%s: %+v", args, read)
		}
	}
	if len(f.queries) != 0 {
		t.Fatalf("a refused call queries nothing: %q", f.queries)
	}
}

func TestMetricsCapsTheSeries(t *testing.T) {
	f := newFakeChat()
	f.objects[key(podsGVR, "shop", "p")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "p", "namespace": "shop"}})
	q := `sum by (container) (rate(container_cpu_usage_seconds_total{namespace="shop",pod=~"^(p)$",container!="",container!="POD"}[5m]))`
	var ss []metrics.LabeledSeries
	for i := 0; i < 12; i++ {
		ss = append(ss, metrics.LabeledSeries{Labels: map[string]string{"container": fmt.Sprintf("c%02d", i)}, Points: pts(fixedNow(), time.Minute, 0.1)})
	}
	f.series = map[string][]metrics.LabeledSeries{q: ss}
	env, _ := run(t, f, toolMetrics, `{"template":"pod_cpu","namespace":"shop","name":"p"}`)
	d := metricsData(t, env)
	if len(d.Series) != chatSeriesMax || d.SeriesTotal != 12 || env.Complete {
		t.Fatalf("%d of %d", len(d.Series), d.SeriesTotal)
	}
}

func TestMetricsPodCPUAgainstAMillicoreLimit(t *testing.T) {
	f := newFakeChat()
	f.objects[key(podsGVR, "shop", "p")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "p", "namespace": "shop"},
		"spec": map[string]any{"containers": []any{map[string]any{"name": "web", "resources": map[string]any{"limits": map[string]any{"cpu": "500m"}}}}}})
	q := `sum by (container) (rate(container_cpu_usage_seconds_total{namespace="shop",pod=~"^(p)$",container!="",container!="POD"}[5m]))`
	f.series = map[string][]metrics.LabeledSeries{q: {{Labels: map[string]string{"container": "web"}, Points: pts(fixedNow().Add(-time.Hour), time.Minute, 0.2, 0.46)}}}
	env, _ := run(t, f, toolMetrics, `{"template":"pod_cpu","namespace":"shop","name":"p"}`)
	if len(env.Checks) != 1 || !strings.Contains(env.Checks[0].Statement, "reached 92% of its CPU limit (500m)") {
		t.Fatalf("%+v", env.Checks)
	}
}

func TestMetricsAFlatRestartCounterMakesNoCheck(t *testing.T) {
	f := newFakeChat()
	f.objects[key(podsGVR, "shop", "p")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "p", "namespace": "shop"}})
	q := `sum by (pod) (kube_pod_container_status_restarts_total{namespace="shop",pod=~"^(p)$"})`
	f.series = map[string][]metrics.LabeledSeries{q: {{Labels: map[string]string{"pod": "p"}, Points: pts(fixedNow().Add(-time.Hour), time.Minute, 2, 2, 2)}}}
	env, _ := run(t, f, toolMetrics, `{"template":"restarts","namespace":"shop","name":"p"}`)
	if d := metricsData(t, env); len(env.Checks) != 0 || d.Series[0].Increase != 0 || d.Series[0].Last != 2 {
		t.Fatalf("%+v %+v", env.Checks, d.Series)
	}
}

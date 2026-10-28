package routemetrics

import (
	"context"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/metrics"
)

const emptyVec = `{"status":"success","data":{"resultType":"vector","result":[]}}`

func vec(elems string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` + elems + `]}}`
}
func el(cluster, val string) string {
	return `{"metric":{"envoy_cluster_name":"` + cluster + `"},"value":[1,"` + val + `"]}`
}

// scriptedVecQ answers each of the 5 route queries (plus the existence probe)
// by a DETERMINISTIC ordered match. Order matters: the 5xx query string also
// contains "upstream_rq_xx", so the class="5" case MUST be checked first.
type scriptedVecQ struct {
	rqTotal, p50, p99, rq5xx, rqAll, count string
}

func (q *scriptedVecQ) InstantQuery(_ context.Context, promql string) (int, []byte, error) {
	pick := func(s string) (int, []byte, error) {
		if s == "" {
			s = emptyVec
		}
		return 200, []byte(s), nil
	}
	switch {
	case strings.Contains(promql, "count("):
		return pick(q.count)
	case strings.Contains(promql, `envoy_response_code_class="5"`):
		return pick(q.rq5xx)
	case strings.Contains(promql, "upstream_rq_xx"):
		return pick(q.rqAll)
	case strings.Contains(promql, "upstream_rq_total"):
		return pick(q.rqTotal)
	case strings.Contains(promql, "histogram_quantile(0.50"):
		return pick(q.p50)
	case strings.Contains(promql, "histogram_quantile(0.99"):
		return pick(q.p99)
	}
	return pick(emptyVec)
}

func TestEnvoyClusterSource_MultiRuleAndIdle(t *testing.T) {
	q := &scriptedVecQ{
		// rps: web has two rules (sum=12.0+0.4=12.4); api idle (0)
		rqTotal: vec(el("httproute/default/web/rule/0", "12.0") + "," + el("httproute/default/web/rule/1", "0.4") + "," + el("httproute/default/api/rule/0", "0")),
		// p99 across web rules: 42 and 50 -> route p99 = max = 50
		p99: vec(el("httproute/default/web/rule/0", "42") + "," + el("httproute/default/web/rule/1", "50")),
		p50: vec(el("httproute/default/web/rule/0", "8") + "," + el("httproute/default/web/rule/1", "9")),
		// err: 5xx across rules = 0.03+0.01 = 0.04; all = 10.0+2.4 = 12.4 -> 0.04/12.4 ~ 0.00323
		rq5xx: vec(el("httproute/default/web/rule/0", "0.03") + "," + el("httproute/default/web/rule/1", "0.01")),
		rqAll: vec(el("httproute/default/web/rule/0", "10.0") + "," + el("httproute/default/web/rule/1", "2.4")),
	}
	out, st, err := NewEnvoyClusterSource(metrics.NewClient(q, metrics.Scope{})).QueryRouteMetrics(context.Background(), []string{"default/web", "default/api"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !st.Available {
		t.Fatalf("want available, got %+v", st)
	}
	web, ok := out["default/web"]
	if !ok || web.RPS == nil || *web.RPS < 12.39 || *web.RPS > 12.41 {
		t.Fatalf("web rps want ~12.4, got %+v", web.RPS)
	}
	if web.P99 == nil || *web.P99 != 50 {
		t.Fatalf("web p99 want 50 (max across rules), got %+v", web.P99)
	}
	if web.ErrRate == nil || *web.ErrRate < 0.0031 || *web.ErrRate > 0.0034 {
		t.Fatalf("web err want ~0.00323 (0.04/12.4), got %+v", web.ErrRate)
	}
	api, ok := out["default/api"]
	if !ok || api.RPS == nil || *api.RPS != 0 {
		t.Fatalf("api should be present with rps 0 (idle), got %+v", api)
	}
	if api.P50 != nil || api.P99 != nil || api.ErrRate != nil {
		t.Fatalf("api idle latency/err must be nil, got %+v", api)
	}
}

func TestEnvoyClusterSource_NoSeriesVsNoMatch(t *testing.T) {
	// All scoped queries empty (zero-value scriptedVecQ). Existence probe decides.
	noSeries := &scriptedVecQ{count: emptyVec} // count of nothing -> empty vector
	_, st, err := NewEnvoyClusterSource(metrics.NewClient(noSeries, metrics.Scope{})).QueryRouteMetrics(context.Background(), []string{"default/web"})
	if err != nil || st.Available || !strings.Contains(st.Message, "no envoy_cluster_* series") {
		t.Fatalf("no-series: want unavailable + reason, got %+v / %v", st, err)
	}

	noMatch := &scriptedVecQ{count: vec(el("", "5"))} // series exist (count=5) but none matched
	_, st2, err := NewEnvoyClusterSource(metrics.NewClient(noMatch, metrics.Scope{})).QueryRouteMetrics(context.Background(), []string{"default/web"})
	if err != nil || !st2.Available || !strings.Contains(st2.Message, "no route series matched") {
		t.Fatalf("no-match: want available + note, got %+v / %v", st2, err)
	}
}

func TestEnvoyClusterSource_ErrRateClampedToOne(t *testing.T) {
	// 5xx rate (8) exceeds total rate (4) -> raw 2.0, must clamp to 1.0.
	q := &scriptedVecQ{
		rqTotal: vec(el("httproute/default/web/rule/0", "1")),
		rq5xx:   vec(el("httproute/default/web/rule/0", "8")),
		rqAll:   vec(el("httproute/default/web/rule/0", "4")),
	}
	out, _, err := NewEnvoyClusterSource(metrics.NewClient(q, metrics.Scope{})).QueryRouteMetrics(context.Background(), []string{"default/web"})
	if err != nil {
		t.Fatal(err)
	}
	if out["default/web"].ErrRate == nil || *out["default/web"].ErrRate != 1 {
		t.Fatalf("errRate must clamp to 1.0, got %+v", out["default/web"].ErrRate)
	}
}

func TestEnvoyClusterSource_P50CanExceedP99MultiRule(t *testing.T) {
	// Documents the max-merge inversion: rule0 high p50, rule1 high p99.
	q := &scriptedVecQ{
		rqTotal: vec(el("httproute/default/web/rule/0", "1") + "," + el("httproute/default/web/rule/1", "1")),
		p50:     vec(el("httproute/default/web/rule/0", "90") + "," + el("httproute/default/web/rule/1", "10")),
		p99:     vec(el("httproute/default/web/rule/0", "20") + "," + el("httproute/default/web/rule/1", "80")),
	}
	out, _, err := NewEnvoyClusterSource(metrics.NewClient(q, metrics.Scope{})).QueryRouteMetrics(context.Background(), []string{"default/web"})
	if err != nil {
		t.Fatal(err)
	}
	web := out["default/web"]
	if web.P50 == nil || *web.P50 != 90 || web.P99 == nil || *web.P99 != 80 {
		t.Fatalf("want p50=90 p99=80 (independent max-merge), got %+v %+v", web.P50, web.P99)
	}
}

func TestEnvoyClusterSource_RouteWithoutRpsDropped(t *testing.T) {
	// A route present in rq_xx but NOT rq_total is "not measured" -> absent from
	// output (contract: measured = has rps series).
	q := &scriptedVecQ{
		rq5xx: vec(el("httproute/default/web/rule/0", "1")),
		rqAll: vec(el("httproute/default/web/rule/0", "10")),
		count: vec(el("", "5")),
	}
	out, st, err := NewEnvoyClusterSource(metrics.NewClient(q, metrics.Scope{})).QueryRouteMetrics(context.Background(), []string{"default/web"})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := out["default/web"]; present {
		t.Fatalf("route without rps series must be dropped, got %+v", out)
	}
	// no measured routes -> existence probe path; series exist (count=5) -> no-match note
	if !st.Available {
		t.Fatalf("want available (series exist, none measured), got %+v", st)
	}
}

func TestEnvoyClusterSource_EmptyKeys(t *testing.T) {
	out, st, err := NewEnvoyClusterSource(metrics.NewClient(&scriptedVecQ{}, metrics.Scope{})).QueryRouteMetrics(context.Background(), nil)
	if err != nil || !st.Available || len(out) != 0 {
		t.Fatalf("empty keys: want available empty, got %+v %+v %v", out, st, err)
	}
}

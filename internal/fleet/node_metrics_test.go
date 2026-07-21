package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/metrics"
)

// utilQ answers the cpu (rate) and mem (working_set) instant queries with
// scripted vectors and records every promql it saw.
type utilQ struct {
	cpu, mem string
	seen     []string
}

func (q *utilQ) InstantQuery(_ context.Context, promql string) (int, []byte, error) {
	q.seen = append(q.seen, promql)
	// The capability probe liveness-checks with vector(1) before any real query.
	if promql == "vector(1)" {
		return 200, []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"1"]}]}}`), nil
	}
	body := `{"status":"success","data":{"resultType":"vector","result":[]}}`
	if strings.Contains(promql, "container_cpu_usage_seconds_total") && q.cpu != "" {
		body = q.cpu
	}
	if strings.Contains(promql, "container_memory_working_set_bytes") && q.mem != "" {
		body = q.mem
	}
	return 200, []byte(body), nil
}

func schedPod(ns, name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.PodSpec{NodeName: node},
	}
}

func utilConn(q metrics.Querier, objs ...*corev1.Pod) *ClusterConn {
	cs := fake.NewSimpleClientset()
	for _, p := range objs {
		_, _ = cs.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{})
	}
	return &ClusterConn{
		typed:      cs,
		clk:        clock.NewFake(time.Unix(1000, 0)),
		metricsCfg: config.MetricsConfig{Endpoint: "http://prom"},
		metricsTF:  fakeTF{q: q},
	}
}

func TestNodeUtilization_JoinsPodUsageToNodes(t *testing.T) {
	q := &utilQ{
		cpu: `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"namespace":"a","pod":"p1"},"value":[0,"0.25"]},
			{"metric":{"namespace":"a","pod":"p2"},"value":[0,"0.50"]}
		]}}`,
		mem: `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"namespace":"a","pod":"p1"},"value":[0,"1000"]}
		]}}`,
	}
	c := utilConn(q, schedPod("a", "p1", "node-1"), schedPod("a", "p2", "node-1"), schedPod("a", "idle", "node-2"))

	out, st := c.NodeUtilization(context.Background())
	if !st.Available {
		t.Fatalf("status: %+v", st)
	}
	if len(out) != 1 || out[0].Node != "node-1" {
		t.Fatalf("only the sampled node must appear: %+v", out)
	}
	if out[0].CPU == nil || *out[0].CPU != 0.75 || out[0].Mem == nil || *out[0].Mem != 1000 || out[0].SampledPods != 2 {
		t.Fatalf("node-1: %+v", out[0])
	}
}

func TestNodeUtilization_UnavailableWithoutMetricsSource(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}
	_, st := c.NodeUtilization(context.Background())
	if st.Available {
		t.Fatalf("no metrics source must be unavailable, got %+v", st)
	}
	if st.Message == "" {
		t.Fatal("unavailable must say why")
	}
}

func TestPodContainerUsage_ScopesQueriesAndMapsByContainer(t *testing.T) {
	q := &utilQ{
		cpu: `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"container":"app"},"value":[0,"0.02"]},
			{"metric":{"container":"sidecar"},"value":[0,"0.30"]}
		]}}`,
		mem: `{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"container":"app"},"value":[0,"500"]}
		]}}`,
	}
	c := utilConn(q)

	out, st := c.PodContainerUsage(context.Background(), "team-a", "api-abc")
	if !st.Available {
		t.Fatalf("status: %+v", st)
	}
	app := out["app"]
	if app.CPU == nil || *app.CPU != 0.02 || app.Mem == nil || *app.Mem != 500 {
		t.Fatalf("app: %+v", app)
	}
	sc := out["sidecar"]
	if sc.CPU == nil || *sc.CPU != 0.30 || sc.Mem != nil {
		t.Fatalf("sidecar mem has no sample and must stay nil: %+v", sc)
	}
	for _, seen := range q.seen {
		if seen == "vector(1)" {
			continue // the capability probe
		}
		if !strings.Contains(seen, `namespace="team-a"`) || !strings.Contains(seen, `pod="api-abc"`) {
			t.Fatalf("query must be scoped to the pod: %q", seen)
		}
	}
}

package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/metrics"
)

// rangeQ answers the Probe's instant queries with success and records every
// range query, returning a canned single-series matrix.
type rangeQ struct {
	rangeQueries []string
	lastStart    time.Time
	lastEnd      time.Time
	lastStep     time.Duration
}

func (q *rangeQ) InstantQuery(_ context.Context, promql string) (int, []byte, error) {
	// Probe runs vector(1); answer 1 so the capability resolves available.
	return 200, []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"1"]}]}}`), nil
}

func (q *rangeQ) RangeQuery(_ context.Context, promql string, start, end time.Time, step time.Duration) (int, []byte, error) {
	q.rangeQueries = append(q.rangeQueries, promql)
	q.lastStart, q.lastEnd, q.lastStep = start, end, step
	return 200, []byte(`{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{},"values":[[100,"0.5"],[160,"0.6"]]}
	]}}`), nil
}

func sparkConn(clk clock.Clock, q metrics.Querier, objs ...interface{}) *ClusterConn {
	var runtimeObjs []runtimeObject
	_ = runtimeObjs
	cs := fake.NewSimpleClientset()
	for _, o := range objs {
		switch v := o.(type) {
		case *appsv1.Deployment:
			_, _ = cs.AppsV1().Deployments(v.Namespace).Create(context.Background(), v, metav1.CreateOptions{})
		case *corev1.Pod:
			_, _ = cs.CoreV1().Pods(v.Namespace).Create(context.Background(), v, metav1.CreateOptions{})
		}
	}
	c := &ClusterConn{
		typed:      cs,
		clk:        clk,
		metricsCfg: config.MetricsConfig{Endpoint: "http://prom"},
		metricsTF:  fakeTF{q: q},
	}
	c.caps = capability.Set{}
	return c
}

// runtimeObject is a tiny alias to keep the helper signature readable.
type runtimeObject = interface{}

func chattyDeployment() (*appsv1.Deployment, []*corev1.Pod) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}},
	}
	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api-1", Labels: map[string]string{"app": "api"}}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "api-2", Labels: map[string]string{"app": "api"}}},
	}
	return dep, pods
}

func TestWorkloadSparklinesQueriesCurrentPods(t *testing.T) {
	clk := clock.NewFake(time.Unix(10000, 0))
	q := &rangeQ{}
	dep, pods := chattyDeployment()
	c := sparkConn(clk, q, dep, pods[0], pods[1])

	set, err := c.WorkloadSparklines(context.Background(), "Deployment", "team", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(q.rangeQueries) != 2 {
		t.Fatalf("want 2 range queries (cpu+mem), got %d: %v", len(q.rangeQueries), q.rangeQueries)
	}
	cpuQ := q.rangeQueries[0]
	if !strings.Contains(cpuQ, `pod=~"^(api-1|api-2)$"`) {
		t.Fatalf("cpu query must target current pods, got %q", cpuQ)
	}
	if !strings.Contains(cpuQ, `namespace="team"`) || !strings.Contains(cpuQ, `container!="POD"`) {
		t.Fatalf("cpu query missing namespace/container guards: %q", cpuQ)
	}
	if !strings.HasPrefix(cpuQ, "sum(rate(container_cpu_usage_seconds_total") {
		t.Fatalf("cpu query shape wrong: %q", cpuQ)
	}
	if !strings.HasPrefix(q.rangeQueries[1], "sum(container_memory_working_set_bytes") {
		t.Fatalf("mem query shape wrong: %q", q.rangeQueries[1])
	}
	// 30m window / 60s step ending "now".
	if q.lastStep != 60*time.Second || q.lastEnd.Sub(q.lastStart) != 30*time.Minute || !q.lastEnd.Equal(time.Unix(10000, 0)) {
		t.Fatalf("window wrong: start=%v end=%v step=%v", q.lastStart, q.lastEnd, q.lastStep)
	}
	if len(set.CPU) != 2 || set.CPU[0] != (metrics.Point{Unix: 100, Value: 0.5}) {
		t.Fatalf("cpu series wrong: %+v", set.CPU)
	}
	if len(set.Mem) != 2 {
		t.Fatalf("mem series wrong: %+v", set.Mem)
	}
}

func TestWorkloadSparklinesZeroPodsShortCircuits(t *testing.T) {
	clk := clock.NewFake(time.Unix(10000, 0))
	q := &rangeQ{}
	dep, _ := chattyDeployment() // deployment exists, no pods match
	c := sparkConn(clk, q, dep)

	set, err := c.WorkloadSparklines(context.Background(), "Deployment", "team", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(q.rangeQueries) != 0 {
		t.Fatalf("no pods -> no prometheus queries, got %v", q.rangeQueries)
	}
	if set.CPU == nil || set.Mem == nil || len(set.CPU) != 0 || len(set.Mem) != 0 {
		t.Fatalf("want empty (non-nil) series, got %+v", set)
	}
}

func TestWorkloadSparklinesUnavailableMetrics(t *testing.T) {
	dep, pods := chattyDeployment()
	cs := fake.NewSimpleClientset(dep, pods[0])
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}
	c.caps = capability.Set{}

	_, err := c.WorkloadSparklines(context.Background(), "Deployment", "team", "api")
	if err == nil || !strings.Contains(err.Error(), "metrics unavailable") {
		t.Fatalf("want metrics-unavailable error, got %v", err)
	}
}

func TestClusterSparklinesUsesFractionQueries(t *testing.T) {
	clk := clock.NewFake(time.Unix(10000, 0))
	q := &rangeQ{}
	c := sparkConn(clk, q)

	set, err := c.ClusterSparklines(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// the queries sent before scopes, byte for byte (Settings S3b §3.3)
	const cpuQuery = `1 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m]))`
	const memQuery = `1 - sum(node_memory_MemAvailable_bytes) / sum(node_memory_MemTotal_bytes)`
	if len(q.rangeQueries) != 2 || q.rangeQueries[0] != cpuQuery || q.rangeQueries[1] != memQuery {
		t.Fatalf("want the instant cpu/mem fraction queries as range twins, got %v", q.rangeQueries)
	}
	if len(set.CPU) != 2 || len(set.Mem) != 2 {
		t.Fatalf("series wrong: %+v", set)
	}
}

package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/metrics"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func svc(ns, name string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: port}}},
	}
}

func TestDiscoverPrefersFirstNamed(t *testing.T) {
	cs := fake.NewSimpleClientset(
		svc("monitoring", "mimir-query-frontend", 8080),
		svc("monitoring", "prometheus-operated", 9090),
	)
	c := &ClusterConn{typed: cs}
	d := c.discover(context.Background())
	if d.MultiMatch || d.Chosen == nil || d.Chosen.Name != "prometheus-operated" {
		t.Fatalf("want prometheus-operated first, got %+v", d.Chosen)
	}
}

func TestDiscoverLabelMultiMatch(t *testing.T) {
	a := svc("ns1", "p1", 9090)
	b := svc("ns2", "p2", 9090)
	for _, s := range []*corev1.Service{a, b} {
		s.Labels = map[string]string{"app.kubernetes.io/name": "prometheus", "app.kubernetes.io/component": "server"}
	}
	cs := fake.NewSimpleClientset(a, b)
	c := &ClusterConn{typed: cs}
	d := c.discover(context.Background())
	if !d.MultiMatch {
		t.Fatalf("want multi-match, got %+v", d)
	}
}

func TestClusterMetricsUnavailableCachesShort(t *testing.T) {
	// No services, no config -> unavailable. With a fake clock, the unavailable
	// capability stays cached before its TTL.
	cs := fake.NewSimpleClientset()
	clk := clock.NewFake(time.Unix(0, 0))
	c := &ClusterConn{typed: cs, clk: clk}
	_, cap1 := c.ClusterMetrics(context.Background(), false)
	if cap1.Available || cap1.Mode != metrics.ModeUnavailable {
		t.Fatalf("want unavailable, got %+v", cap1)
	}
	// before TTL: still cached unavailable (no panic, same result)
	_, cap2 := c.ClusterMetrics(context.Background(), false)
	if cap2.Available {
		t.Fatal("should still be unavailable")
	}
}

// scriptedQ is a programmable Querier: it answers vector(1) liveness and the
// cpu/mem scalar queries, and can be flipped to fail. Single-goroutine use only
// (ClusterMetrics serializes calls under its mutex), so no locking needed.
type scriptedQ struct {
	fail     bool
	cpu, mem string
}

func (q *scriptedQ) InstantQuery(_ context.Context, promql string) (int, []byte, error) {
	if q.fail {
		return 503, []byte("backend down"), nil
	}
	switch {
	case strings.Contains(promql, "vector(1)"):
		return 200, []byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"1"]}]}}`), nil
	case strings.Contains(promql, "node_cpu"):
		return 200, []byte(`{"status":"success","data":{"resultType":"scalar","result":[1,"` + q.cpu + `"]}}`), nil
	case strings.Contains(promql, "node_memory"):
		return 200, []byte(`{"status":"success","data":{"resultType":"scalar","result":[1,"` + q.mem + `"]}}`), nil
	}
	return 200, []byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`), nil
}

type fakeTF struct{ q metrics.Querier }

func (f fakeTF) Direct(_ string, _ metrics.Auth, _ bool) metrics.Querier    { return f.q }
func (f fakeTF) Proxy(_ metrics.ServiceCandidate, _ string) metrics.Querier { return f.q }

// connWithTF builds a ClusterConn whose metrics resolve to the scripted querier
// via an explicit endpoint (skips discovery), using a fake clock.
func connWithTF(clk clock.Clock, q metrics.Querier) *ClusterConn {
	return &ClusterConn{
		typed:      fake.NewSimpleClientset(),
		clk:        clk,
		metricsCfg: config.MetricsConfig{Endpoint: "http://prom"},
		metricsTF:  fakeTF{q: q},
	}
}

func TestClusterMetricsAvailableAndSampleTTL(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	q := &scriptedQ{cpu: "0.4", mem: "0.6"}
	c := connWithTF(clk, q)

	cm, cap := c.ClusterMetrics(context.Background(), false)
	if !cap.Available {
		t.Fatalf("want available, got %+v", cap)
	}
	if cm.CPUFraction == nil || *cm.CPUFraction != 0.4 || cm.MemFraction == nil || *cm.MemFraction != 0.6 {
		t.Fatalf("samples wrong: %+v %+v", cm.CPUFraction, cm.MemFraction)
	}

	// within the 15s sample TTL: underlying change is NOT seen (cached)
	q.cpu = "0.9"
	cm2, _ := c.ClusterMetrics(context.Background(), false)
	if cm2.CPUFraction == nil || *cm2.CPUFraction != 0.4 {
		t.Fatalf("samples should be cached for 15s, got %+v", cm2.CPUFraction)
	}

	// past the sample TTL: re-queried, new value
	clk.Advance(16 * time.Second)
	cm3, _ := c.ClusterMetrics(context.Background(), false)
	if cm3.CPUFraction == nil || *cm3.CPUFraction != 0.9 {
		t.Fatalf("want refreshed 0.9, got %+v", cm3.CPUFraction)
	}
}

func TestClusterMetricsForceReprobeDetectsDeath(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	q := &scriptedQ{cpu: "0.4", mem: "0.6"}
	c := connWithTF(clk, q)

	if _, cap := c.ClusterMetrics(context.Background(), false); !cap.Available {
		t.Fatalf("want available, got %+v", cap)
	}
	q.fail = true // backend dies
	// available is cached for the conn lifetime: no re-probe without force
	if _, cap := c.ClusterMetrics(context.Background(), false); !cap.Available {
		t.Fatal("available should stay cached for lifetime")
	}
	// forceReprobe re-probes and detects the death
	if _, cap := c.ClusterMetrics(context.Background(), true); cap.Available {
		t.Fatalf("forceReprobe should detect death, got %+v", cap)
	}
}

func TestClusterMetricsUnavailableReprobesAfterTTL(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	q := &scriptedQ{cpu: "0.4", mem: "0.6", fail: true}
	c := connWithTF(clk, q)

	if _, cap := c.ClusterMetrics(context.Background(), false); cap.Available {
		t.Fatalf("want unavailable, got %+v", cap)
	}
	q.fail = false // backend recovers
	// before the 45s unavailable TTL: still cached unavailable
	clk.Advance(10 * time.Second)
	if _, cap := c.ClusterMetrics(context.Background(), false); cap.Available {
		t.Fatal("should stay cached unavailable before TTL")
	}
	// past the TTL: re-probe picks up recovery
	clk.Advance(40 * time.Second) // total 50s > 45s
	if _, cap := c.ClusterMetrics(context.Background(), false); !cap.Available {
		t.Fatalf("should re-probe and recover after TTL, got %+v", cap)
	}
}

package fleet

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/metrics"
)

// Settings S3b §2.4 on a connection: until Klyx reads through Loki and
// metrics installations, off makes no request, and a subscription is never
// read by discovery in the member or any other way.

// metricsDiscoveryCounted is a connection whose Service lookups - the
// metrics discovery - are counted.
func metricsDiscoveryCounted(cfg config.MetricsConfig) (*ClusterConn, *atomic.Int32) {
	typed := typedfake.NewSimpleClientset(svc("monitoring", "prometheus-operated", 9090))
	var lookups atomic.Int32
	typed.PrependReactor("*", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		lookups.Add(1)
		return false, nil, nil
	})
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, cfg)
	c.metricsTF = fakeTF{q: &scriptedQ{cpu: "0.4", mem: "0.6"}}
	return c, &lookups
}

func TestMetricsOffOrSubscribedIsNeverDiscovered(t *testing.T) {
	c, lookups := metricsDiscoveryCounted(config.MetricsConfig{})
	_, _ = c.ClusterMetrics(context.Background(), true)
	if lookups.Load() == 0 {
		t.Fatal("the control: a block-less cluster's discovery was not counted")
	}
	c, lookups = metricsDiscoveryCounted(config.MetricsConfig{Off: true})
	_, capb := c.ClusterMetrics(context.Background(), true)
	if capb.Available || capb.Reason != metrics.ReasonOff || lookups.Load() != 0 {
		t.Fatalf("off: %+v, %d lookups", capb, lookups.Load())
	}
	c, lookups = metricsDiscoveryCounted(config.MetricsConfig{Source: "lgtm-we-metrics", Scope: config.ScopeTenant})
	_, capb = c.ClusterMetrics(context.Background(), true)
	if capb.Available || !strings.Contains(capb.Reason, "the installation lgtm-we-metrics") || !strings.Contains(capb.Reason, "does not read through installations yet") || lookups.Load() != 0 {
		t.Fatalf("subscribed: %+v, %d lookups", capb, lookups.Load())
	}
}

func TestLokiOffOrSubscribed(t *testing.T) {
	c := NewClusterConn("x", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	c.WithLoki(&config.EndpointConfig{Off: true})
	if _, err := c.Loki(); !errors.Is(err, ErrLokiOff) || c.logHistoryPresent() {
		t.Fatalf("off: %v, present %v", err, c.logHistoryPresent())
	}
	c.WithLoki(&config.EndpointConfig{Source: "lgtm-we-logs"})
	if _, err := c.Loki(); err == nil || !strings.Contains(err.Error(), "the installation lgtm-we-logs") || errors.Is(err, ErrNoLoki) || !c.logHistoryPresent() {
		t.Fatalf("subscribed: %v, present %v", err, c.logHistoryPresent())
	}
	// the control: a block naming nowhere is no Loki
	c.WithLoki(&config.EndpointConfig{Tenant: "lab"})
	if _, err := c.Loki(); !errors.Is(err, ErrNoLoki) || c.logHistoryPresent() {
		t.Fatalf("unlocated: %v", err)
	}
}

func TestTheFactorySubscribesEveryKind(t *testing.T) {
	sources := []config.SourceConfig{
		{ID: "am", Kind: config.SourceKindAlertmanager, Host: "prd", Discover: true, Serves: []config.ServeConfig{{Group: "we"}}},
		{ID: "logs", Kind: config.SourceKindLoki, Host: "prd", Serves: []config.ServeConfig{{Group: "we"}}},
		{ID: "m", Kind: config.SourceKindMetrics, Host: "prd", Discover: true, Serves: []config.ServeConfig{{Cluster: "dev"}}},
	}
	var got config.ClusterConfig
	f := SubscribingFactory(sources, func(cc config.ClusterConfig) (Conn, error) { got = cc; return nil, nil })
	_, _ = f(config.ClusterConfig{Name: "dev", Group: "we"})
	if got.Alertmanager == nil || got.Alertmanager.Source != "am" || got.Loki == nil || got.Loki.Source != "logs" || got.Metrics == nil || got.Metrics.Source != "m" {
		t.Fatalf("dev: %+v %+v %+v", got.Alertmanager, got.Loki, got.Metrics)
	}
	// a cluster's own block of a kind wins for that kind only
	own := &config.EndpointConfig{Endpoint: "https://loki.example"}
	_, _ = f(config.ClusterConfig{Name: "dev", Group: "we", Loki: own})
	if got.Loki != own || got.Metrics == nil || got.Metrics.Source != "m" {
		t.Fatalf("own Loki: %+v %+v", got.Loki, got.Metrics)
	}
	// nothing serves lab: discovery for alerts and metrics, no Loki
	_, _ = f(config.ClusterConfig{Name: "lab"})
	if got.Alertmanager != nil || got.Loki != nil || got.Metrics != nil {
		t.Fatalf("lab: %+v %+v %+v", got.Alertmanager, got.Loki, got.Metrics)
	}
}

// An S2 candidate is checked for every kind: two Loki installations
// serving one group refuse it as two Alertmanager ones would.
func TestTheCandidateCheckResolvesEveryKind(t *testing.T) {
	logs := func(id string) config.SourceConfig {
		return config.SourceConfig{ID: id, Kind: config.SourceKindLoki, Endpoint: "https://" + id + ".example", Serves: []config.ServeConfig{{Group: "we"}}}
	}
	a, err := NewAlertSources([]config.SourceConfig{logs("a"), logs("b")}, []config.ClusterConfig{{Name: "prd"}})
	if err != nil {
		t.Fatal(err) // nothing in group we yet
	}
	if err := a.Resolve([]config.ClusterConfig{{Name: "prd", Group: "we"}}); err == nil || !strings.Contains(err.Error(), "for loki") {
		t.Fatalf("%v", err)
	}
}

// recordingQ answers liveness and records every query it is sent.
type recordingQ struct{ sent []string }

func (q *recordingQ) InstantQuery(_ context.Context, promql string) (int, []byte, error) {
	q.sent = append(q.sent, promql)
	if promql == "vector(1)" {
		return 200, []byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"1"]}]}}`), nil
	}
	return 200, []byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`), nil
}

func (q *recordingQ) RangeQuery(_ context.Context, promql string, _, _ time.Time, _ time.Duration) (int, []byte, error) {
	q.sent = append(q.sent, promql)
	return 200, []byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`), nil
}

// A cluster's own metrics block with match reads every series through it:
// each data query carries every matcher, the liveness probe none (Settings
// S3b §3.3). Without match, nothing is added.
func TestALegacyMetricsMatchScopesEveryQuery(t *testing.T) {
	for _, tc := range []struct {
		match map[string]string
		want  string
	}{
		{map[string]string{"cluster": "dev", "team": `a"b`}, `cluster="dev",team="a\"b"`},
		{nil, ""},
	} {
		q := &recordingQ{}
		c := connWithTF(clock.NewFake(time.Unix(0, 0)), q)
		c.metricsCfg.Match = tc.match
		if _, cap := c.ClusterMetrics(context.Background(), false); !cap.Available {
			t.Fatalf("unavailable: %+v", cap)
		}
		c.WorkloadMetrics(context.Background(), "shop")
		c.RouteMetrics(context.Background(), []string{"shop/web"})
		var data int
		for _, s := range q.sent {
			if s == "vector(1)" {
				continue
			}
			data++
			if tc.want != "" && strings.Count(s, tc.want) == 0 {
				t.Errorf("%v: unscoped query %s", tc.match, s)
			}
			if tc.want == "" && (strings.Contains(s, "cluster=") || strings.Contains(s, "team=")) {
				t.Errorf("unscoped reader scoped %s", s)
			}
		}
		if data < 3 {
			t.Fatalf("%v: %d data queries: %v", tc.match, data, q.sent)
		}
	}
}

package fleet

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/secretstore"
)

// Codex's C5 implementation review, its probes adopted.

func TestSampleReadAcrossARevocationIsDropped(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "MemTotal") {
			once.Do(func() { close(entered) })
			<-release
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
	}))
	defer srv.Close()
	origin, _ := safehttp.OriginOf(srv.URL)
	store := &secretstore.Memory{}
	storeRecord(t, store, "prd", origin, "bearer", "C5-OLD-TOKEN")
	c, reg := credConn(t, config.MetricsConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}}, store)
	done := make(chan struct {
		sample metrics.ClusterMetrics
		cap    metrics.MetricsCapability
	}, 1)
	go func() {
		s, capb := c.ClusterMetrics(context.Background(), false)
		done <- struct {
			sample metrics.ClusterMetrics
			cap    metrics.MetricsCapability
		}{s, capb}
	}()
	<-entered
	reg.Revoke("prd", config.EndpointMetrics)
	close(release)
	result := <-done
	if result.cap.Available || result.sample.CPUFraction != nil || result.sample.MemFraction != nil {
		t.Errorf("read revoked during sample query was shown: sample=%+v cap=%+v", result.sample, result.cap)
	}
	c.metricsMu.Lock()
	cached := !c.metricsState.samplesExp.IsZero()
	c.metricsMu.Unlock()
	if cached {
		t.Fatal("read revoked during sample query was cached")
	}
}

// anyQ answers every instant query with one labelled sample (a scalar
// reader and a vector reader both take it) and every range query with one
// series.
type anyQ struct{}

func (anyQ) InstantQuery(context.Context, string) (int, []byte, error) {
	return 200, []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"namespace":"a","pod":"p1","container":"c"},"value":[0,"1"]}]}}`), nil
}

func (anyQ) RangeQuery(context.Context, string, time.Time, time.Time, time.Duration) (int, []byte, error) {
	return 200, []byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[100,"0.5"]]}]}}`), nil
}

// movingQ answers through q and, when move is set, moves the endpoint's
// revision on every query but the probe, after the answer is read: the
// change lands in a read the transport has already accepted, which its own
// gate cannot refuse (a proxied endpoint, a reconfiguration, a response
// read just before a Remove).
type movingQ struct {
	q       metrics.Querier
	reg     *endpointcred.Registry
	cluster string
	move    bool
}

func (m *movingQ) moved(promql string) {
	if m.move && !strings.Contains(promql, "vector(1)") {
		m.reg.Bump(m.cluster, config.EndpointMetrics, false)
	}
}

func (m *movingQ) InstantQuery(ctx context.Context, promql string) (int, []byte, error) {
	code, b, err := m.q.InstantQuery(ctx, promql)
	m.moved(promql)
	return code, b, err
}

func (m *movingQ) RangeQuery(ctx context.Context, promql string, start, end time.Time, step time.Duration) (int, []byte, error) {
	code, b, err := m.q.(metrics.RangeQuerier).RangeQuery(ctx, promql, start, end, step)
	m.moved(promql)
	return code, b, err
}

// C5 §7, Codex's finding 6 for every reader: a result read across a move of
// the endpoint's revision is not shown as available, whichever query the
// move landed in. Each reader first reads available with the same answers.
func TestEveryMetricsReaderDropsAReadAcrossARevisionMove(t *testing.T) {
	dep, pods := chattyDeployment()
	type reader struct {
		name string
		conn func(q metrics.Querier) *ClusterConn
		read func(c *ClusterConn) (bool, string)
	}
	plain := func(objs ...any) func(q metrics.Querier) *ClusterConn {
		return func(q metrics.Querier) *ClusterConn {
			c := sparkConn(clock.NewFake(time.Unix(10000, 0)), q, objs...)
			c.name = "prd"
			c.caps = capability.Set{Network: capability.NetworkCapability{HasHubble: true, HasEnvoyProxy: true}}
			return c
		}
	}
	for _, r := range []reader{
		{"cluster samples", plain(), func(c *ClusterConn) (bool, string) {
			s, capb := c.ClusterMetrics(context.Background(), false)
			return capb.Available && s.CPUFraction != nil, capb.Reason
		}},
		{"workload usage", plain(), func(c *ClusterConn) (bool, string) {
			_, st := c.WorkloadMetrics(context.Background(), "")
			return st.Available, st.Message
		}},
		{"node usage", plain(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "p1"}, Spec: corev1.PodSpec{NodeName: "n1"}}),
			func(c *ClusterConn) (bool, string) {
				_, st := c.NodeUtilization(context.Background())
				return st.Available, st.Message
			}},
		{"container usage", plain(), func(c *ClusterConn) (bool, string) {
			_, st := c.PodContainerUsage(context.Background(), "a", "p1")
			return st.Available, st.Message
		}},
		{"route metrics", plain(), func(c *ClusterConn) (bool, string) {
			_, st := c.RouteMetrics(context.Background(), []string{"default/web"})
			return st.Available, st.Message
		}},
		{"hubble flows", plain(), func(c *ClusterConn) (bool, string) {
			_, st := c.HubbleFlowStats(context.Background())
			return st.Available, st.Message
		}},
		{"cluster sparklines", plain(), func(c *ClusterConn) (bool, string) {
			_, err := c.ClusterSparklines(context.Background())
			return err == nil, errString(err)
		}},
		{"workload sparklines", plain(dep, pods[0], pods[1]), func(c *ClusterConn) (bool, string) {
			_, err := c.WorkloadSparklines(context.Background(), "Deployment", dep.Namespace, dep.Name)
			return err == nil, errString(err)
		}},
		{"gateway rules", func(q metrics.Querier) *ClusterConn {
			e := ruleEnv{hasEnvoy: true, q: &ruleQ{}, routes: ruleRoutes(),
				gwInfraRef: epRef("", "eg-proxy"), classRef: epRef("envoy-gateway-system", "default"),
				proxies: []*unstructured.Unstructured{
					envoyProxy("infra", "eg-proxy", map[string]any{"provider": map[string]any{"type": "Kubernetes"}}),
					envoyProxy("envoy-gateway-system", "default", map[string]any{"mergeGateways": false}),
				}}
			c := e.conn(t)
			c.metricsTF = fakeTF{q: q}
			return c
		}, func(c *ClusterConn) (bool, string) {
			res, err := c.RuleMetrics(context.Background(), "infra", "eg")
			return err == nil && res.Status.Available, res.Status.Message
		}},
	} {
		for _, move := range []bool{false, true} {
			reg := endpointcred.NewRegistry()
			var inner metrics.Querier = anyQ{}
			if r.name == "gateway rules" {
				inner = &ruleQ{}
			}
			mq := &movingQ{q: inner, reg: reg, cluster: "prd", move: move}
			c := r.conn(mq)
			c.creds.Revisions = reg
			ok, why := r.read(c)
			if !move && !ok {
				t.Fatalf("%s: with no move the read is available: %s", r.name, why)
			}
			if move && (ok || !strings.Contains(why, "credential changed during the read")) {
				t.Errorf("%s: a read across a revision move was shown: %v %q", r.name, ok, why)
			}
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// blockQ answers like anyQ, holding the first query after the probe until
// released.
type blockQ struct {
	once             sync.Once
	entered, release chan struct{}
}

func (b *blockQ) InstantQuery(ctx context.Context, promql string) (int, []byte, error) {
	if !strings.Contains(promql, "vector(1)") {
		b.once.Do(func() { close(b.entered); <-b.release })
	}
	return anyQ{}.InstantQuery(ctx, promql)
}

// Finding 7 with finding 6: a Settings action that moves the connection to
// the reviewed configuration drops a read already in flight on the old one.
func TestAConfigurationChangeDropsAReadInFlight(t *testing.T) {
	q := &blockQ{entered: make(chan struct{}), release: make(chan struct{})}
	c := sparkConn(clock.NewFake(time.Unix(10000, 0)), q)
	c.name = "prd"
	c.creds.Revisions = endpointcred.NewRegistry()
	done := make(chan bool, 1)
	go func() {
		_, st := c.WorkloadMetrics(context.Background(), "")
		done <- st.Available
	}()
	<-q.entered
	c.SetMetricsConfig(config.MetricsConfig{Endpoint: "http://reviewed"})
	close(q.release)
	if <-done {
		t.Fatal("a read through the old configuration was shown after the change")
	}
}

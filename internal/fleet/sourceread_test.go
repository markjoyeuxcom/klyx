package fleet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	typedfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/secretstore"
)

// Settings S3b §3.1-§3.3: Loki and metrics read through an installation,
// per subscription.

// obsFake answers Prometheus and Loki, directly or behind the host's
// service proxy, and records what it was sent.
type obsFake struct {
	mu          sync.Mutex
	code        int
	queries     []string
	paths       []string
	auth        []string
	tenant      []string
	onData      func() // run on each data query, outside the lock
	hold        chan struct{}
	inFlight    int
	maxInFlight int
}

func (f *obsFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("query")
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.paths = append(f.paths, r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.tenant = append(f.tenant, r.Header.Get("X-Scope-OrgID"))
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	code, on, hold := f.code, f.onData, f.hold
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	path := r.URL.Path
	prom := strings.HasSuffix(path, "/api/v1/query") || strings.HasSuffix(path, "/api/v1/query_range")
	if strings.Contains(path, "/loki/") && hold != nil {
		<-hold
	}
	if prom && q != "vector(1)" && !strings.Contains(path, "/loki/") {
		if on != nil {
			on()
		}
		if hold != nil {
			<-hold
		}
	}
	if code != 0 {
		w.WriteHeader(code)
		return
	}
	switch {
	case strings.HasSuffix(path, "/api/v1/query") && q == "vector(1)":
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`))
	case strings.HasSuffix(path, "/api/v1/query"):
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"0.5"]}]}}`))
	case strings.HasSuffix(path, "/api/v1/query_range"):
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1,"0.5"]]}]}}`))
	case strings.HasSuffix(path, "/loki/api/v1/labels"):
		_, _ = w.Write([]byte(`{"status":"success","data":["namespace","pod"]}`))
	case strings.HasSuffix(path, "/loki/api/v1/status/buildinfo"):
		_, _ = w.Write([]byte(`{"version":"3.1.0"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *obsFake) sent() (queries, tenants, auth []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...), append([]string(nil), f.tenant...), append([]string(nil), f.auth...)
}

type srcRig struct {
	t        *testing.T
	fake     *obsFake
	api      *httptest.Server
	mu       sync.Mutex
	host     *ClusterConn
	sources  *AlertSources
	b        *alertBuckets
	conns    map[string]*ClusterConn
	store    *secretstore.Memory
	revs     *endpointcred.Registry
	srcs     []config.SourceConfig
	clusters []config.ClusterConfig
}

// newSrcRig hosts srcs in prd behind a fake API server and connects each
// member with the blocks the installations give it.
func newSrcRig(t *testing.T, srcs []config.SourceConfig, members ...config.ClusterConfig) *srcRig {
	t.Helper()
	r := &srcRig{t: t, fake: &obsFake{}, conns: map[string]*ClusterConn{}, store: &secretstore.Memory{}, revs: endpointcred.NewRegistry(), srcs: srcs}
	r.api = httptest.NewServer(r.fake)
	t.Cleanup(r.api.Close)
	r.host = r.newHost()
	r.clusters = append([]config.ClusterConfig{{Name: "prd", Context: "prd"}}, members...)
	var err error
	if r.sources, err = NewAlertSources(srcs, r.clusters); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.b = newAlertBuckets(ctx, r.sources, Credentials{Store: r.store, Revisions: r.revs}, clock.Real{}, func(name string) (*ClusterConn, bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if name == "prd" && r.host != nil {
			return r.host, true
		}
		return nil, false
	})
	for _, cc := range members {
		mc := config.MetricsConfig{}
		if m := config.ConnectionMetrics(srcs, cc); m != nil {
			mc = *m
		}
		c := NewClusterConn(cc.Name, typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, mc)
		c.WithLoki(config.ConnectionLoki(srcs, cc))
		c.withAlertBuckets(r.b)
		r.conns[cc.Name] = c
	}
	return r
}

func (r *srcRig) newHost() *ClusterConn {
	h := NewClusterConn("prd", nil, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	h.restConfig = &rest.Config{Host: r.api.URL}
	usable(h, 1)
	return h
}

// reconnect replaces the host's connection: another identity.
func (r *srcRig) reconnect() {
	h := r.newHost()
	r.mu.Lock()
	r.host = h
	r.mu.Unlock()
}

func (r *srcRig) apply(srcs []config.SourceConfig, clusters []config.ClusterConfig) {
	r.t.Helper()
	if _, err := r.sources.Apply(srcs, clusters); err != nil {
		r.t.Fatal(err)
	}
}

func metricsInstallation() config.SourceConfig {
	return config.SourceConfig{ID: "lgtm-we-metrics", Kind: config.SourceKindMetrics, Host: "prd", Tenant: "ops",
		ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "prom", Port: "9090"}, MemberLabel: "cluster",
		Serves: []config.ServeConfig{{Cluster: "dev"}, {Cluster: "stg"}}}
}

func lokiInstallation() config.SourceConfig {
	return config.SourceConfig{ID: "lgtm-we-logs", Kind: config.SourceKindLoki, Host: "prd", Tenant: "ops",
		ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "loki", Port: "3100"}, MemberLabel: "k8s_cluster_name",
		Serves: []config.ServeConfig{{Cluster: "dev"}}}
}

func TestASubscribedMemberReadsMetricsThroughItsHost(t *testing.T) {
	r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	cm, capb := r.conns["dev"].ClusterMetrics(context.Background(), false)
	if !capb.Available || capb.Mode != metrics.ModeSubscribed || !strings.Contains(capb.Source, "lgtm-we-metrics, Service monitoring/prom:9090 in prd") {
		t.Fatalf("capability: %+v", capb)
	}
	if cm.CPUFraction == nil || *cm.CPUFraction != 0.5 {
		t.Fatalf("samples: %+v", cm)
	}
	queries, tenants, _ := r.fake.sent()
	if len(queries) < 3 || queries[0] != "vector(1)" {
		t.Fatalf("sent: %q", queries)
	}
	for i, q := range queries[1:] {
		if !strings.Contains(q, `cluster="dev"`) {
			t.Errorf("unscoped: %s", q)
		}
		if tenants[i+1] != "ops" {
			t.Errorf("tenant %q", tenants[i+1])
		}
	}
	// a scope: tenant subscription sends no matcher
	own := config.ClusterConfig{Name: "dev", Context: "dev", Metrics: &config.MetricsConfig{Source: "lgtm-we-metrics", Scope: config.ScopeTenant}}
	src := metricsInstallation()
	src.Serves = []config.ServeConfig{{Cluster: "dev"}}
	r2 := newSrcRig(t, []config.SourceConfig{src}, own)
	if _, capb := r2.conns["dev"].ClusterMetrics(context.Background(), false); !capb.Available {
		t.Fatalf("tenant-wide: %+v", capb)
	}
	queries, _, _ = r2.fake.sent()
	for _, q := range queries {
		if strings.Contains(q, "cluster=") {
			t.Errorf("a tenant-wide read was scoped: %s", q)
		}
	}
}

func TestADirectMetricsInstallationUsesItsOwnCredential(t *testing.T) {
	f := &obsFake{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	src := config.SourceConfig{ID: "mimir", Kind: config.SourceKindMetrics, Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"},
		Tenant: "ops", MemberLabel: "cluster", Serves: []config.ServeConfig{{Cluster: "dev"}}}
	r := newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"})
	dev := r.conns["dev"]
	if _, capb := dev.ClusterMetrics(context.Background(), true); capb.Available || !strings.Contains(capb.Reason, "no credential is stored") {
		t.Fatalf("without its credential: %+v", capb)
	}
	v, _ := endpointcred.Record{V: 1, Origin: strings.TrimRight(srv.URL, "/"), Type: "bearer", Secret: "S3B-SECRET"}.Encode()
	if err := r.store.Set(endpointcred.SourceAccount("mimir"), v); err != nil {
		t.Fatal(err)
	}
	cm, capb := dev.ClusterMetrics(context.Background(), true)
	if !capb.Available || cm.CPUFraction == nil {
		t.Fatalf("with it: %+v", capb)
	}
	_, tenants, auth := f.sent()
	for i := range auth {
		if auth[i] != "Bearer S3B-SECRET" || tenants[i] != "ops" {
			t.Fatalf("sent auth %q tenant %q", auth[i], tenants[i])
		}
	}
	if strings.Contains(capb.Source+capb.Reason, "S3B-SECRET") {
		t.Fatalf("the secret is shown: %+v", capb)
	}
	// a credential replaced during a read: the read is dropped
	f.mu.Lock()
	f.onData = func() { r.revs.Bump(endpointcred.SourceAccount("mimir"), config.EndpointMetrics, true) }
	f.mu.Unlock()
	if _, err := dev.ChatMetricsRange(context.Background(), metrics.ClusterCPU.With(metrics.None{}), time.Unix(0, 0), time.Unix(60, 0), time.Minute); err == nil ||
		!strings.Contains(err.Error(), "the read was dropped: the installation's credential changed") {
		t.Fatalf("a read across a credential change: %v", err)
	}
}

// A read whose terms move while it reads is dropped whole (S3b §13): the
// installation, the subscription, the host's connection, its usability
// incarnation and an observed denial, each moving mid-read.
func TestAMetricsReadAcrossAMoveIsDropped(t *testing.T) {
	own := func(team string) config.ClusterConfig {
		return config.ClusterConfig{Name: "dev", Context: "dev", Metrics: &config.MetricsConfig{Source: "lgtm-we-metrics", Match: map[string]string{"team": team}}}
	}
	for _, tc := range []struct {
		name string
		move func(r *srcRig)
	}{
		{"installation", func(r *srcRig) {
			src := metricsInstallation()
			src.Tenant = "ops-2"
			r.apply([]config.SourceConfig{src}, r.clusters)
		}},
		{"subscription", func(r *srcRig) { r.apply(r.srcs, []config.ClusterConfig{{Name: "prd"}, own("b")}) }},
		{"host reconnect", func(r *srcRig) { r.reconnect() }},
		{"recovery in place", func(r *srcRig) { usable(r.host, 2) }},
		{"denial", func(r *srcRig) { r.sources.Denied("lgtm-we-metrics") }},
	} {
		for _, read := range []string{"cluster samples", "chat"} {
			r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, own("a"))
			dev := r.conns["dev"]
			if _, capb := dev.ClusterMetrics(context.Background(), true); !capb.Available {
				t.Fatalf("%s: %+v", tc.name, capb)
			}
			var once sync.Once
			r.fake.mu.Lock()
			r.fake.onData = func() { once.Do(func() { tc.move(r) }) }
			r.fake.mu.Unlock()
			var why string
			switch read {
			case "cluster samples":
				dev.metricsMu.Lock()
				dev.metricsState.samplesExp = time.Time{}
				dev.metricsMu.Unlock()
				_, capb := dev.ClusterMetrics(context.Background(), false)
				if capb.Available {
					t.Errorf("%s/%s: a read across the move was shown", tc.name, read)
				}
				why = capb.Reason
			case "chat":
				_, err := dev.ChatMetricsRange(context.Background(), metrics.ClusterCPU.With(metrics.None{}), time.Unix(0, 0), time.Unix(60, 0), time.Minute)
				if err == nil {
					t.Errorf("%s/%s: a read across the move was shown", tc.name, read)
					continue
				}
				why = err.Error()
			}
			if !strings.Contains(why, "the read was dropped") {
				t.Errorf("%s/%s: %q", tc.name, read, why)
			}
		}
	}
}

// A host known to be down serves nothing, a cached value included (§13).
func TestADownHostServesNoCachedMetrics(t *testing.T) {
	r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	dev := r.conns["dev"]
	if _, capb := dev.ClusterMetrics(context.Background(), false); !capb.Available {
		t.Fatalf("%+v", capb)
	}
	r.host.mu.Lock()
	r.host.state = Failed
	r.host.mu.Unlock()
	if _, capb := dev.ClusterMetrics(context.Background(), false); capb.Available || !strings.Contains(capb.Reason, "lgtm-we-metrics's host prd is not connected") {
		t.Fatalf("a cached read was served: %+v", capb)
	}
}

// An observed 401 or 403 moves the installation's access generation: the
// member's cache re-keys.
func TestAMetricsDenialMovesTheAccessGeneration(t *testing.T) {
	r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	dev := r.conns["dev"]
	if _, capb := dev.ClusterMetrics(context.Background(), false); !capb.Available {
		t.Fatalf("%+v", capb)
	}
	key := dev.metricsState.key
	r.fake.mu.Lock()
	r.fake.code = http.StatusForbidden
	r.fake.mu.Unlock()
	_, _ = dev.ChatMetricsRange(context.Background(), metrics.ClusterCPU.With(metrics.None{}), time.Unix(0, 0), time.Unix(60, 0), time.Minute)
	if r.sources.Denials("lgtm-we-metrics") != 1 {
		t.Fatalf("denials %d", r.sources.Denials("lgtm-we-metrics"))
	}
	_, _ = dev.ClusterMetrics(context.Background(), false)
	if dev.metricsState.key == key {
		t.Fatal("the cache did not re-key after a denial")
	}
}

// The host bound (§3.1): four hosted metrics requests and two Loki pages
// in flight through one host, shared by every installation and member; a
// cancelled wait leaves without one; another host has its own.
func TestHostSlots(t *testing.T) {
	var h hostSlots
	ctx := context.Background()
	var held []func()
	for range 4 {
		rel, err := h.take(ctx, config.EndpointMetrics, "prd")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, rel)
	}
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err := h.take(short, config.EndpointMetrics, "prd"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a fifth: %v", err)
	}
	if rel, err := h.take(ctx, config.EndpointMetrics, "stg"); err != nil {
		t.Fatalf("another host: %v", err)
	} else {
		rel()
	}
	for range 2 {
		if _, err := h.take(ctx, config.EndpointLoki, "prd"); err != nil {
			t.Fatal(err)
		}
	}
	short2, cancel2 := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel2()
	if _, err := h.take(short2, config.EndpointLoki, "prd"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a third Loki page: %v", err)
	}
	// a release frees one, once
	held[0]()
	held[0]()
	if rel, err := h.take(ctx, config.EndpointMetrics, "prd"); err != nil {
		t.Fatal(err)
	} else {
		defer rel()
	}
	short3, cancel3 := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel3()
	if _, err := h.take(short3, config.EndpointMetrics, "prd"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("a double release freed two")
	}
}

// Reads of two members through one host never exceed its four slots.
func TestTwoMembersShareTheHostBound(t *testing.T) {
	r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"}, config.ClusterConfig{Name: "stg", Context: "stg"})
	for _, m := range []string{"dev", "stg"} {
		if _, capb := r.conns[m].ClusterMetrics(context.Background(), false); !capb.Available {
			t.Fatalf("%s: %+v", m, capb)
		}
	}
	hold := make(chan struct{})
	r.fake.mu.Lock()
	r.fake.hold, r.fake.maxInFlight = hold, 0
	r.fake.mu.Unlock()
	var wg sync.WaitGroup
	for i := range 8 {
		m := []string{"dev", "stg"}[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.conns[m].ChatMetricsRange(context.Background(), metrics.ClusterCPU.With(metrics.None{}), time.Unix(0, 0), time.Unix(60, 0), time.Minute)
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.fake.mu.Lock()
		n := r.fake.inFlight
		r.fake.mu.Unlock()
		if n == 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// the client sends at its own pace: give every read time to try
	time.Sleep(time.Second)
	close(hold)
	wg.Wait()
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	if r.fake.maxInFlight != 4 {
		t.Fatalf("in flight at most %d, want 4", r.fake.maxInFlight)
	}
}

func TestASubscribedMemberReadsLogsThroughItsHost(t *testing.T) {
	r := newSrcRig(t, []config.SourceConfig{lokiInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	dev := r.conns["dev"]
	if !dev.logHistoryPresent() {
		t.Fatal("a subscription is log history")
	}
	lr, err := dev.Loki()
	if err != nil {
		t.Fatal(err)
	}
	if m := lr.Match(); len(m) != 1 || m["k8s_cluster_name"] != "dev" || !strings.Contains(lr.Source(), "lgtm-we-logs, Service monitoring/loki:3100 in prd") || lr.Tenant() != "ops" {
		t.Fatalf("reader: %v %q %q", m, lr.Source(), lr.Tenant())
	}
	if _, data, err := lr.Labels(context.Background(), time.Unix(0, 0), time.Unix(60, 0), "{k8s_cluster_name=\"dev\"}"); err != nil || !data {
		t.Fatalf("labels: %v %v", data, err)
	}
	r.fake.mu.Lock()
	path, tenant := r.fake.paths[len(r.fake.paths)-1], r.fake.tenant[len(r.fake.tenant)-1]
	r.fake.mu.Unlock()
	if !strings.Contains(path, "/services/http:loki:3100/proxy/loki/api/v1/labels") || tenant != "ops" {
		t.Fatalf("sent %s tenant %q", path, tenant)
	}
	// a page after a move of the terms is of a new source
	r.sources.Denied("lgtm-we-logs")
	if _, _, err := lr.Labels(context.Background(), time.Unix(0, 0), time.Unix(60, 0), ""); !errors.Is(err, ErrLokiSourceChanged) || lr.Current() {
		t.Fatalf("after a denial: %v, current %v", err, lr.Current())
	}
	// a host that is down says so
	r.host.mu.Lock()
	r.host.state = Failed
	r.host.mu.Unlock()
	if _, err := dev.Loki(); err == nil || !strings.Contains(err.Error(), "lgtm-we-logs's host prd is not connected") {
		t.Fatalf("host down: %v", err)
	}
}

func TestADirectLokiInstallationUsesItsOwnCredential(t *testing.T) {
	f := &obsFake{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	src := config.SourceConfig{ID: "logs", Kind: config.SourceKindLoki, Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"},
		Tenant: "ops", MemberLabel: "k8s_cluster_name", Serves: []config.ServeConfig{{Cluster: "dev"}}}
	r := newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"})
	if _, err := r.conns["dev"].Loki(); err == nil || !strings.Contains(err.Error(), "no credential is stored") {
		t.Fatalf("without its credential: %v", err)
	}
	v, _ := endpointcred.Record{V: 1, Origin: strings.TrimRight(srv.URL, "/"), Type: "bearer", Secret: "S3B-LOGS"}.Encode()
	if err := r.store.Set(endpointcred.SourceAccount("logs"), v); err != nil {
		t.Fatal(err)
	}
	lr, err := r.conns["dev"].Loki()
	if err != nil {
		t.Fatal(err)
	}
	if v, err := lr.Status(context.Background()); err != nil || v != "3.1.0" {
		t.Fatalf("status: %q %v", v, err)
	}
	_, tenants, auth := f.sent()
	if auth[len(auth)-1] != "Bearer S3B-LOGS" || tenants[len(tenants)-1] != "ops" || strings.Contains(lr.Source(), "S3B-LOGS") {
		t.Fatalf("sent %q %q, source %q", auth, tenants, lr.Source())
	}
}

// S3b §3.5: the running fleet's subscriptions put in force move each kind's
// block on the running connection.
func TestResubscribeFollowsLokiAndMetrics(t *testing.T) {
	logs := config.SourceConfig{ID: "logs", Kind: config.SourceKindLoki, Host: "prd", MemberLabel: "k8s_cluster_name",
		ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "loki", Port: "3100"}, Serves: []config.ServeConfig{{Group: "web"}}}
	mets := config.SourceConfig{ID: "mets", Kind: config.SourceKindMetrics, Host: "prd", Discover: true, MemberLabel: "cluster", Serves: []config.ServeConfig{{Group: "web"}}}
	cfg := &config.Config{Clusters: []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}, Sources: []config.SourceConfig{logs, mets}}
	var conns []*ClusterConn
	reg := NewRegistry(cfg, SubscribingFactory(cfg.Sources, func(cc config.ClusterConfig) (Conn, error) {
		c := fakeClusterConn(cc.Name)
		c.WithLoki(cc.Loki)
		if cc.Metrics != nil {
			c.metricsCfg = *cc.Metrics
		}
		conns = append(conns, c)
		return c, nil
	}))
	sources, err := NewAlertSources(cfg.Sources, cfg.Clusters)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg.EnableAlertSources(ctx, sources, Credentials{}, clock.Real{})
	reg.Start(ctx)
	t.Cleanup(func() {
		for _, c := range conns {
			c.Stop(5 * time.Second)
		}
	})
	dev := func() *ClusterConn { c, _ := reg.Conn("dev"); return c.(*ClusterConn) }
	if dev().LokiConfig() != nil || dev().MetricsConfig().Source != "" {
		t.Fatal("fixture: dev starts with neither")
	}
	if err := reg.Resubscribe([]config.ClusterConfig{{Name: "prd"}, {Name: "dev", Group: "web"}}); err != nil {
		t.Fatal(err)
	}
	if l := dev().LokiConfig(); l == nil || l.Source != "logs" || dev().MetricsConfig().Source != "mets" {
		t.Fatalf("dev joined web: %+v %+v", l, dev().MetricsConfig())
	}
	for _, kind := range []string{config.EndpointLoki, config.EndpointMetrics} {
		if _, ok := sources.SubscriptionOf(kind, "dev"); !ok {
			t.Fatalf("dev's %s subscription is not in force", kind)
		}
	}
	if err := reg.Resubscribe([]config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}); err != nil {
		t.Fatal(err)
	}
	if dev().LokiConfig() != nil || dev().MetricsConfig().Source != "" {
		t.Fatalf("dev left web: %+v %+v", dev().LokiConfig(), dev().MetricsConfig())
	}
}

// An Apply reports the clusters whose subscription moved, by kind.
func TestApplyReportsMovesPerKind(t *testing.T) {
	am := hostedSource()
	am.Serves = []config.ServeConfig{{Cluster: "dev"}}
	m := metricsInstallation()
	m.Serves = []config.ServeConfig{{Cluster: "dev"}}
	clusters := []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}
	a, err := NewAlertSources([]config.SourceConfig{am, m}, clusters)
	if err != nil {
		t.Fatal(err)
	}
	amSub, _ := a.Subscription("dev")
	m.Tenant = "ops-2"
	moved, err := a.Apply([]config.SourceConfig{am, m}, clusters)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 || len(moved[config.EndpointMetrics]) != 1 || moved[config.EndpointMetrics][0] != "dev" {
		t.Fatalf("moved: %v", moved)
	}
	if now, _ := a.Subscription("dev"); now.SubRev != amSub.SubRev {
		t.Fatal("the alerts' subscription moved with the metrics one")
	}
	if s, ok := a.SubscriptionOf(config.EndpointMetrics, "dev"); !ok || s.Kind != config.EndpointMetrics || s.Tenant != "ops-2" {
		t.Fatalf("metrics: %+v", s)
	}
}

// A hosted installation without a serviceRef is found by the host's
// discovery, cached for the host connection's life once found (§3.1).
func TestAHostedMetricsInstallationIsFoundByItsHostsDiscovery(t *testing.T) {
	src := metricsInstallation()
	src.ServiceRef, src.Discover = nil, true
	r := newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"})
	typed := typedfake.NewSimpleClientset(svc("monitoring", "prometheus-operated", 9090))
	var lookups atomic.Int32
	typed.PrependReactor("*", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		lookups.Add(1)
		return false, nil, nil
	})
	r.host.typed = typed
	dev := r.conns["dev"]
	_, capb := dev.ClusterMetrics(context.Background(), false)
	if !capb.Available || !strings.Contains(capb.Source, "Service monitoring/prometheus-operated:9090 in prd") {
		t.Fatalf("%+v", capb)
	}
	n := lookups.Load()
	if n == 0 {
		t.Fatal("the host's discovery did not run")
	}
	if _, capb := dev.ClusterMetrics(context.Background(), true); !capb.Available || lookups.Load() != n {
		t.Fatalf("found once, it is cached for the host connection: %d lookups, %+v", lookups.Load(), capb)
	}
}

// A block naming one installation while another's subscription is in force
// reads neither (§3.1): the subscription in force is the one read, and only
// when the block names it.
func TestABlockNamingAnotherInstallationReadsNothing(t *testing.T) {
	r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	dev := r.conns["dev"]
	dev.SetMetricsConfig(config.MetricsConfig{Source: "another"})
	if _, capb := dev.ClusterMetrics(context.Background(), true); capb.Available || !strings.Contains(capb.Reason, "subscription to another is not in force") {
		t.Fatalf("%+v", capb)
	}
	if q, _, _ := r.fake.sent(); len(q) != 0 {
		t.Fatalf("sent %q", q)
	}
}

// Loki pages of two members through one host never exceed its two slots.
func TestLokiPagesShareTheHostBound(t *testing.T) {
	src := lokiInstallation()
	src.Serves = []config.ServeConfig{{Cluster: "dev"}, {Cluster: "stg"}}
	r := newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"}, config.ClusterConfig{Name: "stg", Context: "stg"})
	hold := make(chan struct{})
	r.fake.mu.Lock()
	r.fake.hold = hold
	r.fake.mu.Unlock()
	var wg sync.WaitGroup
	for i := range 6 {
		lr, err := r.conns[[]string{"dev", "stg"}[i%2]].Loki()
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = lr.Labels(context.Background(), time.Unix(0, 0), time.Unix(60, 0), "")
		}()
	}
	time.Sleep(time.Second)
	close(hold)
	wg.Wait()
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	if r.fake.maxInFlight != 2 {
		t.Fatalf("pages in flight at most %d, want 2", r.fake.maxInFlight)
	}
}

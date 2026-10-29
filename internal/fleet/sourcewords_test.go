package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/secretstore"
	typedfake "k8s.io/client-go/kubernetes/fake"
)

// Settings S3b §3.2-§3.4: what a Loki or metrics read says about itself.

func TestEveryMetricsReadSaysItsSource(t *testing.T) {
	ctx := context.Background()
	sentence := func(c *ClusterConn) (string, string) {
		t.Helper()
		_, capb := c.ClusterMetrics(ctx, true)
		if !capb.Available {
			t.Fatalf("%+v", capb)
		}
		return capb.Sentence, capb.Lineage
	}
	// a hosted subscription
	r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	s, hosted := sentence(r.conns["dev"])
	if s != "from lgtm-we-metrics, hosted by prd, tenant ops, filtered by cluster=dev" || hosted == "" {
		t.Fatalf("hosted: %q %q", s, hosted)
	}
	// its key moves with the subscription's terms
	r.sources.Denied("lgtm-we-metrics")
	if _, moved := sentence(r.conns["dev"]); moved == hosted {
		t.Fatal("the lineage did not move with the access generation")
	}
	// a tenant-wide one
	src := metricsInstallation()
	src.Serves = []config.ServeConfig{{Cluster: "dev"}}
	own := config.ClusterConfig{Name: "dev", Context: "dev", Metrics: &config.MetricsConfig{Source: "lgtm-we-metrics", Scope: config.ScopeTenant}}
	r = newSrcRig(t, []config.SourceConfig{src}, own)
	if s, _ := sentence(r.conns["dev"]); s != "from lgtm-we-metrics, hosted by prd, tenant ops, everything in its tenant" {
		t.Fatalf("tenant-wide: %q", s)
	}
	// a direct installation
	f := &obsFake{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	direct := config.SourceConfig{ID: "mimir", Kind: config.SourceKindMetrics, Endpoint: srv.URL, MemberLabel: "cluster", Serves: []config.ServeConfig{{Cluster: "dev"}}}
	r = newSrcRig(t, []config.SourceConfig{direct}, config.ClusterConfig{Name: "dev", Context: "dev"})
	if s, _ := sentence(r.conns["dev"]); s != "from mimir at "+srv.URL+", filtered by cluster=dev" {
		t.Fatalf("direct: %q", s)
	}
	// a cluster's own blocks: an endpoint, filtered and not, and a Service
	c := connWithTF(clock.NewFake(time.Unix(0, 0)), &scriptedQ{cpu: "0.1", mem: "0.2"})
	if s, l := sentence(c); s != "from http://prom, unfiltered: everything there is read as this cluster's" || l == "" {
		t.Fatalf("legacy: %q %q", s, l)
	}
	c = connWithTF(clock.NewFake(time.Unix(0, 0)), &scriptedQ{cpu: "0.1", mem: "0.2"})
	c.metricsCfg.Match, c.metricsCfg.Tenant = map[string]string{"cluster": "dev"}, "ops"
	if s, _ := sentence(c); s != "from http://prom, tenant ops, filtered by cluster=dev" {
		t.Fatalf("legacy match: %q", s)
	}
	c = connWithTF(clock.NewFake(time.Unix(0, 0)), &scriptedQ{cpu: "0.1", mem: "0.2"})
	c.metricsCfg = config.MetricsConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "prom", Port: "9090"}}
	if s, _ := sentence(c); s != "from the Service monitoring/prom:9090 on this cluster, unfiltered: everything there is read as this cluster's" {
		t.Fatalf("service: %q", s)
	}
	c, _ = metricsDiscoveryCounted(config.MetricsConfig{})
	if s, _ := sentence(c); s != "from the discovered Service monitoring/prometheus-operated:9090 on this cluster, unfiltered: everything there is read as this cluster's" {
		t.Fatalf("discovered: %q", s)
	}
	// an unavailable read says no sentence
	c = connWithTF(clock.NewFake(time.Unix(0, 0)), &scriptedQ{fail: true})
	if _, capb := c.ClusterMetrics(ctx, true); capb.Available || capb.Sentence != "" || capb.Lineage != "" {
		t.Fatalf("unavailable: %+v", capb)
	}
}

// An empty answer under a scope says so (§3.3), told apart from a failed
// read, a non-finite value and an answer with no scope.
func TestAnEmptyReadUnderAScopeSaysSo(t *testing.T) {
	ctx := context.Background()
	const want = "no data for dev under cluster=dev"
	r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	dev := r.conns["dev"]
	dev.caps = capability.Set{Network: capability.NetworkCapability{HasHubble: true, HasEnvoyProxy: true}}
	expire := func() {
		dev.metricsMu.Lock()
		dev.metricsState.samplesExp = time.Time{}
		dev.metricsMu.Unlock()
	}
	r.fake.mu.Lock()
	r.fake.answer = "empty"
	r.fake.mu.Unlock()
	if cm, capb := dev.ClusterMetrics(ctx, false); !capb.Available || cm.NoData != want || cm.CPUFraction != nil {
		t.Fatalf("readout: %+v %+v", cm, capb)
	}
	if _, st := dev.PodContainerUsage(ctx, "a", "p"); !st.Available || st.NoData != want || st.Message != "" {
		t.Fatalf("containers: %+v", st)
	}
	if set, err := dev.ClusterSparklines(ctx); err != nil || set.NoData != want || set.Sentence == "" {
		t.Fatalf("sparklines: %+v %v", set, err)
	}
	if _, st := dev.RouteMetrics(ctx, []string{"default/web"}); !strings.HasPrefix(st.Message, want+": no envoy_cluster_* series found") || st.Sentence == "" {
		t.Fatalf("routes: %+v", st)
	}
	if _, st := dev.HubbleFlowStats(ctx); !strings.HasPrefix(st.Message, want+": no Hubble metric series found") || st.Sentence == "" {
		t.Fatalf("hubble: %+v", st)
	}
	// a non-finite answer is not "no data"
	r.fake.mu.Lock()
	r.fake.answer = "nan"
	r.fake.mu.Unlock()
	expire()
	if cm, _ := dev.ClusterMetrics(ctx, false); cm.NoData != "" || cm.CPUFraction != nil {
		t.Fatalf("non-finite readout: %+v", cm)
	}
	if _, st := dev.PodContainerUsage(ctx, "a", "p"); st.NoData != "" {
		t.Fatalf("non-finite containers: %+v", st)
	}
	if set, err := dev.ClusterSparklines(ctx); err != nil || set.NoData != "" {
		t.Fatalf("gapped sparklines: %+v %v", set, err)
	}
	if _, st := dev.RouteMetrics(ctx, []string{"default/web"}); st.NoSeries || strings.Contains(st.Message, "no data for") {
		t.Fatalf("non-finite routes: %+v", st)
	}
	if _, st := dev.HubbleFlowStats(ctx); st.NoSeries || strings.Contains(st.Message, "no data for") {
		t.Fatalf("non-finite hubble: %+v", st)
	}
	// nor is a failed read
	r.fake.mu.Lock()
	r.fake.answer, r.fake.onData = "empty", func() {}
	r.fake.mu.Unlock()
	expire()
	r.fake.mu.Lock()
	r.fake.code = http.StatusInternalServerError
	r.fake.mu.Unlock()
	if cm, _ := dev.ClusterMetrics(ctx, false); cm.NoData != "" {
		t.Fatalf("failed readout: %+v", cm)
	}
	// and an empty answer with no scope is said as before
	c := connWithTF(clock.NewFake(time.Unix(0, 0)), &recordingQ{})
	if cm, capb := c.ClusterMetrics(ctx, false); !capb.Available || cm.NoData != "" {
		t.Fatalf("unscoped: %+v %+v", cm, capb)
	}
}

func TestEveryLokiReaderSaysItsSource(t *testing.T) {
	r := newSrcRig(t, []config.SourceConfig{lokiInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	lr, err := r.conns["dev"].Loki()
	if err != nil {
		t.Fatal(err)
	}
	if lr.Sentence() != "from lgtm-we-logs, hosted by prd, tenant ops, filtered by k8s_cluster_name=dev" || lr.Lineage().Kind != config.EndpointLoki || lr.Lineage().Installation != "lgtm-we-logs" {
		t.Fatalf("subscribed: %q %+v", lr.Sentence(), lr.Lineage())
	}
	f := &obsFake{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	direct := config.SourceConfig{ID: "logs", Kind: config.SourceKindLoki, Endpoint: srv.URL, MemberLabel: "k8s_cluster_name", Serves: []config.ServeConfig{{Cluster: "dev"}}}
	r = newSrcRig(t, []config.SourceConfig{direct}, config.ClusterConfig{Name: "dev", Context: "dev"})
	if lr, err := r.conns["dev"].Loki(); err != nil || lr.Sentence() != "from logs at "+srv.URL+", filtered by k8s_cluster_name=dev" {
		t.Fatalf("direct: %v %q", err, lr.Sentence())
	}
	// a cluster's own block
	c := NewClusterConn("dev", nil, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	c.WithLoki(&config.EndpointConfig{Endpoint: srv.URL, Tenant: "ops"})
	c.creds = Credentials{Revisions: endpointcred.NewRegistry()}
	lr, err = c.Loki()
	if err != nil || lr.Sentence() != "from "+srv.URL+", tenant ops, unfiltered: every stream there is read as this cluster's" || lr.Lineage().Mode != config.AMDirect {
		t.Fatalf("legacy: %v %q %+v", err, lr.Sentence(), lr.Lineage())
	}
	c.WithLoki(&config.EndpointConfig{Endpoint: srv.URL, Match: map[string]string{"k8s_cluster_name": "dev"}})
	if lr, err := c.Loki(); err != nil || lr.Sentence() != "from "+srv.URL+", filtered by k8s_cluster_name=dev" {
		t.Fatalf("legacy match: %v %q", err, lr.Sentence())
	}
}

// What a read says about itself never shows the credential it reads with:
// a tenant or match value equal to it is shown redacted, in the sentence
// and the empty answer's words alike, while the request still carries it
// raw (Codex's S3b-2c review, R1). Invented fixture secrets only.
func TestAMetricsReadsWordsNeverShowItsCredential(t *testing.T) {
	ctx := context.Background()
	words := func(t *testing.T, c *ClusterConn, f *obsFake, key string) {
		t.Helper()
		cm, capb := c.ClusterMetrics(ctx, false)
		if !capb.Available || cm.NoData == "" {
			t.Fatalf("not an available empty read: %+v %+v", cm, capb)
		}
		// a key past a part's clip is redacted before it is clipped, so no
		// prefix of it is shown either
		shown := key[:min(len(key), 40)]
		if strings.Contains(capb.Sentence+cm.NoData, shown) || !strings.Contains(capb.Sentence, "[redacted]") || !strings.Contains(cm.NoData, "[redacted]") {
			t.Fatalf("sentence %q, no data %q", capb.Sentence, cm.NoData)
		}
		// a range read says its empty answer in the same words
		if set, err := c.ClusterSparklines(ctx); err != nil || !strings.Contains(set.NoData, "[redacted]") || strings.Contains(set.NoData+set.Sentence, shown) {
			t.Fatalf("range: %+v %v", set, err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if !strings.Contains(strings.Join(f.queries, " "), `cluster="`+key+`"`) || !strings.Contains(strings.Join(f.tenant, " "), key) {
			t.Fatalf("the request did not carry the raw scope: %q %q", f.queries, f.tenant)
		}
	}
	t.Run("a cluster's own block", func(t *testing.T) {
		const key = "S3BCREDENTIAL-opaque-fixture-7941"
		f := &obsFake{answer: "empty"}
		api := httptest.NewServer(f)
		t.Cleanup(api.Close)
		dev := NewClusterConn("dev", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{Endpoint: api.URL, Token: key, Tenant: key, Match: map[string]string{"cluster": key}})
		dev.WithCredentials(Credentials{Store: &secretstore.Memory{}, Revisions: endpointcred.NewRegistry()})
		words(t, dev, f, key)
	})
	installation := func(t *testing.T, key string) {
		f := &obsFake{answer: "empty"}
		api := httptest.NewServer(f)
		t.Cleanup(api.Close)
		src := config.SourceConfig{ID: "mimir", Kind: config.SourceKindMetrics, Endpoint: api.URL, Auth: &config.EndpointAuth{Type: "bearer"}, Tenant: key, MemberLabel: "cluster", Serves: []config.ServeConfig{{Cluster: "dev"}}}
		member := config.ClusterConfig{Name: "dev", Context: "dev", Metrics: &config.MetricsConfig{Source: "mimir", Match: map[string]string{"cluster": key}}}
		r := newSrcRig(t, []config.SourceConfig{src}, member)
		v, _ := endpointcred.Record{V: 1, Origin: api.URL, Type: "bearer", Secret: key}.Encode()
		if err := r.store.Set(endpointcred.SourceAccount("mimir"), v); err != nil {
			t.Fatal(err)
		}
		words(t, r.conns["dev"], f, key)
	}
	t.Run("a direct installation", func(t *testing.T) { installation(t, "S3BCREDENTIAL-opaque-installation-fixture-6852") })
	t.Run("a credential longer than a part's clip", func(t *testing.T) {
		installation(t, "S3BCREDENTIAL-long-fixture-"+strings.Repeat("k", alertmanager.MaxLabelValue))
	})
}

// A subscription's tenant is clipped in its sentence, as every part is.
func TestASubscribedSentenceClipsItsTenant(t *testing.T) {
	src := metricsInstallation()
	src.Tenant = strings.Repeat("t", 600)
	r := newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"})
	_, capb := r.conns["dev"].ClusterMetrics(context.Background(), true)
	if !capb.Available || strings.Contains(capb.Sentence, src.Tenant) || !strings.Contains(capb.Sentence, "tenant ttt") {
		t.Fatalf("%+v", capb)
	}
}

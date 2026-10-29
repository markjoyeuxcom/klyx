package fleet

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/sources"
)

// Settings S3b §4: a log or metrics read's lineage holds while its terms
// do, and says which moved once one does, in its kind's words.

// srcRegistry puts a source rig's connections behind a registry.
func srcRegistry(r *srcRig) *Registry {
	reg := &Registry{retired: map[ConnID]bool{}, alerts: r.b}
	reg.entries = append(reg.entries, &entry{name: "prd", conn: r.host, id: r.host.ID()})
	for name, c := range r.conns {
		reg.entries = append(reg.entries, &entry{name: name, conn: c, id: c.ID()})
	}
	return reg
}

func metricsLineageOf(t *testing.T, c *ClusterConn) sources.Lineage {
	t.Helper()
	_, l, err := c.ChatMetricsRange(context.Background(), metrics.ClusterCPU.With(metrics.None{}), time.Unix(0, 0), time.Unix(60, 0), time.Minute)
	if err != nil || l.Kind != config.EndpointMetrics {
		t.Fatalf("read: %v %+v", err, l)
	}
	return l
}

func TestASubscribedLogOrMetricsLineageHoldsWhileItsTermsDo(t *testing.T) {
	r := newSrcRig(t, []config.SourceConfig{metricsInstallation(), lokiInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	reg := srcRegistry(r)
	m := metricsLineageOf(t, r.conns["dev"])
	lr, err := r.conns["dev"].Loki()
	if err != nil {
		t.Fatal(err)
	}
	logs := lr.Lineage()
	wantProblem(t, reg, "dev", m, "")
	wantProblem(t, reg, "dev", logs, "")
	// a denial moves the metrics installation's access, not the logs'
	r.sources.Denied("lgtm-we-metrics")
	wantProblem(t, reg, "dev", m, "lgtm-we-metrics's access changed since its metrics were read")
	wantProblem(t, reg, "dev", logs, "")
	// an edit of the Loki installation ends the logs' lineage
	l := lokiInstallation()
	l.Tenant = "ops-2"
	r.apply([]config.SourceConfig{metricsInstallation(), l}, r.clusters)
	wantProblem(t, reg, "dev", logs, "lgtm-we-logs changed since its logs were read")
	// a lineage is checked for its own cluster's subscription
	wantProblem(t, reg, "stg", m, "stg's subscription to lgtm-we-metrics changed since its metrics were read")
}

func TestAHostReconnectEndsALogOrMetricsLineage(t *testing.T) {
	r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
	reg := srcRegistry(r)
	m := metricsLineageOf(t, r.conns["dev"])
	r.reconnect()
	wantProblem(t, reg, "dev", m, "lgtm-we-metrics's host prd was reconnected or removed since its metrics were read")
}

// A direct installation's credential, of its kind: replaced, it ends the
// metrics lineage.
func TestADirectMetricsLineageFollowsItsCredential(t *testing.T) {
	f := &obsFake{}
	srv := newObsServer(t, f)
	src := config.SourceConfig{ID: "mimir", Kind: config.SourceKindMetrics, Endpoint: srv, MemberLabel: "cluster", Serves: []config.ServeConfig{{Cluster: "dev"}}}
	r := newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"})
	reg := srcRegistry(r)
	m := metricsLineageOf(t, r.conns["dev"])
	wantProblem(t, reg, "dev", m, "")
	r.revs.Bump(endpointcred.SourceAccount("mimir"), config.EndpointMetrics, true)
	wantProblem(t, reg, "dev", m, "mimir's credential changed since its metrics were read")
}

// A cluster's own metrics block: its source identity and its revision. A
// change of the block, or a discovery that now finds another Service, ends
// the lineage; the source being unreachable for a while does not.
func TestALegacyMetricsLineageFollowsItsSource(t *testing.T) {
	c := connWithTF(clock.NewFake(time.Unix(0, 0)), &recordingQ{})
	c.name = "dev"
	c.creds = Credentials{Revisions: endpointcred.NewRegistry()}
	reg := &Registry{retired: map[ConnID]bool{}}
	reg.entries = append(reg.entries, &entry{name: "dev", conn: c, id: c.ID()})
	m := metricsLineageOf(t, c)
	if m.Mode != config.AMDirect || m.Identity == "" {
		t.Fatalf("legacy lineage: %+v", m)
	}
	wantProblem(t, reg, "dev", m, "")
	// an outage keeps the source's identity: what was read stays sendable
	c.metricsTF = fakeTF{q: &scriptedQ{fail: true}}
	if _, capb := c.ClusterMetrics(context.Background(), true); capb.Available {
		t.Fatal("fixture: the source should be down")
	}
	wantProblem(t, reg, "dev", m, "")
	// the block changes
	c.SetMetricsConfig(config.MetricsConfig{Endpoint: "http://prom-2"})
	wantProblem(t, reg, "dev", m, "dev's metrics source or its credential changed since its metrics were read")

	// discovery that now finds another Service
	d, _ := metricsDiscoveryCounted(config.MetricsConfig{})
	d.metricsTF = fakeTF{q: &recordingQ{}}
	d.name = "dev"
	d.creds = Credentials{Revisions: endpointcred.NewRegistry()}
	reg = &Registry{retired: map[ConnID]bool{}}
	reg.entries = append(reg.entries, &entry{name: "dev", conn: d, id: d.ID()})
	m = metricsLineageOf(t, d)
	wantProblem(t, reg, "dev", m, "")
	if err := d.typed.CoreV1().Services("monitoring").Delete(context.Background(), "prometheus-operated", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.typed.CoreV1().Services("monitoring").Create(context.Background(), svc("monitoring", "prometheus-server", 80), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	_, _ = d.ClusterMetrics(context.Background(), true)
	wantProblem(t, reg, "dev", m, "dev's metrics source changed since its metrics were read")
}

// A cluster's own Loki block: the reader it resolves to now.
func TestALegacyLokiLineageFollowsItsSource(t *testing.T) {
	f := &obsFake{}
	srv := newObsServer(t, f)
	c := NewClusterConn("dev", nil, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	c.creds = Credentials{Revisions: endpointcred.NewRegistry()}
	c.WithLoki(&config.EndpointConfig{Endpoint: srv, Tenant: "ops"})
	reg := &Registry{retired: map[ConnID]bool{}}
	reg.entries = append(reg.entries, &entry{name: "dev", conn: c, id: c.ID()})
	lr, err := c.Loki()
	if err != nil {
		t.Fatal(err)
	}
	l := lr.Lineage()
	if l.Kind != config.EndpointLoki || l.Mode != config.AMDirect {
		t.Fatalf("legacy lineage: %+v", l)
	}
	wantProblem(t, reg, "dev", l, "")
	c.SetLokiConfig(&config.EndpointConfig{Endpoint: srv, Tenant: "ops-2"})
	wantProblem(t, reg, "dev", l, "dev's log history source or its credential changed since its logs were read")
	c.SetLokiConfig(nil)
	wantProblem(t, reg, "dev", l, "dev's log history source changed since its logs were read")
	// the source's identity is compared as well as the revision: without a
	// revision registry, a changed block still ends the lineage
	bare := NewClusterConn("dev", nil, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	bare.WithLoki(&config.EndpointConfig{Endpoint: srv, Tenant: "ops"})
	reg = &Registry{retired: map[ConnID]bool{}}
	reg.entries = append(reg.entries, &entry{name: "dev", conn: bare, id: bare.ID()})
	lr, err = bare.Loki()
	if err != nil {
		t.Fatal(err)
	}
	l = lr.Lineage()
	wantProblem(t, reg, "dev", l, "")
	bare.SetLokiConfig(&config.EndpointConfig{Endpoint: srv, Tenant: "ops-2"})
	wantProblem(t, reg, "dev", l, "dev's log history source or its credential changed since its logs were read")
}

func newObsServer(t *testing.T, f *obsFake) string {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv.URL
}

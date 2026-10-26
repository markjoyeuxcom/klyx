package appbridge

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/policy"
)

// Settings S2a §2.6-§2.7: the running fleet is one list with a revision,
// endpoint activations publish into it, and every reader of protection
// reads the published value.

func TestTheRunningFleetIsOneListWithARevision(t *testing.T) {
	cs := NewConfigService("", &config.Config{Clusters: []config.ClusterConfig{{Name: "a"}, {Name: "b"}}}, func(config.ClusterConfig) error { return nil })
	cs.appendClusters = func(string, []string) error { return nil }
	rev := RunningRevision(cs)
	if r := cs.AddClusters([]string{"c"}); !r.OK {
		t.Fatal(r.Error)
	}
	names := func() []string {
		var out []string
		for _, c := range LiveFleetClusters(cs)() {
			out = append(out, c.Name)
		}
		return out
	}
	if got := names(); len(got) != 3 || got[2] != "c" || RunningRevision(cs) == rev {
		t.Fatalf("after an addition: %v", got)
	}
	rev = RunningRevision(cs)
	ForgetRunningCluster(cs, "a")
	if got := names(); len(got) != 2 || got[0] != "b" || RunningRevision(cs) == rev {
		t.Fatalf("after a removal: %v", got)
	}
	if _, ok := LiveClusterConfig(cs)("a"); ok {
		t.Fatal("a removed cluster still resolves")
	}
}

// Codex's re-review probe, against the fix: an endpoint activated through
// Test after startup is in the running entry, so a reconnect prepared from
// it keeps the endpoint the connection uses.
func TestAnEndpointActivationIsInTheRunningEntry(t *testing.T) {
	e := newEndpointsEnv(t)
	startup, err := config.Load(e.path)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewConfigService(e.path, startup, nil)
	WireEndpointActivation(e.svc, cs)
	e.conn.SetMetricsConfig(*startup.Clusters[0].Metrics)
	e.conn.capb = metrics.MetricsCapability{Available: true, Source: "stand-in"}
	const next = "https://new-mimir.example/prometheus"
	e.edit(t, "https://mimir.example/prometheus", next)
	var row EndpointRowDTO
	for _, r := range e.svc.GetEndpoints().Rows {
		if r.Cluster == "prd" && r.Kind == config.EndpointMetrics {
			row = r
			break
		}
	}
	rev := RunningRevision(cs)
	if r := e.svc.TestEndpoint(row.Cluster, row.Kind, row.Revision); !r.OK {
		t.Fatalf("test: %+v", r)
	}
	cc, ok := LiveClusterConfig(cs)("prd")
	if !ok || cc.Metrics == nil || cc.Metrics.Endpoint != next || e.conn.MetricsConfig().Endpoint != next || RunningRevision(cs) == rev {
		t.Fatalf("the running entry says %+v, the connection %s", cc.Metrics, e.conn.MetricsConfig().Endpoint)
	}
	// the activation ran under the fleet Apply lock: one held elsewhere
	// holds it back
	unlock := FleetApplyLock(cs)
	done := make(chan struct{})
	go func() {
		e.svc.TestEndpoint(row.Cluster, row.Kind, row.Revision)
		close(done)
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("an endpoint activation ran beside an Apply holding the fleet lock")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	<-done
}

// One protection reader: after a published change, the configuration
// service, the fleet DTOs and the notification engine's scope all say the
// published value, not the entry's own inputs.
func TestEveryProtectionReaderReadsThePublishedValue(t *testing.T) {
	cfg := &config.Config{Clusters: []config.ClusterConfig{{Name: "prd-a", Context: "prd-a"}}}
	pol := policy.New()
	cs := WithPolicy(NewConfigService("", cfg, nil), pol)
	cs.kubeconfigPath = func() string { return "/nonexistent/kubeconfig" } // never the owner's
	cs.kubeContexts = func(string) ([]string, error) { return nil, nil }
	fs := NewFleetService(&fakeSnapshotter{snaps: []fleet.Snapshot{{Name: "prd-a", State: fleet.Synced}}}, LiveClusterConfig(cs), time.Now)
	SetFleetProtection(fs, cs.IsProtected)
	ns := NewNotificationsService("", cfg, LiveClusterConfig(cs), nil, nil, nil)
	SetNotifyProtection(ns, cs.IsProtected)
	if !cs.IsProtected("prd-a") || !fs.GetFleet()[0].Protected {
		t.Fatal("rig: inferred from the name")
	}
	if _, err := pol.Begin("prd-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := pol.Publish("prd-a", false); err != nil {
		t.Fatal(err)
	}
	dto := cs.GetFleetConfig()
	if cs.IsProtected("prd-a") || fs.GetFleet()[0].Protected || len(dto.Clusters) != 1 || dto.Clusters[0].Protected {
		t.Fatal("a reader still derives protection from the entry")
	}
	if p := ns.protected.Load(); p == nil || (*p)("prd-a") {
		t.Fatal("the notification scope still derives protection from the entry")
	}
}

// The notification engine's protected scope reads the published value end
// to end: prd-a, protected by its name, published unprotected, is out of
// scope; dev-b, unprotected by its name, published protected, is in it.
func TestTheNotificationScopeReadsThePublishedProtection(t *testing.T) {
	start := time.Now()
	var offset time.Duration
	defer func(c func() time.Time) { notifyClock = c }(notifyClock)
	notifyClock = func() time.Time { return start.Add(offset) }
	cfg := &config.Config{Clusters: []config.ClusterConfig{{Name: "prd-a", Context: "prd-a"}, {Name: "dev-b", Context: "dev-b"}},
		Notifications: &config.NotificationsConfig{Enabled: true, Scope: "protected"}}
	pol := policy.New()
	cs := WithPolicy(NewConfigService("", cfg, nil), pol)
	cs.kubeconfigPath = func() string { return "/nonexistent/kubeconfig" } // never the owner's
	cs.kubeContexts = func(string) ([]string, error) { return nil, nil }
	var sent []string
	ns := NewNotificationsService(filepath.Join(t.TempDir(), "fleet.yaml"), cfg, LiveClusterConfig(cs),
		func(_, _, _ string, data map[string]any) error {
			sent = append(sent, fmt.Sprint(data["cluster"]))
			return nil
		}, nil, nil)
	SetNotifyProtection(ns, cs.IsProtected)
	for name, protected := range map[string]bool{"prd-a": false, "dev-b": true} {
		if _, err := pol.Begin(name); err != nil {
			t.Fatal(err)
		}
		if _, err := pol.Publish(name, protected); err != nil {
			t.Fatal(err)
		}
	}
	l := NotificationsListener(ns)
	for _, name := range []string{"prd-a", "dev-b"} {
		b := batchOf(1, 1, observe.BatchBaseline)
		b.Cluster = name
		l.OnTransitions(b)
		b = batchOf(1, 2, observe.BatchChanges, observe.Transition{Key: "u-" + name, NS: "shop", Name: "web", From: "ok", To: "fault", Label: "CrashLoopBackOff"})
		b.Cluster = name
		l.OnTransitions(b)
	}
	offset = 2 * time.Minute
	ns.engine.Flush()
	if len(sent) != 1 || sent[0] != "dev-b" {
		t.Fatalf("sent for %v, want dev-b alone", sent)
	}
}

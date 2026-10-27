package fleet

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/safehttp"
)

// Settings S3a §4.3: a lineage holds while the terms its read was made
// under do, and says which moved once one does.

// lineageRegistry puts a bucket rig's connections behind a registry: the
// check reads the running connections and the installations in force.
func lineageRegistry(r *bucketRig) *Registry {
	reg := &Registry{retired: map[ConnID]bool{}, alerts: r.b}
	reg.entries = append(reg.entries, &entry{name: "prd", conn: r.host, id: r.host.ID()})
	for name, c := range r.conns {
		reg.entries = append(reg.entries, &entry{name: name, conn: c, id: c.ID()})
	}
	return reg
}

func wantProblem(t *testing.T, reg *Registry, cluster string, l alertmanager.Lineage, want string) {
	t.Helper()
	got := reg.LineageProblem(cluster, l)
	if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
		t.Fatalf("lineage %+v: %q, want %q", l, got, want)
	}
}

func TestAHostedLineageHoldsWhileItsTermsDo(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev", "stg")
	reg := lineageRegistry(r)
	dev, _ := r.mustRead("dev")
	wantProblem(t, reg, "dev", dev.Lineage, "")
	// another member's read, or a new read of the same installation, moves
	// nothing of dev's
	_, _ = r.mustRead("stg")
	wantProblem(t, reg, "dev", dev.Lineage, "")
	// a lineage is checked for its own cluster's subscription
	wantProblem(t, reg, "stg", dev.Lineage, "stg's subscription to lgtm-we changed")
}

// The host reconnected: a replacement is a new connection, whatever it
// publishes later.
func TestAHostReconnectEndsTheLineage(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	reg := lineageRegistry(r)
	dev, _ := r.mustRead("dev")
	next := NewClusterConn("prd", nil, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	next.restConfig = r.host.restConfig
	usable(next, 1)
	reg.mu.Lock()
	reg.entries[0].conn, reg.entries[0].id = next, next.ID()
	reg.mu.Unlock()
	r.host = next // the rig's host lookup: no read is running
	wantProblem(t, reg, "dev", dev.Lineage, "lgtm-we's host prd was reconnected or removed since its alerts were read")
}

// The host failed and has not recovered: its generation is unchanged, but
// it is not usable now, so the lineage does not hold.
func TestAHostThatHasFailedEndsTheLineage(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	reg := lineageRegistry(r)
	dev, _ := r.mustRead("dev")
	r.host.mu.Lock()
	r.host.state = Failed
	r.host.mu.Unlock()
	wantProblem(t, reg, "dev", dev.Lineage, "lgtm-we's host prd is not connected")
	// recovered in place: a new usability incarnation is a new generation
	usable(r.host, 2)
	wantProblem(t, reg, "dev", dev.Lineage, "lgtm-we's access changed since its alerts were read")
}

func TestARemovedHostEndsTheLineage(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	reg := lineageRegistry(r)
	dev, _ := r.mustRead("dev")
	r.b.mu.Lock()
	r.b.host = func(string) (*ClusterConn, bool) { return nil, false }
	r.b.mu.Unlock()
	wantProblem(t, reg, "dev", dev.Lineage, "lgtm-we's host prd was reconnected or removed")
}

func TestADenialEndsTheLineage(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	reg := lineageRegistry(r)
	dev, _ := r.mustRead("dev")
	r.sources.Denied("lgtm-we")
	wantProblem(t, reg, "dev", dev.Lineage, "lgtm-we's access changed since its alerts were read")
}

// The installation or the member's subscription changed, or the
// installation was removed (S3a §4.3: both revisions, on every path).
func TestAnInstallationOrSubscriptionChangeEndsTheLineage(t *testing.T) {
	clusters := []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}
	for _, tc := range []struct {
		name     string
		sources  func() []config.SourceConfig
		clusters []config.ClusterConfig
		want     string
	}{
		{"installation", func() []config.SourceConfig {
			s := hostedSource()
			s.Tenant = "ops"
			return []config.SourceConfig{s}
		}, clusters, "lgtm-we changed since its alerts were read"},
		{"subscription", func() []config.SourceConfig { return []config.SourceConfig{hostedSource()} },
			[]config.ClusterConfig{{Name: "prd"}, {Name: "dev", Alertmanager: &config.EndpointConfig{Source: "lgtm-we", Match: map[string]string{"team": "core"}}}},
			"dev's subscription to lgtm-we changed since its alerts were read"},
		{"removed", func() []config.SourceConfig { return nil }, clusters, "lgtm-we changed since its alerts were read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newBucketRig(t, hostedSource(), "dev")
			reg := lineageRegistry(r)
			dev, _ := r.mustRead("dev")
			if _, err := r.sources.Apply(tc.sources(), tc.clusters); err != nil {
				t.Fatal(err)
			}
			wantProblem(t, reg, "dev", dev.Lineage, tc.want)
		})
	}
}

// A direct installation: its access generation and its own credential's
// revision; a credential stored or removed ends the lineage, and the
// outcome keeps its revision.
func TestADirectLineageFollowsItsCredential(t *testing.T) {
	f := newAMFake()
	f.alerts = bucketAlerts
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	src := config.SourceConfig{ID: "shared", Kind: config.SourceKindAlertmanager, Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"},
		MemberLabel: "cluster", Serves: []config.ServeConfig{{Cluster: "dev"}}}
	r := newBucketRig(t, src, "dev")
	reg := lineageRegistry(r)
	origin, _ := safehttp.OriginOf(srv.URL)
	v, _ := endpointcred.Record{V: 1, Origin: origin, Type: "bearer", Secret: "S3A-SECRET"}.Encode()
	if err := r.store.Set(endpointcred.SourceAccount("shared"), v); err != nil {
		t.Fatal(err)
	}
	dev, _ := r.mustRead("dev")
	if dev.Lineage.Host != "" || dev.Lineage.HostConn != 0 {
		t.Fatalf("a direct lineage names no host: %+v", dev.Lineage)
	}
	wantProblem(t, reg, "dev", dev.Lineage, "")
	r.revs.Bump("source/shared", config.EndpointAlertmanager, true)
	wantProblem(t, reg, "dev", dev.Lineage, "shared's credential changed since its alerts were read")
	if _, rev, _ := r.sources.Installation("shared"); rev != dev.Lineage.InstRev {
		t.Fatalf("a credential change moved the installation's revision: %d, was %d", rev, dev.Lineage.InstRev)
	}
	// the outcome keeps its revision, and is not served again: the member's
	// next read is under the new credential, under the same key
	before := f.calls()
	again, _ := r.mustRead("dev")
	if f.calls() == before || again.Identity != dev.Identity || again.Lineage.CredRev == dev.Lineage.CredRev {
		t.Fatalf("an outcome read under the old credential was served: %+v", again.Lineage)
	}
	wantProblem(t, reg, "dev", again.Lineage, "")
	r.revs.Revoke("source/shared", config.EndpointAlertmanager)
	wantProblem(t, reg, "dev", again.Lineage, "shared's credential changed")
	r.sources.Denied("shared")
	wantProblem(t, reg, "dev", again.Lineage, "shared's access changed")
}

// A legacy block (S3a §4.4): its source identity and its credential
// revision. An endpoint, match or tenant change, or a credential action,
// ends the lineage; that closes today's gap for Alertmanager.
func TestALegacyLineageFollowsItsBlockAndCredential(t *testing.T) {
	f := newAMFake()
	f.alerts = amAlerts
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := &config.EndpointConfig{Endpoint: srv.URL + "/am", Auth: &config.EndpointAuth{Type: "bearer"}, Tenant: "team-a", Match: map[string]string{"cluster": "prd"}}
	for _, tc := range []struct {
		name   string
		change func(e *amEnv)
		want   string
	}{
		{"endpoint", func(e *amEnv) {
			c := *cfg
			c.Endpoint = srv.URL + "/other"
			e.c.SetAlertmanagerConfig(&c)
		}, "prd's alert source changed since its alerts were read"},
		{"match", func(e *amEnv) {
			c := *cfg
			c.Match = map[string]string{"cluster": "stg"}
			e.c.SetAlertmanagerConfig(&c)
		}, "prd's alert source changed"},
		{"tenant", func(e *amEnv) {
			c := *cfg
			c.Tenant = "team-b"
			e.c.SetAlertmanagerConfig(&c)
		}, "prd's alert source changed"},
		{"credential", func(e *amEnv) { e.reg.Bump("prd", config.EndpointAlertmanager, true) },
			"prd's Alertmanager credential changed since its alerts were read"},
		{"credential removed", func(e *amEnv) { e.reg.Revoke("prd", config.EndpointAlertmanager) },
			"prd's Alertmanager credential changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAMEnv(t, cfg, nil, nil)
			origin, _ := safehttp.OriginOf(srv.URL)
			storeAM(t, e.store, origin, "C5-AM-TOKEN")
			got := e.read(t)
			reg := &Registry{retired: map[ConnID]bool{}, entries: []*entry{{name: "prd", conn: e.c, id: e.c.ID()}}}
			if got.Lineage.Mode != config.AMDirect {
				t.Fatalf("fixture: %+v", got.Lineage)
			}
			wantProblem(t, reg, "prd", got.Lineage, "")
			tc.change(e)
			wantProblem(t, reg, "prd", got.Lineage, tc.want)
		})
	}
}

// A legacy lineage whose cluster is no longer running, and the lineages
// that hold nothing: off, and a read without one.
func TestLineagesWithoutTerms(t *testing.T) {
	reg := &Registry{retired: map[ConnID]bool{}}
	wantProblem(t, reg, "prd", alertmanager.Lineage{Mode: config.AMService, Identity: "x"}, "prd is not connected")
	wantProblem(t, reg, "prd", alertmanager.Lineage{Mode: config.AMOff}, "")
	wantProblem(t, reg, "prd", alertmanager.Lineage{}, "")
	// a subscribed lineage without the installation readers
	wantProblem(t, reg, "prd", alertmanager.Lineage{Mode: config.AMSubscribed, Installation: "lgtm-we"}, "lgtm-we is not in force")
}

// A hosted lineage carries no credential revision: the host's own
// credentials are its connection's, and its reconnect moves the lineage.
func TestAHostedLineageIgnoresEndpointCredentials(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	reg := lineageRegistry(r)
	dev, _ := r.mustRead("dev")
	r.revs.Bump("source/lgtm-we", config.EndpointAlertmanager, true)
	r.revs.Bump("dev", config.EndpointAlertmanager, true)
	wantProblem(t, reg, "dev", dev.Lineage, "")
}

// Settings S3a §5: an installation's Test reads its status once through
// the transport its reads take - here its host's Service proxy - and moves
// no bucket's outcome; a host known to be down is said, with no request.
func TestAnInstallationsStatusIsReadOnce(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev")
	r.am.status = `{"versionInfo":{"version":"0.28.1"},"cluster":{"status":"ready","peers":[{},{},{}]}}`
	reg := lineageRegistry(r)
	st, where, err := reg.InstallationStatus(context.Background(), "lgtm-we")
	if err != nil || st.Version != "0.28.1" || where != "Service monitoring/am:web" {
		t.Fatalf("%+v %q %v", st, where, err)
	}
	if r.reads() != 0 {
		t.Fatal("the test read alerts")
	}
	if _, _, err := reg.InstallationStatus(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "is not in force") {
		t.Fatalf("an unknown installation: %v", err)
	}
	usable(r.host, 1)
	r.host.mu.Lock()
	r.host.state = Failed
	r.host.mu.Unlock()
	r.am.mu.Lock()
	before := len(r.am.queries["/api/v2/status"])
	r.am.mu.Unlock()
	if _, _, err := reg.InstallationStatus(context.Background(), "lgtm-we"); err == nil || !strings.Contains(err.Error(), "host prd is not connected") {
		t.Fatalf("a down host: %v", err)
	}
	r.am.mu.Lock()
	defer r.am.mu.Unlock()
	if len(r.am.queries["/api/v2/status"]) != before {
		t.Fatal("a down host was asked")
	}
}

// PublishSources puts new installations and the running fleet's
// subscriptions in force, and each connection whose block changes adopts
// it.
func TestPublishSourcesMovesWhatChanged(t *testing.T) {
	r := newBucketRig(t, hostedSource(), "dev", "stg")
	reg := lineageRegistry(r)
	dev, _ := r.mustRead("dev")
	moved := hostedSource()
	moved.Tenant = "ops"
	running := []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}, {Name: "stg", Alertmanager: &config.EndpointConfig{Off: true}}}
	if err := reg.PublishSources([]config.SourceConfig{moved}, running); err != nil {
		t.Fatal(err)
	}
	wantProblem(t, reg, "dev", dev.Lineage, "lgtm-we changed since its alerts were read")
	if _, ok := r.sources.Subscription("stg"); ok {
		t.Fatal("stg is off, still subscribed")
	}
	if b := r.conns["stg"].AlertmanagerConfig(); b == nil || !b.Off {
		t.Fatalf("stg did not adopt its block: %+v", b)
	}
	if sub, ok := r.sources.Subscription("dev"); !ok || sub.Tenant != "ops" {
		t.Fatalf("dev's subscription: %+v", sub)
	}
}

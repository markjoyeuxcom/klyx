package fleet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	typedfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/observe"
)

// Settings S3a §2.2 on a connection: off reads nothing and is absent; a
// subscription is never read by discovery in the cluster.

// readOnce reads the alerts once under the connection's own identity,
// following one re-key.
func readOnce(t *testing.T, e *amEnv) error {
	t.Helper()
	r, err := e.c.ReadAlerts(context.Background(), e.c.AlertsIdentity())
	if errors.Is(err, ErrSourceChanged) {
		_, err = e.c.ReadAlerts(context.Background(), r.Identity)
	}
	return err
}

// discoveryCounted is a fake whose discovery lists are counted.
func discoveryCounted(t *testing.T, cfg *config.EndpointConfig) (*amEnv, *atomic.Int32) {
	typed := typedfake.NewSimpleClientset()
	servedAM(typed)
	dyn := amDyn()
	var lists atomic.Int32
	dyn.PrependReactor("list", "alertmanagers", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists.Add(1)
		return false, nil, nil
	})
	return newAMEnv(t, cfg, typed, dyn), &lists
}

// The control: an empty block is discovered, and the counter sees it.
func TestDiscoveryIsCounted(t *testing.T) {
	e, lists := discoveryCounted(t, &config.EndpointConfig{})
	_ = readOnce(t, e)
	if lists.Load() == 0 {
		t.Fatal("discovery was not counted")
	}
}

func TestAlertsOffReadsAbsentWithoutDiscovery(t *testing.T) {
	e, lists := discoveryCounted(t, &config.EndpointConfig{Off: true})
	err := readOnce(t, e)
	if !errors.Is(err, ErrAlertsOff) || !errors.Is(err, observe.ErrSourceAbsent) || errors.Is(err, ErrNoAlertmanager) {
		t.Fatalf("off: %v", err)
	}
	if lists.Load() != 0 {
		t.Fatal("off ran discovery")
	}
	if e.c.alertsPresent() {
		t.Fatal("an off cluster has the Alerting capability")
	}
	if id := e.c.AlertsIdentity(); id != amOffIdentity {
		t.Fatalf("identity %q", id)
	}
}

func TestASubscriptionIsNeverReadByDiscovery(t *testing.T) {
	e, lists := discoveryCounted(t, &config.EndpointConfig{Source: "lgtm-we", Match: map[string]string{"team": "core"}})
	err := readOnce(t, e)
	if err == nil || errors.Is(err, observe.ErrSourceAbsent) || !strings.Contains(err.Error(), "the installation lgtm-we") {
		t.Fatalf("subscribed: %v", err)
	}
	if lists.Load() != 0 {
		t.Fatal("a subscription ran discovery in its member")
	}
	if !e.c.alertsPresent() {
		t.Fatal("a subscribed cluster lost the Alerting capability")
	}
	if id := e.c.AlertsIdentity(); id != amSubscribedIdentity("lgtm-we") {
		t.Fatalf("identity %q", id)
	}
	// the same configuration adopted again keeps its identity; another
	// installation, or off, is a different source
	if !sameEndpoint(&config.EndpointConfig{Source: "a"}, &config.EndpointConfig{Source: "a"}) ||
		sameEndpoint(&config.EndpointConfig{Source: "a"}, &config.EndpointConfig{Source: "b"}) ||
		sameEndpoint(&config.EndpointConfig{}, &config.EndpointConfig{Off: true}) ||
		sameEndpoint(&config.EndpointConfig{Source: "a"}, &config.EndpointConfig{Source: "a", Scope: config.ScopeTenant}) {
		t.Fatal("sameEndpoint does not tell the subscription blocks apart")
	}
}

// The factory gives a served cluster its subscription, and never changes
// the entry it was handed.
func TestSubscribingFactory(t *testing.T) {
	sources := []config.SourceConfig{{ID: "lgtm-we", Kind: config.SourceKindAlertmanager, Host: "prd", Discover: true, MemberLabel: "cluster", Serves: []config.ServeConfig{{Group: "we"}}}}
	var got *config.EndpointConfig
	f := SubscribingFactory(sources, func(cc config.ClusterConfig) (Conn, error) { got = cc.Alertmanager; return nil, nil })
	entry := config.ClusterConfig{Name: "dev", Group: "we"}
	_, _ = f(entry)
	if got == nil || got.Source != "lgtm-we" || entry.Alertmanager != nil {
		t.Fatalf("implied: %+v, entry %+v", got, entry.Alertmanager)
	}
	_, _ = f(config.ClusterConfig{Name: "lab"})
	if got != nil {
		t.Fatalf("a cluster no installation serves: %+v", got)
	}
	own := &config.EndpointConfig{Endpoint: "https://am.example"}
	_, _ = f(config.ClusterConfig{Name: "dev", Group: "we", Alertmanager: own})
	if got != own {
		t.Fatal("its own block did not win")
	}
}

// S3a-2 §3.4, §3.5: a legacy read carries the scope sentence and its
// lineage - its mode, its source identity and its credential revision.
func TestALegacyReadSaysItsScope(t *testing.T) {
	f := newAMFake()
	f.alerts = `[{"fingerprint":"f1","labels":{"alertname":"A","cluster":"prd"},"status":{"state":"active"},"receivers":[{"name":"r"}]}]`
	api := httptest.NewServer(http.StripPrefix("/api/v1/namespaces/mimir/services/http:alertmanager:8080/proxy", f))
	defer api.Close()
	for _, tc := range []struct {
		match map[string]string
		want  string
	}{
		{nil, "from the Service mimir/alertmanager:8080/alertmanager on this cluster, tenant t, unfiltered: everything there is read as this cluster's"},
		{map[string]string{"cluster": "prd"}, "from the Service mimir/alertmanager:8080/alertmanager on this cluster, tenant t, filtered by cluster=prd"},
	} {
		e := newAMEnv(t, &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "mimir", Name: "alertmanager", Port: "8080", Path: "/alertmanager"}, Tenant: "t", Match: tc.match}, nil, nil)
		e.c.restConfig = &rest.Config{Host: api.URL}
		r := e.read(t)
		if r.Source != tc.want {
			t.Errorf("sentence: %q, want %q", r.Source, tc.want)
		}
		if r.Lineage.Mode != config.AMService || r.Lineage.Identity != r.Identity || r.Lineage.Tenant != "t" {
			t.Errorf("lineage: %+v", r.Lineage)
		}
	}
}

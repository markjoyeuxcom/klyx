package appbridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/secretstore"
)

// Settings S3a §5: an installation's credential is an action on the
// installation in force, at the revision shown: stored once, under its
// own account and bound to its origin; removed from use at once; never
// copied from a cluster's record.

func directSource(id, endpoint string) config.SourceConfig {
	return config.SourceConfig{ID: id, Kind: config.SourceKindAlertmanager, Endpoint: endpoint, Auth: &config.EndpointAuth{Type: "bearer"},
		MemberLabel: "cluster", Serves: []config.ServeConfig{{Cluster: "dev"}}}
}

func sourceCredRig(t *testing.T, sources ...config.SourceConfig) (*EndpointsService, *fleet.AlertSources, *secretstore.Memory, *endpointcred.Registry) {
	t.Helper()
	a, err := fleet.NewAlertSources(sources, []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}})
	if err != nil {
		t.Fatal(err)
	}
	store, revs := &secretstore.Memory{}, endpointcred.NewRegistry()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: prd\n    context: prd\n  - name: dev\n    context: dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	es := NewEndpointsService(path, store, revs, func(string) (EndpointConn, bool) { return nil, false })
	WireSourceCredentials(es, func() *fleet.AlertSources { return a }, func(_ context.Context, id string) (alertmanager.Status, string, error) {
		if id == "down" {
			return alertmanager.Status{}, "", errors.New("down: the credential is missing")
		}
		return alertmanager.Status{Version: "0.28.1", ClusterStatus: "ready", Peers: 3}, "https://am.example:443", nil
	})
	return es, a, store, revs
}

func revisionOf(a *fleet.AlertSources, id string) string {
	_, rev, _ := a.Installation(id)
	return strconv.FormatUint(rev, 10)
}

func TestAnInstallationsCredentialIsItsOwn(t *testing.T) {
	es, a, store, revs := sourceCredRig(t, directSource("shared", "https://am.example/am"), hostedOnPrd("hosted", config.ServeConfig{Cluster: "prd"}))
	rev := revisionOf(a, "shared")
	if r := es.SetSourceCredential("shared", "stale", "S3A-SECRET"); r.OK || !strings.Contains(r.Message, "changed since it was shown") {
		t.Fatalf("a stale revision: %+v", r)
	}
	if r := es.SetSourceCredential("hosted", revisionOf(a, "hosted"), "S3A-SECRET"); r.OK || !strings.Contains(r.Message, "takes no credential") {
		t.Fatalf("a hosted installation: %+v", r)
	}
	before := revs.Revision("source/shared", config.EndpointAlertmanager)
	r := es.SetSourceCredential("shared", rev, "S3A-SECRET")
	if !r.OK || strings.Contains(r.Message, "S3A-SECRET") || !strings.Contains(r.Message, "https://am.example:443") {
		t.Fatalf("%+v", r)
	}
	if revs.Revision("source/shared", config.EndpointAlertmanager) == before {
		t.Fatal("the credential revision did not move")
	}
	src, _, _ := a.Installation("shared")
	got := endpointcred.Resolve(store, endpointcred.Input{Cluster: "source/shared", Kind: config.EndpointAlertmanager, Account: endpointcred.SourceAccount("shared"),
		Spec: config.EndpointConfig{Endpoint: src.Endpoint, Auth: src.Auth}})
	if !got.Usable() || got.Header() != "Bearer S3A-SECRET" {
		t.Fatalf("the stored credential: %+v", got.State)
	}
	if _, err := store.Get(endpointcred.Account("dev", config.EndpointAlertmanager)); err == nil {
		t.Fatal("a cluster's account was written")
	}
	// bound to its origin: an installation moved elsewhere does not send it
	moved := directSource("shared", "https://elsewhere.example")
	cred := endpointcred.Resolve(store, endpointcred.Input{Cluster: "source/shared", Kind: config.EndpointAlertmanager, Account: endpointcred.SourceAccount("shared"),
		Spec: config.EndpointConfig{Endpoint: moved.Endpoint, Auth: moved.Auth}})
	if cred.Usable() {
		t.Fatal("a credential for another origin was usable")
	}
	if r := es.RemoveSourceCredential("shared", rev); !r.OK || !revs.Revoked("source/shared", config.EndpointAlertmanager) {
		t.Fatalf("%+v", r)
	}
	if _, err := store.Get(endpointcred.SourceAccount("shared")); err == nil {
		t.Fatal("the keychain still holds it")
	}
}

func TestAnInstallationsTest(t *testing.T) {
	down := directSource("down", "https://down.example")
	down.Serves = []config.ServeConfig{{Cluster: "prd"}}
	es, a, _, _ := sourceCredRig(t, directSource("shared", "https://am.example"), down)
	if r := es.TestSource("shared", revisionOf(a, "shared")); !r.OK || r.Message != "Available: Alertmanager 0.28.1, cluster ready with 3 peers at https://am.example:443, answered." {
		t.Fatalf("%+v", r)
	}
	if r := es.TestSource("down", revisionOf(a, "down")); r.OK || !strings.HasPrefix(r.Message, "Unavailable: down: the credential is missing") {
		t.Fatalf("%+v", r)
	}
	if r := es.TestSource("gone", "1"); r.OK || !strings.Contains(r.Message, "is not in force") {
		t.Fatalf("%+v", r)
	}
}

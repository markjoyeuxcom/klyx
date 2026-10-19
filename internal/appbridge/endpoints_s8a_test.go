package appbridge

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	typedfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/secretstore"
)

const amDoc = `clusters:
  - name: prd
    context: prd
    alertmanager:
      endpoint: https://am.example/alertmanager
      auth: {type: bearer}
      match: {cluster: prd}
`

func newAMServiceEnv(t *testing.T, doc string) (*EndpointsService, *endpointConnFake, *endpointcred.Registry, *secretstore.Memory) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, store, fake := endpointcred.NewRegistry(), &secretstore.Memory{}, &endpointConnFake{}
	svc := NewEndpointsService(p, store, reg, func(c string) (EndpointConn, bool) {
		return boundConnFake{endpointConnFake: fake, cluster: c, revs: reg}, true
	})
	return svc, fake, reg, store
}

// S8a §4, §7: Test is offered for Alertmanager; it reads the status through
// the endpoint as reviewed (the connection synced to it first) and reports
// the version and clustering.
func TestAlertmanagerTest(t *testing.T) {
	svc, fake, _, _ := newAMServiceEnv(t, amDoc)
	fake.amStatus, fake.amSrc = alertmanager.Status{Version: "0.34.1", ClusterStatus: "disabled"}, "https://am.example:443/alertmanager"
	var row EndpointRowDTO
	for _, r := range svc.GetEndpoints().Rows {
		if r.Kind == config.EndpointAlertmanager {
			row = r
		}
	}
	if !row.CanTest || row.TestNote != "" {
		t.Fatalf("row: %+v", row)
	}
	res := svc.TestEndpoint("prd", config.EndpointAlertmanager, row.Revision)
	if !res.OK || res.Message != "Available: Alertmanager 0.34.1, clustering disabled at https://am.example:443/alertmanager, answered." {
		t.Fatalf("test: %+v", res)
	}
	if len(fake.amSet) != 1 || fake.amSet[0].Match["cluster"] != "prd" {
		t.Fatalf("the connection was not synced to the reviewed endpoint: %+v", fake.amSet)
	}
	fake.amStatus = alertmanager.Status{Version: "0.34.1", ClusterStatus: "ready", Peers: 3}
	if res := svc.TestEndpoint("prd", config.EndpointAlertmanager, row.Revision); !strings.Contains(res.Message, "cluster ready with 3 peers") {
		t.Fatalf("clustered: %+v", res)
	}
	// a connection already on the reviewed endpoint is not re-synced: no
	// revision move, no reads dropped for nothing
	if len(fake.amSet) != 1 {
		t.Fatalf("re-synced an equal configuration: %d", len(fake.amSet))
	}
	fake.amErr = errors.New("refused: Authorization: Bearer S8-SECRET-TOKEN")
	if res := svc.TestEndpoint("prd", config.EndpointAlertmanager, row.Revision); res.OK || !strings.HasPrefix(res.Message, "Unavailable: ") || strings.Contains(res.Message, "S8-SECRET-TOKEN") {
		t.Fatalf("failure: %+v", res)
	}
}

// Set for a connected cluster installs the credential through the
// connection with the reviewed configuration, as one step.
func TestAlertmanagerSetThroughTheConnection(t *testing.T) {
	svc, fake, reg, store := newAMServiceEnv(t, amDoc)
	var row EndpointRowDTO
	for _, r := range svc.GetEndpoints().Rows {
		if r.Kind == config.EndpointAlertmanager {
			row = r
		}
	}
	before := reg.Revision("prd", config.EndpointAlertmanager)
	if res := svc.SetCredential("prd", config.EndpointAlertmanager, row.Revision, "S8-AM-TOKEN"); !res.OK {
		t.Fatalf("set: %+v", res)
	}
	if len(fake.amSet) != 1 || fake.amSet[0].Endpoint != "https://am.example/alertmanager" || reg.Revision("prd", config.EndpointAlertmanager) == before {
		t.Fatalf("set: %+v rev %d", fake.amSet, reg.Revision("prd", config.EndpointAlertmanager))
	}
	if _, err := store.Get(endpointcred.Account("prd", config.EndpointAlertmanager)); err != nil {
		t.Fatal("not stored")
	}
}

// End to end with a real connection: Set's credential is what the status
// read sends, and Test reads the status, never the alerts.
func TestAlertmanagerSetAndTestOnALiveConnection(t *testing.T) {
	var mu sync.Mutex
	var paths, auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths, auths = append(paths, r.URL.Path), append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"versionInfo":{"version":"0.34.1"},"cluster":{"status":"ready","peers":[{}]}}`)
	}))
	defer srv.Close()
	doc := "clusters:\n  - name: prd\n    context: prd\n    alertmanager:\n      endpoint: " + srv.URL + "/am\n      auth: {type: bearer}\n"
	p := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	reg, store := endpointcred.NewRegistry(), &secretstore.Memory{}
	live := fleet.NewClusterConn("prd", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	live.WithCredentials(fleet.Credentials{Store: store, Revisions: reg})
	svc := NewEndpointsService(p, store, reg, func(string) (EndpointConn, bool) { return live, true })
	row := svc.GetEndpoints().Rows[0]
	if res := svc.SetCredential("prd", config.EndpointAlertmanager, row.Revision, "S8-LIVE-TOKEN"); !res.OK {
		t.Fatalf("set: %+v", res)
	}
	res := svc.TestEndpoint("prd", config.EndpointAlertmanager, row.Revision)
	if !res.OK || !strings.Contains(res.Message, "Alertmanager 0.34.1, cluster ready with 1 peers") {
		t.Fatalf("test: %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/am/api/v2/status" || auths[0] != "Bearer S8-LIVE-TOKEN" {
		t.Fatalf("sent %q %q", paths, auths)
	}
}

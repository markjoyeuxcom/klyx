package appbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/secretstore"
	typedfake "k8s.io/client-go/kubernetes/fake"
)

// Codex's C5 implementation review, its probes adopted.

func TestMalformedEndpointURLDoesNotLeakToSettings(t *testing.T) {
	e := newEndpointsEnv(t)
	e.edit(t, "https://mimir.example/prometheus", "https://user:C5-URL-SECRET@bad%host")
	b, _ := json.Marshal(e.svc.GetEndpoints())
	if strings.Contains(string(b), "C5-URL-SECRET") {
		t.Fatalf("malformed URL leaked userinfo: %s", b)
	}
}

func TestSettingsTestReadsTheReviewedEndpoint(t *testing.T) {
	var oldHits, newHits atomic.Int32
	handler := func(h *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h.Add(1)
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
		}
	}
	oldSrv := httptest.NewServer(handler(&oldHits))
	defer oldSrv.Close()
	newSrv := httptest.NewServer(handler(&newHits))
	defer newSrv.Close()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte("clusters:\n  - name: prd\n    context: prd\n    metrics:\n      endpoint: "+newSrv.URL+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reg := endpointcred.NewRegistry()
	c := fleet.NewClusterConn("prd", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{Endpoint: oldSrv.URL})
	c.WithCredentials(fleet.Credentials{Revisions: reg})
	svc := NewEndpointsService(path, &secretstore.Memory{}, reg, func(string) (EndpointConn, bool) { return c, true })
	row := svc.GetEndpoints().Rows[0]
	result := svc.TestEndpoint(row.Cluster, row.Kind, row.Revision)
	if !result.OK || oldHits.Load() != 0 || newHits.Load() == 0 {
		t.Fatalf("reviewed %s but tested old endpoint: result=%+v old=%d new=%d", row.Source, result, oldHits.Load(), newHits.Load())
	}
}

// Finding 7, Set's half: a Set rebuilds the connection from the endpoint as
// reviewed - its auth and tenant - not the connection's older configuration.
func TestSetUsesTheReviewedEndpointConfiguration(t *testing.T) {
	var mu sync.Mutex
	var auths, tenants []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths, tenants = append(auths, r.Header.Get("Authorization")), append(tenants, r.Header.Get("X-Scope-OrgID"))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	body := "clusters:\n  - name: prd\n    context: prd\n    metrics:\n      endpoint: " + srv.URL + "\n      auth: {type: bearer}\n      tenant: team-b\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	reg := endpointcred.NewRegistry()
	store := &secretstore.Memory{}
	// the connection started before auth and the tenant were added
	c := fleet.NewClusterConn("prd", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{Endpoint: srv.URL})
	c.WithCredentials(fleet.Credentials{Store: store, Revisions: reg})
	svc := NewEndpointsService(path, store, reg, func(string) (EndpointConn, bool) { return c, true })
	row := svc.GetEndpoints().Rows[0]
	if res := svc.SetCredential(row.Cluster, row.Kind, row.Revision, "C5-SET-TOKEN"); !res.OK {
		t.Fatalf("set: %+v", res)
	}
	if _, capb := c.ClusterMetrics(context.Background(), false); !capb.Available {
		t.Fatalf("read after Set: %+v", capb)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) == 0 || auths[len(auths)-1] != "Bearer C5-SET-TOKEN" || tenants[len(tenants)-1] != "team-b" {
		t.Fatalf("the read after Set used the old configuration: auth %q tenant %q", auths, tenants)
	}
}

func TestMalformedEndpointURLNeverBecomesAReason(t *testing.T) {
	// The connection must refuse a malformed URL without returning its userinfo.
	c := fleet.NewClusterConn("prd", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{Endpoint: "https://user:C5-URL-SECRET@bad%host"})
	_, capb := c.ClusterMetrics(context.Background(), false)
	if strings.Contains(capb.Reason, "C5-URL-SECRET") {
		t.Fatalf("metrics capability leaked userinfo: %+v", capb)
	}
}

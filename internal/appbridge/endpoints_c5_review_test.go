package appbridge

import (
	"context"
	"encoding/base64"
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
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/safehttp"
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

// pausingStore holds its first Set, before or after the write, until
// released.
type pausingStore struct {
	*secretstore.Memory
	afterWrite       bool
	once             sync.Once
	entered, release chan struct{}
}

func (p *pausingStore) Set(account, value string) error {
	var err error
	if p.afterWrite {
		err = p.Memory.Set(account, value)
	}
	p.once.Do(func() { close(p.entered); <-p.release })
	if !p.afterWrite {
		err = p.Memory.Set(account, value)
	}
	return err
}

// Revision 1's review, finding 2 (Codex's probe, in the shape the fix
// gives the handoff): a reader running while Set installs a credential
// never pairs the new password with the old username and tenant, or the old
// password with the new ones, whichever side of the keychain write it
// lands on; after Set the reviewed pair is sent.
func TestSetNeverExposesAMixedCredentialAndConfiguration(t *testing.T) {
	basic := func(u, p string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p)) }
	for _, afterWrite := range []bool{false, true} {
		var mu sync.Mutex
		var sent []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			sent = append(sent, r.Header.Get("Authorization")+" tenant="+r.Header.Get("X-Scope-OrgID"))
			mu.Unlock()
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
		}))
		p := filepath.Join(t.TempDir(), "fleet.yaml")
		doc := "clusters:\n  - name: prd\n    context: prd\n    metrics:\n      endpoint: " + srv.URL + "\n      auth: {type: basic, username: bob}\n      tenant: bob\n"
		if err := os.WriteFile(p, []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
		reg := endpointcred.NewRegistry()
		store := &pausingStore{Memory: &secretstore.Memory{}, afterWrite: afterWrite, entered: make(chan struct{}), release: make(chan struct{})}
		origin, _ := safehttp.OriginOf(srv.URL)
		old, _ := endpointcred.Record{V: 1, Origin: origin, Type: "basic", Secret: "OLD-ALICE-PASSWORD"}.Encode()
		if err := store.Memory.Set(endpointcred.Account("prd", "metrics"), old); err != nil {
			t.Fatal(err)
		}
		live := fleet.NewClusterConn("prd", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{},
			config.MetricsConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "basic", Username: "alice"}, Tenant: "alice"})
		live.WithCredentials(fleet.Credentials{Store: store, Revisions: reg})
		svc := NewEndpointsService(p, store, reg, func(string) (EndpointConn, bool) { return live, true })
		row := svc.GetEndpoints().Rows[0]
		done := make(chan EndpointActionDTO, 1)
		go func() { done <- svc.SetCredential("prd", "metrics", row.Revision, "NEW-BOB-PASSWORD") }()
		<-store.entered
		read := make(chan struct{})
		go func() { live.ClusterMetrics(context.Background(), false); close(read) }()
		select {
		case <-read:
			t.Errorf("afterWrite=%v: a reader read while the credential was being installed", afterWrite)
		case <-time.After(100 * time.Millisecond):
		}
		close(store.release)
		if res := <-done; !res.OK {
			t.Fatalf("set: %+v", res)
		}
		<-read
		if _, capb := live.ClusterMetrics(context.Background(), true); !capb.Available {
			t.Fatalf("read after Set: %+v", capb)
		}
		mu.Lock()
		for _, h := range sent {
			if h == basic("alice", "NEW-BOB-PASSWORD")+" tenant=alice" || h == basic("bob", "OLD-ALICE-PASSWORD")+" tenant=bob" ||
				strings.HasPrefix(h, basic("alice", "NEW-BOB-PASSWORD")) || strings.HasPrefix(h, basic("bob", "OLD-ALICE-PASSWORD")) {
				t.Errorf("afterWrite=%v: a mixed pair was sent: %q", afterWrite, h)
			}
		}
		if last := sent[len(sent)-1]; last != basic("bob", "NEW-BOB-PASSWORD")+" tenant=bob" {
			t.Errorf("afterWrite=%v: after Set: %q", afterWrite, last)
		}
		mu.Unlock()
		srv.Close()
	}
}

// With a live connection, a Set after a Remove lifts the tombstone in the
// same step: the endpoint reads available again with the new credential.
func TestSetAfterRemoveOnALiveConnection(t *testing.T) {
	var mu sync.Mutex
	var last string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last = r.Header.Get("Authorization")
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "fleet.yaml")
	doc := "clusters:\n  - name: prd\n    context: prd\n    metrics:\n      endpoint: " + srv.URL + "\n      auth: {type: bearer}\n"
	if err := os.WriteFile(p, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	reg := endpointcred.NewRegistry()
	store := &secretstore.Memory{}
	live := fleet.NewClusterConn("prd", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{},
		config.MetricsConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}})
	live.WithCredentials(fleet.Credentials{Store: store, Revisions: reg})
	svc := NewEndpointsService(p, store, reg, func(string) (EndpointConn, bool) { return live, true })
	row := svc.GetEndpoints().Rows[0]
	if res := svc.SetCredential("prd", "metrics", row.Revision, "C5-FIRST"); !res.OK {
		t.Fatalf("set: %+v", res)
	}
	if res := svc.RemoveCredential("prd", "metrics", row.Revision); !res.OK {
		t.Fatalf("remove: %+v", res)
	}
	if _, capb := live.ClusterMetrics(context.Background(), false); capb.Available {
		t.Fatalf("available after Remove: %+v", capb)
	}
	if res := svc.SetCredential("prd", "metrics", row.Revision, "C5-SECOND"); !res.OK {
		t.Fatalf("set again: %+v", res)
	}
	if _, capb := live.ClusterMetrics(context.Background(), false); !capb.Available {
		t.Fatalf("a Set after Remove reads unavailable: %+v", capb)
	}
	mu.Lock()
	defer mu.Unlock()
	if last != "Bearer C5-SECOND" {
		t.Fatalf("sent %q", last)
	}
}

package fleet

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	typedfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/loki"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/secretstore"
)

// fakeLoki answers the paths S8b reads and records what it was sent.
type fakeLoki struct {
	mu      sync.Mutex
	labels  string
	query   string
	code    int
	body    string
	hold    chan struct{}
	headers []http.Header
	queries []string
	paths   []string
}

func (f *fakeLoki) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.headers = append(f.headers, r.Header.Clone())
	f.queries = append(f.queries, r.URL.Query().Get("query"))
	f.paths = append(f.paths, r.URL.Path)
	code, body, hold := f.code, f.body, f.hold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if code != 0 {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/loki/api/v1/labels"):
		_, _ = io.WriteString(w, f.labels)
	case strings.HasSuffix(r.URL.Path, "/loki/api/v1/query_range"):
		_, _ = io.WriteString(w, f.query)
	case strings.HasSuffix(r.URL.Path, "/loki/api/v1/status/buildinfo"):
		_, _ = io.WriteString(w, `{"version":"3.7.8","revision":"09e6ce2f"}`)
	default:
		http.NotFound(w, r)
	}
}

const lokiStreams = `{"status":"success","data":{"resultType":"streams","encodingFlags":["categorize-labels"],"result":[
 {"stream":{"k8s_namespace_name":"team","k8s_pod_name":"api-1"},"values":[["1792418924000000000","line a",{"structuredMetadata":{"k8s_pod_uid":"u-1"}}]]}]}}`

type lokiEnv struct {
	c     *ClusterConn
	reg   *endpointcred.Registry
	store *secretstore.Memory
}

func newLokiEnv(t *testing.T, cfg *config.EndpointConfig) *lokiEnv {
	t.Helper()
	e := &lokiEnv{reg: endpointcred.NewRegistry(), store: &secretstore.Memory{}}
	e.c = NewClusterConn("prd", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	e.c.WithCredentials(Credentials{Store: e.store, Revisions: e.reg})
	e.c.WithLoki(cfg)
	return e
}

func storeLoki(t *testing.T, s *secretstore.Memory, origin, secret string) {
	t.Helper()
	v, _ := endpointcred.Record{V: 1, Origin: origin, Type: "bearer", Secret: secret}.Encode()
	if err := s.Set(endpointcred.Account("prd", config.EndpointLoki), v); err != nil {
		t.Fatal(err)
	}
}

// Coverage S8b §2, §6: the direct endpoint carries the credential and the
// tenant; labels, query_range (categorized) and the build info are read.
func TestLokiDirect(t *testing.T) {
	f := &fakeLoki{labels: `{"status":"success","data":["k8s_namespace_name","k8s_pod_name"]}`, query: lokiStreams}
	srv := httptest.NewServer(f)
	defer srv.Close()
	e := newLokiEnv(t, &config.EndpointConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}, Tenant: "lab", Match: map[string]string{"k8s_cluster_name": "prd"}})
	origin, _ := safehttp.OriginOf(srv.URL)
	storeLoki(t, e.store, origin, "LOKI-SECRET")
	r, err := e.c.Loki()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	labels, data, err := r.Labels(ctx, time.Now().Add(-time.Hour), time.Now(), `{k8s_cluster_name="prd"}`)
	if err != nil || !data || len(labels) != 2 {
		t.Fatalf("labels: %v %v %v", labels, data, err)
	}
	es, err := r.Query(ctx, `{k8s_cluster_name="prd"}`, 1, 2, 1000)
	if err != nil || len(es) != 1 || es[0].UID() != "u-1" {
		t.Fatalf("query: %+v %v", es, err)
	}
	if v, err := r.Status(ctx); err != nil || v != "3.7.8" {
		t.Fatalf("status: %q %v", v, err)
	}
	h := f.headers[1]
	if h.Get("Authorization") != "Bearer LOKI-SECRET" || h.Get("X-Scope-OrgID") != "lab" || h.Get("X-Loki-Response-Encoding-Flags") != "categorize-labels" {
		t.Fatalf("headers: %v", h)
	}
	if f.headers[0].Get("X-Loki-Response-Encoding-Flags") != "" {
		t.Fatal("the labels read asked for categorized labels")
	}
	if r.LineageKey() == "" || strings.Contains(r.LineageKey(), "prd") || r.Source() == "" {
		t.Fatalf("display: %q %q", r.LineageKey(), r.Source())
	}
}

// Loki's own words are shown redacted; a 401 without a tenant says what
// to set; success with no data at all is "no data", never an empty list
// claimed whole.
func TestLokiAnswers(t *testing.T) {
	f := &fakeLoki{code: 400, body: "max entries limit exceeded; your token LOKI-SECRET"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	e := newLokiEnv(t, &config.EndpointConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}})
	origin, _ := safehttp.OriginOf(srv.URL)
	storeLoki(t, e.store, origin, "LOKI-SECRET")
	r, _ := e.c.Loki()
	_, err := r.Query(context.Background(), "{}", 1, 2, 6000)
	var se *loki.StatusError
	if !errors.As(err, &se) || se.Code != 400 || !strings.Contains(se.Text, "max entries limit exceeded") || strings.Contains(err.Error(), "LOKI-SECRET") {
		t.Fatalf("400: %v", err)
	}
	f.mu.Lock()
	f.code, f.body = 401, "no org id"
	f.mu.Unlock()
	if _, _, err := r.Labels(context.Background(), time.Now(), time.Now(), ""); !errors.As(err, &se) || se.Text != "Loki requires a tenant; set loki.tenant" {
		t.Fatalf("401: %v", err)
	}
	f.mu.Lock()
	f.code, f.body = 0, ""
	f.labels = `{"status":"success"}`
	f.mu.Unlock()
	if l, data, err := r.Labels(context.Background(), time.Now(), time.Now(), ""); err != nil || data || l != nil {
		t.Fatalf("no data: %v %v %v", l, data, err)
	}
	f.mu.Lock()
	f.labels = `null`
	f.mu.Unlock()
	if _, _, err := r.Labels(context.Background(), time.Now(), time.Now(), ""); !errors.Is(err, loki.ErrShape) {
		t.Fatalf("null labels: %v", err)
	}
}

// A read across a configuration or credential change fails, and a reader
// knows it is no longer current (§6, C5 §7).
func TestLokiFence(t *testing.T) {
	f := &fakeLoki{labels: `{"status":"success","data":[]}`, hold: make(chan struct{}), query: lokiStreams}
	srv := httptest.NewServer(f)
	defer srv.Close()
	cfg := &config.EndpointConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}}
	e := newLokiEnv(t, cfg)
	origin, _ := safehttp.OriginOf(srv.URL)
	storeLoki(t, e.store, origin, "LOKI-SECRET")
	r, _ := e.c.Loki()
	if !r.Current() {
		t.Fatal("a fresh reader is not current")
	}
	done := make(chan error, 1)
	go func() { _, err := r.Query(context.Background(), "{}", 1, 2, 10); done <- err }()
	time.Sleep(50 * time.Millisecond)
	e.c.SetLokiConfig(&config.EndpointConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}, Tenant: "other"})
	close(f.hold)
	if err := <-done; err == nil {
		t.Fatal("a read across a configuration change was kept")
	}
	if r.Current() {
		t.Fatal("a reader over the old configuration is current")
	}
	if _, err := newLokiEnv(t, nil).c.Loki(); !errors.Is(err, ErrNoLoki) {
		t.Fatalf("no Loki: %v", err)
	}
}

// The service-proxy path reads through the guarded client: the tenant
// travels, never an endpoint credential, and Loki's 400 words arrive.
func TestLokiServiceRef(t *testing.T) {
	f := &fakeLoki{code: 400, body: "parse error at line 1"}
	api := httptest.NewServer(f)
	defer api.Close()
	e := newLokiEnv(t, &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "logs", Name: "loki", Port: "http", Path: "/gw"}, Tenant: "lab"})
	e.c.restConfig = &rest.Config{Host: api.URL, BearerToken: "kube-token"}
	r, err := e.c.Loki()
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Query(context.Background(), "{}", 1, 2, 10)
	var se *loki.StatusError
	if !errors.As(err, &se) || se.Code != 400 || se.Text != "parse error at line 1" {
		t.Fatalf("proxied 400: %v", err)
	}
	if f.paths[0] != "/api/v1/namespaces/logs/services/http:loki:http/proxy/gw/loki/api/v1/query_range" ||
		f.headers[0].Get("X-Scope-OrgID") != "lab" || f.headers[0].Get("Authorization") != "Bearer kube-token" {
		t.Fatalf("proxied request: %s %v", f.paths[0], f.headers[0])
	}
	if r.Source() != "Service logs/loki:http/gw" && !strings.HasPrefix(r.Source(), "Service logs/loki") {
		t.Fatalf("source: %q", r.Source())
	}
}

// At most two pages per cluster at a time.
func TestLokiSlots(t *testing.T) {
	e := newLokiEnv(t, &config.EndpointConfig{Endpoint: "https://loki.example"})
	r := &LokiReader{c: e.c}
	a, err := r.Slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.Slot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a third page in the cluster: %v", err)
	}
	a()
	c, err := r.Slot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b()
	c()
}

// The proxied path has no transport gate: the reader's own fence fails a
// read across a configuration change (§6, C5 §7).
func TestLokiFenceOnTheProxyPath(t *testing.T) {
	f := &fakeLoki{hold: make(chan struct{}), query: lokiStreams}
	api := httptest.NewServer(f)
	defer api.Close()
	cfg := &config.EndpointConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "logs", Name: "loki", Port: "http"}, Tenant: "lab"}
	e := newLokiEnv(t, cfg)
	e.c.restConfig = &rest.Config{Host: api.URL}
	r, err := e.c.Loki()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := r.Query(context.Background(), "{}", 1, 2, 10); done <- err }()
	time.Sleep(50 * time.Millisecond)
	e.c.SetLokiConfig(&config.EndpointConfig{ServiceRef: cfg.ServiceRef, Tenant: "other"})
	close(f.hold)
	if err := <-done; !errors.Is(err, ErrLokiSourceChanged) {
		t.Fatalf("a proxied read across a configuration change: %v", err)
	}
}

package endpointhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/safehttp"
)

var errBig = errors.New("too large for this kind")

// Coverage S8a §2, §3: a Service's path prefix and repeated parameters (an
// Alertmanager filter per matcher) reach the Service through the API
// server's service proxy.
func TestProxyCarriesThePathPrefixAndRepeatedParameters(t *testing.T) {
	var gotPath, gotTenant string
	var gotFilter []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotTenant, gotFilter = r.URL.Path, r.Header.Get("X-Scope-OrgID"), r.URL.Query()["filter"]
		_, _ = io.WriteString(w, "[]")
	}))
	defer api.Close()
	rc, err := NewGuardedProxyREST(&rest.Config{Host: api.URL}, Limits{MaxBytes: 1 << 20, TooLarge: errBig})
	if err != nil {
		t.Fatal(err)
	}
	p := NewProxy(rc, Service{Namespace: "monitoring", Name: "mimir-alertmanager", Port: "web", Path: "/alertmanager/"}, "team-a", Limits{MaxBytes: 1 << 20, TooLarge: errBig})
	code, body, err := p.Get(context.Background(), "/api/v2/alerts", url.Values{"filter": {`cluster="a"`, `env="prd"`}})
	if err != nil || code != 200 || string(body) != "[]" {
		t.Fatalf("get: %d %q %v", code, body, err)
	}
	if gotPath != "/api/v1/namespaces/monitoring/services/http:mimir-alertmanager:web/proxy/alertmanager/api/v2/alerts" {
		t.Fatalf("path: %s", gotPath)
	}
	if gotTenant != "team-a" || len(gotFilter) != 2 || gotFilter[0] != `cluster="a"` || gotFilter[1] != `env="prd"` {
		t.Fatalf("tenant %q filter %q", gotTenant, gotFilter)
	}
}

// A non-2xx answer through the proxy is its status and no body.
func TestProxyNon2xxIsItsStatus(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":503}`)
	}))
	defer api.Close()
	rc, err := NewGuardedProxyREST(&rest.Config{Host: api.URL}, Limits{MaxBytes: 1 << 20, TooLarge: errBig})
	if err != nil {
		t.Fatal(err)
	}
	code, body, err := NewProxy(rc, Service{Namespace: "m", Name: "am", Port: "9093"}, "", Limits{MaxBytes: 1 << 20, TooLarge: errBig}).Get(context.Background(), "/api/v2/status", nil)
	if err != nil || code != 503 || body != nil {
		t.Fatalf("got %d %q %v", code, body, err)
	}
}

// Each kind's Limits bound its own reads, direct and proxied, as an error.
func TestEachKindsLimitsBoundItsReads(t *testing.T) {
	big := strings.Repeat("x", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, big) }))
	defer srv.Close()
	small := Limits{MaxBytes: 63, TooLarge: errBig}
	enough := Limits{MaxBytes: 64, TooLarge: errBig}
	for _, tc := range []struct {
		lim  Limits
		fail bool
	}{{small, true}, {enough, false}} {
		d := NewDirect(srv.URL, Auth{}, safehttp.NewClient(DirectPolicy(tc.lim, 3*time.Second, false)), tc.lim)
		_, body, err := d.Get(context.Background(), "/x", nil)
		if tc.fail != errors.Is(err, errBig) || (!tc.fail && string(body) != big) {
			t.Fatalf("direct, limit %d: %d bytes %v", tc.lim.MaxBytes, len(body), err)
		}
		rc, err := NewGuardedProxyREST(&rest.Config{Host: srv.URL}, tc.lim)
		if err != nil {
			t.Fatal(err)
		}
		_, body, err = NewProxy(rc, Service{Namespace: "m", Name: "am", Port: "9093"}, "", tc.lim).Get(context.Background(), "/x", nil)
		if tc.fail != errors.Is(err, errBig) || (!tc.fail && string(body) != big) {
			t.Fatalf("proxy, limit %d: %d bytes %v", tc.lim.MaxBytes, len(body), err)
		}
	}
}

// The direct transport sends repeated parameters, the credential and the
// tenant, and returns the body exactly as sent.
func TestDirectGet(t *testing.T) {
	var auth, tenant string
	var filter []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, tenant, filter = r.Header.Get("Authorization"), r.Header.Get("X-Scope-OrgID"), r.URL.Query()["filter"]
		_, _ = io.WriteString(w, `{"echo":"Bearer S8-TOKEN"}`)
	}))
	defer srv.Close()
	a := Auth{Header: "Bearer S8-TOKEN", Tenant: "t1", Redactor: safehttp.NewRedactor(safehttp.Bearer("S8-TOKEN"))}
	lim := Limits{MaxBytes: 1 << 20, TooLarge: errBig}
	d := NewDirect(srv.URL+"/alertmanager/", a, safehttp.NewClient(DirectPolicy(lim, 3*time.Second, false)), lim)
	code, body, err := d.Get(context.Background(), "/api/v2/alerts", url.Values{"filter": {`a="1"`, `b="2"`}})
	if err != nil || code != 200 || string(body) != `{"echo":"Bearer S8-TOKEN"}` {
		t.Fatalf("get: %d %q %v", code, body, err)
	}
	if auth != "Bearer S8-TOKEN" || tenant != "t1" || len(filter) != 2 {
		t.Fatalf("sent %q %q %q", auth, tenant, filter)
	}
}

// ReadCapped reads to the kind's own limit - above the metrics 4 MiB too -
// and errors one byte past it, never returning a short body as whole.
func TestReadCapped(t *testing.T) {
	for _, tc := range []struct {
		limit, size int64
		fail        bool
	}{{100, 100, false}, {100, 101, true}, {8 << 20, 6 << 20, false}, {8 << 20, 8<<20 + 1, true}} {
		body, err := ReadCapped(strings.NewReader(strings.Repeat("x", int(tc.size))), Limits{MaxBytes: tc.limit, TooLarge: errBig})
		if tc.fail != errors.Is(err, errBig) || (!tc.fail && int64(len(body)) != tc.size) {
			t.Fatalf("limit %d size %d: %d bytes %v", tc.limit, tc.size, len(body), err)
		}
	}
}

type failingRoundTripper struct{ msg string }

func (f failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New(f.msg)
}

// An error from the round trip itself that quotes the credential is
// redacted before it leaves the transport (C5 §5).
func TestDirectRedactsTransportErrors(t *testing.T) {
	a := Auth{Header: "Bearer S8-LEAK", Redactor: safehttp.NewRedactor(safehttp.Bearer("S8-LEAK"))}
	client := &http.Client{Transport: failingRoundTripper{msg: "proxy refused Authorization: Bearer S8-LEAK"}}
	_, _, err := NewDirect("http://am.example", a, client, Limits{MaxBytes: 1 << 20, TooLarge: errBig}).Get(context.Background(), "/api/v2/status", nil)
	if err == nil || strings.Contains(err.Error(), "S8-LEAK") {
		t.Fatalf("err: %v", err)
	}
}

func rawProxy(t *testing.T, apiURL string, max int64) *RawProxy {
	t.Helper()
	g, err := NewGuardedProxy(&rest.Config{Host: apiURL, BearerToken: "kube-token"}, Limits{MaxBytes: max, TooLarge: errBig})
	if err != nil {
		t.Fatal(err)
	}
	return NewRawProxy(g, Service{Namespace: "logs", Name: "loki", Port: "http", Path: "/loki"}, "lab", Limits{MaxBytes: max, TooLarge: errBig})
}

// Coverage S8b §9, the transport contract (Codex's probes of c2b4e26): a
// plain-text non-2xx body arrives whole - client-go's Do().Raw() returns
// none of it - and so does a 2xx one.
func TestRawProxyKeepsAPlainTextNon2xxBody(t *testing.T) {
	const msg = "max entries limit per query exceeded, limit > max_entries_limit_per_query (6000 > 5000)"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/logs/services/http:loki:http/proxy/loki/loki/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, msg)
	}))
	defer api.Close()
	code, body, err := rawProxy(t, api.URL, 1<<20).Get(context.Background(), "/loki/api/v1/query_range", url.Values{"limit": {"6000"}}, nil)
	if err != nil || code != 400 || string(body) != msg {
		t.Fatalf("got %d %q %v", code, body, err)
	}
}

// The cap's sentinel survives a non-2xx (DoRaw would replace it with a
// status error), and a cancellation wins over the status.
func TestRawProxyReadErrorsWinOverTheStatus(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, strings.Repeat("x", 64))
	}))
	defer big.Close()
	if code, body, err := rawProxy(t, big.URL, 16).Get(context.Background(), "/x", nil, nil); !errors.Is(err, errBig) || code != 0 || body != nil {
		t.Fatalf("cap on a 400: %d %q %v", code, body, err)
	}
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if code, body, err := rawProxy(t, slow.URL, 1<<20).Get(ctx, "/x", nil, nil); !errors.Is(err, context.DeadlineExceeded) || code != 0 || body != nil {
		t.Fatalf("cancellation: %d %q %v", code, body, err)
	}
}

// The cluster's own credential and the tenant travel; a caller's protocol
// header does too; a caller's Authorization or tenant never does (no
// endpoint credential on the proxy path, C5 §4).
func TestRawProxyHeaders(t *testing.T) {
	var got http.Header
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, "{}")
	}))
	defer api.Close()
	hdr := http.Header{"X-Loki-Response-Encoding-Flags": {"categorize-labels"}, "Authorization": {"Bearer endpoint-secret"}, "X-Scope-OrgID": {"other"}}
	if _, _, err := rawProxy(t, api.URL, 1<<20).Get(context.Background(), "/x", nil, hdr); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer kube-token" || got.Get("X-Scope-OrgID") != "lab" || got.Get("X-Loki-Response-Encoding-Flags") != "categorize-labels" {
		t.Fatalf("headers: %v", got)
	}
}

// Redirects: the API server's own origin only; a cross-origin redirect is
// refused before any request reaches the other server.
func TestRawProxyRedirectGuard(t *testing.T) {
	var elsewhere int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere++ }))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/away") {
			http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/same", http.StatusFound)
	}))
	defer api.Close()
	p := rawProxy(t, api.URL, 1<<20)
	if code, body, err := p.Get(context.Background(), "/here", nil, nil); err != nil || code != 200 || string(body) != "ok" {
		t.Fatalf("same origin: %d %q %v", code, body, err)
	}
	if _, _, err := p.Get(context.Background(), "/away", nil, nil); err == nil || elsewhere != 0 {
		t.Fatalf("cross origin: %v, the other server saw %d", err, elsewhere)
	}
}

// GetWith carries protocol headers; the transport's credential and tenant
// are never replaced by a caller's.
func TestDirectGetWith(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer srv.Close()
	d := NewDirect(srv.URL, Auth{Header: "Bearer endpoint", Tenant: "lab", Redactor: safehttp.NewRedactor(safehttp.Bearer("endpoint"))},
		srv.Client(), Limits{MaxBytes: 1 << 20, TooLarge: errBig})
	hdr := http.Header{"X-Loki-Response-Encoding-Flags": {"categorize-labels"}, "Authorization": {"Bearer other"}, "X-Scope-Orgid": {"other"}}
	if _, _, err := d.GetWith(context.Background(), "/x", nil, hdr); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer endpoint" || got.Get("X-Scope-OrgID") != "lab" || got.Get("X-Loki-Response-Encoding-Flags") != "categorize-labels" ||
		len(got.Values("Authorization")) != 1 {
		t.Fatalf("headers: %v", got)
	}
}

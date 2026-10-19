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

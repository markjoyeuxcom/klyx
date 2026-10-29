package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/safehttp"
)

// Coverage C5 §4 (Codex's design review, finding 1, its probe adopted): the
// service-proxy client refuses a redirect off the API server's origin before
// the redirected request reaches client-go's auth stack, so the other origin
// receives no request and never the kube credential - bearer or basic.
func TestGuardedProxyRedirectDoesNotExportKubeCredential(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func(host string) *rest.Config
	}{
		{"bearer", func(h string) *rest.Config {
			return &rest.Config{Host: h, BearerToken: "C5_SYNTHETIC_KUBE_TOKEN", Timeout: 3 * time.Second}
		}},
		{"basic", func(h string) *rest.Config {
			return &rest.Config{Host: h, Username: "kube", Password: "C5_SYNTHETIC_KUBE_PASSWORD", Timeout: 3 * time.Second}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
			}))
			defer external.Close()
			// another origin, as a proxied response's Location can name
			redirect := strings.Replace(external.URL, "127.0.0.1", "localhost", 1)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, redirect, http.StatusFound) }))
			defer api.Close()
			rc, err := NewGuardedProxyREST(tc.cfg(api.URL))
			if err != nil {
				t.Fatal(err)
			}
			q := NewProxyTransport(rc, ServiceCandidate{Namespace: "monitoring", Name: "echo", Port: "80", Scheme: "http"})
			_, _, err = q.q.InstantQuery(context.Background(), "vector(1)")
			if err == nil || !strings.Contains(err.Error(), "another origin") {
				t.Fatalf("the redirect is refused: %v", err)
			}
			if hits.Load() != 0 {
				t.Fatal("the other origin received a request")
			}
		})
	}
}

// A redirect within the API server's own origin is followed.
func TestGuardedProxyFollowsSameOriginRedirect(t *testing.T) {
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/moved") {
			http.Redirect(w, r, api.URL+r.URL.Path+"/moved?"+r.URL.RawQuery, http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
	}))
	defer api.Close()
	rc, err := NewGuardedProxyREST(&rest.Config{Host: api.URL, BearerToken: "t", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	q := NewProxyTransport(rc, ServiceCandidate{Namespace: "monitoring", Name: "echo", Port: "80", Scheme: "http"})
	if err := NewClient(q, Scope{}).Liveness(context.Background()); err != nil {
		t.Fatalf("same-origin redirect: %v", err)
	}
}

// Coverage C5 §5 (Codex's design review, finding 3, its probe adopted): an
// endpoint echoing the credential, in a JSON error or a plain body, for an
// instant or a range query, never puts it into a reason or an error.
func TestEchoedCredentialNeverLeavesTheTransport(t *testing.T) {
	const token = "C5_SYNTHETIC_ENDPOINT_NEEDLE"
	jsonEcho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "invalid credential: " + r.Header.Get("Authorization")})
	}))
	defer jsonEcho.Close()
	textEcho := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "denied for "+r.Header.Get("Authorization")+" and "+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}))
	defer textEcho.Close()
	for _, srv := range []*httptest.Server{jsonEcho, textEcho} {
		q := NewDirectTransport(srv.URL, token, safehttp.NewClient(DirectPolicy(3*time.Second, false)))
		if got := Probe(context.Background(), Resolution{Transport: q}); strings.Contains(got.Reason, token) || got.Reason == "" {
			t.Fatalf("the probe's reason: %q", got.Reason)
		}
		c := NewClient(q, Scope{})
		_, err := c.InstantScalar(context.Background(), raw("up"))
		_, rerr := c.RangeSeries(context.Background(), raw("up"), time.Unix(0, 0), time.Unix(60, 0), time.Minute)
		for _, e := range []error{err, rerr} {
			if e == nil || strings.Contains(e.Error(), token) {
				t.Fatalf("an error leaked or was lost: %v", e)
			}
		}
	}
}

// A body past the cap is an error, never a short body parsed as whole.
func TestDirectReadPastTheCapIsAnError(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(" ", maxResponseBytes+1))
	}))
	defer big.Close()
	q := NewDirectTransport(big.URL, "", safehttp.NewClient(DirectPolicy(5*time.Second, false)))
	if _, _, err := q.q.InstantQuery(context.Background(), "up"); !strings.Contains(errStr(err), "exceeded") {
		t.Fatalf("past the cap: %v", err)
	}
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return err.Error()
}

// The probe's reason also loses general credential shapes the endpoint
// wrote that are not the request's own credential.
func TestProbeReasonScrubsCredentialShapes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "upstream https://admin:hunter2@mimir.internal refused password=hunter2"})
	}))
	defer srv.Close()
	q := NewDirectTransport(srv.URL, "", safehttp.NewClient(DirectPolicy(3*time.Second, false)))
	if got := Probe(context.Background(), Resolution{Transport: q}); strings.Contains(got.Reason, "hunter2") || !strings.Contains(got.Reason, "refused") {
		t.Fatalf("the reason: %q", got.Reason)
	}
}

// The proxied read's cap is an error too (it has no safehttp transport).
func TestProxyReadPastTheCapIsAnError(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(" ", maxResponseBytes+1))
	}))
	defer api.Close()
	rc, err := NewGuardedProxyREST(&rest.Config{Host: api.URL, BearerToken: "t", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	q := NewProxyTransport(rc, ServiceCandidate{Namespace: "monitoring", Name: "p", Port: "9090", Scheme: "http"})
	if _, _, err := q.q.InstantQuery(context.Background(), "up"); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("past the cap: %v", err)
	}
}

// Coverage C5 §4: a proxied request carries the tenant header to the API
// server, and only the kube credential - never an endpoint credential.
func TestProxyCarriesTenantAndOnlyKubeAuth(t *testing.T) {
	var gotTenant, gotAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant, gotAuth = r.Header.Get("X-Scope-OrgID"), r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
	}))
	defer api.Close()
	rc, err := NewGuardedProxyREST(&rest.Config{Host: api.URL, BearerToken: "kube-tok", Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	q := NewProxyTransportTenant(rc, ServiceCandidate{Namespace: "monitoring", Name: "mimir", Port: "8080", Scheme: "http"}, "platform")
	if err := NewClient(q, Scope{}).Liveness(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotTenant != "platform" || gotAuth != "Bearer kube-tok" {
		t.Fatalf("tenant %q, auth %q", gotTenant, gotAuth)
	}
}

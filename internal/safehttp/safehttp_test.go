package safehttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// C5 §3: one canonical origin per endpoint.
func TestCanonicalOrigin(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://Example.COM/prometheus", "https://example.com:443"},
		{"HTTPS://example.com:443", "https://example.com:443"},
		{"http://mimir.example", "http://mimir.example:80"},
		{"http://mimir.example:80/", "http://mimir.example:80"},
		{"https://bücher.example", "https://xn--bcher-kva.example:443"},
		{"http://[::1]:9090/x", "http://[::1]:9090"},
		{"http://127.0.0.1:9090", "http://127.0.0.1:9090"},
		{"https://example.com./", "https://example.com:443"},
	} {
		got, err := OriginOf(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"ftp://example.com", "https://", "not a url at all\x7f", "/relative"} {
		if o, err := OriginOf(bad); err == nil {
			t.Errorf("%q has no origin, got %q", bad, o)
		}
	}
	a, _ := OriginOf("https://example.com")
	b, _ := OriginOf("http://example.com")
	c, _ := OriginOf("https://example.com:8443")
	if a == b || a == c {
		t.Fatal("scheme and port are part of the origin")
	}
}

// C5 §4: same-origin redirects only, never downgraded, at most five hops; a
// refused redirect sends nothing to its target; Authorization rides every
// allowed hop.
func TestSameOriginRedirects(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherHits.Add(1) }))
	defer other.Close()
	var hopAuth []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hopAuth = append(hopAuth, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/away":
			http.Redirect(w, r, other.URL+"/x", http.StatusFound)
		case r.URL.Path == "/here":
			http.Redirect(w, r, srv.URL+"/final", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/loop"):
			http.Redirect(w, r, srv.URL+"/loop"+fmt.Sprint(len(hopAuth)), http.StatusFound)
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer srv.Close()
	c := NewClient(Policy{Timeout: 3 * time.Second, MaxBytes: 1 << 20})
	get := func(path string) (*http.Response, error) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer s3cret-token")
		return c.Do(req)
	}
	if _, err := get("/away"); err == nil || !strings.Contains(err.Error(), "another origin") {
		t.Fatalf("a cross-origin redirect is refused: %v", err)
	}
	if otherHits.Load() != 0 {
		t.Fatal("the other origin received a request")
	}
	hopAuth = nil
	resp, err := get("/here")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("a same-origin redirect is followed: %v", err)
	}
	if len(hopAuth) != 2 || hopAuth[0] != "Bearer s3cret-token" || hopAuth[1] != "Bearer s3cret-token" {
		t.Fatalf("Authorization on every allowed hop: %q", hopAuth)
	}
	hopAuth = nil
	if _, err := get("/loop"); err == nil || !strings.Contains(err.Error(), "too many redirects") || len(hopAuth) != MaxRedirects {
		t.Fatalf("at most %d hops: %v after %d", MaxRedirects, err, len(hopAuth))
	}
}

// A redirect from HTTPS to HTTP on the same host is another origin.
func TestRedirectDowngradeRefused(t *testing.T) {
	var plainHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { plainHits.Add(1) }))
	defer plain.Close()
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(plain.URL, "http://", "http://", 1)+"/x", http.StatusFound)
	}))
	defer tlsSrv.Close()
	c := NewClient(Policy{Timeout: 3 * time.Second, MaxBytes: 1 << 20, TLSSkipVerify: true})
	if _, err := c.Get(tlsSrv.URL); err == nil || plainHits.Load() != 0 {
		t.Fatalf("a downgrade is refused before any request: %v, %d hits", err, plainHits.Load())
	}
}

// The cap errors past the limit, never a short body read as whole.
func TestCapErrorsInsteadOfTruncating(t *testing.T) {
	tooLarge := errors.New("too large")
	for _, n := range []int{100, 101} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", n)) }))
		c := NewClient(Policy{Timeout: 3 * time.Second, MaxBytes: 100, TooLarge: tooLarge})
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		srv.Close()
		if n == 100 && (err != nil || len(b) != 100) {
			t.Fatalf("at the cap: %d %v", len(b), err)
		}
		if n == 101 && !errors.Is(err, tooLarge) {
			t.Fatalf("past the cap: %d %v", len(b), err)
		}
	}
}

// Endpoint clients ignore the environment's proxy unless asked.
func TestEnvironmentProxyIsOptIn(t *testing.T) {
	off := NewClient(Policy{}).Transport.(cappedTransport).base.(*http.Transport)
	on := NewClient(Policy{EnvProxy: true}).Transport.(cappedTransport).base.(*http.Transport)
	if off.Proxy != nil || on.Proxy == nil {
		t.Fatal("Proxy is nil for endpoint readers and the environment's for AI")
	}
}

// C5 §5: every form of the credential leaves text about the request.
func TestRedactor(t *testing.T) {
	r := NewRedactor(Bearer("tok-ABCDEF123"), Basic("klyx", "pa55word!"))
	basic := "Basic a2x5eDpwYTU1d29yZCE="
	in := `invalid credential: Bearer tok-ABCDEF123; raw tok-ABCDEF123; ` + basic + `; enc a2x5eDpwYTU1d29yZCE=; pw pa55word!`
	out := r.Text(in)
	for _, leak := range []string{"tok-ABCDEF123", "a2x5eDpwYTU1d29yZCE=", "pa55word!"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked: %s", leak, out)
		}
	}
	if !strings.Contains(out, "invalid credential:") {
		t.Fatalf("the rest of the text stays: %s", out)
	}
	if got := r.Exact(`{"error":"tok-ABCDEF123"}`); got != `{"error":"[redacted]"}` {
		t.Fatalf("exact redaction of a body: %s", got)
	}
	// general shapes are Text's, not Exact's (a body must not be altered)
	if got := NewRedactor().Exact("password=hunter2"); got != "password=hunter2" {
		t.Fatalf("Exact touched a non-secret: %s", got)
	}
	err := r.Err(fmt.Errorf("query: %w (tok-ABCDEF123)", context.DeadlineExceeded))
	if strings.Contains(err.Error(), "tok-ABCDEF123") || !errors.Is(err, context.DeadlineExceeded) || errors.Unwrap(err) != nil {
		t.Fatalf("Err: %v", err)
	}
}

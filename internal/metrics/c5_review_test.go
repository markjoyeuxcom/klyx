package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/safehttp"
	"k8s.io/client-go/rest"
)

// Codex's C5 implementation review, its probes adopted.

func TestRemoveStopsAnAuthenticatedRedirectHop(t *testing.T) {
	reg := endpointcred.NewRegistry()
	entered, release := make(chan struct{}), make(chan struct{})
	var after atomic.Int32
	var afterAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/after" {
			after.Add(1)
			afterAuth.Store(r.Header.Get("Authorization"))
			_, _ = io.WriteString(w, `{}`)
			return
		}
		close(entered)
		<-release
		http.Redirect(w, r, "/after", http.StatusFound)
	}))
	defer srv.Close()
	auth := BearerAuth("C5-REVOKED-TOKEN")
	auth.Live = reg.Live("prd", config.EndpointMetrics, reg.Revision("prd", config.EndpointMetrics))
	q := NewDirectTransportAuth(srv.URL, auth, safehttp.NewClient(DirectPolicy(3*time.Second, false)))
	done := make(chan error, 1)
	go func() { _, _, err := q.q.InstantQuery(context.Background(), "up"); done <- err }()
	<-entered
	reg.Revoke("prd", config.EndpointMetrics)
	close(release)
	err := <-done
	if n := after.Load(); n != 0 {
		t.Fatalf("revoked transport followed a new authenticated redirect: hits=%d auth=%v error=%v", n, afterAuth.Load(), err)
	}
}

func TestDecodedEchoOfTheCredentialDoesNotLeak(t *testing.T) {
	token := "c5/SECRET-NEEDLE"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"error","error":"rejected c5\u002fSECRET-NEEDLE"}`)
	}))
	defer srv.Close()
	q := NewDirectTransport(srv.URL, token, safehttp.NewClient(DirectPolicy(3*time.Second, false)))
	capb := Probe(context.Background(), Resolution{Transport: q})
	if strings.Contains(capb.Reason, token) {
		t.Fatalf("decoded response leaked the active credential: %s", capb.Reason)
	}
}

func TestProxiedNon2xxBodyIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":"`+strings.Repeat("x", maxResponseBytes+1)+`"}`)
	}))
	defer srv.Close()
	rc, err := NewGuardedProxyREST(&rest.Config{Host: srv.URL, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	q := NewProxyTransport(rc, ServiceCandidate{Namespace: "n", Name: "prom", Port: "9090"})
	code, _, err := q.q.InstantQuery(context.Background(), "up")
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("oversized non-2xx body was not rejected by cap: code=%d err=%v", code, err)
	}
}

// A response that completes after a revocation is dropped, not returned.
func TestResponseCompletingAfterARevocationIsDropped(t *testing.T) {
	reg := endpointcred.NewRegistry()
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
	}))
	defer srv.Close()
	auth := BearerAuth("tok")
	auth.Live = reg.Live("prd", config.EndpointMetrics, reg.Revision("prd", config.EndpointMetrics))
	q := NewDirectTransportAuth(srv.URL, auth, safehttp.NewClient(DirectPolicy(3*time.Second, false)))
	done := make(chan error, 1)
	go func() { _, _, err := q.q.InstantQuery(context.Background(), "up"); done <- err }()
	<-entered
	reg.Revoke("prd", config.EndpointMetrics)
	close(release)
	if err := <-done; !errors.Is(err, ErrCredentialChanged) {
		t.Fatalf("the late response is dropped: %v", err)
	}
}

// Revision 1's review, finding 1: an opaque credential equal to a protocol
// key, the success status, a result type or a value changes nothing about
// the response; one equal to a label value or a measurement leaves the data
// as sent. Only what is said about the read loses it.
func TestACredentialNeverRewritesTheResponse(t *testing.T) {
	scalar := `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("query"), "vector(1)") {
			_, _ = io.WriteString(w, scalar)
			return
		}
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"api-7","namespace":"team"},"value":[0,"42"]}]}}`)
	}))
	defer srv.Close()
	for _, token := range []string{"status", "success", "resultType", "1", "api-7", "pod", "42"} {
		q := NewDirectTransport(srv.URL, token, safehttp.NewClient(DirectPolicy(3*time.Second, false)))
		if capb := Probe(context.Background(), Resolution{Transport: q}); !capb.Available {
			t.Fatalf("%q: a valid response read unavailable: %+v", token, capb)
		}
		got, err := NewClient(q, Scope{}).InstantVector(context.Background(), raw("up"))
		if err != nil || len(got) != 1 || got[0].Labels["pod"] != "api-7" || got[0].Labels["namespace"] != "team" || got[0].Value != 42 {
			t.Fatalf("%q: the data changed: %+v %v", token, got, err)
		}
	}
}

// An error that quotes a decoded string with %q or strconv escapes it; the
// credential's quoted spelling is redacted too.
func TestAQuotedEchoOfTheCredentialDoesNotLeak(t *testing.T) {
	token := `c5"QUOTED\SECRET`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"c5\"QUOTED\\SECRET"]}}`)
	}))
	defer srv.Close()
	q := NewDirectTransport(srv.URL, token, safehttp.NewClient(DirectPolicy(3*time.Second, false)))
	capb := Probe(context.Background(), Resolution{Transport: q})
	if capb.Available || strings.Contains(capb.Reason, "QUOTED") {
		t.Fatalf("a quoted echo leaked: %+v", capb)
	}
}

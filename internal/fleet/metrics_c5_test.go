package fleet

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	typedfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/secretstore"
)

// Coverage C5 §4: a connection with a rest.Config reads a serviceRef through
// the guarded proxy client - a backend's redirect to another origin is
// refused before the kube credential could follow it, and the metrics read
// unavailable with the reason.
func TestMetricsServiceProxyUsesTheGuardedClient(t *testing.T) {
	var hits atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
	}))
	defer external.Close()
	redirect := strings.Replace(external.URL, "127.0.0.1", "localhost", 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, redirect, http.StatusFound) }))
	defer api.Close()
	c := NewClusterConn("x", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{},
		config.MetricsConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "monitoring", Name: "prometheus", Port: "9090"}})
	c.WithRESTConfig(&rest.Config{Host: api.URL, BearerToken: "C5_SYNTHETIC_KUBE_TOKEN", Timeout: 3 * time.Second})
	_, capb := c.ClusterMetrics(context.Background(), true)
	if capb.Available || !strings.Contains(capb.Reason, "another origin") {
		t.Fatalf("the redirect is refused and said: %+v", capb)
	}
	if hits.Load() != 0 {
		t.Fatal("the other origin received a request")
	}
}

// promRecorder is a Prometheus that answers every instant query with 1 and
// records each request's Authorization and tenant; block, when set, holds
// a request until released.
type promRecorder struct {
	mu      sync.Mutex
	auths   []string
	tenants []string
	block   chan struct{}
	entered chan struct{}
}

func (p *promRecorder) handler(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.auths = append(p.auths, r.Header.Get("Authorization"))
	p.tenants = append(p.tenants, r.Header.Get("X-Scope-OrgID"))
	block, entered := p.block, p.entered
	p.mu.Unlock()
	if block != nil {
		entered <- struct{}{}
		<-block
	}
	_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
}

func (p *promRecorder) last() (string, string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.auths) == 0 {
		return "", "", 0
	}
	return p.auths[len(p.auths)-1], p.tenants[len(p.tenants)-1], len(p.auths)
}

func storeRecord(t *testing.T, s *secretstore.Memory, cluster, origin, typ, secret string) {
	t.Helper()
	v, _ := endpointcred.Record{V: 1, Origin: origin, Type: typ, Secret: secret}.Encode()
	if err := s.Set(endpointcred.Account(cluster, config.EndpointMetrics), v); err != nil {
		t.Fatal(err)
	}
}

func credConn(t *testing.T, mc config.MetricsConfig, s *secretstore.Memory) (*ClusterConn, *endpointcred.Registry) {
	reg := endpointcred.NewRegistry()
	c := NewClusterConn("prd", typedfake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, mc)
	c.WithCredentials(Credentials{Store: s, Revisions: reg})
	return c, reg
}

// Coverage C5 §7: on an endpoint that is already available and cached, a
// Set changes what the next request sends, and a Remove stops even a client
// a reader already holds.
func TestCredentialRevisionReachesCachedClients(t *testing.T) {
	p := &promRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	defer srv.Close()
	origin, _ := safehttp.OriginOf(srv.URL)
	s := &secretstore.Memory{}
	storeRecord(t, s, "prd", origin, "bearer", "tok-A")
	c, reg := credConn(t, config.MetricsConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}, Tenant: "platform"}, s)

	if _, capb := c.ClusterMetrics(context.Background(), false); !capb.Available {
		t.Fatalf("available: %+v", capb)
	}
	if a, ten, _ := p.last(); a != "Bearer tok-A" || ten != "platform" {
		t.Fatalf("first: %q %q", a, ten)
	}
	// Set: the keychain changes and the revision moves; the cached,
	// available client is rebuilt
	storeRecord(t, s, "prd", origin, "bearer", "tok-B")
	reg.Bump("prd", config.EndpointMetrics, true)
	if _, capb := c.ClusterMetrics(context.Background(), false); !capb.Available {
		t.Fatalf("after Set: %+v", capb)
	}
	if a, _, _ := p.last(); a != "Bearer tok-B" {
		t.Fatalf("after Set the next request carries the new token: %q", a)
	}
	// a reader holds the transport; Remove revokes before anything else
	c.metricsMu.Lock()
	held := c.metricsState.client
	c.metricsMu.Unlock()
	reg.Revoke("prd", config.EndpointMetrics)
	_, _, before := p.last()
	if _, err := held.InstantScalar(context.Background(), metrics.ClusterCPU.With(metrics.None{})); !errors.Is(err, metrics.ErrCredentialChanged) {
		t.Fatalf("a held client stops: %v", err)
	}
	_, capb := c.ClusterMetrics(context.Background(), false)
	if capb.Available || !strings.Contains(capb.Reason, "removed from use") {
		t.Fatalf("after Remove: %+v", capb)
	}
	if _, _, after := p.last(); after != before {
		t.Fatal("a request was sent after the Remove")
	}
}

// A read started under an older revision is neither cached nor shown as the
// endpoint's availability.
func TestInFlightReadUnderAnOldRevisionIsDropped(t *testing.T) {
	p := &promRecorder{block: make(chan struct{}), entered: make(chan struct{}, 1)}
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	defer srv.Close()
	origin, _ := safehttp.OriginOf(srv.URL)
	s := &secretstore.Memory{}
	storeRecord(t, s, "prd", origin, "bearer", "tok-A")
	c, reg := credConn(t, config.MetricsConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}}, s)
	done := make(chan metrics.MetricsCapability, 1)
	go func() { _, capb := c.ClusterMetrics(context.Background(), false); done <- capb }()
	<-p.entered
	reg.Bump("prd", config.EndpointMetrics, true) // a Set lands while the probe is in flight
	p.mu.Lock()
	release := p.block
	p.block = nil
	p.mu.Unlock()
	close(release)
	capb := <-done
	if capb.Available || !strings.Contains(capb.Reason, "changed during the read") {
		t.Fatalf("the old read is not shown as available: %+v", capb)
	}
	c.metricsMu.Lock()
	cached := c.metricsState.capSet
	c.metricsMu.Unlock()
	if cached {
		t.Fatal("the old read was cached")
	}
}

// A credential stored for another origin sends nothing; the legacy token
// wins over a keychain copy; basic auth is carried.
func TestCredentialBindingAndPrecedence(t *testing.T) {
	p := &promRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	defer srv.Close()
	origin, _ := safehttp.OriginOf(srv.URL)

	s := &secretstore.Memory{}
	storeRecord(t, s, "prd", "https://elsewhere.example:443", "bearer", "tok-X")
	c, _ := credConn(t, config.MetricsConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}}, s)
	if _, capb := c.ClusterMetrics(context.Background(), false); capb.Available || !strings.Contains(capb.Reason, "stored credential is for https://elsewhere.example:443") {
		t.Fatalf("another origin: %+v", capb)
	}
	if _, _, n := p.last(); n != 0 {
		t.Fatal("a request was sent without the credential the endpoint needs")
	}

	storeRecord(t, s, "prd", origin, "bearer", "kc-tok")
	c, _ = credConn(t, config.MetricsConfig{Endpoint: srv.URL, Token: "file-tok"}, s)
	if _, capb := c.ClusterMetrics(context.Background(), false); !capb.Available {
		t.Fatalf("legacy: %+v", capb)
	}
	if a, _, _ := p.last(); a != "Bearer file-tok" {
		t.Fatalf("the file's token wins while both exist: %q", a)
	}

	storeRecord(t, s, "prd", origin, "basic", "pa55")
	c, _ = credConn(t, config.MetricsConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "basic", Username: "klyx"}}, s)
	if _, capb := c.ClusterMetrics(context.Background(), false); !capb.Available {
		t.Fatalf("basic: %+v", capb)
	}
	if a, _, _ := p.last(); a != "Basic a2x5eDpwYTU1" {
		t.Fatalf("basic header: %q", a)
	}
}

package fleet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/observe"
)

type failingDiscovery struct{}

func (failingDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	return nil, errors.New("the server is currently unable to handle the request")
}
func (failingDiscovery) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return nil, errors.New("unreachable")
}

func probeConn(disc discoveryLike) *ClusterConn {
	c := NewClusterConn("x", fake.NewSimpleClientset(), nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	c.disc = newCachedDiscovery(disc, nil)
	return c
}

// The health Observer's per-kind probe (1c addendum §3.3).
func TestCRDServedProbe(t *testing.T) {
	typed := fake.NewSimpleClientset()
	typed.Resources = fluxSplitDiscovery()
	complete := probeConn(typed.Discovery())
	ctx := context.Background()
	for _, tc := range []struct {
		name, group, resource, fallback string
		want                            observe.Probe
	}{
		{"served at the preferred version", "kustomize.toolkit.fluxcd.io", "kustomizations", "v1", observe.Probe{Served: true, Version: "v1"}},
		{"served only at a non-preferred version", "source.toolkit.fluxcd.io", "ocirepositories", "v1", observe.Probe{Served: true, Version: "v1beta2"}},
		{"complete discovery without the resource is absence", "source.toolkit.fluxcd.io", "buckets", "v1", observe.Probe{Served: false}},
		{"a group missing from a successful discovery is absence, never the fallback", "cert-manager.io", "certificates", "v1", observe.Probe{Served: false}},
	} {
		got, err := complete.crdServed(ctx, tc.group, tc.resource, tc.fallback)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}

	// One unreadable version is uncertainty, never absence: the fallback
	// that could not be ruled out is tried.
	partial := probeConn(partialDiscovery{typed.Discovery()})
	if got, err := partial.crdServed(ctx, "source.toolkit.fluxcd.io", "buckets", "v1beta2"); err != nil || got != (observe.Probe{Served: true, Version: "v1beta2"}) {
		t.Errorf("partial discovery: got %+v, %v", got, err)
	}

	// A discovery error confirms nothing: it is an error, not absence.
	if _, err := probeConn(failingDiscovery{}).crdServed(ctx, "cert-manager.io", "certificates", "v1"); err == nil {
		t.Error("a discovery failure must be reported, never read as absence")
	}
}

// hangingAPI is a real HTTP API server whose /apis lists cert-manager.io at
// three versions and whose version reads hang until the request is
// cancelled. It counts requests in flight and the most ever at once.
type hangingAPI struct {
	srv      *httptest.Server
	mu       sync.Mutex
	inflight int
	max      int
	hangAll  bool // /apis hangs too
}

func newHangingAPI(t *testing.T, hangAll bool) *hangingAPI {
	t.Helper()
	h := &hangingAPI{hangAll: hangAll}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis" && !h.hangAll {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"kind":"APIGroupList","apiVersion":"v1","groups":[{"name":"cert-manager.io",
				"versions":[{"groupVersion":"cert-manager.io/v1","version":"v1"},{"groupVersion":"cert-manager.io/v1beta1","version":"v1beta1"},{"groupVersion":"cert-manager.io/v1alpha2","version":"v1alpha2"}],
				"preferredVersion":{"groupVersion":"cert-manager.io/v1","version":"v1"}}]}`))
			return
		}
		h.mu.Lock()
		h.inflight++
		h.max = max(h.max, h.inflight)
		h.mu.Unlock()
		defer func() { h.mu.Lock(); h.inflight--; h.mu.Unlock() }()
		<-r.Context().Done() // hang until the client cancels the request
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hangingAPI) conn(t *testing.T) *ClusterConn {
	t.Helper()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: h.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return NewClusterConn("x", cs, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
}

func (h *hangingAPI) drained(t *testing.T) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		h.mu.Lock()
		n := h.inflight
		h.mu.Unlock()
		if n == 0 {
			return
		}
	}
	t.Fatal("discovery requests survived their probe")
}

func withProbeTimeout(t *testing.T, d time.Duration) {
	old := crdProbeTimeout
	crdProbeTimeout = d
	t.Cleanup(func() { crdProbeTimeout = old })
}

// Codex 2d review: one deadline spans every request of a probe, it reaches
// the HTTP transport, and nothing survives the probe or lands in the cache.
func TestCRDServedDeadlineSpansEveryRequestAtTheTransport(t *testing.T) {
	withProbeTimeout(t, 150*time.Millisecond)
	h := newHangingAPI(t, false)
	c := h.conn(t)
	start := time.Now()
	_, err := c.crdServed(context.Background(), "cert-manager.io", "certificates", "v1")
	took := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a deadline mid-probe is an error that confirms nothing, never a version guess: %v", err)
	}
	if took > time.Second {
		t.Fatalf("three hanging versions took %v: the deadline must span them all", took)
	}
	h.drained(t)
	cd := c.probeDiscovery()
	cd.mu.Lock()
	cached := len(cd.resources)
	cd.mu.Unlock()
	if cached != 0 {
		t.Fatalf("an abandoned read was cached: %d entries", cached)
	}
}

func TestCRDServedCancellationAndConcurrentRetriesLeaveNoWork(t *testing.T) {
	withProbeTimeout(t, time.Hour)
	h := newHangingAPI(t, true) // /apis itself hangs
	c := h.conn(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ { // retries across a fleet's probes, at once
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.crdServed(ctx, "cert-manager.io", "certificates", "v1")
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond)
	cancel() // teardown
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("probes outlived their context")
	}
	close(errs)
	for err := range errs {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled probe says so: %v", err)
		}
	}
	h.drained(t)
	if h.max > 8 {
		t.Fatalf("more requests than probes: %d", h.max)
	}
}

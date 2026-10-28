package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/metrics"
)

// Codex's review of S3b-2b: a read's terms are checked at every round
// trip, a redirect's hop included (R1); a hosted installation's discovery
// is a hosted read, under the lease, in the host's slots and fenced (R2);
// and a denial with a broken body moves the access, so nothing cached
// under the old terms is served (R3).

func TestARedirectAfterAMoveIsNotSent(t *testing.T) {
	for _, move := range []bool{false, true} {
		var rig *srcRig
		var followed atomic.Int32
		var src config.SourceConfig
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
			switch {
			case q.URL.Path == "/follow":
				followed.Add(1)
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
			case strings.HasSuffix(q.URL.Path, "query_range"):
				if move {
					next := src
					next.Tenant = "new-tenant"
					rig.apply([]config.SourceConfig{next}, rig.clusters)
				}
				http.Redirect(w, q, "/follow?"+q.URL.RawQuery, http.StatusFound)
			default:
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":[1,"1"]}]}}`))
			}
		}))
		src = config.SourceConfig{ID: "mets", Kind: config.SourceKindMetrics, Endpoint: api.URL, Tenant: "old-tenant", MemberLabel: "cluster", Serves: []config.ServeConfig{{Cluster: "dev"}}}
		rig = newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"})
		_, err := rig.conns["dev"].ChatMetricsRange(context.Background(), metrics.ClusterCPU.With(metrics.None{}), time.Unix(0, 0), time.Unix(60, 0), time.Minute)
		api.Close()
		if move && (err == nil || followed.Load() != 0) {
			t.Fatalf("a redirect's hop was sent after the terms moved: %d, %v", followed.Load(), err)
		}
		if !move && followed.Load() != 1 {
			t.Fatalf("control: the redirect was not followed: %d, %v", followed.Load(), err)
		}
	}
}

// discoveryRig is a discovering installation hosted by prd, whose Service
// reads reach a stand-in API server.
func discoveryRig(t *testing.T, onRead func()) (*srcRig, *atomic.Int32) {
	t.Helper()
	src := metricsInstallation()
	src.ServiceRef, src.Discover = nil, true
	r := newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"})
	hits := new(atomic.Int32)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		hits.Add(1)
		if onRead != nil {
			onRead()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Service","metadata":{"name":"prometheus-operated","namespace":"monitoring"},"spec":{"ports":[{"port":9090}]}}`))
	}))
	t.Cleanup(api.Close)
	typed, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	r.host.typed = typed
	return r, hits
}

func TestAHostedDiscoveryIsAHostedRead(t *testing.T) {
	// the control: it reads and is cached
	r, hits := discoveryRig(t, nil)
	sub, _ := r.sources.SubscriptionOf(config.EndpointMetrics, "dev")
	_, _, release, err := r.b.metricsSource(context.Background(), r.b.termsOf(sub), "a test")
	if err != nil || hits.Load() != 1 {
		t.Fatalf("control: %d, %v", hits.Load(), err)
	}
	release()

	// a lease ended before discovery: nothing is read on the retired host
	r, hits = discoveryRig(t, nil)
	sub, _ = r.sources.SubscriptionOf(config.EndpointMetrics, "dev")
	terms := r.b.termsOf(sub)
	old := r.host
	life, end := context.WithCancel(context.Background())
	r.b.leaser = func(string, string) (Conn, context.Context, func(), error) {
		r.reconnect()
		end()
		return old, life, func() {}, nil
	}
	if _, _, _, err := r.b.metricsSource(context.Background(), terms, "a test"); err == nil || hits.Load() != 0 {
		t.Fatalf("discovery on an ended lease: %d, %v", hits.Load(), err)
	}

	// every host slot held: discovery waits within the read's context, and
	// sends nothing; what it did not finish is not cached
	r, hits = discoveryRig(t, nil)
	sub, _ = r.sources.SubscriptionOf(config.EndpointMetrics, "dev")
	var held []func()
	for range hostMetricsSlots {
		rel, err := r.b.slots.take(context.Background(), config.EndpointMetrics, "prd")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, rel)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, _, _, err = r.b.metricsSource(ctx, r.b.termsOf(sub), "a test")
	cancel()
	for _, rel := range held {
		rel()
	}
	if err == nil || hits.Load() != 0 {
		t.Fatalf("discovery with no slot: %d, %v", hits.Load(), err)
	}
	if _, _, rel, err := r.b.metricsSource(context.Background(), r.b.termsOf(sub), "a test"); err != nil || hits.Load() != 1 {
		t.Fatalf("an unfinished discovery was cached: %d, %v", hits.Load(), err)
	} else {
		rel()
	}

	// the lease ending during a Service read cancels it
	var r3 *srcRig
	life3, end3 := context.WithCancel(context.Background())
	defer end3()
	r3, _ = discoveryRig(t, func() {
		end3()
		time.Sleep(2 * time.Second) // the read is cancelled long before
	})
	r3.b.leaser = func(string, string) (Conn, context.Context, func(), error) { return r3.host, life3, func() {}, nil }
	sub, _ = r3.sources.SubscriptionOf(config.EndpointMetrics, "dev")
	start := time.Now()
	if _, _, _, err := r3.b.metricsSource(context.Background(), r3.b.termsOf(sub), "a test"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("a lease ending during discovery: %v after %v", err, time.Since(start))
	}

	// the terms moving during a Service read: discovery stops there, with
	// no fallback, and nothing is cached
	var r2 *srcRig
	r2, hits = discoveryRig(t, func() { r2.sources.Denied("lgtm-we-metrics") })
	sub, _ = r2.sources.SubscriptionOf(config.EndpointMetrics, "dev")
	if _, _, _, err := r2.b.metricsSource(context.Background(), r2.b.termsOf(sub), "a test"); !termsMoved(err) || hits.Load() != 1 {
		t.Fatalf("a move during discovery: %d, %v", hits.Load(), err)
	}
	r2.b.mu.Lock()
	cached := len(r2.b.mdisc)
	r2.b.mu.Unlock()
	if cached != 0 {
		t.Fatal("a discovery across a move was cached")
	}
}

// A truncated 403 is a denial: the access moves, and the metrics read
// before it are no longer served as current.
func TestABrokenDenialMovesTheAccess(t *testing.T) {
	f := &obsFake{}
	var deny atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if deny.Load() {
			w.Header().Set("Content-Length", "32")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("x"))
			return
		}
		f.ServeHTTP(w, q)
	}))
	defer api.Close()
	src := config.SourceConfig{ID: "mets", Kind: config.SourceKindMetrics, Endpoint: api.URL, MemberLabel: "cluster", Serves: []config.ServeConfig{{Cluster: "dev"}}}
	r := newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"})
	dev := r.conns["dev"]
	if old, capb := dev.ClusterMetrics(context.Background(), false); !capb.Available || old.CPUFraction == nil {
		t.Fatalf("fixture: %+v %+v", old, capb)
	}
	deny.Store(true)
	if _, err := dev.ChatMetricsRange(context.Background(), metrics.ClusterCPU.With(metrics.None{}), time.Unix(0, 0), time.Unix(60, 0), time.Minute); err == nil {
		t.Fatal("a truncated denial was read")
	}
	cached, after := dev.ClusterMetrics(context.Background(), false)
	// each denied request moves the access once; the re-probe is denied too
	if r.sources.Denials("mets") == 0 || after.Available || cached.CPUFraction != nil {
		t.Fatalf("denials %d, available %v, cached CPU %v", r.sources.Denials("mets"), after.Available, cached.CPUFraction != nil)
	}
}

// A subscribed probe and its samples settle while their host lease is
// held (§13): a move made as the lease is released is the next read's,
// and the result read before it stands.
func TestAReadSettlesBeforeItsLeaseIsReleased(t *testing.T) {
	for _, at := range []int{1, 2} { // the probe's lease, the samples'
		r := newSrcRig(t, []config.SourceConfig{metricsInstallation()}, config.ClusterConfig{Name: "dev", Context: "dev"})
		releases := 0
		r.b.leaser = func(host, what string) (Conn, context.Context, func(), error) {
			return r.host, context.Background(), func() {
				if releases++; releases == at {
					r.sources.Denied("lgtm-we-metrics")
				}
			}, nil
		}
		dev := r.conns["dev"]
		if at == 1 {
			dev.metricsMu.Lock()
			dev.ensureMetricsLocked(context.Background(), false, time.Now())
			st := dev.metricsState
			dev.metricsMu.Unlock()
			if !st.capSet || !st.cap.Available || releases != 1 {
				t.Fatalf("the probe settled after its lease: %+v, %d releases", st.cap, releases)
			}
			continue
		}
		if cm, capb := dev.ClusterMetrics(context.Background(), false); !capb.Available || cm.CPUFraction == nil || releases != 2 {
			t.Fatalf("the samples settled after their lease: %+v %+v, %d releases", cm, capb, releases)
		}
	}
}

// The terms moving before discovery starts: no Service is read.
func TestADiscoveryUnderMovedTermsReadsNothing(t *testing.T) {
	r, hits := discoveryRig(t, nil)
	sub, _ := r.sources.SubscriptionOf(config.EndpointMetrics, "dev")
	terms := r.b.termsOf(sub)
	r.sources.Denied("lgtm-we-metrics")
	if _, _, _, err := r.b.metricsSource(context.Background(), terms, "a test"); !termsMoved(err) || hits.Load() != 0 {
		t.Fatalf("%d reads: %v", hits.Load(), err)
	}
}

// A discovery whose last read is cancelled by the lease's end is an ended
// discovery, never "no Prometheus" cached for the unavailable TTL.
func TestADiscoveryCancelledInItsLastReadIsNotCached(t *testing.T) {
	src := metricsInstallation()
	src.ServiceRef, src.Discover = nil, true
	r := newSrcRig(t, []config.SourceConfig{src}, config.ClusterConfig{Name: "dev", Context: "dev"})
	life, end := context.WithCancel(context.Background())
	defer end()
	var lists atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if q.URL.Query().Get("labelSelector") == "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"NotFound","code":404}`))
			return
		}
		if lists.Add(1) == int32(len(labelSelectors)) {
			end() // the lease ends during the last read
			<-q.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{"kind":"ServiceList","apiVersion":"v1","items":[]}`))
	}))
	defer api.Close()
	typed, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	r.host.typed = typed
	r.b.leaser = func(string, string) (Conn, context.Context, func(), error) { return r.host, life, func() {}, nil }
	sub, _ := r.sources.SubscriptionOf(config.EndpointMetrics, "dev")
	if _, _, _, err := r.b.metricsSource(context.Background(), r.b.termsOf(sub), "a test"); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Fatalf("a cancelled discovery: %v", err)
	}
	r.b.mu.Lock()
	cached := len(r.b.mdisc)
	r.b.mu.Unlock()
	if cached != 0 {
		t.Fatal("a cancelled discovery was cached as no Prometheus")
	}
}

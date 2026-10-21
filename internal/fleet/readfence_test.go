package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/readfence"
	"github.com/moomora/klyx/internal/safehttp"
)

// AI A1 implementation review, finding 3: a drawing is a composite read -
// the Gateway, then its routes, classes, grants and policies - and its
// fence is checked before each component, not once before it starts. A
// fence that changes once the Gateway is read stops every later read
// before it reaches the API server.
func TestACompositeReadStopsAtItsFence(t *testing.T) {
	var mu sync.Mutex
	gatewayRead, after := false, 0
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api":
			_, _ = w.Write([]byte(`{"kind":"APIVersions","versions":["v1"]}`))
			return
		case "/apis":
			_, _ = w.Write([]byte(`{"kind":"APIGroupList","groups":[{"name":"gateway.networking.k8s.io","versions":[{"groupVersion":"gateway.networking.k8s.io/v1","version":"v1"}],"preferredVersion":{"groupVersion":"gateway.networking.k8s.io/v1","version":"v1"}}]}`))
			return
		case "/apis/gateway.networking.k8s.io/v1":
			var rs []string
			for _, p := range []string{"gateways", "httproutes", "gatewayclasses", "referencegrants", "grpcroutes"} {
				rs = append(rs, `{"name":"`+p+`","kind":"X","namespaced":true,"verbs":["get","list"]}`)
			}
			_, _ = w.Write([]byte(`{"kind":"APIResourceList","groupVersion":"gateway.networking.k8s.io/v1","resources":[` + strings.Join(rs, ",") + `]}`))
			return
		case "/apis/gateway.networking.k8s.io/v1/namespaces/infra/gateways/web":
			gatewayRead = true
			b, _ := json.Marshal(gw("web", "infra").Object)
			_, _ = w.Write(b)
			return
		}
		if gatewayRead {
			after++
			paths = append(paths, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"kind":"List","apiVersion":"v1","metadata":{},"items":[]}`))
	}))
	defer srv.Close()
	conn := func() *ClusterConn {
		rc := &rest.Config{Host: srv.URL}
		rc.Wrap(readfence.Transport) // as cluster.RESTConfig does
		typed, err := kubernetes.NewForConfig(rc)
		if err != nil {
			t.Fatal(err)
		}
		dyn, err := dynamic.NewForConfig(rc)
		if err != nil {
			t.Fatal(err)
		}
		return NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	}
	changed := errors.New("the fence changed")
	ctx := readfence.With(context.Background(), func() error {
		mu.Lock()
		defer mu.Unlock()
		if gatewayRead {
			return changed
		}
		return nil
	})
	topo, err := conn().GetGatewayTopology(ctx, "infra", "web")
	mu.Lock()
	stopped := after
	mu.Unlock()
	if err != nil || topo.Gateway.Name != "web" || stopped != 0 {
		t.Fatalf("the Gateway is read and nothing after it: %v %d %v", err, stopped, paths)
	}
	// the control: without a fence the drawing reads on
	mu.Lock()
	gatewayRead, after = false, 0
	mu.Unlock()
	if _, err := conn().GetGatewayTopology(context.Background(), "infra", "web"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if after == 0 {
		t.Fatal("the control: the drawing makes later component reads")
	}
}

// The issuance chain is a composite: the Certificate, its issuer, then the
// version resolved for its requests and their lists. A fence that changes
// once the Certificate is read stops the rest, discovery included.
func TestAChainStopsAtItsFence(t *testing.T) {
	var mu sync.Mutex
	certRead, after := false, 0
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if certRead {
			after++
			paths = append(paths, r.URL.Path)
		}
		switch r.URL.Path {
		case "/api":
			_, _ = w.Write([]byte(`{"kind":"APIVersions","versions":["v1"]}`))
		case "/apis":
			_, _ = w.Write([]byte(`{"kind":"APIGroupList","groups":[{"name":"cert-manager.io","versions":[{"groupVersion":"cert-manager.io/v1","version":"v1"}],"preferredVersion":{"groupVersion":"cert-manager.io/v1","version":"v1"}}]}`))
		case "/apis/cert-manager.io/v1":
			_, _ = w.Write([]byte(`{"kind":"APIResourceList","groupVersion":"cert-manager.io/v1","resources":[{"name":"certificates","kind":"Certificate","namespaced":true,"verbs":["get","list"]},{"name":"certificaterequests","kind":"CertificateRequest","namespaced":true,"verbs":["get","list"]}]}`))
		case "/apis/cert-manager.io/v1/namespaces/shop/certificates/web":
			certRead = true
			_, _ = w.Write([]byte(`{"apiVersion":"cert-manager.io/v1","kind":"Certificate","metadata":{"name":"web","namespace":"shop","uid":"u"},"spec":{"secretName":"web-tls"}}`))
		default:
			_, _ = w.Write([]byte(`{"kind":"List","apiVersion":"v1","metadata":{},"items":[]}`))
		}
	}))
	defer srv.Close()
	rc := &rest.Config{Host: srv.URL}
	rc.Wrap(readfence.Transport)
	typed, _ := kubernetes.NewForConfig(rc)
	dyn, _ := dynamic.NewForConfig(rc)
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	changed := errors.New("the fence changed")
	ctx := readfence.With(context.Background(), func() error {
		mu.Lock()
		defer mu.Unlock()
		if certRead {
			return changed
		}
		return nil
	})
	chain, err := c.GetCertificateChain(ctx, "shop", "web")
	mu.Lock()
	defer mu.Unlock()
	if err != nil || chain.Certificate.Name != "web" || after != 0 {
		t.Fatalf("the Certificate is read and nothing after it: %v %d %v", err, after, paths)
	}
}

// A composite's discovery is bound to its context: a cached answer is
// refused once the fence changes, as a network read is.
func TestBoundDiscoveryRefusesOnceTheFenceChanges(t *testing.T) {
	c := probeConn(gwDiscovery([2]string{"v1", "gateways"}))
	if _, err := c.gwGVR(context.Background(), "gateways"); err != nil {
		t.Fatal(err) // warms the cache
	}
	changed := errors.New("the fence changed")
	ctx := readfence.With(context.Background(), func() error { return changed })
	if _, err := c.discoveryFor(ctx).ServerResourcesForGroupVersion("gateway.networking.k8s.io/v1"); !errors.Is(err, changed) {
		t.Fatalf("a cached answer is refused: %v", err)
	}
	if _, err := c.discoveryFor(ctx).ServerGroups(); !errors.Is(err, changed) {
		t.Fatalf("%v", err)
	}
}

// A Loki page is a composite too - its labels, its query, a re-read by
// UID - and a direct endpoint's client checks the fence before each
// request: one that changes after the labels read sends no query.
func TestALokiPageStopsAtItsFence(t *testing.T) {
	f := &fakeLoki{labels: `{"status":"success","data":["k8s_namespace_name"]}`, query: lokiStreams}
	srv := httptest.NewServer(f)
	defer srv.Close()
	e := newLokiEnv(t, &config.EndpointConfig{Endpoint: srv.URL, Auth: &config.EndpointAuth{Type: "bearer"}})
	origin, _ := safehttp.OriginOf(srv.URL)
	storeLoki(t, e.store, origin, "LOKI-SECRET")
	r, err := e.c.Loki()
	if err != nil {
		t.Fatal(err)
	}
	changed := errors.New("the fence changed")
	ctx := readfence.With(context.Background(), func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.paths) >= 1 {
			return changed
		}
		return nil
	})
	if _, _, err := r.Labels(ctx, time.Now().Add(-time.Hour), time.Now(), ""); err != nil {
		t.Fatalf("the labels are read: %v", err)
	}
	// (the reader rewords transport errors through its redactor)
	if _, err := r.Query(ctx, "{}", 1, 2, 10); err == nil || !strings.Contains(err.Error(), changed.Error()) {
		t.Fatalf("the query is refused: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.paths) != 1 {
		t.Fatalf("nothing after the labels reached Loki: %v", f.paths)
	}
}

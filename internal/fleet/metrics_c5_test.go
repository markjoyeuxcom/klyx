package fleet

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	typedfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
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

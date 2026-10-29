package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/config"
)

// Coverage S8a §2: metrics.serviceRef.path is the prefix the Service
// serves its Prometheus API under (Mimir's /prometheus); the proxied query
// carries it, and the source names it.
func TestServiceRefPathReachesTheProxy(t *testing.T) {
	var gotPath string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`)
	}))
	defer api.Close()
	rc, err := NewGuardedProxyREST(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	tf := proxyOnly{rc: rc}
	cfg := config.MetricsConfig{ServiceRef: &config.MetricsServiceRef{Namespace: "mimir", Name: "query-frontend", Port: "8080", Path: "/prometheus/"}}
	res := Resolve(cfg, Auth{}, DiscoveryResult{}, tf)
	if res.Source != "mimir/query-frontend:8080/prometheus" {
		t.Fatalf("source %q", res.Source)
	}
	if capb := Probe(context.Background(), res); !capb.Available {
		t.Fatalf("probe: %+v", capb)
	}
	if gotPath != "/api/v1/namespaces/mimir/services/http:query-frontend:8080/proxy/prometheus/api/v1/query" {
		t.Fatalf("path %q", gotPath)
	}
}

type proxyOnly struct{ rc rest.Interface }

func (p proxyOnly) Direct(string, Auth, bool) Transport { return Transport{} }
func (p proxyOnly) Proxy(c ServiceCandidate, tenant string) Transport {
	return NewProxyTransportTenant(p.rc, c, tenant)
}

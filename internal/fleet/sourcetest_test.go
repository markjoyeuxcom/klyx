package fleet

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/safehttp"
)

// Settings S3b §7: a Loki or metrics installation's Test is its
// reachability, read once through the transport its reads take, fenced by
// the revision reviewed; never anything of a member, and an observed 401
// or 403 is said, never counted as a loss of access.

func directOf(kind, endpoint string) config.SourceConfig {
	return config.SourceConfig{ID: "shared-" + kind, Kind: kind, Endpoint: endpoint, Tenant: "ops", MemberLabel: "cluster", Serves: []config.ServeConfig{{Cluster: "dev"}}}
}

type probeRT struct {
	mu   sync.Mutex
	reqs []*http.Request
	code int
	body string
}

func (p *probeRT) client(safehttp.Policy) *http.Client {
	return &http.Client{Transport: f1RT(func(req *http.Request) (*http.Response, error) {
		p.mu.Lock()
		p.reqs = append(p.reqs, req)
		p.mu.Unlock()
		return &http.Response{StatusCode: p.code, Body: io.NopCloser(strings.NewReader(p.body)), Header: http.Header{}, Request: req}, nil
	})}
}

func TestALokiOrMetricsInstallationsTestIsItsReachability(t *testing.T) {
	for _, tc := range []struct {
		kind, endpoint, body, words, path, query string
	}{
		{config.SourceKindLoki, "https://loki.example/loki", `{"version":"3.4.2"}`, "Loki 3.4.2 answered", "/loki/loki/api/v1/status/buildinfo", ""},
		{config.SourceKindMetrics, "https://mimir.example/prometheus", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`,
			"the Prometheus API answered", "/prometheus/api/v1/query", "vector(1)"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			src := directOf(tc.kind, tc.endpoint)
			r := newBucketRig(t, src, "dev")
			reg := lineageRegistry(r)
			rt := &probeRT{code: 200, body: tc.body}
			r.b.client = rt.client
			_, rev, _ := r.sources.Installation(src.ID)
			words, where, err := reg.InstallationProbe(context.Background(), src.ID, rev)
			if err != nil || words != tc.words || !strings.HasPrefix(where, strings.SplitN(tc.endpoint, "/", 4)[0]+"//"+strings.SplitN(tc.endpoint, "/", 4)[2]) {
				t.Fatalf("%q at %q: %v", words, where, err)
			}
			if len(rt.reqs) != 1 || rt.reqs[0].URL.Path != tc.path || rt.reqs[0].URL.Query().Get("query") != tc.query {
				t.Fatalf("requests: %+v", rt.reqs)
			}
			// a denial is said, and the installation's access does not move
			before := r.sources.Denials(src.ID)
			rt.code, rt.body = 401, "no"
			if _, _, err := reg.InstallationProbe(context.Background(), src.ID, rev); err == nil || !strings.Contains(err.Error(), src.ID+" refused Klyx's read (401 or 403)") {
				t.Fatalf("a denial: %v", err)
			}
			if r.sources.Denials(src.ID) != before {
				t.Fatal("a Test's denial was counted as a loss of access")
			}
			if _, _, err := reg.InstallationProbe(context.Background(), src.ID, rev+1); err == nil || !strings.Contains(err.Error(), "changed since it was shown") {
				t.Fatalf("a stale revision: %v", err)
			}
		})
	}
	am := directOf(config.SourceKindAlertmanager, "https://am.example")
	r := newBucketRig(t, am, "dev")
	_, rev, _ := r.sources.Installation(am.ID)
	if _, _, err := lineageRegistry(r).InstallationProbe(context.Background(), am.ID, rev); err == nil || !strings.Contains(err.Error(), "is tested by its status") {
		t.Fatalf("an Alertmanager installation: %v", err)
	}
}

// A Loki or metrics installation that moves before the request sends
// nothing and says so.
func TestALokiOrMetricsTestIsFencedByItsRevision(t *testing.T) {
	for _, kind := range []string{config.SourceKindLoki, config.SourceKindMetrics} {
		t.Run(kind, func(t *testing.T) {
			src := directOf(kind, "https://lgtm.example/before")
			r := newBucketRig(t, src, "dev")
			reg := lineageRegistry(r)
			_, rev, _ := r.sources.Installation(src.ID)
			rt := &probeRT{code: 200, body: `{"version":"3.4.2"}`}
			r.b.client = func(p safehttp.Policy) *http.Client {
				next := src
				next.Endpoint = "https://lgtm.example/after"
				if _, err := r.sources.Apply([]config.SourceConfig{next}, []config.ClusterConfig{{Name: "prd"}, {Name: "dev"}}); err != nil {
					t.Fatal(err)
				}
				return rt.client(p)
			}
			if _, _, err := reg.InstallationProbe(context.Background(), src.ID, rev); err == nil || len(rt.reqs) != 0 || !strings.Contains(err.Error(), "changed during the test") {
				t.Fatalf("a moved installation was read: %d requests: %v", len(rt.reqs), err)
			}
		})
	}
}

// A Loki installation's Test holds one of the fleet's Loki page slots: with
// none free it waits within its deadline, and sends nothing.
func TestALokiTestHoldsAFleetPageSlot(t *testing.T) {
	src := directOf(config.SourceKindLoki, "https://loki.example")
	r := newBucketRig(t, src, "dev")
	reg := lineageRegistry(r)
	rt := &probeRT{code: 200, body: `{"version":"3.4.2"}`}
	r.b.client = rt.client
	_, rev, _ := r.sources.Installation(src.ID)
	for i := 0; i < lokiMaxInFlight; i++ {
		lokiPages <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := reg.InstallationProbe(ctx, src.ID, rev)
	for i := 0; i < lokiMaxInFlight; i++ {
		<-lokiPages
	}
	if err == nil || !strings.Contains(err.Error(), "was not read in time") || len(rt.reqs) != 0 {
		t.Fatalf("%d requests: %v", len(rt.reqs), err)
	}
	if _, _, err := reg.InstallationProbe(context.Background(), src.ID, rev); err != nil || len(rt.reqs) != 1 {
		t.Fatalf("with a slot free: %v", err)
	}
}

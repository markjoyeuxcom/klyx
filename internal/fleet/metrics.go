package fleet

import (
	"context"
	"strconv"
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/safehttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

const (
	cpuQuery = `1 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m]))`
	memQuery = `1 - sum(node_memory_MemAvailable_bytes) / sum(node_memory_MemTotal_bytes)`

	metricsSampleTTL      = 15 * time.Second
	metricsUnavailableTTL = 45 * time.Second
	metricsHTTPTimeout    = 3 * time.Second // UI status line must fail fast
)

// namedCandidates is the ranked named-Service probe list. First existing wins;
// ranking is the deliberate tiebreak (prometheus before mimir).
var namedCandidates = []metrics.ServiceCandidate{
	{Namespace: "monitoring", Name: "prometheus-operated", Port: "9090", Scheme: "http"},
	{Namespace: "monitoring", Name: "kube-prometheus-stack-prometheus", Port: "9090", Scheme: "http"},
	{Namespace: "monitoring", Name: "prometheus-server", Port: "80", Scheme: "http"},
	{Namespace: "monitoring", Name: "mimir-query-frontend", Port: "8080", Scheme: "http"},
	{Namespace: "monitoring", Name: "mimir-nginx", Port: "80", Scheme: "http"},
}

// labelSelectors are tried only when NO named candidate exists. A single hit
// with one port is used; multiple hits -> multi-match (unavailable).
var labelSelectors = []string{
	"app.kubernetes.io/name=prometheus,app.kubernetes.io/component=server",
	"app.kubernetes.io/name=mimir,app.kubernetes.io/component=query-frontend",
}

type metricsCache struct {
	capSet    bool
	cap       metrics.MetricsCapability
	capExpiry time.Time // zero = cached for lifetime (available)
	transport metrics.Querier

	samples    metrics.ClusterMetrics
	samplesExp time.Time
}

// transportFactory builds real transports: a direct endpoint through the
// safe HTTP policy, a Service through the guarded proxy client (C5 §4).
type transportFactory struct {
	rest    rest.Interface
	restErr error // the guarded proxy client could not be built
}

func (f transportFactory) Direct(base, token string, skip bool) metrics.Querier {
	return metrics.NewDirectTransport(base, token, safehttp.NewClient(metrics.DirectPolicy(metricsHTTPTimeout, skip)))
}

func (f transportFactory) Proxy(c metrics.ServiceCandidate) metrics.Querier {
	if f.restErr != nil {
		return failedQuerier{err: f.restErr}
	}
	return metrics.NewProxyTransport(f.rest, c)
}

// failedQuerier reads as unavailable with the reason, never through an
// unguarded client.
type failedQuerier struct{ err error }

func (q failedQuerier) InstantQuery(context.Context, string) (int, []byte, error) {
	return 0, nil, q.err
}
func (q failedQuerier) RangeQuery(context.Context, string, time.Time, time.Time, time.Duration) (int, []byte, error) {
	return 0, nil, q.err
}

// proxyREST is the REST client the service-proxy path uses: the guarded one
// built from the cluster's rest.Config, which refuses redirects off the API
// server's origin (C5 §4). A connection without a rest.Config (fake clients
// in tests) uses its clientset's. Caller MUST hold c.metricsMu.
func (c *ClusterConn) proxyREST() (rest.Interface, error) {
	if c.restConfig == nil {
		return c.typed.CoreV1().RESTClient(), nil
	}
	if c.guardedREST == nil && c.guardedRESTErr == nil {
		c.guardedREST, c.guardedRESTErr = metrics.NewGuardedProxyREST(c.restConfig)
	}
	return c.guardedREST, c.guardedRESTErr
}

// discover reduces in-cluster Services to a single DiscoveryResult: first
// existing named candidate, else single-hit label fallback, else multi-match
// or none.
func (c *ClusterConn) discover(ctx context.Context) metrics.DiscoveryResult {
	for _, cand := range namedCandidates {
		if _, err := c.typed.CoreV1().Services(cand.Namespace).Get(ctx, cand.Name, metav1.GetOptions{}); err == nil {
			chosen := cand
			return metrics.DiscoveryResult{Chosen: &chosen}
		}
	}
	for _, sel := range labelSelectors {
		list, err := c.typed.CoreV1().Services("").List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil || len(list.Items) == 0 {
			continue
		}
		if len(list.Items) > 1 {
			return metrics.DiscoveryResult{MultiMatch: true}
		}
		s := list.Items[0]
		if len(s.Spec.Ports) != 1 {
			continue // ambiguous port; do not guess
		}
		return metrics.DiscoveryResult{Chosen: &metrics.ServiceCandidate{
			Namespace: s.Namespace, Name: s.Name,
			Port: strconv.Itoa(int(s.Spec.Ports[0].Port)), Scheme: "http",
		}}
	}
	return metrics.DiscoveryResult{}
}

// ensureMetricsLocked resolves+probes the metrics endpoint into c.metricsState
// when the cached capability is invalid. Available is cached for the conn
// lifetime, unavailable is short-TTL re-probed, forceReprobe bypasses the cache.
// Caller MUST hold c.metricsMu.
func (c *ClusterConn) ensureMetricsLocked(ctx context.Context, forceReprobe bool, now time.Time) {
	capValid := c.metricsState.capSet && !forceReprobe &&
		(c.metricsState.cap.Available || now.Before(c.metricsState.capExpiry))
	if capValid {
		return
	}
	pr, prErr := c.proxyREST()
	var tf metrics.TransportFactory = transportFactory{rest: pr, restErr: prErr}
	if c.metricsTF != nil {
		tf = c.metricsTF
	}
	disco := metrics.DiscoveryResult{}
	if c.metricsCfg.Endpoint == "" && c.metricsCfg.ServiceRef == nil {
		disco = c.discover(ctx)
	}
	res := metrics.Resolve(c.metricsCfg, disco, tf)
	cap := metrics.Probe(ctx, res)

	c.metricsState.capSet = true
	c.metricsState.cap = cap
	c.metricsState.transport = res.Transport
	if cap.Available {
		c.metricsState.capExpiry = time.Time{}
	} else {
		c.metricsState.capExpiry = now.Add(metricsUnavailableTTL)
	}
	// re-probe invalidates any cached samples
	c.metricsState.samples = metrics.ClusterMetrics{}
	c.metricsState.samplesExp = time.Time{}
}

// ClusterMetrics returns the proof-of-life metrics and probe-confirmed
// capability. Lazy resolve+probe on first call; available is cached for the
// conn lifetime, unavailable is short-TTL re-probed, forceReprobe bypasses the
// cache. Sample values cache with their own short TTL.
func (c *ClusterConn) ClusterMetrics(ctx context.Context, forceReprobe bool) (metrics.ClusterMetrics, metrics.MetricsCapability) {
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()

	clk := c.clk
	if clk == nil { // defensive: manual struct construction in tests
		clk = clock.Real{}
	}
	now := clk.Now()
	c.ensureMetricsLocked(ctx, forceReprobe, now)

	cap := c.metricsState.cap
	if !cap.Available {
		return metrics.ClusterMetrics{}, cap
	}

	if c.metricsState.samplesExp.IsZero() || now.After(c.metricsState.samplesExp) {
		c.metricsState.samples = querySamples(ctx, c.metricsState.transport)
		c.metricsState.samplesExp = now.Add(metricsSampleTTL)
	}
	return c.metricsState.samples, cap
}

func querySamples(ctx context.Context, q metrics.Querier) metrics.ClusterMetrics {
	cl := metrics.NewClient(q)
	var out metrics.ClusterMetrics
	if s, err := cl.InstantScalar(ctx, cpuQuery); err == nil && !s.Absent {
		v := s.Value
		out.CPUFraction = &v
	}
	if s, err := cl.InstantScalar(ctx, memQuery); err == nil && !s.Absent {
		v := s.Value
		out.MemFraction = &v
	}
	return out
}

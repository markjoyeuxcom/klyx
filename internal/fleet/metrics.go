package fleet

import (
	"context"
	"strconv"
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/safehttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

const (
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
	// credRev is the endpoint credential's revision the cache was built
	// under (C5 §7): a different one discards it.
	credRev   uint64
	capSet    bool
	cap       metrics.MetricsCapability
	capExpiry time.Time // zero = cached for lifetime (available)
	// client is the transport under the reader's effective match, pinned
	// together: every query is rendered under that scope, and no reader
	// holds the transport, whose requests take PromQL text (Settings S3b
	// §3.3)
	client *metrics.Client

	samples    metrics.ClusterMetrics
	samplesExp time.Time
}

// transportFactory builds real transports: a direct endpoint through the
// safe HTTP policy, a Service through the guarded proxy client (C5 §4).
type transportFactory struct {
	rest    rest.Interface
	restErr error // the guarded proxy client could not be built
}

func (f transportFactory) Direct(base string, auth metrics.Auth, skip bool) metrics.Transport {
	return metrics.NewDirectTransportAuth(base, auth, safehttp.NewClient(metrics.DirectPolicy(metricsHTTPTimeout, skip)))
}

func (f transportFactory) Proxy(c metrics.ServiceCandidate, tenant string) metrics.Transport {
	if f.restErr != nil {
		return metrics.FailedTransport(f.restErr)
	}
	return metrics.NewProxyTransportTenant(f.rest, c, tenant)
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
	revs := c.creds.Revisions
	rev := revs.Revision(c.name, config.EndpointMetrics)
	capValid := c.metricsState.capSet && !forceReprobe && c.metricsState.credRev == rev &&
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
	// off or a subscription is never discovered (Settings S3b §2.4)
	if c.metricsCfg.Endpoint == "" && c.metricsCfg.ServiceRef == nil && !c.metricsCfg.Off && c.metricsCfg.Source == "" {
		disco = c.discover(ctx)
	}
	res := metrics.Resolve(c.metricsCfg, c.metricsAuth(rev), disco, tf)
	cap := metrics.Probe(ctx, res)

	// a read started under an older credential revision is neither cached
	// nor shown as the endpoint's availability (C5 §7)
	if revs.Revision(c.name, config.EndpointMetrics) != rev {
		c.metricsState = metricsCache{cap: metrics.MetricsCapability{Mode: res.Mode, Source: res.Source,
			Reason: reasonCredentialChanged}}
		return
	}
	c.metricsState.credRev = rev
	c.metricsState.capSet = true
	c.metricsState.cap = cap
	c.metricsState.client = metrics.NewClient(res.Transport, metrics.ScopeOf(c.metricsCfg.Match))
	if cap.Available {
		c.metricsState.capExpiry = time.Time{}
	} else {
		c.metricsState.capExpiry = now.Add(metricsUnavailableTTL)
	}
	// re-probe invalidates any cached samples
	c.metricsState.samples = metrics.ClusterMetrics{}
	c.metricsState.samplesExp = time.Time{}
}

// reasonCredentialChanged is what a read that crossed a change to the
// endpoint's credential or configuration says (C5 §7).
const reasonCredentialChanged = "the endpoint's credential changed during the read; it is read again next time"

// metricsRead is what a reader takes under c.metricsMu and queries outside
// it: the capability, the client (its transport under its own scope, never
// a caller's: Settings S3b §3.3), and the credential revision both were
// built under.
type metricsRead struct {
	cap    metrics.MetricsCapability
	client *metrics.Client
	rev    uint64
}

// readMetrics resolves the capability (the lazy probe and its cache) and
// takes a metricsRead of it.
func (c *ClusterConn) readMetrics(ctx context.Context, now time.Time) metricsRead {
	c.metricsMu.Lock()
	defer c.metricsMu.Unlock()
	c.ensureMetricsLocked(ctx, false, now)
	return metricsRead{cap: c.metricsState.cap, client: c.metricsState.client, rev: c.metricsState.credRev}
}

// credentialMoved reports whether the endpoint's revision moved since rev.
// A result read across the move is neither shown nor cached, whichever of a
// reader's queries the move landed in (C5 §7): the transport's own gate
// refuses a response still arriving, not one already read.
func (c *ClusterConn) credentialMoved(rev uint64) bool {
	return c.creds.Revisions.Revision(c.name, config.EndpointMetrics) != rev
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
		rev := c.metricsState.credRev
		samples := querySamples(ctx, c.metricsState.client)
		if c.credentialMoved(rev) {
			c.metricsState = metricsCache{}
			return metrics.ClusterMetrics{}, metrics.MetricsCapability{Mode: cap.Mode, Source: cap.Source,
				Reason: reasonCredentialChanged}
		}
		c.metricsState.samples = samples
		c.metricsState.samplesExp = now.Add(metricsSampleTTL)
	}
	return c.metricsState.samples, cap
}

func querySamples(ctx context.Context, cl *metrics.Client) metrics.ClusterMetrics {
	var out metrics.ClusterMetrics
	if s, err := cl.InstantScalar(ctx, metrics.ClusterCPU.With(metrics.None{})); err == nil && !s.Absent {
		v := s.Value
		out.CPUFraction = &v
	}
	if s, err := cl.InstantScalar(ctx, metrics.ClusterMem.With(metrics.None{})); err == nil && !s.Absent {
		v := s.Value
		out.MemFraction = &v
	}
	return out
}

// metricsAuth is the metrics endpoint's credential and tenant as resolved
// now, gated on revision rev (C5 §3, §7).
func (c *ClusterConn) metricsAuth(rev uint64) metrics.Auth {
	return c.endpointAuth(config.EndpointMetrics, c.metricsCfg.Spec(), c.metricsCfg.Token, rev)
}

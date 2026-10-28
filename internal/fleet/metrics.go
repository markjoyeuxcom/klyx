package fleet

import (
	"context"
	"strconv"
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/metrics"
	"github.com/moomora/klyx/internal/readfence"
	"github.com/moomora/klyx/internal/safehttp"
	corev1 "k8s.io/api/core/v1"
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
	// terms are a subscription's, pinned when it was probed, and key their
	// lineage key (Settings S3b §3.1, §3.2): another key now discards the
	// cache. A subscribed read builds its client per read, from the terms.
	terms *sourceTerms
	key   string

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
// in tests) uses its clientset's.
func (c *ClusterConn) proxyREST() (rest.Interface, error) {
	if c.restConfig == nil {
		return c.typed.CoreV1().RESTClient(), nil
	}
	c.guardedMu.Lock()
	defer c.guardedMu.Unlock()
	if c.guardedREST == nil && c.guardedRESTErr == nil {
		c.guardedREST, c.guardedRESTErr = metrics.NewGuardedProxyREST(c.restConfig)
	}
	return c.guardedREST, c.guardedRESTErr
}

// discover reduces in-cluster Services to a single DiscoveryResult: first
// existing named candidate, else single-hit label fallback, else multi-match
// or none.
func (c *ClusterConn) discover(ctx context.Context) metrics.DiscoveryResult {
	res, _ := c.discoverWith(ctx, nil)
	return res
}

// discoverWith is discover with each Service read through gate: taken
// before the read, which it may refuse, and released after it (an
// installation's discovery in its host: Settings S3b-2b review, R2). A
// refusal, or the context's end, stops discovery there, with no fallback
// to the next candidate, and is returned; nil gates nothing.
func (c *ClusterConn) discoverWith(ctx context.Context, gate func(context.Context) (func() error, error)) (metrics.DiscoveryResult, error) {
	step := func(read func() error) error {
		done := func() error { return nil }
		if gate != nil {
			var err error
			if done, err = gate(ctx); err != nil {
				return err
			}
		}
		rerr := read()
		if err := done(); err != nil {
			return err
		}
		// a read the context's fence refused - a round trip, a retry, a
		// redirect's hop - is the discovery's refusal, never an absence
		// (Codex's re-review of S3b-2b, RR1)
		if readfence.Failed(rerr) {
			return rerr
		}
		if gate != nil {
			return ctx.Err()
		}
		return nil
	}
	for _, cand := range namedCandidates {
		var err error
		if serr := step(func() error {
			_, err = c.typed.CoreV1().Services(cand.Namespace).Get(ctx, cand.Name, metav1.GetOptions{})
			return err
		}); serr != nil {
			return metrics.DiscoveryResult{}, serr
		}
		if err == nil {
			chosen := cand
			return metrics.DiscoveryResult{Chosen: &chosen}, nil
		}
	}
	for _, sel := range labelSelectors {
		var list *corev1.ServiceList
		var err error
		if serr := step(func() error {
			list, err = c.typed.CoreV1().Services("").List(ctx, metav1.ListOptions{LabelSelector: sel})
			return err
		}); serr != nil {
			return metrics.DiscoveryResult{}, serr
		}
		if err != nil || len(list.Items) == 0 {
			continue
		}
		if len(list.Items) > 1 {
			return metrics.DiscoveryResult{MultiMatch: true}, nil
		}
		s := list.Items[0]
		if len(s.Spec.Ports) != 1 {
			continue // ambiguous port; do not guess
		}
		return metrics.DiscoveryResult{Chosen: &metrics.ServiceCandidate{
			Namespace: s.Namespace, Name: s.Name,
			Port: strconv.Itoa(int(s.Spec.Ports[0].Port)), Scheme: "http",
		}}, nil
	}
	return metrics.DiscoveryResult{}, nil
}

// ensureMetricsLocked resolves+probes the metrics endpoint into c.metricsState
// when the cached capability is invalid. Available is cached for the conn
// lifetime, unavailable is short-TTL re-probed, forceReprobe bypasses the cache.
// A read the call itself ended - its context's fence refused it, or its
// context ended - in its discovery or its probe is this call's answer,
// returned and never kept: a later reader resolves again (Codex's
// re-reviews 2 and 3 of S3b-2b, R2-1, R3-1). A backend's own failure, the
// direct client's timeout included, is cached as before. Caller MUST hold
// c.metricsMu.
func (c *ClusterConn) ensureMetricsLocked(ctx context.Context, forceReprobe bool, now time.Time) (refused *metrics.MetricsCapability) {
	if c.metricsCfg.Source != "" {
		return c.ensureSubscribedMetricsLocked(ctx, forceReprobe, now)
	}
	revs := c.creds.Revisions
	rev := revs.Revision(c.name, config.EndpointMetrics)
	capValid := c.metricsState.capSet && !forceReprobe && c.metricsState.credRev == rev &&
		(c.metricsState.cap.Available || now.Before(c.metricsState.capExpiry))
	if capValid {
		return nil
	}
	pr, prErr := c.proxyREST()
	var tf metrics.TransportFactory = transportFactory{rest: pr, restErr: prErr}
	if c.metricsTF != nil {
		tf = c.metricsTF
	}
	disco := metrics.DiscoveryResult{}
	// off or a subscription is never discovered (Settings S3b §2.4)
	if c.metricsCfg.Endpoint == "" && c.metricsCfg.ServiceRef == nil && !c.metricsCfg.Off && c.metricsCfg.Source == "" {
		var derr error
		if disco, derr = c.discoverWith(ctx, nil); callEnded(ctx, derr) {
			return &metrics.MetricsCapability{Mode: metrics.ModeUnavailable, Reason: endedWhy(ctx, derr), Withdrawn: true}
		}
	}
	res := metrics.Resolve(c.metricsCfg, c.metricsAuth(rev), disco, tf)
	cap := metrics.Probe(ctx, res)
	if cap.Withdrawn {
		return &cap
	}

	// a read started under an older credential revision is neither cached
	// nor shown as the endpoint's availability (C5 §7)
	if revs.Revision(c.name, config.EndpointMetrics) != rev {
		c.metricsState = metricsCache{cap: metrics.MetricsCapability{Mode: res.Mode, Source: res.Source,
			Reason: reasonCredentialChanged}}
		return nil
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
	return nil
}

// ensureSubscribedMetricsLocked probes a subscription's metrics through
// its installation (Settings S3b §3.1) into c.metricsState, keyed by its
// lineage: the cache is valid while the key and the block's revision are,
// available for their life and unavailable for the short TTL. Caller MUST
// hold c.metricsMu.
func (c *ClusterConn) ensureSubscribedMetricsLocked(ctx context.Context, forceReprobe bool, now time.Time) (refused *metrics.MetricsCapability) {
	rev := c.creds.Revisions.Revision(c.name, config.EndpointMetrics)
	id := c.metricsCfg.Source
	unavailable := func(source, why string) {
		c.metricsState = metricsCache{credRev: rev, capSet: true, capExpiry: now.Add(metricsUnavailableTTL),
			cap: metrics.MetricsCapability{Mode: metrics.ModeUnavailable, Source: source, Reason: why}}
	}
	b := c.am.buckets
	if b == nil {
		unavailable("the installation "+id, "this connection was built without the installations in force, so it reads no installation")
		return
	}
	sub, ok := b.sources.SubscriptionOf(config.EndpointMetrics, c.name)
	if !ok || sub.Source.ID != id {
		unavailable("the installation "+id, "this cluster's subscription to "+id+" is not in force yet; Klyx reads it once it is applied")
		return
	}
	t := b.termsOf(sub)
	key := t.key()
	st := c.metricsState
	// a known-down host, or terms moved since, serve nothing cached (§13)
	if st.capSet && !forceReprobe && st.key == key && st.credRev == rev && (st.cap.Available || now.Before(st.capExpiry)) &&
		(st.terms == nil || b.checkTerms(*st.terms) == nil) {
		return
	}
	q, display, release, err := b.metricsSource(ctx, t, "the metrics of "+c.name)
	source := id
	if display != "" {
		source += ", " + display
	}
	if err != nil {
		if callEnded(ctx, err) {
			return &metrics.MetricsCapability{Mode: metrics.ModeSubscribed, Source: source, Reason: endedWhy(ctx, err), Withdrawn: true}
		}
		unavailable(source, safehttp.NewRedactor().Text(err.Error()))
		c.metricsState.key, c.metricsState.terms = key, &t
		return
	}
	cap := metrics.Probe(ctx, metrics.Resolution{Mode: metrics.ModeSubscribed, Source: source, Transport: q})
	if cap.Withdrawn {
		release()
		return &cap
	}
	// a probe across a move is neither cached nor shown as the source's
	// availability; it settles before its host lease is released (§13)
	why := c.readMoved(rev, &t)
	release()
	if why != "" {
		c.metricsState = metricsCache{cap: metrics.MetricsCapability{Mode: metrics.ModeSubscribed, Source: source, Reason: why}}
		return
	}
	c.metricsState = metricsCache{credRev: rev, capSet: true, cap: cap, terms: &t, key: key}
	if !cap.Available {
		c.metricsState.capExpiry = now.Add(metricsUnavailableTTL)
	}
	return nil
}

// reasonCredentialChanged is what a read that crossed a change to the
// endpoint's credential or configuration says (C5 §7).
const reasonCredentialChanged = "the endpoint's credential changed during the read; it is read again next time"

// metricsRead is what a reader takes under c.metricsMu and queries outside
// it: the capability, the client (its transport under its own scope, never
// a caller's: Settings S3b §3.3), and the credential revision both were
// built under. A subscribed read also pins its terms, and release ends its
// host lease; a reader defers it.
type metricsRead struct {
	c       *ClusterConn
	cap     metrics.MetricsCapability
	client  *metrics.Client
	rev     uint64
	terms   *sourceTerms
	release func()
}

// readMetrics resolves the capability (the lazy probe and its cache) and
// takes a metricsRead of it: a subscription's client is built for this
// read alone, under the terms it was probed with (S3b §3.1, §13).
func (c *ClusterConn) readMetrics(ctx context.Context, now time.Time) metricsRead {
	c.metricsMu.Lock()
	refused := c.ensureMetricsLocked(ctx, false, now)
	mr := metricsRead{c: c, cap: c.metricsState.cap, client: c.metricsState.client, rev: c.metricsState.credRev, terms: c.metricsState.terms, release: func() {}}
	if refused != nil {
		mr.cap, mr.terms = *refused, nil
	}
	c.metricsMu.Unlock()
	if mr.terms == nil || !mr.cap.Available {
		return mr
	}
	q, _, release, err := c.am.buckets.metricsSource(ctx, *mr.terms, "the metrics of "+c.name)
	if err != nil {
		mr.cap = metrics.MetricsCapability{Mode: mr.cap.Mode, Source: mr.cap.Source, Reason: safehttp.NewRedactor().Text(err.Error())}
		return mr
	}
	mr.client, mr.release = metrics.NewClient(q, mr.terms.scope()), release
	return mr
}

// unsettled is "" while the read's result stands; otherwise why it is not
// shown: the endpoint's revision moved (its credential or its block), or a
// subscribed read's terms did, whichever of its queries the move landed in.
func (mr metricsRead) unsettled() string { return mr.c.readMoved(mr.rev, mr.terms) }

// readMoved is why a read under rev and terms is not shown, "" when it is.
func (c *ClusterConn) readMoved(rev uint64, terms *sourceTerms) string {
	if c.credentialMoved(rev) {
		return reasonCredentialChanged
	}
	if terms != nil && c.am.buckets != nil {
		if err := c.am.buckets.checkTerms(*terms); err != nil {
			return "the read was dropped: " + err.Error() + "; it is read again next time"
		}
	}
	return ""
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
	if refused := c.ensureMetricsLocked(ctx, forceReprobe, now); refused != nil {
		return metrics.ClusterMetrics{}, *refused
	}

	cap := c.metricsState.cap
	if !cap.Available {
		return metrics.ClusterMetrics{}, cap
	}

	if c.metricsState.samplesExp.IsZero() || now.After(c.metricsState.samplesExp) {
		rev, terms, cl := c.metricsState.credRev, c.metricsState.terms, c.metricsState.client
		release := func() {}
		if terms != nil {
			// a subscription's client, for this read alone (S3b §3.1)
			q, _, rel, err := c.am.buckets.metricsSource(ctx, *terms, "the metrics of "+c.name)
			if err != nil {
				return metrics.ClusterMetrics{}, metrics.MetricsCapability{Mode: cap.Mode, Source: cap.Source, Reason: safehttp.NewRedactor().Text(err.Error())}
			}
			cl, release = metrics.NewClient(q, terms.scope()), rel
		}
		samples, refusedBy := querySamples(ctx, cl)
		// settled before the host lease is released (§13)
		why := c.readMoved(rev, terms)
		release()
		if why != "" {
			c.metricsState = metricsCache{}
			return metrics.ClusterMetrics{}, metrics.MetricsCapability{Mode: cap.Mode, Source: cap.Source,
				Reason: why}
		}
		// samples the call itself ended are this call's answer, never kept
		// (R2-1, R3-1)
		if refusedBy != nil {
			return metrics.ClusterMetrics{}, metrics.MetricsCapability{Mode: cap.Mode, Source: cap.Source, Reason: endedWhy(ctx, refusedBy), Withdrawn: true}
		}
		c.metricsState.samples = samples
		c.metricsState.samplesExp = now.Add(metricsSampleTTL)
	}
	return c.metricsState.samples, cap
}

func querySamples(ctx context.Context, cl *metrics.Client) (metrics.ClusterMetrics, error) {
	var out metrics.ClusterMetrics
	// a sample the call itself ended - its fence refused it, or its context
	// ended - is the caller's, never an absence (Codex's re-reviews 2 and 3
	// of S3b-2b, R2-1, R3-1)
	s, err := cl.InstantScalar(ctx, metrics.ClusterCPU.With(metrics.None{}))
	if callEnded(ctx, err) {
		return metrics.ClusterMetrics{}, endedErr(ctx, err)
	}
	if err == nil && !s.Absent {
		v := s.Value
		out.CPUFraction = &v
	}
	s, err = cl.InstantScalar(ctx, metrics.ClusterMem.With(metrics.None{}))
	if callEnded(ctx, err) {
		return metrics.ClusterMetrics{}, endedErr(ctx, err)
	}
	if err == nil && !s.Absent {
		v := s.Value
		out.MemFraction = &v
	}
	return out, nil
}

// metricsAuth is the metrics endpoint's credential and tenant as resolved
// now, gated on revision rev (C5 §3, §7).
func (c *ClusterConn) metricsAuth(rev uint64) metrics.Auth {
	return c.endpointAuth(config.EndpointMetrics, c.metricsCfg.Spec(), c.metricsCfg.Token, rev)
}

// callEnded is whether the call itself ended a read: its context's fence
// refused it (err), or its context ended. Neither says anything of the
// backend, so neither is kept (R2-1, R3-1).
func callEnded(ctx context.Context, err error) bool {
	return readfence.Failed(err) || ctx.Err() != nil
}

// endedErr is why a call ended its read: the fence's refusal, or the
// context's end.
func endedErr(ctx context.Context, err error) error {
	if readfence.Failed(err) || ctx.Err() == nil {
		return err
	}
	return ctx.Err()
}

// endedWhy is endedErr in words.
func endedWhy(ctx context.Context, err error) string {
	return safehttp.NewRedactor().Text(endedErr(ctx, err).Error())
}

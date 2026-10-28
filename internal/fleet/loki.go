package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/moomora/klyx/internal/alertmanager"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointhttp"
	"github.com/moomora/klyx/internal/loki"
	"github.com/moomora/klyx/internal/readfence"
	"github.com/moomora/klyx/internal/safehttp"
	"github.com/moomora/klyx/internal/sources"
)

// Coverage S8b: a connection's Loki endpoint - configured only (§2: no
// discovery), read on demand for pod log history (never an Observer
// snapshot), through C5's credential path and transport.

const (
	lokiMaxBytes    = 8 << 20
	lokiPageTimeout = 30 * time.Second
	lokiMaxInFlight = 4 // pages fleet-wide (§8)
	lokiPerCluster  = 2
	lokiTextMax     = 512 // bytes of Loki's own words shown in an error
)

var errLokiTooLarge = fmt.Errorf("the Loki response exceeded %d MiB; narrow the range or choose a container", lokiMaxBytes>>20)

var lokiLimits = endpointhttp.Limits{MaxBytes: lokiMaxBytes, TooLarge: errLokiTooLarge}

// lokiPages bounds history pages in flight fleet-wide.
var lokiPages = make(chan struct{}, lokiMaxInFlight)

// ErrNoLoki: no Loki is configured for this cluster.
var ErrNoLoki = errors.New("no Loki is configured for this cluster; set loki in fleet.yaml")

// ErrLokiOff: the cluster's own loki block says off (Settings S3b §2.4).
var ErrLokiOff = errors.New("log history is off for this cluster in fleet.yaml")

// ErrLokiSourceChanged: the cluster's Loki configuration or credential
// changed since the reader was built; a page set over it ends (§6).
var ErrLokiSourceChanged = errors.New("the Loki source changed; reading the newest again")

// lokiState is a connection's Loki endpoint. mu guards the configuration
// and the guarded proxy client.
type lokiState struct {
	mu         sync.Mutex
	cfg        *config.EndpointConfig
	proxy      *endpointhttp.GuardedProxy
	proxyErr   error
	client     func(policy safehttp.Policy) *http.Client // tests inject
	slotsOnce  sync.Once
	slots      chan struct{}
	configured atomic.Bool
}

// WithLoki sets the configured Loki (nil: none). Call it in the factory,
// before Start.
func (c *ClusterConn) WithLoki(cfg *config.EndpointConfig) *ClusterConn {
	c.loki.cfg = cloneEndpoint(cfg)
	c.loki.configured.Store(lokiLocated(cfg))
	return c
}

// logHistoryPresent is the LogHistory capability: a Loki configured where
// Klyx can read it, or through an installation.
func (c *ClusterConn) logHistoryPresent() bool { return c.loki.configured.Load() }

// lokiLocated: a Loki block names a URL or a Service, or subscribes to an
// installation (Settings S3b §2.4). Loki is never discovered, so a block
// with none of these - a match alone - is no log history, and never
// advertised as one (Settings review slice 0); off is none either.
func lokiLocated(cfg *config.EndpointConfig) bool {
	return cfg != nil && !cfg.Off && (cfg.Located() || cfg.Source != "")
}

// LokiConfig is the configured Loki as this connection uses it.
func (c *ClusterConn) LokiConfig() *config.EndpointConfig {
	c.loki.mu.Lock()
	defer c.loki.mu.Unlock()
	return cloneEndpoint(c.loki.cfg)
}

// SetLokiConfig adopts a reviewed configuration; the revision moves, so a
// page set over the old source ends (§6, C5 §7).
func (c *ClusterConn) SetLokiConfig(cfg *config.EndpointConfig) {
	c.loki.mu.Lock()
	defer c.loki.mu.Unlock()
	c.loki.cfg = cloneEndpoint(cfg)
	c.loki.configured.Store(lokiLocated(cfg))
	if c.creds.Revisions != nil {
		c.creds.Revisions.Bump(c.name, config.EndpointLoki, false)
	}
}

// ChangeLokiCredential installs a credential and the configuration it was
// reviewed with as one step for this connection's reads (C5 §7).
func (c *ClusterConn) ChangeLokiCredential(cfg *config.EndpointConfig, install func() error) error {
	c.loki.mu.Lock()
	defer c.loki.mu.Unlock()
	if err := install(); err != nil {
		return err
	}
	c.loki.cfg = cloneEndpoint(cfg)
	c.loki.configured.Store(lokiLocated(cfg))
	if c.creds.Revisions != nil {
		c.creds.Revisions.Bump(c.name, config.EndpointLoki, true)
	}
	return nil
}

// LokiReader reads one resolved Loki source: an endpoint or a Service. Its
// identity and match stay raw, in process; what it says is display values.
type LokiReader struct {
	c        *ClusterConn
	identity string
	display  string
	tenant   string
	match    map[string]string
	rev      uint64
	red      safehttp.Redactor
	get      lokiGet
	// terms are a subscription's, pinned when the reader was built
	// (Settings S3b §3.1): every page is fenced by them
	terms *sourceTerms
	// sentence and lineage say where the reader reads and what makes it
	// this cluster's (S3b §3.2, §3.4)
	sentence string
	lineage  sources.Lineage
}

// lokiGet is one page's GET.
type lokiGet func(ctx context.Context, path string, q url.Values, hdr http.Header) (int, []byte, error)

// Loki resolves the cluster's Loki as configured now.
func (c *ClusterConn) Loki() (*LokiReader, error) {
	c.loki.mu.Lock()
	defer c.loki.mu.Unlock()
	cfg := c.loki.cfg
	switch {
	case cfg == nil:
		return nil, ErrNoLoki
	case cfg.Off:
		return nil, ErrLokiOff
	case cfg.Source != "":
		// read through its installation, never another way (S3b §3.1)
		return c.subscribedLokiLocked(cfg.Source)
	}
	rev := uint64(0)
	if c.creds.Revisions != nil {
		rev = c.creds.Revisions.Revision(c.name, config.EndpointLoki)
	}
	r := &LokiReader{c: c, tenant: cfg.Tenant, match: cfg.Match, rev: rev}
	if cfg.Endpoint != "" {
		at, err := endpointAt(cfg.Endpoint)
		if err != nil {
			return nil, err
		}
		auth := c.endpointAuth(config.EndpointLoki, *cfg, "", rev)
		if auth.Unusable != "" {
			return nil, errors.New(auth.Unusable)
		}
		client := c.lokiClient(endpointhttp.DirectPolicy(lokiLimits, lokiPageTimeout, cfg.TLSSkipVerify))
		d := endpointhttp.NewDirect(strings.TrimRight(cfg.Endpoint, "/"), auth, client, lokiLimits)
		r.identity, r.display, r.red, r.get = amIdentity("endpoint", at, cfg.Tenant, cfg.Match), at, d.Redactor(), d.GetWith
		r.lineage, r.sentence = legacyLoki(config.AMDirect, r.identity, "from "+at, cfg.Tenant, cfg.Match, rev)
		return r, nil
	}
	if cfg.ServiceRef == nil {
		return nil, ErrNoLoki
	}
	g, err := c.lokiProxyLocked()
	if err != nil {
		return nil, err
	}
	svc := serviceOf(cfg.ServiceRef)
	p := endpointhttp.NewRawProxy(g, svc, cfg.Tenant, lokiLimits)
	r.identity, r.display, r.get = amIdentity("service", serviceAt(svc), cfg.Tenant, cfg.Match), "Service "+serviceAt(svc), p.Get
	r.lineage, r.sentence = legacyLoki(config.AMService, r.identity, "from the Service "+serviceAt(svc)+" on this cluster", cfg.Tenant, cfg.Match, rev)
	return r, nil
}

// lokiProxyLocked is the connection's guarded service-proxy client for
// Loki, built once. Caller holds c.loki.mu.
func (c *ClusterConn) lokiProxyLocked() (*endpointhttp.GuardedProxy, error) {
	if c.restConfig == nil {
		return nil, errors.New("this connection has no service-proxy client")
	}
	if c.loki.proxy == nil && c.loki.proxyErr == nil {
		c.loki.proxy, c.loki.proxyErr = endpointhttp.NewGuardedProxy(c.restConfig, lokiLimits)
	}
	if c.loki.proxyErr != nil {
		return nil, fmt.Errorf("the service-proxy client could not be built: %w", c.loki.proxyErr)
	}
	return c.loki.proxy, nil
}

// lokiProxy is lokiProxyLocked for a member reading through an
// installation this connection hosts (S3b §3.1).
func (c *ClusterConn) lokiProxy() (*endpointhttp.GuardedProxy, error) {
	c.loki.mu.Lock()
	defer c.loki.mu.Unlock()
	return c.lokiProxyLocked()
}

// subscribedLokiLocked is the reader of the cluster's Loki subscription
// (Settings S3b §3.1), its terms pinned now: the installation's endpoint
// with its own credential and the 30 s page timeout, or its Service
// through a lease on the host's current connection taken per page. Every
// page is fenced by the terms; a hosted one holds one of the host's Loki
// slots, beside the member's and the fleet's. The match is the
// subscription's effective one, none for scope: tenant. Caller holds
// c.loki.mu.
func (c *ClusterConn) subscribedLokiLocked(id string) (*LokiReader, error) {
	b := c.am.buckets
	if b == nil {
		return nil, fmt.Errorf("this cluster's logs come from the installation %s, and this connection was built without the installations in force", id)
	}
	sub, ok := b.sources.SubscriptionOf(config.EndpointLoki, c.name)
	if !ok || sub.Source.ID != id {
		return nil, fmt.Errorf("this cluster's subscription to %s is not in force yet; Klyx reads it once it is applied", id)
	}
	t := b.termsOf(sub)
	if err := b.checkTerms(t); err != nil {
		return nil, err
	}
	rev := uint64(0)
	if c.creds.Revisions != nil {
		rev = c.creds.Revisions.Revision(c.name, config.EndpointLoki)
	}
	match := sub.Match
	if sub.ScopeTenant {
		match = nil
	}
	r := &LokiReader{c: c, identity: t.key(), tenant: sub.Tenant, match: match, rev: rev, terms: &t, lineage: t.lineage()}
	get, at, red, err := b.lokiSource(t, b.readFence(t), "the log history of "+c.name, c.lokiClient)
	if err != nil {
		return nil, err
	}
	r.get, r.red, r.display, r.sentence = get, red, sub.Source.ID+", "+at, t.sentence("", red)
	if t.direct() {
		r.sentence = t.sentence(at, red)
	}
	return r, nil
}

// lokiSource is an installation's Loki pages under t, each under fence's
// check and denial: its endpoint with its own credential and the 30 s
// page timeout, or its Service through a lease on the host's pinned
// connection taken per page, holding one of the host's Loki slots. where
// names it: the endpoint's origin, or "Service ns/name:port in host".
func (b *alertBuckets) lokiSource(t sourceTerms, fence endpointhttp.Fence, what string, client func(safehttp.Policy) *http.Client) (get lokiGet, where string, red safehttp.Redactor, err error) {
	src := t.sub.Source
	if t.direct() {
		at, err := endpointAt(src.Endpoint)
		if err != nil {
			return nil, "", red, fmt.Errorf("%s: %w", src.ID, err)
		}
		auth, err := b.sourceCredential(t)
		if err != nil {
			return nil, "", red, err
		}
		d := endpointhttp.NewDirect(strings.TrimRight(src.Endpoint, "/"), auth, client(endpointhttp.DirectPolicy(lokiLimits, lokiPageTimeout, src.TLSSkipVerify)), lokiLimits)
		return func(ctx context.Context, path string, q url.Values, hdr http.Header) (int, []byte, error) {
			return fence.Do(ctx, func(ctx context.Context) (int, []byte, error) { return d.GetWith(ctx, path, q, hdr) })
		}, at, d.Redactor(), nil
	}
	if src.ServiceRef == nil {
		return nil, "", red, fmt.Errorf("%s has no serviceRef, and Loki is not discovered", src.ID)
	}
	svc := serviceOf(src.ServiceRef)
	return func(ctx context.Context, path string, q url.Values, hdr http.Header) (int, []byte, error) {
		conn, life, release, err := b.lease(src.Host, what)
		if err != nil {
			return 0, nil, fmt.Errorf("%s's host %s is not connected: %w", src.ID, src.Host, err)
		}
		defer release()
		host, ok := conn.(*ClusterConn)
		if !ok || host.ID() != t.hostConn {
			return 0, nil, errAccessMoved
		}
		g, err := host.lokiProxy()
		if err != nil {
			return 0, nil, fmt.Errorf("%s: %w", src.ID, err)
		}
		p := endpointhttp.NewRawProxy(g, svc, t.sub.Tenant, lokiLimits)
		f := fence
		f.Life = life
		f.Slot = func(ctx context.Context) (func(), error) { return b.slots.take(ctx, config.EndpointLoki, src.Host) }
		return f.Do(ctx, func(ctx context.Context) (int, []byte, error) { return p.Get(ctx, path, q, hdr) })
	}, "Service " + serviceAt(svc) + " in " + src.Host, red, nil
}

func (c *ClusterConn) lokiClient(p safehttp.Policy) *http.Client {
	if c.loki.client != nil {
		return c.loki.client(p)
	}
	return safehttp.NewClient(p)
}

// LineageKey is the source identity, hashed: display-safe (§6).
func (r *LokiReader) LineageKey() string {
	sum := sha256.Sum256([]byte(r.identity))
	return hex.EncodeToString(sum[:8])
}

// Revision is the endpoint revision the reader was built at.
func (r *LokiReader) Revision() uint64 { return r.rev }

// Source and Tenant are display values.
func (r *LokiReader) Source() string { return alertmanager.Clip(r.red.Text(r.display), 512) }
func (r *LokiReader) Tenant() string { return alertmanager.Clip(r.red.Text(r.tenant), 256) }

// Sentence is the reader's scope sentence (S3b §3.4), a display value.
func (r *LokiReader) Sentence() string { return alertmanager.Clip(r.red.Text(r.sentence), sentenceMax) }

// Lineage is the reader's lineage (S3b §3.2).
func (r *LokiReader) Lineage() sources.Lineage { return r.lineage }

// Match is the configured match, raw: for building queries in process.
func (r *LokiReader) Match() map[string]string { return r.match }

// Redactor is the reading transport's redactor.
func (r *LokiReader) Redactor() safehttp.Redactor { return r.red }

// Current reports whether the cluster's Loki is still this source at this
// revision.
func (r *LokiReader) Current() bool {
	now, err := r.c.Loki()
	return err == nil && now.identity == r.identity && now.rev == r.rev
}

// Slot takes a page's slot: at most two per cluster and four fleet-wide.
func (r *LokiReader) Slot(ctx context.Context) (func(), error) {
	r.c.loki.slotsOnce.Do(func() { r.c.loki.slots = make(chan struct{}, lokiPerCluster) })
	select {
	case r.c.loki.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case lokiPages <- struct{}{}:
	case <-ctx.Done():
		<-r.c.loki.slots
		return nil, ctx.Err()
	}
	return func() { <-lokiPages; <-r.c.loki.slots }, nil
}

// read GETs path and returns the body of a 2xx. A non-2xx is a
// *loki.StatusError with Loki's words as a display value; a 401 without a
// tenant says what to set. A read across a credential change fails (C5 §7).
func (r *LokiReader) read(ctx context.Context, path string, q url.Values, hdr http.Header) ([]byte, error) {
	code, body, err := r.get(ctx, path, q, hdr)
	// a page read across a move of the subscription's terms is of a new
	// source (S3b §3.2); a host that went down says so
	if r.terms != nil && r.c.am.buckets != nil {
		if terr := r.c.am.buckets.checkTerms(*r.terms); terr != nil {
			if termsMoved(terr) {
				return nil, ErrLokiSourceChanged
			}
			return nil, terr
		}
	}
	if err != nil {
		return nil, r.red.Err(err, errLokiTooLarge, endpointhttp.ErrCredentialChanged, context.Canceled, context.DeadlineExceeded, readfence.ErrRefused)
	}
	if r.c.creds.Revisions != nil && r.c.creds.Revisions.Revision(r.c.name, config.EndpointLoki) != r.rev {
		return nil, ErrLokiSourceChanged
	}
	if code < 200 || code > 299 {
		text := strings.TrimSpace(string(body))
		if !utf8.ValidString(text) {
			text = ""
		}
		text = alertmanager.Clip(r.red.Text(text), lokiTextMax)
		if code == http.StatusUnauthorized && r.tenant == "" {
			text = "Loki requires a tenant; set loki.tenant"
		}
		return nil, &loki.StatusError{Code: code, Text: text}
	}
	return body, nil
}

// Labels lists the label names of the streams the query selects in the
// range (no query: the tenant's). data is false when Loki answered success
// with no data at all - an unknown tenant reads the same (lab finding 7).
func (r *LokiReader) Labels(ctx context.Context, start, end time.Time, query string) ([]string, bool, error) {
	q := url.Values{"start": {strconv.FormatInt(start.UnixNano(), 10)}, "end": {strconv.FormatInt(end.UnixNano(), 10)}}
	if query != "" {
		q.Set("query", query)
	}
	body, err := r.read(ctx, "/loki/api/v1/labels", q, nil)
	if err != nil {
		return nil, false, err
	}
	var w struct {
		Status string    `json:"status"`
		Data   *[]string `json:"data"`
	}
	if err := json.Unmarshal(body, &w); err != nil || w.Status != "success" {
		return nil, false, fmt.Errorf("%w: the labels answer does not decode", loki.ErrShape)
	}
	// no data, or an empty list: nothing selectable - the caller's fallback
	// read says why (Codex's review of b2f945c)
	if w.Data == nil || len(*w.Data) == 0 {
		return nil, false, nil
	}
	return *w.Data, true, nil
}

// Query reads one query_range [start, end) backward with the limit, with
// structured metadata categorized, parsed strictly.
func (r *LokiReader) Query(ctx context.Context, query string, start, end int64, limit int) ([]loki.Entry, error) {
	q := url.Values{"query": {query}, "start": {strconv.FormatInt(start, 10)}, "end": {strconv.FormatInt(end, 10)},
		"limit": {strconv.Itoa(limit)}, "direction": {"backward"}}
	body, err := r.read(ctx, "/loki/api/v1/query_range", q, http.Header{"X-Loki-Response-Encoding-Flags": {"categorize-labels"}})
	if err != nil {
		return nil, err
	}
	return loki.ParseStreams(body)
}

// Status reads the build info (no tenant needed): Settings' Test.
func (r *LokiReader) Status(ctx context.Context) (string, error) {
	body, err := r.read(ctx, lokiBuildInfo, nil, nil)
	if err != nil {
		return "", err
	}
	return buildVersion(body, r.red)
}

// lokiBuildInfo is Loki's build info, which needs no tenant.
const lokiBuildInfo = "/loki/api/v1/status/buildinfo"

// buildVersion is the version a build info answer names, a display value.
func buildVersion(body []byte, red safehttp.Redactor) (string, error) {
	var w struct {
		Version *string `json:"version"`
	}
	if err := json.Unmarshal(body, &w); err != nil || w.Version == nil {
		return "", fmt.Errorf("%w: the build info does not decode", loki.ErrShape)
	}
	return alertmanager.Clip(red.Text(*w.Version), 64), nil
}

// LokiTestResult is what Settings' Test read (S8b §6): display values.
type LokiTestResult struct {
	Version   string
	Source    string
	Tenant    string
	Data      bool     // the labels read in this cluster's match answered with data
	Namespace []string // the namespace spellings among the labels
}

// LokiTest reads the build info (no tenant needed), then the label names of
// this cluster's streams over the last hour. It never reads lines.
func (c *ClusterConn) LokiTest(ctx context.Context) (LokiTestResult, error) {
	r, err := c.Loki()
	if err != nil {
		return LokiTestResult{}, err
	}
	return lokiTestWith(ctx, r)
}

// lokiTestWith is LokiTest through the reader r.
func lokiTestWith(ctx context.Context, r *LokiReader) (LokiTestResult, error) {
	v, err := r.Status(ctx)
	if err != nil {
		return LokiTestResult{}, err
	}
	out := LokiTestResult{Version: v, Source: r.Source(), Tenant: r.Tenant()}
	now := time.Now()
	labels, data, err := r.Labels(ctx, now.Add(-time.Hour), now, matchSelectorOf(r.match))
	if err != nil {
		return LokiTestResult{}, err
	}
	out.Data = data
	for _, l := range labels {
		if l == loki.LabelNamespace || l == loki.LabelK8sNamespace {
			out.Namespace = append(out.Namespace, l)
		}
	}
	return out, nil
}

// matchSelectorOf is the match alone as a stream selector ("" without one).
func matchSelectorOf(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Quote(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// legacyLoki is a cluster's own Loki block's lineage and sentence: where it
// reads, its tenant and its match, or that nothing filters it.
func legacyLoki(mode, identity, where, tenant string, match map[string]string, rev uint64) (sources.Lineage, string) {
	parts := []string{where}
	if tenant != "" {
		parts = append(parts, "tenant "+alertmanager.Clip(tenant, alertmanager.MaxLabelValue))
	}
	words := scopeWords(match, safehttp.Redactor{})
	if words == "" {
		parts = append(parts, "unfiltered: every stream there is read as this cluster's")
	} else {
		parts = append(parts, "filtered by "+words)
	}
	return sources.Lineage{Kind: config.EndpointLoki, Mode: mode, Identity: identity, Tenant: tenant, Match: words, CredRev: rev},
		strings.Join(parts, ", ")
}

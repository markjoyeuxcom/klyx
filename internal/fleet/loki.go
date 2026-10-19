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
	"github.com/moomora/klyx/internal/safehttp"
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
	c.loki.configured.Store(cfg != nil)
	return c
}

// logHistoryPresent is the LogHistory capability: a Loki configured.
func (c *ClusterConn) logHistoryPresent() bool { return c.loki.configured.Load() }

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
	c.loki.configured.Store(cfg != nil)
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
	c.loki.configured.Store(cfg != nil)
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
	get      func(ctx context.Context, path string, q url.Values, hdr http.Header) (int, []byte, error)
}

// Loki resolves the cluster's Loki as configured now.
func (c *ClusterConn) Loki() (*LokiReader, error) {
	c.loki.mu.Lock()
	defer c.loki.mu.Unlock()
	cfg := c.loki.cfg
	if cfg == nil {
		return nil, ErrNoLoki
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
		return r, nil
	}
	if cfg.ServiceRef == nil {
		return nil, ErrNoLoki
	}
	if c.restConfig == nil {
		return nil, errors.New("this connection has no service-proxy client")
	}
	if c.loki.proxy == nil && c.loki.proxyErr == nil {
		c.loki.proxy, c.loki.proxyErr = endpointhttp.NewGuardedProxy(c.restConfig, lokiLimits)
	}
	if c.loki.proxyErr != nil {
		return nil, fmt.Errorf("the service-proxy client could not be built: %w", c.loki.proxyErr)
	}
	svc := serviceOf(cfg.ServiceRef)
	p := endpointhttp.NewRawProxy(c.loki.proxy, svc, cfg.Tenant, lokiLimits)
	r.identity, r.display, r.get = amIdentity("service", serviceAt(svc), cfg.Tenant, cfg.Match), "Service "+serviceAt(svc), p.Get
	return r, nil
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
	if err != nil {
		return nil, r.red.Err(err, errLokiTooLarge, endpointhttp.ErrCredentialChanged, context.Canceled, context.DeadlineExceeded)
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
	if w.Data == nil {
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
	body, err := r.read(ctx, "/loki/api/v1/status/buildinfo", nil, nil)
	if err != nil {
		return "", err
	}
	var w struct {
		Version *string `json:"version"`
	}
	if err := json.Unmarshal(body, &w); err != nil || w.Version == nil {
		return "", fmt.Errorf("%w: the build info does not decode", loki.ErrShape)
	}
	return alertmanager.Clip(r.red.Text(*w.Version), 64), nil
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

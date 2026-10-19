// Package endpointhttp is the HTTP path to an external endpoint Klyx reads
// (metrics, Alertmanager, Loki), whatever the endpoint speaks. It holds
// what coverage C5 built and accepted for metrics, made kind-neutral
// (coverage S8a §3): the direct transport (the credential and tenant headers,
// a revision gate on every round trip, a completion re-check, errors
// redacted) and the API server's service proxy through a guarded client
// (same-origin redirects, the cap below client-go's decoding). Each kind
// brings its own Limits; the protocol on top is the caller's.
package endpointhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/safehttp"
)

// Limits are one endpoint kind's response bounds: the cap and the error a
// body past it reads as (never a truncated body read as whole, C5 §4).
type Limits struct {
	MaxBytes int64
	TooLarge error
}

// Auth is what a direct request to an endpoint carries (coverage C5 §3-§7):
// the Authorization header ("" for none), the tenant (X-Scope-OrgID), the
// redactor for the credential, and a gate that refuses to send once the
// credential's revision has moved. Unusable says why the credential cannot
// be used: the endpoint then reads unavailable and nothing is sent.
type Auth struct {
	Header   string
	Tenant   string
	Redactor safehttp.Redactor
	Live     func() bool
	Unusable string
}

// ErrCredentialChanged: the endpoint's credential changed or was removed
// after this client was built; it sends nothing more (C5 §7).
var ErrCredentialChanged = errors.New("the endpoint's credential changed; read it again")

// DirectPolicy is the HTTP policy for a direct endpoint: same-origin
// redirects only, the cap as an error, no environment proxy (C5 §4).
func DirectPolicy(lim Limits, timeout time.Duration, tlsSkipVerify bool) safehttp.Policy {
	return safehttp.Policy{Timeout: timeout, MaxBytes: lim.MaxBytes, TooLarge: lim.TooLarge, TLSSkipVerify: tlsSkipVerify}
}

// Direct reads an endpoint over HTTP(S) at its base URL. It holds the
// credential, so it redacts it from every error it returns (C5 §5) and sends
// nothing once the credential's revision has moved (§7). Bodies are returned
// as sent: the protocol and the data are never rewritten.
type Direct struct {
	base   string // no trailing slash
	auth   Auth
	client *http.Client
	lim    Limits
}

// NewDirect builds a direct transport. With a revision gate, every round
// trip - the first request and each redirect hop - is refused once the
// credential's revision has moved (C5 §7).
func NewDirect(base string, auth Auth, client *http.Client, lim Limits) *Direct {
	if auth.Live != nil && client != nil {
		gated := *client
		rt := gated.Transport
		if rt == nil {
			rt = http.DefaultTransport
		}
		gated.Transport = liveRoundTripper{base: rt, live: auth.Live}
		client = &gated
	}
	return &Direct{base: strings.TrimRight(base, "/"), auth: auth, client: client, lim: lim}
}

// liveRoundTripper sends nothing once the credential's revision has moved.
type liveRoundTripper struct {
	base http.RoundTripper
	live func() bool
}

func (t liveRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.live() {
		return nil, ErrCredentialChanged
	}
	return t.base.RoundTrip(req)
}

// Redactor is the credential's redactor, for what a caller says about a
// response this transport read.
func (d *Direct) Redactor() safehttp.Redactor { return d.auth.Redactor }

// Get reads path (beginning "/") with the query, capped. The credential and
// the tenant are set here, for the endpoint's origin; the redirect policy
// keeps them on that origin.
func (d *Direct) Get(ctx context.Context, path string, q url.Values) (int, []byte, error) {
	return d.GetWith(ctx, path, q, nil)
}

// GetWith is Get with protocol headers (coverage S8b: Loki's
// X-Loki-Response-Encoding-Flags). They never replace the credential or the
// tenant this transport sets.
func (d *Direct) GetWith(ctx context.Context, path string, q url.Values, hdr http.Header) (int, []byte, error) {
	u := d.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, d.auth.Redactor.Err(err)
	}
	if d.auth.Live != nil && !d.auth.Live() {
		return 0, nil, ErrCredentialChanged
	}
	setProtocolHeaders(req, hdr)
	if d.auth.Header != "" {
		req.Header.Set("Authorization", d.auth.Header)
	}
	if d.auth.Tenant != "" {
		req.Header.Set("X-Scope-OrgID", d.auth.Tenant)
	}
	red := d.auth.Redactor
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, nil, red.Err(err, ErrCredentialChanged, d.lim.TooLarge)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := ReadCapped(resp.Body, d.lim)
	// a response that completes after the credential changed is dropped:
	// no reader may show or cache it (C5 §7)
	if d.auth.Live != nil && !d.auth.Live() {
		return 0, nil, ErrCredentialChanged
	}
	// the body is returned as sent: a credential is removed from what is
	// said about it, never from the protocol or the data (C5 §5)
	return resp.StatusCode, body, red.Err(err, d.lim.TooLarge)
}

// ReadCapped reads at most lim.MaxBytes; one byte more is lim.TooLarge,
// never a short body.
func ReadCapped(r io.Reader, lim Limits) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, lim.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > lim.MaxBytes {
		return nil, lim.TooLarge
	}
	return body, nil
}

// NewGuardedProxyREST is the CoreV1 REST client a service-proxy read uses
// (C5 §4), one per endpoint kind for its Limits. It is built from the
// cluster's rest.Config with its own http.Client whose CheckRedirect allows
// only the API server's own origin, never downgraded, at most five hops:
// client-go re-adds the kube credential on every redirected request, and
// CheckRedirect runs before the redirected request reaches that stack - so a
// refused redirect sends nothing, and no other origin ever sees the kube
// credential. The cap sits below client-go's decoding, so a non-2xx body -
// which client-go reads whole - is bounded too.
func NewGuardedProxyREST(cfg *rest.Config, lim Limits) (rest.Interface, error) {
	g, err := NewGuardedProxy(cfg, lim)
	if err != nil {
		return nil, err
	}
	return g.REST, nil
}

// GuardedProxy is the guarded service-proxy client pair for one kind's
// Limits: REST builds service-proxy requests, and HTTP is the http.Client
// beneath it - the cluster's credentials and TLS (rest.HTTPClientFor), the
// same-origin redirect check, and the cap - for reads client-go's response
// handling cannot carry (RawProxy).
type GuardedProxy struct {
	REST      rest.Interface
	HTTP      *http.Client
	UserAgent string
}

// NewGuardedProxy builds the pair (see NewGuardedProxyREST for the guard).
func NewGuardedProxy(cfg *rest.Config, lim Limits) (*GuardedProxy, error) {
	c := rest.CopyConfig(cfg)
	c.APIPath = "/api"
	c.GroupVersion = &corev1.SchemeGroupVersion
	c.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	if c.UserAgent == "" {
		c.UserAgent = rest.DefaultKubernetesUserAgent()
	}
	hc, err := rest.HTTPClientFor(c)
	if err != nil {
		return nil, err
	}
	hc.CheckRedirect = safehttp.SameOriginRedirect(safehttp.MaxRedirects)
	hc.Transport = safehttp.Cap(hc.Transport, lim.MaxBytes, lim.TooLarge)
	rc, err := rest.RESTClientForConfigAndClient(c, hc)
	if err != nil {
		return nil, err
	}
	return &GuardedProxy{REST: rc, HTTP: hc, UserAgent: c.UserAgent}, nil
}

// Service is an in-cluster Service read through the API server's service
// proxy: its port (a number or a name), its scheme ("" is http), and the
// path prefix it serves under ("" for none).
type Service struct {
	Namespace, Name, Port, Scheme, Path string
}

// Proxy reads a Service through the API server's service proxy with the
// cluster's own REST credentials (through NewGuardedProxyREST's client). No
// endpoint credential is ever added here; the tenant header is (C5 §4).
type Proxy struct {
	rest   rest.Interface
	svc    Service
	tenant string
	lim    Limits
}

// NewProxy builds a service-proxy transport.
func NewProxy(r rest.Interface, svc Service, tenant string, lim Limits) *Proxy {
	return &Proxy{rest: r, svc: svc, tenant: tenant, lim: lim}
}

// proxyRequest is the service-proxy request for path (beginning "/") under
// the Service's path prefix, with the query's repeated parameters in order.
func proxyRequest(r rest.Interface, svc Service, path string, q url.Values) *rest.Request {
	scheme := svc.Scheme
	if scheme == "" {
		scheme = "http"
	}
	name := scheme + ":" + svc.Name + ":" + svc.Port
	suffix := strings.Trim(svc.Path, "/")
	if suffix != "" {
		suffix += "/"
	}
	suffix += strings.TrimLeft(path, "/")
	req := r.Get().
		Namespace(svc.Namespace).
		Resource("services").
		Name(name).
		SubResource("proxy").
		Suffix(suffix)
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range q[k] {
			req = req.Param(k, v)
		}
	}
	return req
}

// Get reads path (beginning "/") under the Service's path prefix, streamed
// and capped. A non-2xx answer returns its status and no body: client-go
// folds it into a StatusError.
func (p *Proxy) Get(ctx context.Context, path string, q url.Values) (int, []byte, error) {
	req := proxyRequest(p.rest, p.svc, path, q)
	if p.tenant != "" {
		req = req.SetHeader("X-Scope-OrgID", p.tenant)
	}
	rc, err := req.Stream(ctx)
	if err != nil {
		var se *apierrors.StatusError
		if errors.As(err, &se) && se.Status().Code != 0 {
			return int(se.Status().Code), nil, nil
		}
		return 0, nil, err // a transport error, with no status
	}
	defer func() { _ = rc.Close() }()
	body, err := ReadCapped(rc, p.lim)
	if err != nil {
		return 0, nil, err
	}
	return 200, body, nil
}

// RawProxy reads a Service through the API server's service proxy and
// returns the status and the bounded body for any status (coverage S8b §9):
// Loki's 400 diagnosis is a plain-text body. client-go cannot carry it -
// Do(ctx) drops a non-2xx body it cannot decode before Result.Raw, and
// DoRaw overwrites a body-read error (the cap) with a status error - so the
// REST client only builds the URL, and the request runs through the guarded
// http.Client: the cluster's credentials and TLS, the same-origin redirect
// check, the cap. No endpoint credential is ever sent here (C5 §4).
type RawProxy struct {
	g      *GuardedProxy
	svc    Service
	tenant string
	lim    Limits
}

// NewRawProxy builds a raw service-proxy transport.
func NewRawProxy(g *GuardedProxy, svc Service, tenant string, lim Limits) *RawProxy {
	return &RawProxy{g: g, svc: svc, tenant: tenant, lim: lim}
}

// Get reads path with the query and the protocol headers. The body is read,
// capped, before the status is looked at: a body-read, cap or cancellation
// error takes precedence, and only a complete body is paired with its
// status, 2xx or not.
func (p *RawProxy) Get(ctx context.Context, path string, q url.Values, hdr http.Header) (int, []byte, error) {
	r := proxyRequest(p.g.REST, p.svc, path, q)
	if err := r.Error(); err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL().String(), nil)
	if err != nil {
		return 0, nil, err
	}
	setProtocolHeaders(req, hdr)
	req.Header.Set("Accept", "application/json, */*")
	if p.g.UserAgent != "" {
		req.Header.Set("User-Agent", p.g.UserAgent)
	}
	if p.tenant != "" {
		req.Header.Set("X-Scope-OrgID", p.tenant)
	}
	resp, err := p.g.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := ReadCapped(resp.Body, p.lim)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// setProtocolHeaders copies a caller's protocol headers. The credential and
// the tenant are the transport's alone: a caller's Authorization or
// X-Scope-OrgID is never sent.
func setProtocolHeaders(req *http.Request, hdr http.Header) {
	for k, vs := range hdr {
		switch http.CanonicalHeaderKey(k) {
		case "Authorization", "X-Scope-Orgid", "Proxy-Authorization", "Cookie":
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
}

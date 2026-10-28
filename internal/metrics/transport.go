package metrics

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/endpointhttp"
	"github.com/moomora/klyx/internal/safehttp"
)

// maxResponseBytes caps a Prometheus response read. Instant-query results are
// kilobytes; this guards against a hostile or misconfigured endpoint.
const maxResponseBytes = 4 << 20 // 4 MiB

// ErrResponseTooLarge: a response went past maxResponseBytes. It is an error,
// never a truncated body read as whole (coverage C5 §4).
var ErrResponseTooLarge = fmt.Errorf("the metrics endpoint's response exceeded %d MiB; Klyx stopped reading it", maxResponseBytes>>20)

// limits are the metrics endpoint's response bounds.
var limits = endpointhttp.Limits{MaxBytes: maxResponseBytes, TooLarge: ErrResponseTooLarge}

// DirectPolicy is the HTTP policy for a direct metrics endpoint: same-origin
// redirects only, the cap as an error, no environment proxy (C5 §4).
func DirectPolicy(timeout time.Duration, tlsSkipVerify bool) safehttp.Policy {
	return endpointhttp.DirectPolicy(limits, timeout, tlsSkipVerify)
}

// ErrCredentialChanged: the endpoint's credential changed or was removed
// after this client was built; it sends nothing more (C5 §7).
var ErrCredentialChanged = endpointhttp.ErrCredentialChanged

// directTransport queries an external Prometheus/Mimir base URL over HTTP(S)
// through endpointhttp's direct transport, which holds the credential: it
// redacts it from every error and sends nothing once the credential's
// revision has moved (C5 §5, §7).
type directTransport struct{ d *endpointhttp.Direct }

// NewDirectTransport builds a direct HTTP transport with a bearer token.
// The caller supplies the *http.Client (DirectPolicy's).
func NewDirectTransport(base, token string, client *http.Client) Transport {
	return NewDirectTransportAuth(base, BearerAuth(token), client)
}

// NewDirectTransportAuth builds a direct HTTP transport with an endpoint's
// resolved credential and tenant. With a revision gate, every round trip -
// the first request and each redirect hop - is refused once the
// credential's revision has moved (C5 §7).
func NewDirectTransportAuth(base string, auth Auth, client *http.Client) Transport {
	return Transport{q: &directTransport{d: endpointhttp.NewDirect(base, auth, client, limits)}}
}

func (t *directTransport) redactor() safehttp.Redactor { return t.d.Redactor() }

func (t *directTransport) InstantQuery(ctx context.Context, promql string) (int, []byte, error) {
	return t.d.Get(ctx, "/api/v1/query", url.Values{"query": {promql}})
}

func (t *directTransport) RangeQuery(ctx context.Context, promql string, start, end time.Time, step time.Duration) (int, []byte, error) {
	return t.d.Get(ctx, "/api/v1/query_range", rangeParams(promql, start, end, step))
}

func rangeParams(promql string, start, end time.Time, step time.Duration) url.Values {
	return url.Values{
		"query": {promql},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10)},
	}
}

// NewGuardedProxyREST is the CoreV1 REST client the metrics service-proxy
// path uses (C5 §4): endpointhttp's guarded client with the metrics cap.
func NewGuardedProxyREST(cfg *rest.Config) (rest.Interface, error) {
	return endpointhttp.NewGuardedProxyREST(cfg, limits)
}

// proxyTransport queries through the kube API-server service proxy using the
// cluster's existing REST credentials, through NewGuardedProxyREST's client.
// No endpoint credential is ever added here; the tenant header is (C5 §4).
type proxyTransport struct{ p *endpointhttp.Proxy }

// NewProxyTransport builds a proxy transport over a cluster REST client.
func NewProxyTransport(r rest.Interface, c ServiceCandidate) Transport {
	return NewProxyTransportTenant(r, c, "")
}

// NewProxyTransportTenant is NewProxyTransport with a tenant header.
func NewProxyTransportTenant(r rest.Interface, c ServiceCandidate, tenant string) Transport {
	svc := endpointhttp.Service{Namespace: c.Namespace, Name: c.Name, Port: c.Port, Scheme: schemeOr(c.Scheme), Path: c.Path}
	return Transport{q: &proxyTransport{p: endpointhttp.NewProxy(r, svc, tenant, limits)}}
}

func (t *proxyTransport) InstantQuery(ctx context.Context, promql string) (int, []byte, error) {
	return t.p.Get(ctx, "/api/v1/query", url.Values{"query": {promql}})
}

func (t *proxyTransport) RangeQuery(ctx context.Context, promql string, start, end time.Time, step time.Duration) (int, []byte, error) {
	return t.p.Get(ctx, "/api/v1/query_range", rangeParams(promql, start, end, step))
}

package metrics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	"github.com/moomora/klyx/internal/safehttp"
)

// maxResponseBytes caps a Prometheus response read. Instant-query results are
// kilobytes; this guards against a hostile or misconfigured endpoint.
const maxResponseBytes = 4 << 20 // 4 MiB

// ErrResponseTooLarge: a response went past maxResponseBytes. It is an error,
// never a truncated body read as whole (coverage C5 §4).
var ErrResponseTooLarge = fmt.Errorf("the metrics endpoint's response exceeded %d MiB; Klyx stopped reading it", maxResponseBytes>>20)

// DirectPolicy is the HTTP policy for a direct metrics endpoint: same-origin
// redirects only, the cap as an error, no environment proxy (C5 §4).
func DirectPolicy(timeout time.Duration, tlsSkipVerify bool) safehttp.Policy {
	return safehttp.Policy{Timeout: timeout, MaxBytes: maxResponseBytes, TooLarge: ErrResponseTooLarge, TLSSkipVerify: tlsSkipVerify}
}

// directTransport queries an external Prometheus/Mimir base URL over HTTP(S).
// It holds the credential, so it redacts it from everything it returns - the
// body before any parse quotes it, and every error (C5 §5).
type directTransport struct {
	base   string // Prometheus base URL, no trailing /api/v1
	token  string
	client *http.Client
	red    safehttp.Redactor
}

// NewDirectTransport builds a direct HTTP transport. The caller supplies the
// *http.Client (DirectPolicy's, with any TLS settings already applied).
func NewDirectTransport(base, token string, client *http.Client) Querier {
	return &directTransport{base: base, token: token, client: client, red: safehttp.NewRedactor(safehttp.Bearer(token))}
}

func (t *directTransport) InstantQuery(ctx context.Context, promql string) (int, []byte, error) {
	u := t.base + "/api/v1/query?query=" + url.QueryEscape(promql)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.do(req)
}

func (t *directTransport) RangeQuery(ctx context.Context, promql string, start, end time.Time, step time.Duration) (int, []byte, error) {
	u := t.base + "/api/v1/query_range?query=" + url.QueryEscape(promql) +
		"&start=" + strconv.FormatInt(start.Unix(), 10) +
		"&end=" + strconv.FormatInt(end.Unix(), 10) +
		"&step=" + strconv.FormatInt(int64(step.Seconds()), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.do(req)
}

// do sends one request and reads its capped body, redacted.
func (t *directTransport) do(req *http.Request) (int, []byte, error) {
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, nil, t.red.Err(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readCapped(resp.Body)
	return resp.StatusCode, t.red.ExactBytes(body), t.red.Err(err)
}

// readCapped reads at most maxResponseBytes; one byte more is
// ErrResponseTooLarge, never a short body.
func readCapped(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	return body, nil
}

// NewGuardedProxyREST is the CoreV1 REST client the service-proxy path uses
// (C5 §4). It is built from the cluster's rest.Config with its own
// http.Client whose CheckRedirect allows only the API server's own origin,
// never downgraded, at most five hops: client-go re-adds the kube
// credential on every redirected request, and CheckRedirect runs before the
// redirected request reaches that stack - so a refused redirect sends
// nothing, and no other origin ever sees the kube credential.
func NewGuardedProxyREST(cfg *rest.Config) (rest.Interface, error) {
	c := rest.CopyConfig(cfg)
	c.APIPath = "/api"
	c.GroupVersion = &schemeGroupVersionV1
	c.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	if c.UserAgent == "" {
		c.UserAgent = rest.DefaultKubernetesUserAgent()
	}
	hc, err := rest.HTTPClientFor(c)
	if err != nil {
		return nil, err
	}
	hc.CheckRedirect = safehttp.SameOriginRedirect(safehttp.MaxRedirects)
	return rest.RESTClientForConfigAndClient(c, hc)
}

// proxyTransport queries through the kube API-server service proxy using the
// cluster's existing REST credentials, through NewGuardedProxyREST's client.
type proxyTransport struct {
	rest rest.Interface
	c    ServiceCandidate
}

// NewProxyTransport builds a proxy transport over a cluster REST client.
func NewProxyTransport(r rest.Interface, c ServiceCandidate) Querier {
	return &proxyTransport{rest: r, c: c}
}

func (t *proxyTransport) InstantQuery(ctx context.Context, promql string) (int, []byte, error) {
	return t.get(ctx, "api/v1/query", [][2]string{{"query", promql}})
}

func (t *proxyTransport) RangeQuery(ctx context.Context, promql string, start, end time.Time, step time.Duration) (int, []byte, error) {
	return t.get(ctx, "api/v1/query_range", [][2]string{
		{"query", promql},
		{"start", strconv.FormatInt(start.Unix(), 10)},
		{"end", strconv.FormatInt(end.Unix(), 10)},
		{"step", strconv.FormatInt(int64(step.Seconds()), 10)},
	})
}

// get streams the proxied response and caps the read at maxResponseBytes -
// the same guard the direct transport has (Raw() would buffer an arbitrarily
// large body from a misbehaving in-cluster Prometheus into memory).
func (t *proxyTransport) get(ctx context.Context, suffix string, params [][2]string) (int, []byte, error) {
	name := schemeOr(t.c.Scheme) + ":" + t.c.Name + ":" + t.c.Port
	req := t.rest.Get().
		Namespace(t.c.Namespace).
		Resource("services").
		Name(name).
		SubResource("proxy").
		Suffix(suffix)
	for _, p := range params {
		req = req.Param(p[0], p[1])
	}
	rc, err := req.Stream(ctx)
	if err != nil {
		// Stream folds a non-2xx response into a StatusError; recover the code
		// so the Client keeps reporting "prometheus returned HTTP <n>".
		var se *apierrors.StatusError
		if errors.As(err, &se) && se.Status().Code != 0 {
			return int(se.Status().Code), nil, nil
		}
		return 0, nil, err // genuine transport error (no status)
	}
	defer func() { _ = rc.Close() }()
	body, err := readCapped(rc)
	if err != nil {
		return 0, nil, err
	}
	return 200, body, nil
}

// schemeGroupVersionV1 is the core API group's v1.
var schemeGroupVersionV1 = corev1.SchemeGroupVersion

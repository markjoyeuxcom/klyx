package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
)

// maxResponseBytes caps a Prometheus response read. Instant-query results are
// kilobytes; this guards against a hostile or misconfigured endpoint.
const maxResponseBytes = 4 << 20 // 4 MiB

// directTransport queries an external Prometheus/Mimir base URL over HTTP(S).
type directTransport struct {
	base   string // Prometheus base URL, no trailing /api/v1
	token  string
	client *http.Client
}

// NewDirectTransport builds a direct HTTP transport. The caller supplies the
// *http.Client (with any TLS settings already applied).
func NewDirectTransport(base, token string, client *http.Client) Querier {
	return &directTransport{base: base, token: token, client: client}
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
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	return resp.StatusCode, body, err
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
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	return resp.StatusCode, body, err
}

// proxyTransport queries through the kube API-server service proxy using the
// cluster's existing REST credentials.
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
	body, err := io.ReadAll(io.LimitReader(rc, maxResponseBytes))
	if err != nil {
		return 0, nil, err
	}
	return 200, body, nil
}

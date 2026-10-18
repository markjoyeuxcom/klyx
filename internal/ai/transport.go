package ai

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/moomora/klyx/internal/safehttp"
)

// maxResponseBytes caps every response body a provider reads - completions,
// model lists and error bodies alike - so a hostile or misconfigured endpoint
// cannot buffer an unbounded body into memory (spec §1.2; the same guard as
// internal/metrics). A timeout bounds time, not bytes.
const maxResponseBytes = 4 << 20 // 4 MiB

// maxRedirects bounds a redirect chain.
const maxRedirects = 5

// httpBackstop is the http.Client timeout behind each call's own deadline
// (Purpose budgets in budget.go): it only matters if a caller forgets one.
const httpBackstop = 3 * time.Minute

// ErrResponseTooLarge: a response body went past maxResponseBytes.
var ErrResponseTooLarge = fmt.Errorf("the AI endpoint's response exceeded %d MiB; Klyx stopped reading it", maxResponseBytes>>20)

// NewHTTPClient is the one HTTP client every provider uses: the response cap
// and the redirect policy are applied to it, and only an openaiCompatible
// endpoint may skip TLS verification (a self-signed local server).
func NewHTTPClient(tlsSkipVerify bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if tlsSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in, openaiCompatible only (config validation)
	}
	return Harden(&http.Client{Transport: tr, Timeout: httpBackstop})
}

// Harden returns a copy of c with the response cap on its transport and the
// redirect policy installed. The caller's client is not mutated.
func Harden(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{Timeout: httpBackstop}
	}
	hc := *c
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc.Transport = safehttp.Cap(base, maxResponseBytes, ErrResponseTooLarge)
	hc.CheckRedirect = checkRedirect
	return &hc
}

// checkRedirect refuses a redirect to another host (the egress policy covers
// the configured endpoint only, and a 307/308 re-sends the POST body - cluster
// context - wherever it points; review 2026-10-03 #14), a downgrade from HTTPS
// to HTTP (it would put a key and cluster context on the wire in clear), and a
// chain longer than maxRedirects.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	first := via[0].URL
	if !strings.EqualFold(req.URL.Host, first.Host) {
		return fmt.Errorf("refusing redirect from %s to %s: the AI egress policy covers the configured endpoint only", first.Host, req.URL.Host)
	}
	if strings.EqualFold(first.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return fmt.Errorf("refusing redirect from HTTPS to %s", req.URL.Scheme)
	}
	if len(via) >= maxRedirects {
		return errors.New("too many redirects")
	}
	return nil
}

// IsLoopbackEndpoint reports whether baseURL points at THIS machine (localhost
// or a loopback IP). Only such an endpoint keeps data on the machine: an
// openaiCompatible server on the LAN or a hosted gateway still sends cluster
// context off-machine. No DNS lookup: a name other than localhost is remote.
func IsLoopbackEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// readBody reads a capped response body; a non-2xx status is an error that
// carries a short, scrubbed excerpt of the body.
func readBody(resp *http.Response, secrets ...string) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPStatusError{Code: resp.StatusCode, Body: Scrub(string(body), secrets...)}
	}
	return body, nil
}

// HTTPStatusError is a non-2xx response: the status and a scrubbed body. It
// lets a caller positively identify one failure (a server that does not
// support tools) without parsing message text.
type HTTPStatusError struct {
	Code int
	Body string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("AI endpoint returned HTTP %d: %s", e.Code, e.Body)
}

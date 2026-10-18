// Package safehttp is the HTTP policy for every endpoint Klyx reads outside
// the cluster's own API (coverage C5 §4): redirects only to the same
// canonical origin and never downgraded, a response cap that errors rather
// than truncating, no environment proxy unless a caller asks for it, and a
// redactor that keeps a request's credential out of everything said about
// the request (§5).
package safehttp

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

	"golang.org/x/net/idna"
)

// MaxRedirects bounds a redirect chain.
const MaxRedirects = 5

// CanonicalOrigin is a URL's origin as Klyx compares origins: the scheme in
// lower case (http or https only), the host lower-cased and IDNA-normalised
// to its ASCII form (an IPv6 address in brackets), and the effective port
// (443 for https, 80 for http when none is written). A URL without one is an
// error: nothing is sent to it.
func CanonicalOrigin(u *url.URL) (string, error) {
	if u == nil {
		return "", errors.New("no URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("the scheme %q is not http or https", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("the URL names no host")
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	} else {
		ascii, err := idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
		if err != nil {
			return "", fmt.Errorf("the host is not a valid name: %v", err)
		}
		host = strings.ToLower(ascii)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[scheme]
	}
	return scheme + "://" + host + ":" + port, nil
}

// OriginOf is CanonicalOrigin for a URL string.
func OriginOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("the URL does not parse: %v", err)
	}
	return CanonicalOrigin(u)
}

// SameOriginRedirect allows a redirect only to the first request's canonical
// origin (which also refuses any downgrade: the scheme is part of the
// origin), at most max hops. It runs before the redirected request is handed
// to the transport, so a refused redirect sends nothing anywhere.
func SameOriginRedirect(max int) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		from, err := CanonicalOrigin(via[0].URL)
		if err != nil {
			return fmt.Errorf("refusing a redirect: %v", err)
		}
		to, err := CanonicalOrigin(req.URL)
		if err != nil || to != from {
			return fmt.Errorf("refusing a redirect from %s to another origin; Klyx does not follow it", from)
		}
		if len(via) >= max {
			return errors.New("too many redirects")
		}
		return nil
	}
}

// Policy configures a client for one kind of endpoint.
type Policy struct {
	Timeout       time.Duration
	MaxBytes      int64
	TooLarge      error // the error a body past MaxBytes reads as
	TLSSkipVerify bool  // the user's explicit opt-in
	// EnvProxy keeps the environment's HTTP proxy (AI's behaviour);
	// endpoint readers leave it off, as metrics always has.
	EnvProxy bool
}

// NewClient is the client for an external endpoint: a fresh transport (no
// environment proxy unless asked), the cap, and same-origin redirects only.
func NewClient(p Policy) *http.Client {
	tr := &http.Transport{}
	if p.EnvProxy {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	if p.TLSSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // the user's explicit opt-in
	}
	return &http.Client{
		Transport:     Cap(tr, p.MaxBytes, p.TooLarge),
		Timeout:       p.Timeout,
		CheckRedirect: SameOriginRedirect(MaxRedirects),
	}
}

// Cap wraps a transport so every response body fails - rather than silently
// truncating - once it passes max bytes.
func Cap(base http.RoundTripper, max int64, tooLarge error) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if tooLarge == nil {
		tooLarge = fmt.Errorf("the response exceeded %d bytes; Klyx stopped reading it", max)
	}
	return cappedTransport{base: base, max: max, tooLarge: tooLarge}
}

type cappedTransport struct {
	base     http.RoundTripper
	max      int64
	tooLarge error
}

func (t cappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &cappedBody{rc: resp.Body, remaining: t.max, tooLarge: t.tooLarge}
	return resp, nil
}

type cappedBody struct {
	rc        io.ReadCloser
	remaining int64
	tooLarge  error
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		// At the cap: one more byte means the body is over it.
		var probe [1]byte
		n, err := b.rc.Read(probe[:])
		if n > 0 {
			return 0, b.tooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.rc.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *cappedBody) Close() error { return b.rc.Close() }

package fleetindex

import (
	"errors"
	"net/http"

	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
)

// HeaderBytes bounds a response's headers on the index's transport: an API
// server's are about a kilobyte; Go's default (10 MiB) would hold far more
// than a stream's reserve.
const HeaderBytes = 64 << 10

// NewReader builds the index's read path for a cluster from its REST config
// (review of 9a): a copy with no client timeout (every read is bounded by
// its context) and HTTP/1.1 only. Over HTTP/2 the transport buffers up to
// its per-stream receive window (4 MiB in client-go's transport) ahead of
// the reader, beyond the stream reserve; over HTTP/1.1 a connection holds
// fixed buffers and the socket applies backpressure. The base transport is
// the index's own clone of client-go's, with response headers bounded by
// HeaderBytes (an oversized header fails the request before a watch is
// opened), and the reader closes its idle connections after every stream,
// so only open streams - each admitted - hold buffers.
func NewReader(rc *rest.Config) (*Reader, error) {
	c := rest.CopyConfig(rc)
	c.Timeout = 0
	c.NextProtos = []string{"http/1.1"}
	bounded := false
	own := func(rt http.RoundTripper) http.RoundTripper {
		// the cache hands back its transport inside a tracking wrapper
		base := rt
		for {
			w, ok := base.(utilnet.RoundTripperWrapper)
			if !ok {
				break
			}
			base = w.WrappedRoundTripper()
		}
		t, ok := base.(*http.Transport)
		if !ok {
			return rt
		}
		// a clone: client-go's transport is cached and may be shared, so it
		// is never mutated
		t = t.Clone()
		t.MaxResponseHeaderBytes = HeaderBytes
		bounded = true
		return t
	}
	c.WrapTransport = transport.Wrappers(own, c.WrapTransport)
	hc, err := rest.HTTPClientFor(c)
	if err != nil {
		return nil, err
	}
	if !bounded {
		return nil, errors.New("fleetindex: the cluster's transport cannot be bounded (a custom transport)")
	}
	base, _, err := rest.DefaultServerUrlFor(c)
	if err != nil {
		return nil, err
	}
	return &Reader{Client: hc, Host: base.String()}, nil
}

// closeIdle drops the client's idle connections once a stream ends. The
// client's transport is client-go's chain of wrappers, which
// http.Client.CloseIdleConnections does not see through.
func (r *Reader) closeIdle() { utilnet.CloseIdleConnectionsFor(r.Client.Transport) }

package fleetindex

import (
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/rest"
)

// NewReader builds the index's read path for a cluster from its REST config
// (review of 9a): a copy with no client timeout (every read is bounded by
// its context) and HTTP/1.1 only. Over HTTP/2 the transport buffers up to
// its per-stream receive window (4 MiB in client-go's transport) ahead of
// the reader, beyond the stream reserve; over HTTP/1.1 a connection holds
// fixed buffers and the socket applies backpressure. The client is the
// index's own, and the reader closes its idle connections after every
// stream, so only open streams - each admitted - hold buffers.
func NewReader(rc *rest.Config) (*Reader, error) {
	c := rest.CopyConfig(rc)
	c.Timeout = 0
	c.NextProtos = []string{"http/1.1"}
	hc, err := rest.HTTPClientFor(c)
	if err != nil {
		return nil, err
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

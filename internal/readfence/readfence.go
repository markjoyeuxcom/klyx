// Package readfence carries a read's fence in its context (AI A1
// implementation review, finding 3). A composite read - a drawing, a Git
// comparison, a Loki page - makes several component reads, and the fence is
// checked before each of them, not once before the composite starts: the
// cluster's HTTP transports check it before every request, a redirect's hop
// included, and readers that are not HTTP (Git, the confined build) check
// it before each step. A context without a fence reads as before.
package readfence

import (
	"context"
	"net/http"
)

type key struct{}

// With returns ctx carrying check, which runs before each component read.
// A fence already on ctx runs first.
func With(ctx context.Context, check func() error) context.Context {
	if check == nil {
		return ctx
	}
	if outer, ok := ctx.Value(key{}).(func() error); ok {
		inner := check
		check = func() error {
			if err := outer(); err != nil {
				return err
			}
			return inner()
		}
	}
	return context.WithValue(ctx, key{}, check)
}

// Check runs ctx's fence: nil when it holds, or when ctx has none.
func Check(ctx context.Context) error {
	if f, ok := ctx.Value(key{}).(func() error); ok {
		return f()
	}
	return nil
}

// Transport checks the request's fence before each round trip.
func Transport(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return transport{rt: rt}
}

type transport struct{ rt http.RoundTripper }

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := Check(req.Context()); err != nil {
		return nil, err
	}
	return t.rt.RoundTrip(req)
}

// WrappedRoundTripper lets client-go reach the transport beneath (its TLS
// and dialer), as its own wrappers do.
func (t transport) WrappedRoundTripper() http.RoundTripper { return t.rt }

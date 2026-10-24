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
	"errors"
	"net/http"
	"sync/atomic"
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

// Error is a fence that no longer holds: the read was not made. It wraps
// the fence's own error.
type Error struct{ Err error }

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Check runs ctx's fence: nil when it holds, or when ctx has none, else an
// *Error.
func Check(ctx context.Context) error {
	if f, ok := ctx.Value(key{}).(func() error); ok {
		if err := f(); err != nil {
			return &Error{Err: err}
		}
	}
	return nil
}

// Failed says whether err, however wrapped, is a fence that no longer
// holds, so a caller reports it as such and never as the read's own
// failure.
func Failed(err error) bool {
	var e *Error
	return errors.As(err, &e)
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
	mutating := req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodOptions
	wt, _ := req.Context().Value(writeKey{}).(*writeTracker)
	if err := Check(req.Context()); err != nil {
		if mutating && wt != nil && !wt.forwarded.Load() {
			return nil, notForwarded{err}
		}
		return nil, err
	}
	if mutating && wt != nil {
		wt.forwarded.Store(true)
	}
	return t.rt.RoundTrip(req)
}

// ErrNotForwarded marks a fence's refusal of a write when no write under
// the same tracker was forwarded before it (AI A3 stage 1 review, finding
// 5): nothing of that write can have reached the server. A refusal after a
// write was forwarded - a retry, a redirect's hop - is not marked: the
// earlier send may have landed.
var ErrNotForwarded = errors.New("the write was not sent")

type writeKey struct{}

type writeTracker struct{ forwarded atomic.Bool }

// WithWriteTracker returns ctx tracking whether a write made under it was
// forwarded past the fence.
func WithWriteTracker(ctx context.Context) context.Context {
	return context.WithValue(ctx, writeKey{}, &writeTracker{})
}

// notForwarded keeps the fence's *Error and adds ErrNotForwarded.
type notForwarded struct{ err error }

func (e notForwarded) Error() string   { return e.err.Error() }
func (e notForwarded) Unwrap() []error { return []error{e.err, ErrNotForwarded} }

// WrappedRoundTripper lets client-go reach the transport beneath (its TLS
// and dialer), as its own wrappers do.
func (t transport) WrappedRoundTripper() http.RoundTripper { return t.rt }

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
// the fence's own error, and is ErrRefused.
type Error struct{ Err error }

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Is makes every refusal ErrRefused, which a redactor can keep by identity
// where it keeps no type (Settings S3b-2b re-review 2, R2-1).
func (e *Error) Is(target error) bool { return target == ErrRefused }

// ErrRefused is what every fence's refusal is, however it is wrapped or
// redacted.
var ErrRefused = errors.New("the read's fence no longer holds")

// Check runs ctx's fence: nil when it holds, or when ctx has none, else an
// *Error - marked ErrNotForwarded when ctx tracks a write and nothing of
// it was forwarded yet, as a refused request is (a discovery read before
// the write: Settings F1 review, findings F2 and F6).
func Check(ctx context.Context) error {
	if f, ok := ctx.Value(key{}).(func() error); ok {
		if err := f(); err != nil {
			wt, _ := ctx.Value(writeKey{}).(*writeTracker)
			return refusal(wt, &Error{Err: err})
		}
	}
	return nil
}

// Failed says whether err, however wrapped, is a fence that no longer
// holds, so a caller reports it as such and never as the read's own
// failure.
func Failed(err error) bool {
	var e *Error
	return errors.As(err, &e) || errors.Is(err, ErrRefused)
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
	if err := Check(req.Context()); err != nil {
		return nil, err
	}
	if wt, _ := req.Context().Value(writeKey{}).(*writeTracker); mutating && wt != nil {
		wt.forwarded.Store(true)
	}
	return t.rt.RoundTrip(req)
}

// ErrNotForwarded marks a fence's refusal under a write tracker when no
// write under it was forwarded before (AI A3 stage 1 review, finding 5):
// nothing of that write can have reached the server. The refused request
// may be the write itself or a read before it - a refresh's GET of its
// object (Settings F1 review, finding F6). A refusal after a write was
// forwarded - a retry, a redirect's hop - is not marked: the earlier send
// may have landed.
var ErrNotForwarded = errors.New("the write was not sent")

// refusal marks a fence's refusal under an unforwarded tracker; without a
// tracker nothing says whether an earlier attempt was sent.
func refusal(wt *writeTracker, err *Error) error {
	if wt != nil && !wt.forwarded.Load() {
		return notForwarded{err}
	}
	return err
}

// Send checks ctx's fence before a send that is not an HTTP request - a
// child process's start, a prompt's handoff on its stdin - and, when it
// holds, records on ctx's tracker that the send went ahead. A refusal is
// marked as the transport marks one: ErrNotForwarded when nothing under
// the tracker was forwarded before it.
func Send(ctx context.Context) error {
	if err := Check(ctx); err != nil {
		return err
	}
	if wt, _ := ctx.Value(writeKey{}).(*writeTracker); wt != nil {
		wt.forwarded.Store(true)
	}
	return nil
}

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

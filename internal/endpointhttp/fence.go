package endpointhttp

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/moomora/klyx/internal/readfence"
)

// Settings S3b §3.1: a read through an installation is fenced on every
// request, whatever the endpoint speaks.

// Fence is what a read through an installation does around each request.
type Fence struct {
	// Check is run before and after each request: the read's terms still
	// in force (its subscription, the installation's access generation,
	// the host's pinned connection, the credential's revision). An error
	// discards the request.
	Check func() error
	// Slot takes one of the host's request slots for the request and its
	// body, waiting within the request's context; nil for a direct
	// installation, which takes none.
	Slot func(ctx context.Context) (release func(), err error)
	// Life is the host lease's context: its end cancels the request. Nil
	// for a direct installation.
	Life context.Context
	// Denied records an observed 401 or 403 before the request returns:
	// the installation's access generation moves.
	Denied func()
}

// Do sends one request inside the fence: a host slot waited for within
// ctx and held until send has returned (the request and its body read),
// the check before and after, a 401 or 403 recorded, the lease's end
// cancelling it. The check also runs before every round trip the request
// makes - a redirect's hop, a client's retry - through its context's read
// fence, which the transports check (Codex's review of S3b-2b, R1); a
// request that fails across a move says the move. A 401 or 403 is recorded
// once its headers arrive, whatever its body does (R3).
func (f Fence) Do(ctx context.Context, send func(context.Context) (int, []byte, error)) (int, []byte, error) {
	if f.Life != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(f.Life, cancel)()
	}
	if f.Slot != nil {
		release, err := f.Slot(ctx)
		if err != nil {
			return 0, nil, err
		}
		defer release()
	}
	if err := f.Check(); err != nil {
		return 0, nil, err
	}
	seen := &observed{}
	code, body, err := send(readfence.With(context.WithValue(ctx, observedKey{}, seen), f.Check))
	if (seen.denied.Load() || code == http.StatusUnauthorized || code == http.StatusForbidden) && f.Denied != nil {
		f.Denied()
	}
	if err != nil {
		if cerr := f.Check(); cerr != nil {
			return 0, nil, cerr
		}
		return code, body, err
	}
	if err := f.Check(); err != nil {
		return 0, nil, err
	}
	return code, body, nil
}

// observed is what the transports saw of a fenced request's responses: a
// 401 or 403, recorded when its headers arrive, before its body is read.
type observed struct{ denied atomic.Bool }

type observedKey struct{}

// Observe records, on a fenced request's context, a 401 or 403 the round
// trip answered, whatever its body then does (Codex's review of S3b-2b,
// R3). A request without a fence is unaffected.
func Observe(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return observing{rt: rt}
}

type observing struct{ rt http.RoundTripper }

func (t observing) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.rt.RoundTrip(req)
	if err == nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		if o, ok := req.Context().Value(observedKey{}).(*observed); ok {
			o.denied.Store(true)
		}
	}
	return resp, err
}

// WrappedRoundTripper lets client-go reach the transport beneath, as its
// own wrappers do.
func (t observing) WrappedRoundTripper() http.RoundTripper { return t.rt }

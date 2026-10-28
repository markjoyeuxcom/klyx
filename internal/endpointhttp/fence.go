package endpointhttp

import (
	"context"
	"net/http"
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
// cancelling it.
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
	code, body, err := send(ctx)
	if err == nil && (code == http.StatusUnauthorized || code == http.StatusForbidden) && f.Denied != nil {
		f.Denied()
	}
	if err != nil {
		return code, body, err
	}
	if err := f.Check(); err != nil {
		return 0, nil, err
	}
	return code, body, nil
}

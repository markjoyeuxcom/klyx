package metrics

import (
	"context"
	"errors"
	"time"

	"github.com/moomora/klyx/internal/endpointhttp"
	"github.com/moomora/klyx/internal/safehttp"
)

// Settings S3b §3.1: a read through an installation is fenced on every
// request. The wrapper lives here so the transport it wraps keeps its
// redactor.

// Fence is what a read through an installation does around each request.
type Fence = endpointhttp.Fence

var errNoRange = errors.New("transport does not support range queries")

// Fenced is t with f around every request.
func Fenced(t Transport, f Fence) Transport { return Transport{q: &fenced{q: t.q, f: f}} }

type fenced struct {
	q Querier
	f Fence
}

func (t *fenced) redactor() safehttp.Redactor {
	if r, ok := t.q.(credentialed); ok {
		return r.redactor()
	}
	return safehttp.Redactor{}
}

func (t *fenced) InstantQuery(ctx context.Context, promql string) (int, []byte, error) {
	return t.f.Do(ctx, func(ctx context.Context) (int, []byte, error) { return t.q.InstantQuery(ctx, promql) })
}

func (t *fenced) RangeQuery(ctx context.Context, promql string, start, end time.Time, step time.Duration) (int, []byte, error) {
	rq, ok := t.q.(RangeQuerier)
	if !ok {
		return 0, nil, errNoRange
	}
	return t.f.Do(ctx, func(ctx context.Context) (int, []byte, error) { return rq.RangeQuery(ctx, promql, start, end, step) })
}

package observe

import (
	"context"
	"reflect"
	"time"
)

// SnapshotSpec is a bounded periodic read (Helm, Events): never live, a
// named cadence and read deadline, single-flight.
type SnapshotSpec struct {
	Name    string
	Scope   string
	Cadence time.Duration
	Timeout time.Duration
	// Read returns the items (already projected) and whether they were capped.
	Read func(ctx context.Context) (items []any, truncated bool, err error)
}

type snapState struct {
	spec  SnapshotSpec
	refs  int
	gen   *snapGen
	parts []partKey
}

type snapGen struct {
	o       *Observer
	ss      *snapState
	ctx     context.Context
	cancel  context.CancelFunc
	items   []any
	fresh   freshness
	refresh chan struct{}
	// offered restarts the cadence after an Offer; epoch counts offers, so
	// a read that started before one is known to hold older evidence.
	offered chan struct{}
	epoch   uint64
}

// startSnap starts a generation. Caller holds o.mu.
func (o *Observer) startSnap(ss *snapState) {
	ctx, cancel := context.WithCancel(o.ctx)
	g := &snapGen{o: o, ss: ss, ctx: ctx, cancel: cancel, fresh: freshness{cov: Checking}, refresh: make(chan struct{}, 1), offered: make(chan struct{}, 1)}
	ss.gen = g
	o.markSnapLocked(ss)
	o.goSafe(func() { g.run() })
}

// stopSnap ends the generation. Caller holds o.mu.
func (o *Observer) stopSnap(ss *snapState) {
	if ss.gen == nil {
		return
	}
	ss.gen.cancel()
	ss.gen = nil
	o.markSnapLocked(ss)
}

// run reads at once, then every cadence or on a refresh request. A request
// that arrives during a read is coalesced into one follow-up read. An Offer
// counts as a read: the cadence restarts from it.
func (g *snapGen) run() {
	for {
		g.read()
	wait:
		for {
			t := g.o.clk.NewTimer(g.ss.spec.Cadence)
			select {
			case <-g.ctx.Done():
				t.Stop()
				return
			case <-t.C():
				break wait
			case <-g.refresh:
				t.Stop()
				break wait
			case <-g.offered:
				t.Stop()
			}
		}
	}
}

// read runs one bounded read. Read must return promptly once its context
// is cancelled; its worker is tracked, so Observer.Done waits for it. A read
// that misses its deadline is recorded as a timeout at once, and no further
// read starts until that worker has returned - single-flight even across a
// timeout (Go cannot kill a callback; Klyx never overlaps one).
func (g *snapGen) read() {
	rctx, cancel := context.WithCancel(g.ctx)
	defer cancel()
	g.o.mu.Lock()
	epoch := g.epoch
	g.o.mu.Unlock()
	type result struct {
		items     []any
		truncated bool
		err       error
	}
	done := make(chan result, 1)
	g.o.goSafe(func() {
		items, truncated, err := g.ss.spec.Read(rctx)
		done <- result{items, truncated, err}
	})
	t := g.o.clk.NewTimer(g.ss.spec.Timeout)
	defer t.Stop()
	var r result
	timedOut := false
	select {
	case <-g.ctx.Done():
		return
	case r = <-done:
	case <-t.C():
		r, timedOut = result{err: context.DeadlineExceeded}, true
	}
	g.apply(epoch, r.items, r.truncated, r.err)
	if timedOut {
		cancel()
		select { // the abandoned worker must finish before the next read
		case <-done:
		case <-g.ctx.Done():
		}
	}
}

func (g *snapGen) apply(epoch uint64, items []any, truncated bool, err error) {
	o := g.o
	o.mu.Lock()
	defer o.mu.Unlock()
	if g.ss.gen != g || g.ctx.Err() != nil {
		return
	}
	if epoch != g.epoch {
		return // an Offer landed during this read: its evidence is newer
	}
	g.applyLocked(items, truncated, err)
}

// Offer hands a snapshot a successful read made elsewhere - the Helm lens's
// own helm list - as if its own read had just returned, so one exec serves
// the lens and the summary, which then share a snapshot. A read already in
// flight started earlier: its result is dropped, never applied over the
// offer. The cadence restarts from the offer. No-op when the snapshot is
// not running.
func (o *Observer) Offer(snapshot string, items []any, truncated bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ss := o.snaps[snapshot]
	if ss == nil || ss.gen == nil || ss.gen.ctx.Err() != nil {
		return
	}
	g := ss.gen
	g.epoch++
	g.applyLocked(items, truncated, nil)
	select {
	case g.offered <- struct{}{}:
	default:
	}
}

// applyLocked records one read's outcome. Caller holds o.mu.
func (g *snapGen) applyLocked(items []any, truncated bool, err error) {
	o := g.o
	now := o.clk.Now()
	if err != nil {
		class := classify(err)
		msg := err.Error()
		if err == context.DeadlineExceeded {
			msg = "read did not finish within " + g.ss.spec.Timeout.String()
		}
		switch g.fresh.cov {
		case Live:
			g.fresh.cov, g.fresh.lastGoodAt = Stale, g.fresh.readAt
		case Checking:
			g.fresh.cov = Unreadable
		}
		g.fresh.class, g.fresh.err = class, msg
		o.markSnapLocked(g.ss)
		return
	}
	if !reflect.DeepEqual(g.items, items) || g.fresh.truncated != truncated {
		g.items = items
		o.bumpSnapLocked(g.ss)
	}
	// A successful read advances freshness even when nothing changed.
	g.fresh = freshness{cov: Live, readAt: &now, truncated: truncated}
	o.markSnapLocked(g.ss)
}

// overdue turns a live snapshot whose last read is older than 2x its
// cadence stale. Called from the loop's boundary check; caller holds o.mu.
func (g *snapGen) overdue(now time.Time) bool {
	if g.fresh.cov != Live || g.fresh.readAt == nil {
		return false
	}
	if now.Before(g.fresh.readAt.Add(2 * g.ss.spec.Cadence)) {
		return false
	}
	g.fresh.cov, g.fresh.lastGoodAt = Stale, g.fresh.readAt
	g.fresh.class, g.fresh.err = ClassOverdue, "no successful read for 2x its cadence"
	return true
}

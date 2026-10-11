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
}

// startSnap starts a generation. Caller holds o.mu.
func (o *Observer) startSnap(ss *snapState) {
	ctx, cancel := context.WithCancel(o.ctx)
	g := &snapGen{o: o, ss: ss, ctx: ctx, cancel: cancel, fresh: freshness{cov: Checking}, refresh: make(chan struct{}, 1)}
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
// that arrives during a read is coalesced into one follow-up read.
func (g *snapGen) run() {
	for {
		g.read()
		t := g.o.clk.NewTimer(g.ss.spec.Cadence)
		select {
		case <-g.ctx.Done():
			t.Stop()
			return
		case <-t.C():
		case <-g.refresh:
			t.Stop()
		}
	}
}

// read runs one bounded read. A hung read is abandoned at its deadline and
// recorded as a timeout, so the snapshot never stays checking indefinitely.
func (g *snapGen) read() {
	rctx, cancel := context.WithCancel(g.ctx)
	defer cancel()
	type result struct {
		items     []any
		truncated bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		items, truncated, err := g.ss.spec.Read(rctx)
		done <- result{items, truncated, err}
	}()
	t := g.o.clk.NewTimer(g.ss.spec.Timeout)
	defer t.Stop()
	var r result
	select {
	case <-g.ctx.Done():
		return
	case r = <-done:
	case <-t.C():
		r = result{err: context.DeadlineExceeded}
	}

	o := g.o
	o.mu.Lock()
	defer o.mu.Unlock()
	if g.ss.gen != g || g.ctx.Err() != nil {
		return
	}
	now := o.clk.Now()
	if r.err != nil {
		class := classify(r.err)
		msg := r.err.Error()
		if r.err == context.DeadlineExceeded {
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
	if !reflect.DeepEqual(g.items, r.items) || g.fresh.truncated != r.truncated {
		g.items = r.items
		o.bumpSnapLocked(g.ss)
	}
	// A successful read advances freshness even when nothing changed.
	g.fresh = freshness{cov: Live, readAt: &now, truncated: r.truncated}
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

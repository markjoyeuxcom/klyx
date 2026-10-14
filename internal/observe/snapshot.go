package observe

import (
	"context"
	"errors"
	"fmt"
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
	// waiters are ReadSnapshot requests owed the next read to START (under
	// o.mu): a read already in flight began before them.
	waiters []chan SnapshotRead
	// id is the generation's identity; epoch advances on every failed or
	// overdue read (slice 2b §2): the next good read follows a gap.
	id    uint64
	epoch uint64
}

// SnapshotRead is one read's outcome as ReadSnapshot returns it - the
// snapshot's own outcome, already applied to its freshness.
type SnapshotRead struct {
	Items     []any
	Truncated bool
	Err       error
}

// ErrSnapshotNotRunning: the snapshot has no running generation (the
// Observer is not started, or has stopped). The caller reads directly.
var ErrSnapshotNotRunning = errors.New("snapshot is not running")

// startSnap starts a generation. Caller holds o.mu.
func (o *Observer) startSnap(ss *snapState) {
	ctx, cancel := context.WithCancel(o.ctx)
	o.kindGenSeq++
	g := &snapGen{o: o, ss: ss, ctx: ctx, cancel: cancel, fresh: freshness{cov: Checking}, refresh: make(chan struct{}, 1), id: o.kindGenSeq}
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

// ReadSnapshot is the one way a caller outside the Observer reads a
// snapshot's source (the Helm lens's helm list): it asks for a read and
// waits for its outcome, so every read of the source - cadence, Refresh and
// lens - is the snapshot's own single-flight read, applied in order, and a
// failure opens the snapshot's freshness gap like any other. The request is
// served by the next read to START after it (a read in flight began earlier
// and is older evidence); requests coalesce into that read. ctx bounds only
// the caller's wait: the read itself belongs to the snapshot and is applied
// whether or not anyone still waits.
func (o *Observer) ReadSnapshot(ctx context.Context, snapshot string) (SnapshotRead, error) {
	reply := make(chan SnapshotRead, 1)
	o.mu.Lock()
	ss := o.snaps[snapshot]
	if ss == nil || ss.gen == nil || ss.gen.ctx.Err() != nil {
		o.mu.Unlock()
		return SnapshotRead{}, ErrSnapshotNotRunning
	}
	g := ss.gen
	g.waiters = append(g.waiters, reply)
	select { // under the lock, with the append: read() takes both together
	case g.refresh <- struct{}{}:
	default:
	}
	o.mu.Unlock()
	select {
	case r := <-reply:
		return r, nil
	case <-ctx.Done():
		return SnapshotRead{}, ctx.Err()
	case <-g.ctx.Done():
		return SnapshotRead{}, ErrSnapshotNotRunning
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
	// The requests made so far are served by this read; so is any pending
	// refresh, which must not cause a second read for the same requests.
	g.o.mu.Lock()
	waiters := g.waiters
	g.waiters = nil
	select {
	case <-g.refresh:
	default:
	}
	g.o.mu.Unlock()
	done := make(chan SnapshotRead, 1)
	g.o.goSafe(func() {
		items, truncated, err := g.ss.spec.Read(rctx)
		done <- SnapshotRead{items, truncated, err}
	})
	t := g.o.clk.NewTimer(g.ss.spec.Timeout)
	defer t.Stop()
	var r SnapshotRead
	timedOut := false
	select {
	case <-g.ctx.Done():
		return
	case r = <-done:
	case <-t.C():
		r, timedOut = SnapshotRead{Err: fmt.Errorf("read did not finish within %s: %w", g.ss.spec.Timeout, context.DeadlineExceeded)}, true
	}
	if g.apply(r) {
		for _, w := range waiters {
			w <- r // buffered: a waiter that gave up never blocks the read
		}
	}
	if timedOut {
		cancel()
		select { // the abandoned worker must finish before the next read
		case <-done:
		case <-g.ctx.Done():
		}
	}
}

// apply records one read's outcome; false when the generation has ended.
func (g *snapGen) apply(r SnapshotRead) bool {
	o := g.o
	o.mu.Lock()
	defer o.mu.Unlock()
	if g.ss.gen != g || g.ctx.Err() != nil {
		return false
	}
	now := o.clk.Now()
	if r.Err != nil {
		g.epoch++
		switch g.fresh.cov {
		case Live:
			g.fresh.cov, g.fresh.lastGoodAt = Stale, g.fresh.readAt
		case Checking:
			g.fresh.cov = Unreadable
		}
		g.fresh.class, g.fresh.err = classify(r.Err), r.Err.Error()
		o.markSnapLocked(g.ss)
		return true
	}
	if !reflect.DeepEqual(g.items, r.Items) || g.fresh.truncated != r.Truncated {
		g.items = r.Items
		o.bumpSnapLocked(g.ss)
	}
	// A successful read advances freshness even when nothing changed.
	g.fresh = freshness{cov: Live, readAt: &now, truncated: r.Truncated}
	o.markSnapLocked(g.ss)
	return true
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
	g.epoch++
	return true
}

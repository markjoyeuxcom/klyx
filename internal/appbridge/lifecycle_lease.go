package appbridge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/policy"
)

// Leases (Settings S2 §2.3): a connection is never looked up and used
// later. Every piece of work on a cluster takes a lease, as one step under
// the lifecycle's lock: admitted only now, it pins the cluster's current
// connection and is registered, pending, before any I/O. Retiring that
// connection ends its leases - their contexts cancelled and their workers
// told why - unless the work continues (a drain, a rollback, a confirmed
// A3 request), which is listed under Continues and runs to its end. A
// retired lease cannot settle, so a worker that finishes opening after the
// retirement closes what it opened.

// Work is one piece of work on a cluster, as a plan names it.
type Work struct {
	Kind string // forward | logs | events | log-history | chat | investigation | step | explain | suggest-fix | drain | rollback
	What string // "a port-forward to shop/web, opening"
	// Continues: the connection's retirement does not stop it; it is
	// listed under a plan's Continues and runs to its end.
	Continues bool
	// End is told why the work ended when its connection is retired; it
	// runs once, outside every lock, and must not block. Nil for work that
	// ends through its context alone, and reads EndedWhy. End may run
	// before Acquire has returned: it must find its work by an identity the
	// worker made before acquiring, never by the lease it is given.
	End func(why string)
}

// ErrRetired is a lease whose connection was retired: the work it opened
// must be closed.
var ErrRetired = errors.New("the cluster's connection was replaced or removed while this was opening")

// Lease is one admitted piece of work on a cluster's current connection.
type Lease struct {
	life    *Lifecycle
	n       uint64
	Cluster string
	// Conn is the pinned connection; a worker asserts its own interface.
	Conn   any
	ConnID fleet.ConnID
	// Ctx is the work's context: cancelled when its connection is retired,
	// unless the work continues.
	Ctx    context.Context
	cancel context.CancelFunc

	// guarded by life.lmu
	work    Work
	started time.Time
	settled bool
	retired bool
	why     string // why its connection ended it
}

// PlainLease is a lease outside any lifecycle - tools and tests - on conn,
// with its own context: it never retires, and Settle and Release are
// no-ops.
func PlainLease(cluster string, conn any) *Lease {
	ctx, cancel := context.WithCancel(context.Background())
	return &Lease{Cluster: cluster, Conn: conn, Ctx: ctx, cancel: cancel}
}

// Acquire admits one piece of work on cluster, now (S2 §2.3): refused
// unless the cluster is Active and has a current connection. It pins that
// connection and registers the work, pending, before any I/O. The work's
// context is a new root, which the retirement cancels; a continuing work's
// is never cancelled by it.
func (l *Lifecycle) Acquire(cluster string, w Work) (*Lease, error) {
	l.lmu.Lock()
	defer l.lmu.Unlock()
	snap, ok := l.pol.Snapshot(cluster)
	switch {
	case !ok:
		return nil, fmt.Errorf("Klyx does not run a cluster named %q", cluster)
	case snap.State != policy.Active:
		return nil, admissionRefusal(cluster, snap)
	}
	c, ok := l.reg.Conn(cluster)
	if !ok {
		return nil, fmt.Errorf("cluster not connected: %s", cluster)
	}
	id, ok := connID(c)
	if !ok || !l.reg.IsCurrent(cluster, id) {
		return nil, fmt.Errorf("cluster not connected: %s", cluster)
	}
	le := &Lease{life: l, Cluster: cluster, Conn: c, ConnID: id, work: w, started: time.Now()}
	if w.Continues {
		le.Ctx = context.Background()
	} else {
		le.Ctx, le.cancel = context.WithCancel(context.Background())
	}
	l.leaseSeq++
	le.n = l.leaseSeq
	l.leases[le.n] = le
	return le, nil
}

func admissionRefusal(cluster string, snap policy.Snapshot) error {
	if snap.Reason != "" {
		return fmt.Errorf("%s: %s", cluster, snap.Reason)
	}
	return fmt.Errorf("%s is applying a change to its settings; try again in a moment", cluster)
}

func connID(c any) (fleet.ConnID, bool) {
	if i, ok := c.(interface{ ID() fleet.ConnID }); ok {
		return i.ID(), true
	}
	return 0, false
}

// Settle records what the work became, once its I/O succeeded. It fails
// with ErrRetired when the connection was retired meanwhile: the worker
// then closes what it opened.
func (le *Lease) Settle(what string) error {
	if le == nil || le.life == nil {
		return nil
	}
	l := le.life
	l.lmu.Lock()
	defer l.lmu.Unlock()
	if le.retired && !le.work.Continues {
		return ErrRetired // a continuing work keeps its outcome (S2 §10)
	}
	le.work.What, le.settled = what, true
	return nil
}

// Pins reports whether the lease pins conn: the connection a worker found
// earlier is still the one admitted. A plain lease, outside any lifecycle,
// pins whatever its worker found.
func (le *Lease) Pins(conn any) bool {
	return le.life == nil || le.Conn == conn
}

// EndedWhy is why the lease's connection ended its work, "" while it has
// not (S2 §2.3). It is recorded before the lease's context is cancelled, so
// a worker that ends through its context can always say why - however its
// End and its own registration interleaved (Codex's review of S2a, F1).
func (le *Lease) EndedWhy() string {
	if le == nil || le.life == nil {
		return ""
	}
	le.life.lmu.Lock()
	defer le.life.lmu.Unlock()
	return le.why
}

// Retired reports whether the lease's connection was retired.
func (le *Lease) Retired() bool {
	if le == nil || le.life == nil {
		return false
	}
	le.life.lmu.Lock()
	defer le.life.lmu.Unlock()
	return le.retired
}

// Release ends the lease: the work is over. Idempotent.
func (le *Lease) Release() {
	if le == nil {
		return
	}
	if le.cancel != nil {
		le.cancel()
	}
	if le.life == nil {
		return
	}
	le.life.lmu.Lock()
	delete(le.life.leases, le.n)
	le.life.lmu.Unlock()
}

// endLeases ends the leases on a retired connection (S2 §2.3): each is
// marked retired; a work that does not continue has its context cancelled
// and is told why, outside the lock.
func (l *Lifecycle) endLeases(cluster string, id fleet.ConnID, why string) {
	var ends []func()
	l.lmu.Lock()
	for n, le := range l.leases {
		if le.Cluster != cluster || le.ConnID != id || le.retired {
			continue
		}
		le.retired = true
		if le.work.Continues {
			continue // runs to its end; listed under Continues until released
		}
		delete(l.leases, n)
		le.why = why
		cancel, end := le.cancel, le.work.End
		ends = append(ends, func() {
			if end != nil {
				end(why) // first: a worker records why before its context ends
			}
			if cancel != nil {
				cancel()
			}
		})
	}
	l.lmu.Unlock()
	for _, f := range ends {
		f()
	}
}

// ClusterWork lists the work on cluster's current connection, by identity
// (S2 §2.3): what retiring it ends, and what continues - pending leases
// included, so the list a plan binds covers work still opening.
func (l *Lifecycle) ClusterWork(cluster string) (ends, continues []PlanWorkDTO) {
	ends, continues = []PlanWorkDTO{}, []PlanWorkDTO{}
	l.lmu.Lock()
	defer l.lmu.Unlock()
	for _, le := range l.leases {
		if le.Cluster != cluster || (le.retired && !le.work.Continues) {
			continue
		}
		w := PlanWorkDTO{ID: fmt.Sprintf("lease:%d", le.n), Kind: le.work.Kind, Cluster: cluster,
			Started: le.started.UTC().Format(time.RFC3339), What: le.work.What}
		if le.work.Continues {
			continues = append(continues, w)
		} else {
			ends = append(ends, w)
		}
	}
	for _, ws := range [][]PlanWorkDTO{ends, continues} {
		sort.Slice(ws, func(i, j int) bool { return ws[i].ID < ws[j].ID })
	}
	return ends, continues
}

// leaser is how a worker takes a lease: the lifecycle's Acquire in the app
// (UseLeases), a plain lease over the worker's lookup in tools and tests.
type leaser func(cluster string, w Work) (*Lease, error)

// plainLeaser leases over a lookup: no lifecycle, so nothing retires.
func plainLeaser[T any](lookup func(string) (T, bool)) leaser {
	return func(cluster string, _ Work) (*Lease, error) {
		c, ok := lookup(cluster)
		if !ok {
			return nil, fmt.Errorf("cluster not connected: %s", cluster)
		}
		return PlainLease(cluster, c), nil
	}
}

// leased is a worker that takes leases.
type leased interface{ setLeaser(leaser) }

// UseLeases gives every worker the lifecycle's leases (S2 §2.3): from now
// on each acquires one, as one step, for every piece of work it starts.
func UseLeases(l *Lifecycle, workers ...leased) {
	for _, w := range workers {
		w.setLeaser(l.Acquire)
	}
}

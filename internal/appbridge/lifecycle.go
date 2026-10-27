package appbridge

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/endpointcred"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/policy"
)

// Lifecycle is the cluster lifecycle (Settings S2 §2): it publishes each
// connection's usability under the cluster's commit gate, retires a
// connection's identity with a trusted end before anything replaces it,
// and forgets a removed cluster everywhere.
//
// Lock order: a connection's own lock may enqueue a usability event (a
// leaf lock), never take a gate. A gate may take the registry's and the
// policy registry's locks, and a connection's lock to acknowledge a move;
// it never delivers to the health sink, starts or stops a connection, or
// calls back into a worker (S2 §12): those run after it is released.
type Lifecycle struct {
	reg  *fleet.Registry
	pol  *policy.Registry
	sink *HealthSink
	ix   *fleetindex.Index // nil without an index

	gmu   sync.Mutex
	gates map[string]*sync.Mutex

	qmu   sync.Mutex
	queue []fleet.UsabilityEvent
	wake  chan struct{}

	hmu        sync.Mutex
	enders     []func(cluster string, id fleet.ConnID, why string)
	forgetters []func(cluster string)
	releasers  []func(cluster string, id fleet.ConnID)
	publishers []func(cluster string, id fleet.ConnID)

	// leases (S2 §2.3), by number
	lmu      sync.Mutex
	leases   map[uint64]*Lease
	leaseSeq uint64
}

// NewLifecycle wires the lifecycle to the registry, the policy registry,
// the health sink and the index: the registry's connections report their
// usability to it, and the sink takes a publication only from a cluster's
// current connection.
func NewLifecycle(reg *fleet.Registry, pol *policy.Registry, sink *HealthSink, ix *fleetindex.Index) *Lifecycle {
	l := &Lifecycle{reg: reg, pol: pol, sink: sink, ix: ix, gates: map[string]*sync.Mutex{}, wake: make(chan struct{}, 1), leases: map[uint64]*Lease{}}
	l.enders = append(l.enders, l.endLeases) // the work ends first, then the generation
	reg.SetOnUsable(l.OnUsable)
	if sink != nil {
		sink.SetIdentityCheck(func(cluster string, inc uint64) bool { return reg.IsCurrent(cluster, fleet.ConnID(inc)) })
	}
	return l
}

// OnEnd registers what ends work on a retired connection (S2 §2.3).
func (l *Lifecycle) OnEnd(f func(cluster string, id fleet.ConnID, why string)) {
	l.hmu.Lock()
	defer l.hmu.Unlock()
	l.enders = append(l.enders, f)
}

// OnForget registers what drops a removed cluster's state (S2 §2.5).
func (l *Lifecycle) OnForget(f func(cluster string)) {
	l.hmu.Lock()
	defer l.hmu.Unlock()
	l.forgetters = append(l.forgetters, f)
}

func (l *Lifecycle) gate(cluster string) *sync.Mutex {
	l.gmu.Lock()
	defer l.gmu.Unlock()
	g := l.gates[cluster]
	if g == nil {
		g = &sync.Mutex{}
		l.gates[cluster] = g
	}
	return g
}

// OnUsable queues a connection's move into usable (S2 §4.5). It never
// blocks: it runs under the connection's own lock.
func (l *Lifecycle) OnUsable(ev fleet.UsabilityEvent) {
	l.qmu.Lock()
	l.queue = append(l.queue, ev)
	l.qmu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Run applies queued usability events until ctx ends.
func (l *Lifecycle) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		}
		for {
			l.qmu.Lock()
			q := l.queue
			l.queue = nil
			l.qmu.Unlock()
			if len(q) == 0 {
				break
			}
			for _, ev := range q {
				l.applyUsable(ev)
			}
		}
	}
}

// applyUsable publishes a move under the cluster's gate: the incarnation
// first, then the move made visible (slice F §1, S2 §12). A retired
// identity's move is dropped: it is never published.
//
// The visible state then reaches the Observer and the index after the gate
// is left: their handoff may deliver to the health sink, and the gate takes
// no reentrant callback (S2 §12).
//
// A cluster Held for this connection's attempt is released by its first
// usability, exactly once (S2 §4.5): its new protection is published, and
// the OnReleased hooks run after the gate - the new terms become the last
// terms that connected.
func (l *Lifecycle) applyUsable(ev fleet.UsabilityEvent) {
	published, released := l.ackUsable(ev)
	if published != nil {
		published()
		l.hmu.Lock()
		ps := append([]func(string, fleet.ConnID){}, l.publishers...)
		l.hmu.Unlock()
		for _, f := range ps {
			f(ev.Cluster, ev.ID)
		}
	}
	if released {
		l.hmu.Lock()
		rs := append([]func(string, fleet.ConnID){}, l.releasers...)
		l.hmu.Unlock()
		for _, f := range rs {
			f(ev.Cluster, ev.ID)
		}
	}
}

func (l *Lifecycle) ackUsable(ev fleet.UsabilityEvent) (published func(), released bool) {
	g := l.gate(ev.Cluster)
	g.Lock()
	defer g.Unlock()
	if !l.reg.IsCurrent(ev.Cluster, ev.ID) {
		return nil, false
	}
	l.pol.ConnUp(ev.Cluster)
	_, released = l.pol.ReleaseAttempt(ev.Cluster, uint64(ev.ID))
	if c, ok := l.reg.Conn(ev.Cluster); ok {
		if a, ok := c.(interface {
			ID() fleet.ConnID
			AckUsable(uint64) bool
			Published()
		}); ok && a.ID() == ev.ID && a.AckUsable(ev.Inc) {
			published = a.Published
		}
	}
	return published, released
}

// OnPublished registers what learns that a connection's move into usable
// was published - the connection really connected (S2 §4.5). It runs after
// the gate.
func (l *Lifecycle) OnPublished(f func(cluster string, id fleet.ConnID)) {
	l.hmu.Lock()
	defer l.hmu.Unlock()
	l.publishers = append(l.publishers, f)
}

// OnReleased registers what records a held attempt's release (S2 §4.5):
// the attempt's terms become the last terms that connected. It runs after
// the gate.
func (l *Lifecycle) OnReleased(f func(cluster string, id fleet.ConnID)) {
	l.hmu.Lock()
	defer l.hmu.Unlock()
	l.releasers = append(l.releasers, f)
}

// retireLocked retires name's current identity: under the gate, so no
// usability of it is published after. Caller holds the gate.
func (l *Lifecycle) retireLocked(name string) (fleet.Conn, fleet.ConnID, bool) {
	return l.reg.Retire(name)
}

// ForgetEndpointRevisions retires a removed cluster's endpoint credential
// revisions, for every kind (Settings S2 §2.5; Codex's review of S2a, F7):
// an endpoint client or reader built before the removal is never current
// again, even once the name is added back. For OnForget.
func ForgetEndpointRevisions(revs *endpointcred.Registry) func(cluster string) {
	return func(cluster string) { revs.Retire(cluster, config.EndpointKinds...) }
}

// ended delivers the end of a retired identity, outside the gate: its work
// ends, then its generation's trusted end reaches the sink and the index
// (S2 §2.4) - before anything replaces it.
func (l *Lifecycle) ended(name string, id fleet.ConnID, why string) {
	l.hmu.Lock()
	enders := append([]func(string, fleet.ConnID, string){}, l.enders...)
	l.hmu.Unlock()
	for _, f := range enders {
		f(name, id, why)
	}
	if l.sink != nil {
		l.sink.EndIdentity(name, uint64(id), "unreachable")
	}
	if l.ix != nil {
		l.ix.End(name, uint64(id))
	}
}

// stopRetired stops a retired connection in the background: its identity
// is already retired, so whatever it publishes before it returns is
// dropped.
func stopRetired(name string, old fleet.Conn) {
	s, ok := old.(interface{ Stop(time.Duration) bool })
	if !ok {
		return
	}
	go func() {
		if !s.Stop(retiredStopWait) {
			log.Printf("cluster %s: its retired connection had not stopped after %s; what it publishes is dropped", name, retiredStopWait)
		}
	}()
}

const retiredStopWait = 10 * time.Second

// Reconnect replaces name's connection with p, prepared from its new
// terms (S2 §2.2): the old identity is retired under the gate, its work and
// generation end, then p is installed and started and the old connection
// stops. S2b wraps this in its plan's transaction.
func (l *Lifecycle) Reconnect(ctx context.Context, p *fleet.Prepared, why string) error {
	name := p.Name()
	g := l.gate(name)
	g.Lock()
	old, id, ok := l.retireLocked(name)
	g.Unlock()
	if ok {
		l.ended(name, id, why)
	}
	if err := l.reg.Install(ctx, p); err != nil {
		p.Discard()
		return err
	}
	if ok {
		stopRetired(name, old)
	}
	return nil
}

// Remove takes name out of the running fleet (S2 §2.5): its identity is
// retired, its work and generation end, its slot goes, its connection
// stops, and the cluster is forgotten everywhere - its last epoch kept as
// a tombstone.
func (l *Lifecycle) Remove(name, why string) error {
	g := l.gate(name)
	g.Lock()
	old, id, ok := l.retireLocked(name)
	l.pol.Forget(name) // published with the retirement (S2b review, R2)
	g.Unlock()
	if ok {
		l.ended(name, id, why)
	}
	if err := l.reg.Drop(name); err != nil {
		return err
	}
	if ok {
		stopRetired(name, old)
	}
	l.forget(name)
	return nil
}

func (l *Lifecycle) forget(name string) {
	l.pol.Forget(name)
	if l.sink != nil {
		l.sink.ForgetCluster(name)
	}
	if l.ix != nil {
		l.ix.Forget(name)
	}
	l.hmu.Lock()
	fs := append([]func(string){}, l.forgetters...)
	l.hmu.Unlock()
	for _, f := range fs {
		f(name)
	}
}

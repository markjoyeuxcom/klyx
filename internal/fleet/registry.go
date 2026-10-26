package fleet

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/cluster"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/observe"
)

// ConnFactory builds a Conn for a cluster. Injected so tests use fakes.
type ConnFactory func(config.ClusterConfig) (Conn, error)

// entry is one cluster's slot: its connection and its identity, or why it
// failed to build (with the entry it was built from, for Retry).
type entry struct {
	name     string
	cc       config.ClusterConfig
	conn     Conn
	id       ConnID
	failed   bool
	failMsg  string
	retiring bool // the connection's identity is retired; Install or Drop follows (S2 §2.4)
}

type Registry struct {
	factory ConnFactory
	cfg     *config.Config // the startup configuration, for Start

	mu         sync.RWMutex
	started    bool
	entries    []*entry
	retired    map[ConnID]bool // every identity ever retired: never current again
	healthSink observe.Sink
	index      *fleetindex.Index
	onUsable   func(UsabilityEvent)
}

func NewRegistry(cfg *config.Config, factory ConnFactory) *Registry {
	return &Registry{cfg: cfg, factory: factory, retired: map[ConnID]bool{}}
}

// identityOf is a connection's identity: its own, or one drawn for a
// connection that has none (a fake).
func identityOf(c Conn) ConnID {
	if id, ok := c.(interface{ ID() ConnID }); ok {
		return id.ID()
	}
	return newConnID()
}

// Start constructs and starts every configured conn. A conn that fails to
// construct is recorded as Failed and does not stop the others; Retry
// builds it again (Settings S2 §2.2).
func (r *Registry) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return // already started
	}
	r.started = true
	defer r.indexClustersLocked()
	for _, cc := range r.cfg.Clusters {
		conn, err := r.factory(cc)
		if err != nil {
			r.entries = append(r.entries, &entry{name: cc.Name, cc: cc, failed: true, failMsg: failedMsg(err)})
			continue
		}
		e := &entry{name: cc.Name, cc: cc, conn: conn, id: identityOf(conn)}
		r.entries = append(r.entries, e)
		r.wireHealth(conn)
		conn.Start(ctx)
	}
}

func failedMsg(err error) string {
	return fmt.Sprintf("failed to connect: %s", cluster.FriendlyErrorMessage(err))
}

// Add constructs and starts a conn for a cluster added at runtime (Settings
// "add to fleet" - no restart). A name already in the registry is refused,
// a Failed one included: Retry builds that. A factory error is returned to
// the caller rather than recorded as a Failed entry.
func (r *Registry) Add(ctx context.Context, cc config.ClusterConfig) error {
	p, err := r.Prepare(cc)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.findLocked(cc.Name) != nil {
		return fmt.Errorf("cluster %q is already in the fleet", cc.Name)
	}
	r.installLocked(ctx, p, nil)
	return nil
}

// Prepared is a connection built for a cluster and not started (Settings S2
// §2.2): built outside every lock, so a context that cannot be resolved
// changes nothing.
type Prepared struct {
	conn Conn
	cc   config.ClusterConfig
	id   ConnID
}

// ID is the prepared connection's identity.
func (p *Prepared) ID() ConnID { return p.id }

// Name is the cluster the connection was prepared for.
func (p *Prepared) Name() string { return p.cc.Name }

// Prepare builds a connection for cc through the factory, without starting
// it. A factory error returns, and nothing changes.
func (r *Registry) Prepare(cc config.ClusterConfig) (*Prepared, error) {
	conn, err := r.factory(cc)
	if err != nil {
		return nil, errors.New(failedMsg(err))
	}
	return &Prepared{conn: conn, cc: cc, id: identityOf(conn)}, nil
}

// Discard stops a prepared connection that will not be installed.
func (p *Prepared) Discard() {
	if s, ok := p.conn.(interface{ Stop(time.Duration) bool }); ok {
		s.Stop(stopWait)
	}
}

// stopWait bounds how long stopping a connection waits for its loops.
const stopWait = 10 * time.Second

// Retire retires name's current connection identity (Settings S2 §2.4): it
// is never current again, so whatever it publishes from now on is dropped,
// and lookups no longer return it. The connection stays in its slot,
// running, until Install replaces it or Drop removes it; the caller then
// stops it. ok is false when name has no connection to retire.
func (r *Registry) Retire(name string) (Conn, ConnID, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.findLocked(name)
	if e == nil || e.conn == nil || e.retiring {
		return nil, 0, false
	}
	e.retiring = true
	r.retired[e.id] = true
	return e.conn, e.id, true
}

// Install puts p in its cluster's slot and starts it: a slot whose
// connection was retired or failed to build, or a new slot at the end of
// the fleet. A slot whose connection is still current is refused - retire
// it first.
func (r *Registry) Install(ctx context.Context, p *Prepared) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.findLocked(p.cc.Name)
	if e != nil && e.conn != nil && !e.retiring {
		return fmt.Errorf("cluster %q has a current connection: retire it first", p.cc.Name)
	}
	r.installLocked(ctx, p, e)
	return nil
}

// installLocked wires p and starts it, in e or in a new slot.
func (r *Registry) installLocked(ctx context.Context, p *Prepared, e *entry) {
	if e == nil {
		e = &entry{}
		r.entries = append(r.entries, e)
	}
	*e = entry{name: p.cc.Name, cc: p.cc, conn: p.conn, id: p.id}
	r.wireHealth(p.conn)
	p.conn.Start(ctx)
	r.indexClustersLocked()
}

// Drop removes name's slot. Its connection, if any, must be retired first;
// the caller stops it.
func (r *Registry) Drop(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.entries {
		if e.name != name {
			continue
		}
		if e.conn != nil && !e.retiring {
			return fmt.Errorf("cluster %q has a current connection: retire it first", name)
		}
		r.entries = append(r.entries[:i], r.entries[i+1:]...)
		r.indexClustersLocked()
		return nil
	}
	return fmt.Errorf("cluster %q is not in the fleet", name)
}

// Retry builds again a cluster whose connection failed to build, from the
// entry it was built from (Settings S2 §2.2).
func (r *Registry) Retry(ctx context.Context, name string) error {
	r.mu.RLock()
	e := r.findLocked(name)
	var cc config.ClusterConfig
	ok := e != nil && e.failed
	if ok {
		cc = e.cc
	}
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("cluster %q has no connection that failed to build", name)
	}
	p, err := r.Prepare(cc)
	if err != nil {
		r.mu.Lock()
		if e := r.findLocked(name); e != nil && e.failed {
			e.failMsg = err.Error()
		}
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.findLocked(name); e == nil || !e.failed {
		p.Discard()
		return fmt.Errorf("cluster %q changed while it was retried", name)
	}
	r.installLocked(ctx, p, r.findLocked(name))
	return nil
}

// IsCurrent reports whether id is name's current connection: neither
// retired nor replaced (Settings S2 §2.4).
func (r *Registry) IsCurrent(name string, id ConnID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.retired[id] {
		return false
	}
	e := r.findLocked(name)
	return e != nil && e.conn != nil && !e.retiring && e.id == id
}

// IsRetired reports whether id was retired.
func (r *Registry) IsRetired(id ConnID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.retired[id]
}

// findLocked is name's slot. Caller holds r.mu.
func (r *Registry) findLocked(name string) *entry {
	for _, e := range r.entries {
		if e.name == name {
			return e
		}
	}
	return nil
}

// SetHealthSink routes every cluster's health publications (1c) to s. Call
// before Start; conns added later get it too.
func (r *Registry) SetHealthSink(s observe.Sink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.healthSink = s
}

// SetIndex gives every conn the fleet metadata index (The Line slice 9).
// Call before Start; conns added later get it too.
func (r *Registry) SetIndex(ix *fleetindex.Index) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.index = ix
}

// SetOnUsable gives every conn the incarnation hook (Settings slice F §1,
// S2 §4.5). Call before Start; conns added later get it too.
func (r *Registry) SetOnUsable(f func(UsabilityEvent)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onUsable = f
}

// wireHealth hands a conn the hook, the sink and the index before it
// starts. Caller holds r.mu.
func (r *Registry) wireHealth(conn Conn) {
	if ou, ok := conn.(interface{ SetOnUsable(func(UsabilityEvent)) }); ok && r.onUsable != nil {
		ou.SetOnUsable(r.onUsable)
	}
	if hs, ok := conn.(interface{ SetHealthSink(observe.Sink) }); ok && r.healthSink != nil {
		hs.SetHealthSink(r.healthSink)
	}
	if is, ok := conn.(interface{ SetIndex(*fleetindex.Index) }); ok && r.index != nil {
		is.SetIndex(r.index)
	}
}

// indexClustersLocked tells the index every cluster of the running fleet,
// in fleet order - the startup order, then each addition - so coverage
// names the ones never ready (Settings S2 §2.2). Caller holds r.mu.
func (r *Registry) indexClustersLocked() {
	if r.index == nil {
		return
	}
	names := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		names = append(names, e.name)
	}
	r.index.SetClusters(names)
}

// Conn returns the live Conn for a cluster by name: false when it is
// absent, failed to build, or retired.
func (r *Registry) Conn(name string) (Conn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e := r.findLocked(name)
	if e == nil || e.failed || e.conn == nil || e.retiring {
		return nil, false
	}
	return e.conn, true
}

// Snapshots returns one snapshot per cluster of the running fleet, sorted
// by name.
func (r *Registry) Snapshots() []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Snapshot, 0, len(r.entries))
	for _, e := range r.entries {
		if e.failed {
			out = append(out, Snapshot{Name: e.name, State: Failed, Reason: e.failMsg})
			continue
		}
		out = append(out, e.conn.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

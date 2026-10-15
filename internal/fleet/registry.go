package fleet

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/moomora/klyx/internal/cluster"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/observe"
)

// ConnFactory builds a Conn for a cluster. Injected so tests use fakes.
type ConnFactory func(config.ClusterConfig) (Conn, error)

type entry struct {
	conn     Conn
	failed   bool
	failName string
	failMsg  string
}

type Registry struct {
	cfg     *config.Config
	factory ConnFactory

	mu         sync.RWMutex
	entries    []entry
	healthSink observe.Sink
	index      *fleetindex.Index
}

func NewRegistry(cfg *config.Config, factory ConnFactory) *Registry {
	return &Registry{cfg: cfg, factory: factory}
}

// Start constructs and starts every configured conn. A conn that fails to
// construct is recorded as Failed and does not stop the others.
func (r *Registry) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) > 0 {
		return // already started
	}
	defer r.indexClustersLocked()
	for _, cc := range r.cfg.Clusters {
		conn, err := r.factory(cc)
		if err != nil {
			r.entries = append(r.entries, entry{
				failed: true, failName: cc.Name,
				failMsg: fmt.Sprintf("failed to connect: %s", cluster.FriendlyErrorMessage(err)),
			})
			continue
		}
		r.wireHealth(conn)
		conn.Start(ctx)
		r.entries = append(r.entries, entry{conn: conn})
	}
}

// Add constructs and starts a conn for a cluster added at runtime (Settings
// "add to fleet" - no restart). Duplicate names are refused, including ones
// recorded as Failed at startup: those retry on restart, not here. A factory
// error is returned to the caller rather than recorded as a Failed entry -
// the user is watching the Settings result, and the fleet file already has
// the entry so a restart retries it.
func (r *Registry) Add(ctx context.Context, cc config.ClusterConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		name := e.failName
		if e.conn != nil {
			name = e.conn.Name()
		}
		if name == cc.Name {
			return fmt.Errorf("cluster %q is already in the fleet", cc.Name)
		}
	}
	conn, err := r.factory(cc)
	if err != nil {
		return fmt.Errorf("failed to connect: %s", cluster.FriendlyErrorMessage(err))
	}
	r.wireHealth(conn)
	conn.Start(ctx)
	r.entries = append(r.entries, entry{conn: conn})
	r.indexClustersLocked()
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

// wireHealth hands a conn the sink and the index before it starts. Caller
// holds r.mu.
func (r *Registry) wireHealth(conn Conn) {
	if hs, ok := conn.(interface{ SetHealthSink(observe.Sink) }); ok && r.healthSink != nil {
		hs.SetHealthSink(r.healthSink)
	}
	if is, ok := conn.(interface{ SetIndex(*fleetindex.Index) }); ok && r.index != nil {
		is.SetIndex(r.index)
	}
}

// indexClustersLocked tells the index every configured cluster, in fleet
// order, so coverage names the ones never ready. Caller holds r.mu.
func (r *Registry) indexClustersLocked() {
	if r.index == nil {
		return
	}
	names := make([]string, 0, len(r.cfg.Clusters))
	for _, cc := range r.cfg.Clusters {
		names = append(names, cc.Name)
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	for _, e := range r.entries {
		n := e.failName
		if e.conn != nil {
			n = e.conn.Name()
		}
		if !seen[n] {
			names = append(names, n)
			seen[n] = true
		}
	}
	r.index.SetClusters(names)
}

// Conn returns the live Conn for a cluster by name (nil,false if absent or failed).
func (r *Registry) Conn(name string) (Conn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if e.failed || e.conn == nil {
			continue
		}
		if e.conn.Name() == name {
			return e.conn, true
		}
	}
	return nil, false
}

// Snapshots returns one snapshot per configured cluster, sorted by name.
func (r *Registry) Snapshots() []Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Snapshot, 0, len(r.entries))
	for _, e := range r.entries {
		if e.failed {
			out = append(out, Snapshot{Name: e.failName, State: Failed, Reason: e.failMsg})
			continue
		}
		out = append(out, e.conn.Snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

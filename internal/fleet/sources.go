package fleet

import (
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/moomora/klyx/internal/config"
)

// Settings S3a §3.4, §3.6: the installations in force and every cluster's
// subscription to them, with revisions. Only an Apply changes them - at
// start, an S2 commit that moves the running fleet, or (S3a-4)
// Observability's plan - never a hand edit of fleet.yaml on its own.

// sourceSeq is the one process-wide sequence revisions are drawn from: a
// revision is never reused, so an installation removed and re-created
// under its id, or a change and its undo, never revives an old lineage.
var sourceSeq atomic.Uint64

func nextSourceRev() uint64 { return sourceSeq.Add(1) }

// Subscription is one cluster's resolved use of an installation (S3a
// §2.3): the installation as it is in force, its revision and the
// subscription's, and the effective scope.
type Subscription struct {
	Cluster     string
	Source      config.SourceConfig
	InstRev     uint64
	SubRev      uint64
	Tenant      string
	Match       map[string]string
	ScopeTenant bool
}

// AlertSources are the installations in force and the subscriptions.
type AlertSources struct {
	mu      sync.Mutex
	rev     uint64 // the whole set's revision: S2's plans bind it
	inst    map[string]installation
	order   []string
	subs    map[string]Subscription
	denials map[string]uint64 // observed 401/403 per installation (§3.4)
}

type installation struct {
	cfg config.SourceConfig
	rev uint64
}

// NewAlertSources resolves the installations against the running fleet.
func NewAlertSources(sources []config.SourceConfig, clusters []config.ClusterConfig) (*AlertSources, error) {
	a := &AlertSources{inst: map[string]installation{}, subs: map[string]Subscription{}, denials: map[string]uint64{}}
	if _, err := a.Apply(sources, clusters); err != nil {
		return nil, err
	}
	return a, nil
}

// Resolve resolves a candidate without applying it: an S2 plan checks that
// its runtime candidate resolves (S3a §3.6).
func (a *AlertSources) Resolve(clusters []config.ClusterConfig) error {
	a.mu.Lock()
	sources := a.sourcesLocked()
	a.mu.Unlock()
	// every kind: a candidate whose Loki or metrics resolution fails is
	// refused as an Alertmanager one is (S3b §2.2)
	return config.ResolveAll(sources, clusters)
}

// Apply puts sources and the running fleet in force: an installation
// whose record changed, or a subscription whose resolution changed, gets
// a new revision. It reports the clusters whose subscription changed.
func (a *AlertSources) Apply(sources []config.SourceConfig, clusters []config.ClusterConfig) ([]string, error) {
	res, err := config.ResolveAlertmanager(sources, clusters)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	moved := false
	inst := map[string]installation{}
	order := make([]string, 0, len(sources))
	for _, s := range sources {
		prev, ok := a.inst[s.ID]
		if ok && reflect.DeepEqual(prev.cfg, s) {
			inst[s.ID] = prev
		} else {
			inst[s.ID], moved = installation{cfg: s, rev: nextSourceRev()}, true
		}
		order = append(order, s.ID)
	}
	if len(inst) != len(a.inst) {
		moved = true
	}
	subs := map[string]Subscription{}
	var changed []string
	for _, cl := range clusters {
		r := res[cl.Name]
		if r.Mode != config.AMSubscribed {
			if _, was := a.subs[cl.Name]; was {
				changed = append(changed, cl.Name)
			}
			continue
		}
		in := inst[r.Source]
		next := Subscription{Cluster: cl.Name, Source: in.cfg, InstRev: in.rev, Tenant: r.Tenant, Match: r.Match, ScopeTenant: r.ScopeTenant}
		prev, ok := a.subs[cl.Name]
		if ok && prev.InstRev == next.InstRev && prev.Source.ID == next.Source.ID && prev.Tenant == next.Tenant &&
			prev.ScopeTenant == next.ScopeTenant && reflect.DeepEqual(prev.Match, next.Match) {
			next.SubRev = prev.SubRev
		} else {
			next.SubRev = nextSourceRev()
			changed = append(changed, cl.Name)
		}
		subs[cl.Name] = next
	}
	for name := range a.subs {
		if _, still := subs[name]; !still && !slices.Contains(changed, name) {
			changed = append(changed, name)
		}
	}
	if moved || len(changed) > 0 || a.rev == 0 {
		a.rev = nextSourceRev()
	}
	a.inst, a.order, a.subs = inst, order, subs
	return changed, nil
}

// Subscription is cluster's subscription in force, if it has one.
func (a *AlertSources) Subscription(cluster string) (Subscription, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.subs[cluster]
	return s, ok
}

// Subscribers are the clusters subscribed to installation id.
func (a *AlertSources) Subscribers(id string) []Subscription {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Subscription
	for _, s := range a.subs {
		if s.Source.ID == id {
			out = append(out, s)
		}
	}
	return out
}

// Sources are the installations in force, in the file's order.
func (a *AlertSources) Sources() []config.SourceConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sourcesLocked()
}

func (a *AlertSources) sourcesLocked() []config.SourceConfig {
	out := make([]config.SourceConfig, 0, len(a.order))
	for _, id := range a.order {
		out = append(out, a.inst[id].cfg)
	}
	return out
}

// Revision is the whole set's revision: it moves with any change in force.
func (a *AlertSources) Revision() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rev
}

// Installation is an installation in force, with its revision.
func (a *AlertSources) Installation(id string) (config.SourceConfig, uint64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	in, ok := a.inst[id]
	return in.cfg, in.rev, ok
}

// Denied records an observed 401 or 403 from an installation: its access
// generation moves, for good (S3a §3.4).
func (a *AlertSources) Denied(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.denials[id]++
}

// Denials are an installation's observed 401s and 403s.
func (a *AlertSources) Denials(id string) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.denials[id]
}

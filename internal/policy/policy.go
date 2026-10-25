// Package policy is the single published source of each cluster's
// admission state (Settings slice F §1): its effective protection, its
// epoch and whether it is Active. Every admission - the protected-cluster
// guard, a write's fence, an AI start, read or send, an A3 offer, review or
// run - reads one Snapshot, so protection and the epoch can never come from
// two moments.
//
// The epoch moves whenever the snapshot's meaning does: on every
// publication step (Begin, Publish, Hold, Release) and each time the
// cluster's connection becomes usable again. Work that captured an epoch is
// refused once it has moved; nothing captured before or during a change
// survives it.
package policy

import (
	"errors"
	"fmt"
	"sync"
)

// Epoch is opaque: only equality means anything.
type Epoch struct{ policy, conn uint64 }

// Moved says which parts moved since an earlier epoch: the policy, the
// connection's incarnation, or both.
func (e Epoch) Moved(since Epoch) (policy, conn bool) {
	return e.policy != since.policy, e.conn != since.conn
}

// String names the epoch for a plan's revisions and for errors.
func (e Epoch) String() string { return fmt.Sprintf("p%d.c%d", e.policy, e.conn) }

// State is whether a cluster admits work.
type State int

const (
	// Active admits work under the published protection.
	Active State = iota
	// Applying is a change between Begin and Publish: nothing is admitted.
	Applying
	// Held is saved, not active (a failed activation): nothing is admitted
	// until Release.
	Held
)

func (s State) String() string {
	switch s {
	case Active:
		return "active"
	case Applying:
		return "applying"
	}
	return "held"
}

// Snapshot is one cluster's admission state at one moment.
type Snapshot struct {
	Protected bool
	Epoch     Epoch
	State     State
	Reason    string // why it is Held
}

type entry struct {
	protected    bool
	policy, conn uint64
	state        State
	reason       string
}

func (e *entry) snapshot() Snapshot {
	return Snapshot{Protected: e.protected, Epoch: Epoch{e.policy, e.conn}, State: e.state, Reason: e.reason}
}

// Registry holds every cluster's entry. Its mutex is never held while
// calling out, and it never calls into a caller, so a caller may read it
// under its own lock without any lock-order inversion.
type Registry struct {
	mu sync.Mutex
	m  map[string]*entry
}

// New is an empty registry: a cluster it does not know admits nothing.
func New() *Registry { return &Registry{m: map[string]*entry{}} }

// ErrUnknown is a cluster the registry does not know.
var ErrUnknown = errors.New("policy: an unknown cluster")

// Seed publishes a new cluster, Active at policy 1. Seeding a known
// cluster changes nothing: a change goes through Begin and Publish.
func (r *Registry) Seed(cluster string, protected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[cluster]; ok {
		return
	}
	r.m[cluster] = &entry{protected: protected, policy: 1}
}

// Snapshot is the cluster's admission state, false for an unknown one.
func (r *Registry) Snapshot(cluster string) (Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[cluster]
	if !ok {
		return Snapshot{}, false
	}
	return e.snapshot(), true
}

func (r *Registry) step(cluster string, from []State, to State, f func(*entry)) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[cluster]
	if !ok {
		return Snapshot{}, ErrUnknown
	}
	allowed := false
	for _, s := range from {
		allowed = allowed || e.state == s
	}
	if !allowed {
		return e.snapshot(), fmt.Errorf("policy: %s is %s", cluster, e.state)
	}
	e.state = to
	e.policy++
	if f != nil {
		f(e)
	}
	return e.snapshot(), nil
}

// Begin starts a change: the cluster stops admitting work and its epoch
// moves, so nothing captured before the change survives it. From Active,
// or from Held (a retried change).
func (r *Registry) Begin(cluster string) (Snapshot, error) {
	return r.step(cluster, []State{Active, Held}, Applying, func(e *entry) { e.reason = "" })
}

// Publish ends a change with the new protection: Active again, the epoch
// moved once more, so a guard that ran during the change cannot pair the
// old protection with the new epoch.
func (r *Registry) Publish(cluster string, protected bool) (Snapshot, error) {
	return r.step(cluster, []State{Applying}, Active, func(e *entry) { e.protected = protected })
}

// Hold leaves the cluster saved, not active: it admits nothing until
// Release, and its epoch moves.
func (r *Registry) Hold(cluster, reason string) (Snapshot, error) {
	return r.step(cluster, []State{Active, Applying, Held}, Held, func(e *entry) { e.reason = reason })
}

// Release makes a held cluster Active with the given protection. Its epoch
// moves, so releasing never revives an arm, a review or an offer.
func (r *Registry) Release(cluster string, protected bool) (Snapshot, error) {
	return r.step(cluster, []State{Held}, Active, func(e *entry) { e.protected, e.reason = protected, "" })
}

// ConnUp records that the cluster's connection became usable from not
// usable: a new incarnation. The connection calls it before its new state
// is visible. Per cluster name, so a replacement connection's incarnation
// can never repeat its predecessor's. An unknown cluster is ignored.
func (r *Registry) ConnUp(cluster string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.m[cluster]; ok {
		e.conn++
	}
}

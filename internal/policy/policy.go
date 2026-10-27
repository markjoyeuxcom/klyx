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
	// Reserved: an Apply holds the cluster's reservation (Settings S2
	// §4.1). Nothing new is admitted; what was admitted before stays valid
	// under its epoch.
	Reserved bool
	// Attempt is the connection attempt a Held cluster waits on: only its
	// first usability releases it (S2 §4.5). Zero for none.
	Attempt uint64
}

type entry struct {
	protected    bool
	policy, conn uint64
	state        State
	reason       string

	// the reservation overlay (S2 §4.1): the owner's token number, zero
	// for none. A placeholder is a reserved name the registry does not
	// have yet: it admits nothing and is published nowhere.
	reserved    uint64
	placeholder bool
	// a Held cluster bound to a connection attempt, and the protection its
	// release publishes
	attempt          uint64
	releaseProtected bool
}

func (e *entry) snapshot() Snapshot {
	return Snapshot{Protected: e.protected, Epoch: Epoch{e.policy, e.conn}, State: e.state, Reason: e.reason,
		Reserved: e.reserved != 0, Attempt: e.attempt}
}

// Registry holds every cluster's entry. Its mutex is never held while
// calling out, and it never calls into a caller, so a caller may read it
// under its own lock without any lock-order inversion.
type Registry struct {
	mu sync.Mutex
	m  map[string]*entry
	// tombs keeps a forgotten cluster's last epoch (Settings S2 §2.5), so a
	// cluster seeded again under its name starts above it and no ticket,
	// arm, review or offer from before the removal can ever match.
	tombs  map[string]Epoch
	tokSeq uint64
}

// New is an empty registry: a cluster it does not know admits nothing.
func New() *Registry { return &Registry{m: map[string]*entry{}, tombs: map[string]Epoch{}} }

// ErrUnknown is a cluster the registry does not know.
var ErrUnknown = errors.New("policy: an unknown cluster")

// lookup is the cluster's entry, never a placeholder. Under r.mu.
func (r *Registry) lookup(cluster string) (*entry, bool) {
	e, ok := r.m[cluster]
	if !ok || e.placeholder {
		return nil, false
	}
	return e, true
}

// Seed publishes a new cluster, Active at policy 1, or above its tombstone
// when it was forgotten. Over a placeholder it is a committed addition or
// connect (S2 §4.1): the cluster starts, still reserved until its Apply
// unreserves it. Seeding a known cluster changes nothing: a change goes
// through Begin and Publish, or the overlay's transitions.
func (r *Registry) Seed(cluster string, protected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reserved := uint64(0)
	if e, ok := r.m[cluster]; ok {
		if !e.placeholder {
			return
		}
		reserved = e.reserved
	}
	t := r.tombs[cluster]
	r.m[cluster] = &entry{protected: protected, policy: t.policy + 1, conn: t.conn, reserved: reserved}
}

// Forget removes a cluster (Settings S2 §2.5): it admits nothing from now
// on, and its last epoch is kept as a tombstone, so a later Seed of the
// same name starts above it. Forgetting an unknown cluster changes
// nothing.
func (r *Registry) Forget(cluster string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[cluster]
	if !ok {
		return
	}
	if !e.placeholder { // a placeholder never had an epoch to keep
		r.tombs[cluster] = Epoch{e.policy, e.conn}
	}
	delete(r.m, cluster)
}

// Snapshot is the cluster's admission state, false for an unknown one or a
// placeholder.
func (r *Registry) Snapshot(cluster string) (Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.lookup(cluster)
	if !ok {
		return Snapshot{}, false
	}
	return e.snapshot(), true
}

func (r *Registry) step(cluster string, from []State, to State, f func(*entry)) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.lookup(cluster)
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
	return r.step(cluster, []State{Active, Held}, Applying, func(e *entry) { e.reason, e.attempt = "", 0 })
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
	return r.step(cluster, []State{Active, Applying, Held}, Held, func(e *entry) { e.reason, e.attempt = reason, 0 })
}

// Release makes a held cluster Active with the given protection. Its epoch
// moves, so releasing never revives an arm, a review or an offer.
func (r *Registry) Release(cluster string, protected bool) (Snapshot, error) {
	return r.step(cluster, []State{Held}, Active, func(e *entry) { e.protected, e.reason, e.attempt = protected, "", 0 })
}

// ConnUp records that the cluster's connection became usable from not
// usable: a new incarnation. The connection calls it before its new state
// is visible. Per cluster name, so a replacement connection's incarnation
// can never repeat its predecessor's. An unknown cluster is ignored.
func (r *Registry) ConnUp(cluster string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.lookup(cluster); ok {
		e.conn++
	}
}

// The reservation overlay (Settings S2 §4.1). An Apply reserves its
// cluster before it re-plans: new admissions stop, and nothing else
// changes - the activation state, its reason, its epoch and its attempt
// stay as they were, so a refusal before the write unreserves and has
// changed nothing. Only a committed transition moves the epoch, inside the
// reservation, which is removed last.

// Token is one Apply's reservation of one cluster.
type Token struct {
	cluster string
	n       uint64
}

// Cluster is the reserved cluster's name.
func (t Token) Cluster() string { return t.cluster }

// ErrReserved: another Apply holds the cluster's reservation.
var ErrReserved = errors.New("policy: another change holds this cluster's reservation")

// ErrNotReserved: the token does not hold the cluster's reservation, or
// the cluster is not in the state the transition needs.
var ErrNotReserved = errors.New("policy: this change does not hold the cluster's reservation")

// Reserve adds the overlay to cluster, whatever its activation state:
// Active, Held, or absent - a name the registry does not have yet, which
// gets a placeholder that admits nothing and is published nowhere.
func (r *Registry) Reserve(cluster string) (Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[cluster]
	if !ok {
		e = &entry{placeholder: true}
		r.m[cluster] = e
	}
	if e.reserved != 0 {
		return Token{}, ErrReserved
	}
	r.tokSeq++
	e.reserved = r.tokSeq
	return Token{cluster: cluster, n: r.tokSeq}, nil
}

// Unreserve removes the overlay and nothing else: a placeholder goes, so
// the name is absent again. A token that no longer holds the reservation
// changes nothing.
func (r *Registry) Unreserve(t Token) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[t.cluster]
	if !ok || e.reserved != t.n || t.n == 0 {
		return
	}
	e.reserved = 0
	if e.placeholder {
		delete(r.m, t.cluster)
	}
}

// held is t's reserved entry in one of states. Under r.mu.
func (r *Registry) held(t Token, states ...State) (*entry, error) {
	e, ok := r.lookup(t.cluster)
	if !ok {
		return nil, ErrUnknown
	}
	if t.n == 0 || e.reserved != t.n {
		return nil, ErrNotReserved
	}
	for _, s := range states {
		if e.state == s {
			return e, nil
		}
	}
	return nil, fmt.Errorf("policy: %s is %s", t.cluster, e.state)
}

// Advance commits a protection change on an Active cluster under t: the
// new protection, and the epoch moved once.
func (r *Registry) Advance(t Token, protected bool) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, err := r.held(t, Active)
	if err != nil {
		return Snapshot{}, err
	}
	e.protected = protected
	e.policy++
	return e.snapshot(), nil
}

// HoldAttempt commits a reconnect, a Revert or a Retry under t: the
// cluster is Held with reason, bound to the connection attempt whose first
// usability releases it with protected; its epoch moves.
func (r *Registry) HoldAttempt(t Token, reason string, attempt uint64, protected bool) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if attempt == 0 {
		return Snapshot{}, errors.New("policy: a hold is bound to a connection attempt")
	}
	e, err := r.held(t, Active, Held)
	if err != nil {
		return Snapshot{}, err
	}
	e.state, e.reason, e.attempt, e.releaseProtected = Held, reason, attempt, protected
	e.policy++
	return e.snapshot(), nil
}

// ReleaseAttempt is attempt's first usability: a cluster Held for that
// attempt becomes Active with the protection its hold recorded, and its
// epoch moves. Any other attempt, or a cluster not held for one, changes
// nothing. It needs no reservation: a release during another Apply's
// reservation is recorded exactly once, and that Apply then sees the epoch
// moved at its re-plan.
func (r *Registry) ReleaseAttempt(cluster string, attempt uint64) (Snapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.lookup(cluster)
	if !ok {
		return Snapshot{}, false
	}
	if e.state != Held || attempt == 0 || e.attempt != attempt {
		return e.snapshot(), false
	}
	e.state, e.reason, e.attempt, e.protected = Active, "", 0, e.releaseProtected
	e.policy++
	return e.snapshot(), true
}

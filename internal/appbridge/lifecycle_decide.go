package appbridge

import (
	"context"
	"errors"

	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/policy"
)

// The commit gate's decision (Settings S2 §4.5). An Apply takes the fleet
// Apply lock, reserves its cluster, re-plans and checks its interlock, and
// prepares any connection outside the gate. Then it decides inside the
// gate, finally: the cluster's activation is still the one its plan bound,
// or it refuses with a fresh plan having changed nothing; then it writes
// and commits by class. Only after the gate does the retired identity's
// end reach its work, the sink and the index, and a connection start or
// stop.

// Bound is what a plan binds of a cluster's activation: the epoch it was
// planned under and, while Held, the attempt the hold waits on.
type Bound struct {
	Epoch   policy.Epoch
	Attempt uint64
}

// BoundOf is cluster's activation as a plan binds it; false for a name the
// policy registry does not have (an addition or a connect binds nothing).
func (l *Lifecycle) BoundOf(cluster string) (Bound, bool) {
	snap, ok := l.pol.Snapshot(cluster)
	if !ok {
		return Bound{}, false
	}
	return Bound{Epoch: snap.Epoch, Attempt: snap.Attempt}, true
}

// ErrMoved is an activation that moved after its plan bound it: a usability
// applied since, a release or a recovery. Nothing was written or committed;
// the Apply refuses with a fresh plan.
var ErrMoved = errors.New("the cluster's connection changed since this was planned, so nothing was applied: review the new plan")

// Decide is an Apply's final decision on cluster, under its commit gate:
// the activation must still be bound - or Decide returns ErrMoved having
// run nothing - and then commit runs, still under the gate: the write, then
// the class's transition. A usability queued meanwhile waits for the gate,
// and is applied after it: stale when the decision retired its identity,
// applied once when it did not. commit must not deliver to the health sink,
// start or stop a connection, or call into a worker.
func (l *Lifecycle) Decide(cluster string, bound Bound, commit func() error) error {
	g := l.gate(cluster)
	g.Lock()
	defer g.Unlock()
	now, ok := l.BoundOf(cluster)
	if ok != (bound != Bound{}) || now != bound {
		return ErrMoved
	}
	return commit()
}

// Switch is the reconnect class's decision and handover, for a reconnect,
// a Revert or a Retry (S2 §4.5 steps 6-7): under the gate, the plan's bound
// still current, it writes, holds the cluster under tok for p's first
// usability - which publishes protected - and retires the current identity.
// After the gate, the retired identity's work and generation end with why,
// p is installed and started, and the old connection stops. Refused or
// failed before its commit, it discards p and changes nothing.
func (l *Lifecycle) Switch(ctx context.Context, tok policy.Token, bound Bound, p *fleet.Prepared, reason, why string, protected bool, write func() error) error {
	name := p.Name()
	var old fleet.Conn
	var oldID fleet.ConnID
	var retired bool
	err := l.Decide(name, bound, func() error {
		if err := write(); err != nil {
			return err
		}
		if _, err := l.pol.HoldAttempt(tok, reason, uint64(p.ID()), protected); err != nil {
			return err
		}
		old, oldID, retired = l.retireLocked(name)
		return nil
	})
	if err != nil {
		p.Discard()
		return err
	}
	if retired {
		l.ended(name, oldID, why)
	}
	if err := l.reg.Install(ctx, p); err != nil {
		p.Discard()
		return err
	}
	if retired {
		stopRetired(name, old)
	}
	return nil
}

// Unseat is the removal class's decision and handover (S2 §4.5): under the
// gate, the plan's bound still current, it writes and retires the current
// identity; after the gate, its work and generation end with why, its slot
// goes, its connection stops, and the cluster is forgotten everywhere.
func (l *Lifecycle) Unseat(bound Bound, name, why string, write func() error) error {
	var old fleet.Conn
	var oldID fleet.ConnID
	var retired bool
	if err := l.Decide(name, bound, func() error {
		if err := write(); err != nil {
			return err
		}
		old, oldID, retired = l.retireLocked(name)
		// the removal is published with the retirement, under the gate:
		// work admitted before - a ticket, a fence - is refused from now,
		// before any callback below can block (Codex's review of S2b, R2)
		l.pol.Forget(name)
		return nil
	}); err != nil {
		return err
	}
	if retired {
		l.ended(name, oldID, why)
	}
	if err := l.reg.Drop(name); err != nil {
		return err
	}
	if retired {
		stopRetired(name, old)
	}
	l.forget(name)
	return nil
}

// HoldBuildFailures holds each cluster whose connection failed to build at
// start (S2 §4.5): saved, not active, with why, until a Retry connects it.
func HoldBuildFailures(reg *fleet.Registry, pol *policy.Registry) {
	for name, why := range reg.BuildFailures() {
		_, _ = pol.Hold(name, why)
	}
}

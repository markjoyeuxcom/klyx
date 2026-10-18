package lb

import (
	"time"

	corev1 "k8s.io/api/core/v1"
)

// SvcState is a Service's state as the episode tracker records it.
type SvcState int

const (
	StateOther     SvcState = iota // not a LoadBalancer
	StateAllocated                 // a LoadBalancer with an address
	StatePending                   // a LoadBalancer without one
)

// StateOf classifies a Service for the tracker.
func StateOf(s *corev1.Service) SvcState {
	switch {
	case !IsLoadBalancer(s):
		return StateOther
	case AllocationOf(s).Allocated:
		return StateAllocated
	}
	return StatePending
}

// Continuity identifies one unbroken observation of the Services kind: its
// generation and continuity epoch (observe.KindState). Any change - a
// relist, a recorded error, a new generation - ends continuity: pending
// before and after cannot prove the Service stayed pending (S4 §3).
type Continuity struct{ Generation, Epoch uint64 }

// Tracker records each Service's last observed state and its current
// pending episode, keyed by UID (S4 §3, revisions 1 and 2). It is fed
// level-triggered snapshots: transitions between two snapshots are not
// seen, so only a pending state following a recorded non-pending one has
// an observed start; every Service first seen pending, whenever it was
// created, gets a lower bound from its first observation.
type Tracker struct {
	cont  Continuity
	began bool
	last  map[string]tracked
}

type tracked struct {
	state SvcState
	ep    *Episode
}

// Observe applies one complete, live snapshot (UID to state) taken at now
// under continuity c, and returns every pending Service's episode. A
// snapshot that is not live and complete must not be passed: the caller
// calls Break instead, and the next Observe restarts every episode.
func (t *Tracker) Observe(c Continuity, now time.Time, states map[string]SvcState) map[string]Episode {
	gap := !t.began || c != t.cont
	next := make(map[string]tracked, len(states))
	out := map[string]Episode{}
	for uid, st := range states {
		if st != StatePending {
			next[uid] = tracked{state: st}
			continue
		}
		prev, had := t.last[uid]
		var ep Episode
		switch {
		case gap || !had:
			ep = Episode{Start: now} // a lower bound: the start was not observed
		case prev.state == StatePending && prev.ep != nil:
			ep = *prev.ep
		default:
			ep = Episode{Start: now, Observed: true} // seen leaving allocated or another type
		}
		e := ep
		next[uid] = tracked{state: st, ep: &e}
		out[uid] = ep
	}
	t.cont, t.began, t.last = c, true, next // absent UIDs drop: a new UID is a new Service
	return out
}

// Break records a continuity loss (the kind left live): the next Observe
// restarts every pending episode as a lower bound.
func (t *Tracker) Break() { t.began = false }

package observe

import (
	"context"
	"time"
)

// The role view (The Line slice 9 note §3): what the committed ledger says
// of objects by UID, read on the loop goroutine - the ledger is loop-owned,
// committed after the capture's lock is released, so only the loop reads it
// without racing a commit.

// Role view states.
const (
	RoleCurrent      = "current"      // evaluated, in a live partition
	RoleLastObserved = "lastObserved" // in a frozen partition: continuity was lost
	RoleUnverified   = "unverified"   // no evaluated evidence
)

// Unverified reasons.
const (
	ReasonNotEvaluated = "not evaluated" // a member the evaluator left without a role
	ReasonCapped       = "the role list is capped"
	ReasonNoLedger     = "no ledger for this kind" // the Observer evaluates no part counting it
	ReasonNotObserved  = "not observed"            // no entry in any committed partition
)

// RoleQuery names one object: its UID and the Observer kind it came from.
type RoleQuery struct{ UID, Source string }

// RoleView is the ledger's word on one object.
type RoleView struct {
	State       string
	Role, Label string
	// At: current - the commit it belongs to; last observed - the last
	// commit before the loss.
	At     *time.Time
	Reason string // unverified: why
}

type rolesReq struct {
	qs    []RoleQuery
	reply chan []RoleView
}

// RolesNow answers each query from the committed ledger, in order. ok is
// false once the Observer ended, before it started, or when ctx ends first.
func (o *Observer) RolesNow(ctx context.Context, qs []RoleQuery) ([]RoleView, bool) {
	o.mu.Lock()
	started := o.started
	o.mu.Unlock()
	if !started {
		return nil, false
	}
	reply := make(chan []RoleView, 1) // buffered: the loop never blocks on a gone caller
	select {
	case o.roleReqs <- rolesReq{qs: qs, reply: reply}:
	case <-o.ctx.Done():
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
	select {
	case r := <-reply:
		return r, true
	case <-ctx.Done():
		return nil, false
	case <-o.ctx.Done():
		return nil, false
	}
}

// serveRoles reads the ledger after reconciling what it has not yet
// committed (review of 9b): a change the Observer has recorded - a lost
// connection, a relist, a mutation - marks its parts dirty at once, but the
// ledger commits it only at the next (throttled) evaluation. Each dirty
// ledger part that counts a queried source is evaluated now, as a rows
// bootstrap does, so its loss or after-gap is committed - and emitted -
// before any role is read from it. It reports whether a publish is owed.
// Loop goroutine only.
func (o *Observer) serveRoles(req rolesReq) bool {
	sources := map[string]bool{}
	for _, q := range req.qs {
		sources[q.Source] = true
	}
	var due []*partState
	o.mu.Lock()
	for _, d := range o.domains {
		for _, p := range d.parts {
			if p.ledger == nil || !o.dirty[p.key] {
				continue
			}
			for _, src := range p.spec.Counted {
				if sources[src] {
					delete(o.dirty, p.key) // this capture covers every mutation so far
					due = append(due, p)
					break
				}
			}
		}
	}
	o.mu.Unlock()
	owed := false
	now := o.clk.Now()
	for _, p := range due {
		if verChanged, _ := o.evaluate(p, now, false); verChanged {
			owed = true
		}
	}
	if owed {
		o.mu.Lock()
		o.summaryDirty = true
		o.mu.Unlock()
	}
	out := make([]RoleView, len(req.qs))
	for i, q := range req.qs {
		out[i] = o.roleOf(q)
	}
	req.reply <- out
	return owed
}

func (o *Observer) roleOf(q RoleQuery) RoleView {
	best := RoleView{State: RoleUnverified, Reason: ReasonNoLedger}
	counted := false
	better := func(v RoleView) bool {
		switch {
		case v.State != best.State:
			return stateRank(v.State) < stateRank(best.State)
		case v.State == RoleCurrent:
			return RoleRank(v.Role) < RoleRank(best.Role)
		}
		return false
	}
	for _, d := range o.domains {
		for _, p := range d.parts {
			lg := p.ledger
			if lg == nil || !countsSource(p.spec, q.Source) {
				continue
			}
			counted = true
			var v RoleView
			pt := lg.parts[q.Source]
			switch e, ok := partEntry(pt, q.UID); {
			case lg.capped[q.Source]:
				v = RoleView{State: RoleUnverified, Reason: ReasonCapped}
			case !ok:
				v = RoleView{State: RoleUnverified, Reason: ReasonNotObserved}
			case e.unevaluated:
				v = RoleView{State: RoleUnverified, Reason: ReasonNotEvaluated}
			case pt.state == partitionLive:
				v = RoleView{State: RoleCurrent, Role: e.Role, Label: e.Label, At: copyTime(&pt.committedAt)}
			default:
				v = RoleView{State: RoleLastObserved, Role: e.Role, Label: e.Label, At: copyTime(&pt.committedAt)}
			}
			if best.Reason == ReasonNoLedger || better(v) {
				best = v
			}
		}
	}
	if !counted {
		return RoleView{State: RoleUnverified, Reason: ReasonNoLedger}
	}
	return best
}

func partEntry(pt *partition, uid string) (ledgerEntry, bool) {
	if pt == nil {
		return ledgerEntry{}, false
	}
	e, ok := pt.entries[uid]
	return e, ok
}

func countsSource(spec PartSpec, src string) bool {
	for _, c := range spec.Counted {
		if c == src {
			return true
		}
	}
	return false
}

// stateRank: current, then last observed, then unverified.
func stateRank(s string) int {
	switch s {
	case RoleCurrent:
		return 0
	case RoleLastObserved:
		return 1
	}
	return 2
}

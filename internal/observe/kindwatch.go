package observe

import (
	"sort"
	"sync"
	"time"
)

// KindState is one kind's state, captured atomically under the Observer's
// lock: its coverage and its model count can never disagree, and both
// belong to Generation. It is level-triggered: a consumer decides from the
// latest state alone, never from the edges between states.
type KindState struct {
	Kind string
	// GVR is the kind's effective served API (group/version/resource), kept
	// across an absence; empty before discovery first resolved it.
	GVR string
	// Generation is the kind generation the state belongs to (0: none
	// running, e.g. not served or no reader).
	Generation uint64
	// Seq is the kind's watermark: it advances on every model mutation,
	// freshness change, complete read and generation change.
	Seq        uint64
	Coverage   Coverage
	Class, Err string
	// CountKnown: a complete read happened in this generation, so Objects is
	// evidence - current while Live, last good while Stale. Without it no
	// count is known, which is never zero.
	CountKnown bool
	Objects    int
	// Tally is the kind's KindSpec.Tally over the same model as Objects
	// (one capture: never a count from one read and a tally from another);
	// set exactly when CountKnown and the kind has a tally hook.
	Tally map[string]int
	// ObservedAt is the last successful observation: a complete read or a
	// model mutation. Never a failure, never a publication.
	ObservedAt *time.Time
	LastGoodAt *time.Time // Stale: when it left live
}

// kindWatcher delivers one listener's states, newest-wins: a slow listener
// skips intermediate states (they are levels, not edges) and the mailbox is
// one state deep, so nothing queues without bound.
type kindWatcher struct {
	f      func(KindState)
	mu     sync.Mutex
	latest KindState
	has    bool
	signal chan struct{}
	done   chan struct{}
	exited chan struct{}
}

func (w *kindWatcher) offer(st KindState) {
	w.mu.Lock()
	w.latest, w.has = st, true
	w.mu.Unlock()
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

func (w *kindWatcher) run(stop <-chan struct{}) {
	defer close(w.exited)
	for {
		select {
		case <-w.done:
			return
		case <-stop:
			return
		case <-w.signal:
		}
		w.mu.Lock()
		st, ok := w.latest, w.has
		w.has = false
		w.mu.Unlock()
		if ok {
			w.f(st)
		}
	}
}

// WatchKind calls f with kind's state: at once with the state captured at
// subscription (so an already-live kind is never missed), then with the
// newest state after every change. Calls are serial, on one goroutine per
// listener that Observer.Done waits for, and never hold the Observer's
// lock. The returned cancel stops delivery and waits for an in-flight call
// to return; it must not be called from f. An unknown kind, or an Observer
// not started, returns a no-op.
func (o *Observer) WatchKind(kind string, f func(KindState)) (cancel func()) {
	w := &kindWatcher{f: f, signal: make(chan struct{}, 1), done: make(chan struct{}), exited: make(chan struct{})}
	o.mu.Lock()
	ks := o.kinds[kind]
	if ks == nil || !o.started || o.ctx.Err() != nil {
		o.mu.Unlock()
		return func() {}
	}
	ks.watchers = append(ks.watchers, w)
	w.offer(o.kindStateLocked(ks))
	stop := o.ctx.Done()
	o.mu.Unlock()
	o.goSafe(func() { w.run(stop) })
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			for i, x := range ks.watchers {
				if x == w {
					ks.watchers = append(ks.watchers[:i], ks.watchers[i+1:]...)
					break
				}
			}
			o.mu.Unlock()
			close(w.done)
			<-w.exited
		})
	}
}

// notifyKindLocked advances the kind's watermark and offers its listeners
// the new state. Caller holds o.mu.
func (o *Observer) notifyKindLocked(ks *kindState) {
	ks.stateSeq++
	if len(ks.watchers) == 0 {
		return
	}
	st := o.kindStateLocked(ks)
	for _, w := range ks.watchers {
		w.offer(st)
	}
}

// kindStateLocked captures ks's state. Caller holds o.mu.
func (o *Observer) kindStateLocked(ks *kindState) KindState {
	st := KindState{Kind: ks.spec.Name, GVR: ks.servedGVR, Seq: ks.stateSeq}
	g := ks.gen
	if g == nil {
		st.Coverage, st.Class, st.Err = o.kindCoverageLocked(ks)
		return st
	}
	st.Generation = g.id
	f := g.fresh
	st.Coverage, st.Class, st.Err = f.cov, f.class, f.err
	if f.cov == Live || f.cov == Stale {
		st.CountKnown, st.Objects = true, len(g.model)
		if ks.spec.Tally != nil {
			objs := make([]any, 0, len(g.model))
			for _, o := range g.model {
				objs = append(objs, o)
			}
			st.Tally = ks.spec.Tally(objs)
		}
	}
	if !g.observedAt.IsZero() {
		st.ObservedAt = copyTime(&g.observedAt)
	}
	if f.cov == Stale {
		st.LastGoodAt = copyTime(f.lastGoodAt)
	}
	return st
}

// KindInfo is one declared kind: its name and its spec GVR (a CRD kind's
// is group/resource; a core kind's is complete).
type KindInfo struct{ Name, GVR string }

// Kinds lists the Observer's declared kinds, by name.
func (o *Observer) Kinds() []KindInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]KindInfo, 0, len(o.kinds))
	for name, ks := range o.kinds {
		out = append(out, KindInfo{Name: name, GVR: ks.spec.GVR})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// VisitKind calls visit with each object of kind's current model, under
// the Observer's lock, and returns the kind's state captured in the same
// critical section, so what was visited and its generation, seq and
// coverage agree (The Line slice 9 note §2.4). Objects are visited only
// while a count is known (Live or Stale). visit must not retain obj, call
// the Observer, or take a lock that is held while calling it; it returns
// false to stop. complete reports whether every object was visited. ok is
// false for an unknown kind or an Observer not started.
func (o *Observer) VisitKind(kind string, visit func(obj any) bool) (st KindState, complete, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ks := o.kinds[kind]
	if ks == nil || !o.started {
		return KindState{}, false, false
	}
	st = o.kindStateLocked(ks)
	if !st.CountKnown {
		return st, false, true
	}
	for _, obj := range ks.gen.model {
		if !visit(obj) {
			return st, false, true
		}
	}
	return st, true, true
}

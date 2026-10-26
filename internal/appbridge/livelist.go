package appbridge

import (
	"sync"
	"time"
)

const (
	// liveListInterval is the debounce window. Watch events mark the sub dirty;
	// the loop re-runs compute() at most once per interval when dirty. A daily
	// driver watching one namespace sees bursty change (a rollout flips dozens of
	// pods at once); coalescing to 1s keeps the webview calm without feeling
	// laggy. Overridable per-service for tests (10ms).
	liveListInterval = 1 * time.Second

	// liveCloseWaitTimeout bounds the wait on a replaced/closed sub's loop exit so
	// the UI thread is never blocked indefinitely (mirrors logs' closeWaitTimeout).
	liveCloseWaitTimeout = 2 * time.Second

	// liveRetryMaxIntervals caps the failed-compute retry backoff, in ticker
	// intervals (1s production -> retries at 1, 2, 4, ... up to 30s).
	liveRetryMaxIntervals = 30
)

// liveStatusDTO is the liveness edge payload emitted on the *Status event for a
// live subscription. live=true means the watch is up and the latest re-list
// succeeded; false means the watch is down or the latest re-list failed.
type liveStatusDTO struct {
	Live bool `json:"live"`
	// Ended: why the subscription's connection ended it - its cluster
	// reconnected or was removed (Settings S2 §2.3)
	Ended string `json:"ended,omitempty"`
}

// liveSub is one live subscription. stopWatch tears down the fleet WatchDirty
// goroutines; cancel signals the loop to exit; done closes once the loop has
// fully exited (stopped the watch, returned). One subscriber per key.
type liveSub struct {
	stopWatch func()
	cancel    chan struct{}
	done      chan struct{}
}

// liveRegistry runs watch-driven live subscriptions keyed by an opaque string
// (e.g. "pods:<cluster>:<ns>"). Single-subscriber per key: a new open for the
// same key replaces (stops) the old one. The generic core is policy-free - the
// service supplies watchStart, compute, emit, and emitLive closures.
//
// Lifecycle / leak-safety: each open spawns exactly one loop goroutine plus the
// WatchDirty supervisor goroutines. The loop exits on exactly one signal
// (sub.cancel closed), at which point it stops the watch and closes done. open
// replacing the same key, close(key), and closeAll all converge on that single
// exit path. interval is a field (not a const) so tests can drive a 10ms ticker.
type liveRegistry struct {
	interval time.Duration

	mu   sync.Mutex
	subs map[string]*liveSub
}

func newLiveRegistry() *liveRegistry {
	return &liveRegistry{interval: liveListInterval, subs: map[string]*liveSub{}}
}

// open starts (or replaces) the subscription for key.
//
//   - watchStart establishes the dirty-signal source and returns its stop func.
//     onDirty/onLive are wired into the loop below.
//   - compute re-lists and builds the payload; its bool=false means the list
//     failed (compute emits nothing, and the loop reports emitLive(false) once
//     until the next successful compute).
//   - emit pushes a successful payload to the frontend.
//   - emitLive pushes a liveness edge {live:bool}.
//
// On open the loop does ONE immediate compute+emit so a subscriber gets current
// state without waiting a full interval. Watch liveness (onLive) and compute
// liveness are unified onto emitLive: the frontend sees "live" only when the
// watch is up AND the most recent compute succeeded.
func (r *liveRegistry) open(
	key string,
	watchStart func(onDirty func(), onLive func(bool)) (func(), error),
	compute func() (any, bool),
	emit func(payload any),
	emitLive func(bool),
) {
	sub := &liveSub{
		cancel: make(chan struct{}),
		done:   make(chan struct{}),
	}

	// Replace any existing sub for this key BEFORE registering the new one, so a
	// rapid re-open never leaves two loops emitting on the same event. The old
	// sub is detached under the lock but drained OUTSIDE it: stopSub waits on the
	// old loop's done, and that loop (or its watch-failed path) may itself need
	// r.mu to deregister - holding r.mu across the wait would deadlock.
	r.mu.Lock()
	old := r.subs[key]
	delete(r.subs, key)
	r.subs[key] = sub
	interval := r.interval
	r.mu.Unlock()
	if old != nil {
		r.stopSub(old)
	}

	// dirty is the coalescing flag; the loop reads+clears it each tick. watchUp
	// tracks the watch's own liveness so emitLive reflects watch ∧ compute.
	var mu sync.Mutex
	dirty := true // immediate first compute
	watchUp := false
	computeOK := false
	// Failed-compute retry (review 2026-10-03 #6): a failed re-list used to sit
	// not-live until some watch event happened to arrive - forever on a quiet
	// namespace. failures counts consecutive failures; retryAt is when the loop
	// re-lists on its own (exponential backoff in ticker intervals, capped).
	failures := 0
	var retryAt time.Time
	// watchGen counts watch up-edges. A compute records it when it STARTS; a
	// success only certifies "live" if no reconnect happened meanwhile - data
	// fetched before the latest (re)establishment is still emitted, but cannot
	// vouch for the new watch (Codex batch-2 review R5).
	watchGen := 0
	noteCompute := func(ok bool, startedGen int) {
		if ok {
			failures = 0
			computeOK = startedGen == watchGen
			return
		}
		computeOK = false
		failures++
		backoff := interval << (failures - 1)
		if limit := interval * liveRetryMaxIntervals; failures > 6 || backoff > limit {
			backoff = limit
		}
		retryAt = time.Now().Add(backoff)
	}
	// lastLive is tri-state: -1 = never reported, 0 = false, 1 = true. The
	// sentinel forces the FIRST report to emit an explicit edge (even false), so
	// the frontend gets a definite initial liveness signal - e.g. a cold-start
	// compute failure surfaces live(false) once, per the live-list contract.
	lastLive := -1

	report := func() {
		mu.Lock()
		up := 0
		if watchUp && computeOK {
			up = 1
		}
		changed := up != lastLive
		lastLive = up
		mu.Unlock()
		if changed {
			emitLive(up == 1)
		}
	}

	onDirty := func() {
		mu.Lock()
		dirty = true
		mu.Unlock()
	}
	onLive := func(up bool) {
		mu.Lock()
		if up && !watchUp {
			// The watch (re)established. Whatever we hold was computed before this
			// watch existed: changes made while it was down (e.g. deletions down to
			// an empty set, which a fresh watch never replays) are not in it. Force
			// a re-list, and withhold "live" until a compute that started AFTER the
			// watch was up has succeeded (review 2026-10-03 #6).
			dirty = true
			computeOK = false
			watchGen++
		}
		watchUp = up
		mu.Unlock()
		report()
	}

	stopWatch, err := watchStart(onDirty, onLive)
	if err != nil {
		// Could not establish the watch. Report not-live and run a single compute
		// so the subscriber still gets a one-shot snapshot, then exit the sub: with
		// no watch there is nothing to drive re-lists.
		emitLive(false)
		if payload, ok := compute(); ok {
			// Guard: if this sub was replaced while compute ran, drop the stale emit.
			select {
			case <-sub.cancel:
			default:
				emit(payload)
			}
		}
		r.mu.Lock()
		// Only deregister if we are still the registered sub (a racing re-open may
		// have already replaced us).
		if r.subs[key] == sub {
			delete(r.subs, key)
		}
		r.mu.Unlock()
		close(sub.done)
		return
	}
	sub.stopWatch = stopWatch

	// Immediate compute+emit so the UI paints without waiting a full interval.
	// Liveness still waits for the watch's own onLive(true) edge; watchStart
	// merely spawned the watch supervisors and is not itself proof that a watch
	// was established.
	//
	// Clear the coalescing flag BEFORE computing (the tick loop's own
	// clear-before-compute discipline): an onDirty that lands mid-compute must
	// survive so the next tick re-lists - clearing after the compute would
	// erase it and leave the list stale until the next watch event.
	mu.Lock()
	dirty = false
	gen := watchGen
	mu.Unlock()
	if payload, ok := compute(); ok {
		// Guard: if a replace arrived while compute ran, drop the stale emit.
		select {
		case <-sub.cancel:
		default:
			emit(payload)
			mu.Lock()
			noteCompute(true, gen)
			mu.Unlock()
			report()
		}
	} else {
		// A failed read still reaches the frontend: compute returns the DTO with
		// its error, so the view leaves "loading" and shows WHY instead of
		// waiting on a bare live:false (Codex batch-2 review R3).
		emitFailure(sub, payload, emit)
		mu.Lock()
		noteCompute(false, gen)
		mu.Unlock()
		report()
	}

	// Panic net: compute() parses live cluster objects, so a malformed object must
	// not crash the whole app. On a recovered panic the onPanic callback reports
	// not-live and stops the watch, so the lens degrades honestly instead of
	// leaving the last live:true edge latched over a dead compute loop (an
	// error-honesty violation). fn's own defers (close(sub.done), ticker stop)
	// still run first, so stopSub never hangs.
	safeGoOnPanic("livelist:"+key, func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		defer close(sub.done)
		for {
			select {
			case <-sub.cancel:
				sub.stopWatch()
				return
			case <-t.C:
				mu.Lock()
				// Re-list when a watch event marked us dirty, OR when the last
				// compute failed and its backoff has elapsed (no event needed).
				d := dirty || (failures > 0 && !time.Now().Before(retryAt))
				dirty = false
				gen := watchGen
				mu.Unlock()
				if !d {
					continue
				}
				if payload, ok := compute(); ok {
					// Guard: if this sub was replaced/cancelled while compute ran,
					// drop the stale emit rather than clobbering fresh state.
					select {
					case <-sub.cancel:
						continue
					default:
					}
					emit(payload)
					mu.Lock()
					noteCompute(true, gen)
					mu.Unlock()
					report()
				} else {
					emitFailure(sub, payload, emit)
					mu.Lock()
					noteCompute(false, gen)
					mu.Unlock()
					report()
				}
			}
		}
	}, func(any) {
		emitLive(false)
		sub.stopWatch()
	})
}

// emitFailure forwards a failed compute's payload (the DTO carrying its error)
// unless the sub was replaced meanwhile. A nil payload emits nothing.
func emitFailure(sub *liveSub, payload any, emit func(any)) {
	if payload == nil {
		return
	}
	select {
	case <-sub.cancel:
	default:
		emit(payload)
	}
}

// stopSub signals a sub's loop to exit and waits briefly for it to drain. The
// caller must have already removed it from the map. close(cancel) is guarded so
// a double stop (replace + closeAll) never panics on a re-close.
func (r *liveRegistry) stopSub(sub *liveSub) {
	select {
	case <-sub.cancel:
		// already cancelled
	default:
		close(sub.cancel)
	}
	select {
	case <-sub.done:
	case <-time.After(liveCloseWaitTimeout):
	}
}

// close stops the subscription for key. Idempotent: an unknown key is a no-op.
func (r *liveRegistry) close(key string) {
	r.mu.Lock()
	sub := r.subs[key]
	delete(r.subs, key)
	r.mu.Unlock()
	if sub != nil {
		r.stopSub(sub)
	}
}

// closeAll stops every subscription and waits briefly for each to drain. Called
// on app shutdown.
func (r *liveRegistry) closeAll() {
	r.mu.Lock()
	subs := make([]*liveSub, 0, len(r.subs))
	for k, sub := range r.subs {
		subs = append(subs, sub)
		delete(r.subs, k)
	}
	r.mu.Unlock()

	// Signal all first, then wait, so total drain is bounded by one timeout, not N.
	for _, sub := range subs {
		select {
		case <-sub.cancel:
		default:
			close(sub.cancel)
		}
	}
	for _, sub := range subs {
		select {
		case <-sub.done:
		case <-time.After(liveCloseWaitTimeout):
		}
	}
}

// count returns the number of active subscriptions (test helper hook point).
func (r *liveRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subs)
}

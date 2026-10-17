package observe

import (
	"context"
	"math/rand"
	"reflect"
	"sort"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// KindSpec is one watched kind. Kinds are shared by name: a domain that
// counts pods and another that depends on them hold one model.
type KindSpec struct {
	Name  string // unique in the Observer: "pods", "certificates", ...
	GVR   string
	Scope string // "all namespaces" | "cluster-scoped"
	// Probe resolves this kind's served state and version from discovery;
	// nil means always served (core kinds). Discovery is per kind: one
	// domain can mix versions or miss an optional kind (spec §3.3).
	Probe func(ctx context.Context) (Probe, error)
	// NewListWatch builds the source for one reflector instance at the
	// version Probe chose ("" without a probe).
	NewListWatch func(version string) cache.ListerWatcherWithContext
	ExpectedType runtime.Object
	// Project returns the pruned copy kept in the model. It must not mutate
	// its input. Nil keeps the object as decoded.
	Project func(obj any) (any, error)
	// Tally counts named properties of the kind's model - the nodes kind's
	// native Ready tally (1c slice 3 §6) - from the SAME model as Objects,
	// evaluated under the Observer's lock when a state is captured and
	// carried on KindState. It must not mutate or retain the objects. Nil:
	// none.
	Tally func(objs []any) map[string]int
	// Budget bounds the bytes this kind's model retains (coverage S1a §3):
	// projections are admitted in key order (namespace/name) while they
	// fit, and past it they are not retained - the kind then reads
	// truncated, its counts lower bounds. Size measures one projection and
	// MaxObject bounds one: a larger projection is never retained. A change
	// that can let more in (room freed, the run's blocker shrinking or
	// going, an omitted object shrinking to fit) refills by relisting, at
	// most once per RefillEvery. Budget 0 is unbounded.
	Budget    int
	MaxObject int
	Size      func(v any) int
	// Rank orders a budgeted kind's admission in place of the key
	// (coverage S2a §3): the budget admits, evicts and blocks in ascending
	// rank, the key breaking ties. It is a function of the projection. A
	// rank may change with the object: the watch path re-places it (out
	// when it moves past the run's end, in with eviction when it moves
	// into the run, a relist when the blocker moves), so the retained set
	// stays the run a list would admit. Nil ranks by key. A ranked kind's
	// capture carries its window (Capture.Windows).
	Rank func(key string, v any) string
}

// rankOf is a projection's admission order.
func (s KindSpec) rankOf(key string, v any) string {
	if s.Rank != nil {
		return s.Rank(key, v)
	}
	return key
}

// rankBefore orders two admissions: rank, then key.
func rankBefore(ra, ka, rb, kb string) bool {
	if ra != rb {
		return ra < rb
	}
	return ka < kb
}

// RefillEvery bounds a budgeted kind's refill relists.
const RefillEvery = time.Minute

// freshness is one kind's or snapshot's state machine (spec §3.1).
type freshness struct {
	cov        Coverage
	class, err string
	lastGoodAt *time.Time // when it left live (stale)
	readAt     *time.Time // snapshots: last successful read
	truncated  bool
}

// kindGen is one generation of a kind: a model plus a sequence of reflector
// instances. A generation is never restarted; a new one replaces it.
type kindGen struct {
	o          *Observer
	ks         *kindState
	ctx        context.Context
	cancel     context.CancelFunc
	version    string
	model      map[string]any
	id         uint64    // the generation's identity (KindState.Generation)
	observedAt time.Time // the last complete read or model mutation
	current    uint64    // the instance allowed to write; 0 = detached
	nextInst   uint64
	fresh      freshness
	backoff    time.Duration
	// Continuity (The Line slice 2b §2): epoch advances on every recorded
	// error, benign or not, and every Replace; relisting holds from an error
	// until the next Replace. A resumable watch rotation changes neither.
	epoch     uint64
	relisting bool
	// The budget (KindSpec.Budget): retained bytes, each key's size, and
	// whether objects were left out. notRetained counts the objects left
	// out: exact at a complete list, and kept exact by the watch (an Add is
	// a new object, an Update an existing one, a Delete one gone). capped
	// stays set until a complete list clears it. blockKey is the first key
	// the budget ended the run at ("" when only oversized objects were left
	// out), and blockSize its size: every retained key sorts before it.
	retained    int
	sizes       map[string]int
	capped      bool
	notRetained int
	blockKey    string
	blockSize   int
	// blockRank is the blocker's rank; oversized holds, for a ranked kind,
	// the rank of each object left out for its size, so the window covers
	// every omission (S2a §3, Codex's review finding 6).
	blockRank  string
	oversized  map[string]string
	refillAt   time.Time
	refillDue  bool // a refill waits for its window
	cancelInst context.CancelFunc
}

type kindState struct {
	spec  KindSpec
	refs  int      // active domains reading this kind; one generation serves them all
	gen   *kindGen // the single authoritative generation, while served
	parts []partKey
	// Discovery evidence, per kind:
	probe    *probeGen
	probed   bool // a probe has succeeded since activation
	probedAt time.Time
	served   bool
	version  string
	probeErr string // the current discovery failure; cleared by a success
	// servedGVR is the last effective served GVR (group/version/resource):
	// kept across an absence, so a loss names what was observed.
	servedGVR string
	// watchers are WatchKind listeners; stateSeq is the kind's watermark.
	watchers []*kindWatcher
	stateSeq uint64
}

type probeGen struct {
	ctx     context.Context
	cancel  context.CancelFunc
	reprobe chan struct{}
}

// activateKind starts discovery (or, without a probe, the generation) for a
// kind gaining its first reader. Caller holds o.mu.
func (o *Observer) activateKind(ks *kindState) {
	if ks.spec.Probe == nil {
		ks.probed, ks.served = true, true
		o.startKind(ks, "")
		return
	}
	ctx, cancel := context.WithCancel(o.ctx)
	pg := &probeGen{ctx: ctx, cancel: cancel, reprobe: make(chan struct{}, 1)}
	ks.probe = pg
	o.markKindLocked(ks)
	o.goSafe(func() { o.probeLoop(ks, pg) })
}

// deactivateKind ends discovery and the generation when the last reader
// leaves. A later reader starts both afresh. Caller holds o.mu.
func (o *Observer) deactivateKind(ks *kindState) {
	if ks.probe != nil {
		ks.probe.cancel()
		ks.probe = nil
	}
	o.stopKind(ks)
	ks.probed, ks.served, ks.version, ks.probeErr, ks.probedAt = false, false, "", "", time.Time{}
	o.markKindLocked(ks)
}

// probeLoop probes at once, then every ReprobeEvery or on a NotFound list. A
// failed probe confirms nothing: the last known served state stands and the
// probe is retried with backoff. A result for an ended discovery generation
// (released mid-probe) is discarded.
func (o *Observer) probeLoop(ks *kindState, pg *probeGen) {
	backoff := o.cfg.BackoffMin
	for {
		res, err := ks.spec.Probe(pg.ctx)
		wait := o.cfg.ReprobeEvery
		o.mu.Lock()
		if ks.probe != pg || pg.ctx.Err() != nil {
			o.mu.Unlock()
			return
		}
		if err != nil {
			// A failed probe confirms nothing: the last known served state
			// and any live generation stand, and the gap is named.
			if ks.probeErr != err.Error() {
				ks.probeErr = err.Error()
				o.markKindLocked(ks)
			}
			wait = backoff
			backoff = min(backoff*2, o.cfg.BackoffMax)
		} else {
			backoff = o.cfg.BackoffMin
			o.applyProbeLocked(ks, res)
		}
		o.mu.Unlock()
		t := o.clk.NewTimer(wait)
		select {
		case <-pg.ctx.Done():
			t.Stop()
			return
		case <-t.C():
		case <-pg.reprobe:
			t.Stop()
		}
	}
}

func (o *Observer) applyProbeLocked(ks *kindState, res Probe) {
	if ks.probeErr != "" {
		ks.probeErr = ""
		o.markKindLocked(ks) // the discovery gap closes
	}
	first := !ks.probed
	ks.probed, ks.probedAt = true, o.clk.Now()
	switch {
	case !res.Served:
		if ks.served || first {
			ks.served, ks.version = false, ""
			o.stopKind(ks) // absent; a reappearance starts a new generation
			o.markKindLocked(ks)
		}
	case !ks.served || ks.gen == nil || ks.gen.version != res.Version:
		ks.served, ks.version = true, res.Version
		o.stopKind(ks) // a version change starts a new generation
		o.startKind(ks, res.Version)
	}
}

// reprobeKindLocked asks a kind's discovery to run now (a NotFound list may
// mean the CRD or its version went away). Caller holds o.mu.
func (o *Observer) reprobeKindLocked(ks *kindState) {
	if ks.probe != nil {
		select {
		case ks.probe.reprobe <- struct{}{}:
		default:
		}
	}
}

// kindCoverageLocked is a kind's coverage when no generation decides it.
func (o *Observer) kindCoverageLocked(ks *kindState) (Coverage, string, string) {
	switch {
	case ks.refs == 0:
		return Checking, "", "" // not observed by any active domain
	case ks.probed && !ks.served && ks.probeErr != "":
		// Last known absent, but discovery is failing now: absence is not
		// confirmed - retained evidence, named as a discovery gap.
		return Stale, ClassDiscovery, ks.probeErr
	case ks.probed && !ks.served:
		return Absent, "", ""
	case !ks.probed && ks.probeErr != "":
		return Unreadable, ClassDiscovery, ks.probeErr
	default:
		return Checking, "", ""
	}
}

// startKind starts a new generation for ks. Caller holds o.mu.
func (o *Observer) startKind(ks *kindState, version string) {
	ctx, cancel := context.WithCancel(o.ctx)
	o.kindGenSeq++
	g := &kindGen{o: o, ks: ks, ctx: ctx, cancel: cancel, version: version, id: o.kindGenSeq,
		model: map[string]any{}, fresh: freshness{cov: Checking}, backoff: o.cfg.BackoffMin}
	ks.gen = g
	ks.servedGVR = servedGVR(ks.spec.GVR, version)
	o.markKindLocked(ks)
	o.goSafe(func() { g.run() })
	o.goSafe(func() { g.initialTimeout() })
}

// stopKind ends ks's generation: its context is cancelled and its model
// detached, so late callbacks from its instances write nowhere. Caller
// holds o.mu.
func (o *Observer) stopKind(ks *kindState) {
	if ks.gen == nil {
		return
	}
	ks.gen.current = 0
	ks.gen.cancel()
	ks.gen = nil
	o.markKindLocked(ks)
}

// run drives reflector instances until the generation ends. A recorded
// error ends the instance (its context is cancelled, stopping the
// reflector's own resume-from-resourceVersion retry); the next instance
// starts after an exponential backoff and must list completely - a fresh
// reflector has no resourceVersion - before the kind is live again.
func (g *kindGen) run() {
	for {
		o := g.o
		o.mu.Lock()
		if g.ctx.Err() != nil || g.ks.gen != g {
			o.mu.Unlock()
			return
		}
		g.nextInst++
		inst := g.nextInst
		g.current = inst
		o.mu.Unlock()

		ictx, icancel := context.WithCancel(g.ctx)
		o.mu.Lock()
		g.cancelInst = icancel
		o.mu.Unlock()
		lw := &recordingLW{inner: g.ks.spec.NewListWatch(g.version), ctx: ictx,
			onErr: func(err error) { g.record(inst, err, icancel) }}
		r := cache.NewReflectorWithOptions(lw, g.ks.spec.ExpectedType, &modelAdapter{g: g, inst: inst},
			cache.ReflectorOptions{Name: "observe/" + g.ks.spec.Name, TypeDescription: g.ks.spec.Name})
		r.RunWithContext(ictx)
		icancel()
		if g.ctx.Err() != nil {
			return
		}
		// The instance ended on a recorded error: back off, never reset by
		// the start itself - only a Replace (live) resets it.
		o.mu.Lock()
		d := g.backoff
		g.backoff *= 2
		if g.backoff > o.cfg.BackoffMax {
			g.backoff = o.cfg.BackoffMax
		}
		o.mu.Unlock()
		d = d/2 + time.Duration(rand.Int63n(int64(d/2)+1)) // jitter in [d/2, d]
		t := o.clk.NewTimer(d)
		select {
		case <-g.ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
	}
}

// initialTimeout turns a kind that never completed a list into unreadable.
func (g *kindGen) initialTimeout() {
	t := g.o.clk.NewTimer(g.o.cfg.InitialSyncTimeout)
	defer t.Stop()
	select {
	case <-g.ctx.Done():
		return
	case <-t.C():
	}
	o := g.o
	o.mu.Lock()
	defer o.mu.Unlock()
	if g.ks.gen != g || g.fresh.cov != Checking {
		return
	}
	g.fresh.cov = Unreadable
	if g.fresh.class == "" { // no recorded error names a better cause
		g.fresh.class, g.fresh.err = ClassTimeout, "no complete list within "+o.cfg.InitialSyncTimeout.String()
	}
	o.markKindLocked(g.ks)
}

// record applies one recorded error from instance inst and ends that
// instance. Errors after the instance was cancelled, and the protocol's own
// recovery paths, are not freshness events.
func (g *kindGen) record(inst uint64, err error, cancelInst context.CancelFunc) {
	if err == nil {
		return
	}
	o := g.o
	o.mu.Lock()
	if g.ks.gen != g || g.current != inst || g.ctx.Err() != nil {
		o.mu.Unlock()
		return
	}
	// Continuity (slice 2b §2): any error, benign included, means watch
	// history may be lost until the next complete list. It schedules an
	// evaluation without advancing the data watermark.
	g.epoch++
	g.relisting = true
	if benign(err) {
		o.markKindLocked(g.ks)
		o.mu.Unlock()
		return
	}
	class := classify(err)
	now := o.clk.Now()
	switch g.fresh.cov {
	case Live:
		g.fresh.cov, g.fresh.lastGoodAt = Stale, &now
	case Checking:
		if class == ClassForbidden || class == ClassUnauthorized {
			g.fresh.cov = Unreadable
		}
	}
	g.fresh.class, g.fresh.err = class, err.Error()
	g.current = 0 // fence the ended instance at once
	o.markKindLocked(g.ks)
	if class == ClassNotFound {
		o.reprobeKindLocked(g.ks)
	}
	o.mu.Unlock()
	cancelInst()
}

// modelAdapter is the reflector's store for one instance. Writes from any
// instance but the current one are dropped.
type modelAdapter struct {
	g    *kindGen
	inst uint64
}

func (a *modelAdapter) project(obj any) (string, any, error) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return "", nil, err
	}
	if p := a.g.ks.spec.Project; p != nil {
		v, err := p(obj)
		return key, v, err
	}
	return key, obj, nil
}

func (a *modelAdapter) Add(obj any) error    { return a.put(obj, true) }
func (a *modelAdapter) Update(obj any) error { return a.put(obj, false) }

// put records one watched object; added is true for a new object (the
// budget's count of omitted objects tells a new one from a changed one).
func (a *modelAdapter) put(obj any, added bool) error {
	key, v, err := a.project(obj)
	if err != nil {
		return err
	}
	o := a.g.o
	o.mu.Lock()
	defer o.mu.Unlock()
	if a.g.current != a.inst || a.g.ks.gen != a.g {
		return nil
	}
	if old, ok := a.g.model[key]; ok && reflect.DeepEqual(old, v) {
		return nil // nothing the model keeps changed
	}
	if a.g.ks.spec.Budget > 0 {
		a.g.putBudgetedLocked(key, v, added)
		return nil
	}
	a.g.model[key] = v
	a.g.observedAt = o.clk.Now()
	o.bumpKindLocked(a.g.ks)
	return nil
}

func (a *modelAdapter) Delete(obj any) error {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		return err
	}
	o := a.g.o
	o.mu.Lock()
	defer o.mu.Unlock()
	if a.g.current != a.inst || a.g.ks.gen != a.g {
		return nil
	}
	if _, ok := a.g.model[key]; !ok {
		if a.g.capped {
			a.g.omittedGoneLocked(key)
		}
		return nil
	}
	delete(a.g.model, key)
	if a.g.ks.spec.Budget > 0 {
		a.g.retained -= a.g.sizes[key]
		delete(a.g.sizes, key)
		a.g.roomFreedLocked()
	}
	a.g.observedAt = o.clk.Now()
	o.bumpKindLocked(a.g.ks)
	return nil
}

// Replace is the complete-list barrier: it is the only transition to live.
func (a *modelAdapter) Replace(list []any, _ string) error {
	next := make(map[string]any, len(list))
	for _, obj := range list {
		key, v, err := a.project(obj)
		if err != nil {
			return err
		}
		next[key] = v
	}
	var adm admission
	if spec := a.g.ks.spec; spec.Budget > 0 {
		next, adm = admit(next, spec)
	}
	o := a.g.o
	o.mu.Lock()
	defer o.mu.Unlock()
	g := a.g
	if g.current != a.inst || g.ks.gen != g {
		return nil
	}
	changed := !reflect.DeepEqual(g.model, next) || g.capped != adm.capped || g.notRetained != adm.notRetained
	g.model = next
	if g.ks.spec.Budget > 0 {
		g.sizes, g.retained, g.capped, g.notRetained = adm.sizes, adm.retained, adm.capped, adm.notRetained
		g.blockKey, g.blockSize, g.blockRank, g.oversized = adm.blockKey, adm.blockSize, adm.blockRank, adm.oversized
	}
	g.observedAt = o.clk.Now() // a complete read is an observation, changed or not
	// Every list replaces the model: continuity is unproven across it (slice
	// 2b §2), so it schedules an evaluation even when nothing changed.
	g.epoch++
	g.relisting = false
	if g.fresh.cov != Live {
		g.fresh = freshness{cov: Live}
	}
	if changed {
		o.bumpKindLocked(g.ks)
	} else {
		o.markKindLocked(g.ks) // also notifies: the observation time moved
	}
	g.backoff = o.cfg.BackoffMin
	return nil
}

func (a *modelAdapter) Resync() error { return nil }

// admission is a budgeted kind's model after a complete list.
type admission struct {
	sizes       map[string]int
	retained    int
	capped      bool
	notRetained int
	blockKey    string
	blockSize   int
	blockRank   string
	oversized   map[string]string
}

// admit keeps the longest rank-ordered run of projections that fits the
// budget: deterministic whatever order the list arrived in. A projection
// over MaxObject is left out on its own and does not end the run.
func admit(all map[string]any, spec KindSpec) (map[string]any, admission) {
	type ranked struct{ key, rank string }
	keys := make([]ranked, 0, len(all))
	for k, v := range all {
		keys = append(keys, ranked{k, spec.rankOf(k, v)})
	}
	sort.Slice(keys, func(a, b int) bool { return rankBefore(keys[a].rank, keys[a].key, keys[b].rank, keys[b].key) })
	// sized by what is admitted, never by the whole list (Codex's S1a
	// implementation review, finding 2): the retained model's allocation
	// follows the budget
	out := map[string]any{}
	adm := admission{sizes: map[string]int{}}
	full := false
	for _, kr := range keys {
		k := kr.key
		n := spec.Size(all[k])
		if full || (spec.MaxObject > 0 && n > spec.MaxObject) || adm.retained+n > spec.Budget {
			adm.capped, adm.notRetained = true, adm.notRetained+1
			oversize := spec.MaxObject > 0 && n > spec.MaxObject
			if !full && !oversize {
				full = true // the budget ended the run here
				adm.blockKey, adm.blockSize, adm.blockRank = k, n, kr.rank
			}
			if oversize && spec.Rank != nil {
				if adm.oversized == nil {
					adm.oversized = map[string]string{}
				}
				adm.oversized[k] = kr.rank
			}
			continue
		}
		out[k], adm.sizes[k] = all[k], n
		adm.retained += n
	}
	return out, adm
}

// putBudgetedLocked records one watched projection, as a complete list
// would admit it: the run is the key-ordered prefix that fits, an
// oversized projection is left out on its own, and the count of omitted
// objects stays exact (added tells a new object from a changed one). A
// change that a list could admit differently than this update can - the
// blocker shrinking, room freed - refills instead.
func (g *kindGen) putBudgetedLocked(key string, v any, added bool) {
	spec, o := g.ks.spec, g.o
	n := spec.Size(v)
	r := spec.rankOf(key, v)
	old, had := g.sizes[key]
	switch {
	case spec.MaxObject > 0 && n > spec.MaxObject:
		// never retained; it does not end the run
		orank := r
		switch {
		case had:
			g.retained -= old
			delete(g.model, key)
			delete(g.sizes, key)
			g.capped, g.notRetained = true, g.notRetained+1
			g.roomFreedLocked()
		case added:
			g.capped, g.notRetained = true, g.notRetained+1
		case key == g.blockKey:
			// the blocker no longer ends the run: a list may admit past it.
			// Until that list, the boundary stays at the earlier of its old
			// and new ranks: what ranked past the old blocker is not
			// individually known (Codex's S2a implementation review,
			// finding 3)
			if rankBefore(g.blockRank, key, r, key) {
				orank = g.blockRank
			}
			g.blockKey, g.blockSize, g.blockRank = "", 0, ""
			g.refillLocked()
		}
		g.noteOversizedLocked(key, orank)
	case had && g.blockKey != "" && rankBefore(g.blockRank, g.blockKey, r, key):
		// a retained object whose rank moved past the run's end (S2a §3,
		// revision 2): left out, as a list would leave it, and the room it
		// frees can refill
		g.retained -= old
		delete(g.model, key)
		delete(g.sizes, key)
		g.capped, g.notRetained = true, g.notRetained+1
		g.roomFreedLocked()
	case had:
		g.retained += n - old
		g.model[key], g.sizes[key] = v, n
		g.evictLocked()
		if n < old {
			g.roomFreedLocked()
		}
	case key == g.blockKey:
		// the blocker changed: still left out, and a list decides again
		// when it now fits or moved in the order. Until that list, the
		// boundary stays at the earlier of its old and new ranks: objects
		// past the old boundary are not individually known, and one could
		// rank anywhere after it (S2a §3, revision 3)
		moved := r != g.blockRank
		g.blockSize = n
		if rankBefore(r, key, g.blockRank, key) {
			g.blockRank = r
		}
		if moved || g.retained+n <= spec.Budget {
			g.refillLocked()
		}
	case g.blockKey != "" && rankBefore(g.blockRank, g.blockKey, r, key):
		// past the run's end: left out until a list admits it
		if added {
			g.notRetained++
		}
		delete(g.oversized, key) // left out by the run now, not its size
	default:
		// within the run: retained, evicting from the end while over
		if !added && g.notRetained > 0 {
			g.notRetained-- // an object left out for its size now fits
		}
		delete(g.oversized, key)
		g.model[key], g.sizes[key] = v, n
		g.retained += n
		g.evictLocked()
	}
	if g.capped && g.notRetained == 0 {
		g.refillLocked() // nothing left out: a list clears the cap
	}
	g.observedAt = o.clk.Now()
	o.bumpKindLocked(g.ks)
}

// evictLocked leaves out retained keys from the end while over budget;
// the last left out becomes the blocker.
func (g *kindGen) evictLocked() {
	for g.retained > g.ks.spec.Budget {
		last, rank := g.lastKeyLocked()
		n := g.sizes[last]
		g.retained -= n
		delete(g.model, last)
		delete(g.sizes, last)
		g.capped, g.notRetained = true, g.notRetained+1
		g.blockKey, g.blockSize, g.blockRank = last, n, rank
	}
}

// noteOversizedLocked records a ranked kind's object left out for its size,
// so the window covers it.
func (g *kindGen) noteOversizedLocked(key, rank string) {
	if g.ks.spec.Rank == nil {
		return
	}
	if g.oversized == nil {
		g.oversized = map[string]string{}
	}
	g.oversized[key] = rank
}

// windowLocked is a capped ranked kind's window: the rank of its first
// left-out object in admission order, whatever left it out. Every object
// ranking before it is retained. ok is false when the kind is not capped,
// not ranked, or (never expected) capped with no recorded omission.
func (g *kindGen) windowLocked() (string, bool) {
	if !g.capped || g.ks.spec.Rank == nil {
		return "", false
	}
	first, firstKey, ok := g.blockRank, g.blockKey, g.blockKey != ""
	for k, r := range g.oversized {
		if !ok || rankBefore(r, k, first, firstKey) {
			first, firstKey, ok = r, k, true
		}
	}
	return first, ok
}

// roomFreedLocked refills a capped kind when the blocker would now fit.
func (g *kindGen) roomFreedLocked() {
	if g.capped && g.blockKey != "" && g.retained+g.blockSize <= g.ks.spec.Budget {
		g.refillLocked()
	}
}

// omittedGoneLocked counts a left-out object's deletion. The blocker going
// can let the run continue, and nothing left out means a list can clear
// the cap: both refill.
func (g *kindGen) omittedGoneLocked(key string) {
	if g.notRetained > 0 {
		g.notRetained--
	}
	delete(g.oversized, key)
	if key == g.blockKey {
		g.blockKey, g.blockSize, g.blockRank = "", 0, ""
		g.refillLocked()
	} else if g.notRetained == 0 {
		g.refillLocked()
	}
	g.observedAt = g.o.clk.Now()
	g.o.markKindLocked(g.ks)
}

// lastKeyLocked is the retained key last in admission order, and its rank.
func (g *kindGen) lastKeyLocked() (string, string) {
	last, lastRank, found := "", "", false
	for k := range g.sizes {
		r := g.ks.spec.rankOf(k, g.model[k])
		if !found || rankBefore(lastRank, last, r, k) {
			last, lastRank, found = k, r, true
		}
	}
	return last, lastRank
}

// refillLocked relists a capped kind after room was freed, at most once per
// RefillEvery: ending the current instance makes run start a new one, whose
// complete list admits again in key order. No error is recorded, so the
// kind stays live.
func (g *kindGen) refillLocked() {
	o := g.o
	now := o.clk.Now()
	if wait := RefillEvery - now.Sub(g.refillAt); !g.refillAt.IsZero() && wait > 0 {
		if g.refillDue {
			return
		}
		g.refillDue = true
		o.goSafe(func() {
			t := o.clk.NewTimer(wait)
			defer t.Stop()
			select {
			case <-g.ctx.Done():
				return
			case <-t.C():
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			g.refillDue = false
			if g.ks.gen == g && g.capped {
				g.refillLocked()
			}
		})
		return
	}
	g.refillAt = now
	if g.cancelInst != nil {
		cancel := g.cancelInst
		o.goSafe(cancel)
	}
}

// recordingLW wraps a kind's source so every list error, watch-establish
// error and in-stream watch.Error reaches the freshness machine. Errors of a
// watch-list attempt (SendInitialEvents) are not recorded: the reflector
// falls back to a plain list, whose errors are.
type recordingLW struct {
	inner cache.ListerWatcherWithContext
	ctx   context.Context
	onErr func(error)
}

func (l *recordingLW) note(err error) {
	if err != nil && l.ctx.Err() == nil {
		l.onErr(err)
	}
}

func (l *recordingLW) ListWithContext(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
	obj, err := l.inner.ListWithContext(ctx, o)
	l.note(err)
	return obj, err
}

func (l *recordingLW) WatchWithContext(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
	w, err := l.inner.WatchWithContext(ctx, o)
	watchList := o.SendInitialEvents != nil && *o.SendInitialEvents
	if err != nil {
		if !watchList {
			l.note(err)
		}
		return nil, err
	}
	// A successful watch-list stream is reused for ordinary watching after
	// its initial-events-end bookmark and Replace (client-go reflector.go
	// :508/:907): its errors are suppressed only before that barrier.
	return newRecordingWatch(w, l.note, !watchList), nil
}

func (l *recordingLW) List(o metav1.ListOptions) (runtime.Object, error) {
	return l.ListWithContext(l.ctx, o)
}

func (l *recordingLW) Watch(o metav1.ListOptions) (watch.Interface, error) {
	return l.WatchWithContext(l.ctx, o)
}

// IsWatchListSemanticsUnSupported forwards the inner source's answer so
// fake sources in tests disable watch-list as client-go expects.
func (l *recordingLW) IsWatchListSemanticsUnSupported() bool {
	if u, ok := l.inner.(interface{ IsWatchListSemanticsUnSupported() bool }); ok {
		return u.IsWatchListSemanticsUnSupported()
	}
	return false
}

// recordingWatch forwards a watch, noting watch.Error events once armed. A
// plain watch is armed from the start; a watch-list stream arms at its
// initial-events-end bookmark - before it, errors make the reflector fall
// back to a plain list, whose errors are recorded instead.
type recordingWatch struct {
	inner watch.Interface
	out   chan watch.Event
	stop  chan struct{}
	once  sync.Once
}

func newRecordingWatch(inner watch.Interface, note func(error), armed bool) *recordingWatch {
	w := &recordingWatch{inner: inner, out: make(chan watch.Event), stop: make(chan struct{})}
	go func() {
		defer close(w.out)
		for {
			select {
			case ev, ok := <-inner.ResultChan():
				if !ok {
					return
				}
				switch {
				case ev.Type == watch.Error && armed:
					note(apierrors.FromObject(ev.Object))
				case ev.Type == watch.Bookmark && !armed && initialEventsEnd(ev.Object):
					armed = true
				}
				select {
				case w.out <- ev:
				case <-w.stop:
					return
				}
			case <-w.stop:
				return
			}
		}
	}()
	return w
}

func (w *recordingWatch) Stop() {
	w.once.Do(func() {
		close(w.stop)
		w.inner.Stop()
	})
}

func (w *recordingWatch) ResultChan() <-chan watch.Event { return w.out }

func initialEventsEnd(obj runtime.Object) bool {
	m, err := meta.Accessor(obj)
	return err == nil && m.GetAnnotations()[metav1.InitialEventsAnnotationKey] == "true"
}

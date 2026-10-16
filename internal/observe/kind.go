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
	// MaxObject bounds one: a larger projection is never retained. A
	// deletion while capped refills by relisting, at most once per
	// RefillEvery. Budget 0 is unbounded.
	Budget    int
	MaxObject int
	Size      func(v any) int
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
	// out since the last list, a lower bound.
	retained    int
	sizes       map[string]int
	capped      bool
	notRetained int
	refillAt    time.Time
	refillDue   bool // a refill waits for its window
	cancelInst  context.CancelFunc
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

func (a *modelAdapter) Add(obj any) error    { return a.put(obj) }
func (a *modelAdapter) Update(obj any) error { return a.put(obj) }

func (a *modelAdapter) put(obj any) error {
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
		a.g.putBudgetedLocked(key, v)
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
		// a left-out object went: a capped kind may now fit, so it relists
		if a.g.capped {
			a.g.refillLocked()
		}
		return nil
	}
	delete(a.g.model, key)
	if a.g.ks.spec.Budget > 0 {
		a.g.retained -= a.g.sizes[key]
		delete(a.g.sizes, key)
		if a.g.capped {
			a.g.refillLocked()
		}
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
}

// admit keeps the longest key-ordered run of projections that fits the
// budget: deterministic whatever order the list arrived in. A projection
// over MaxObject is left out on its own and does not end the run.
func admit(all map[string]any, spec KindSpec) (map[string]any, admission) {
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(all))
	adm := admission{sizes: map[string]int{}}
	full := false
	for _, k := range keys {
		n := spec.Size(all[k])
		if full || (spec.MaxObject > 0 && n > spec.MaxObject) || adm.retained+n > spec.Budget {
			adm.capped, adm.notRetained = true, adm.notRetained+1
			if spec.MaxObject <= 0 || n <= spec.MaxObject {
				full = true // the budget ended the run
			}
			continue
		}
		out[k], adm.sizes[k] = all[k], n
		adm.retained += n
	}
	return out, adm
}

// putBudgetedLocked admits one watched projection. An object already
// retained is updated; a new one is retained when it fits, or when it sorts
// before the last retained key, which is then left out until it fits. Past
// a capped run's end nothing is admitted until the next list refills it.
func (g *kindGen) putBudgetedLocked(key string, v any) {
	spec, o := g.ks.spec, g.o
	n := spec.Size(v)
	_, had := g.sizes[key]
	switch {
	case spec.MaxObject > 0 && n > spec.MaxObject:
		if had {
			delete(g.model, key)
			g.retained -= g.sizes[key]
			delete(g.sizes, key)
		}
		g.capped, g.notRetained = true, g.notRetained+1
	case had:
		g.retained += n - g.sizes[key]
		g.model[key], g.sizes[key] = v, n
	case g.capped && key > g.lastKeyLocked():
		g.notRetained++
		g.observedAt = o.clk.Now()
		o.markKindLocked(g.ks)
		return
	default:
		g.model[key], g.sizes[key] = v, n
		g.retained += n
	}
	for g.retained > spec.Budget {
		last := g.lastKeyLocked()
		g.retained -= g.sizes[last]
		delete(g.model, last)
		delete(g.sizes, last)
		g.capped, g.notRetained = true, g.notRetained+1
	}
	g.observedAt = o.clk.Now()
	o.bumpKindLocked(g.ks)
}

// lastKeyLocked is the greatest retained key.
func (g *kindGen) lastKeyLocked() string {
	last := ""
	for k := range g.sizes {
		if k > last {
			last = k
		}
	}
	return last
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

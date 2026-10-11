package observe

import (
	"context"
	"math/rand"
	"reflect"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	// NewListWatch builds the source for one reflector instance at the
	// served version the domain probe chose ("" for core kinds).
	NewListWatch func(version string) cache.ListerWatcherWithContext
	ExpectedType runtime.Object
	// Project returns the pruned copy kept in the model. It must not mutate
	// its input. Nil keeps the object as decoded.
	Project func(obj any) (any, error)
}

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
	o        *Observer
	ks       *kindState
	ctx      context.Context
	cancel   context.CancelFunc
	version  string
	model    map[string]any
	current  uint64 // the instance allowed to write; 0 = detached
	nextInst uint64
	fresh    freshness
	backoff  time.Duration
}

type kindState struct {
	spec  KindSpec
	refs  int
	gen   *kindGen
	parts []partKey // parts that count or depend on this kind
}

// startKind starts a new generation for ks. Caller holds o.mu.
func (o *Observer) startKind(ks *kindState, version string) {
	ctx, cancel := context.WithCancel(o.ctx)
	g := &kindGen{o: o, ks: ks, ctx: ctx, cancel: cancel, version: version,
		model: map[string]any{}, fresh: freshness{cov: Checking}, backoff: o.cfg.BackoffMin}
	ks.gen = g
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
	if err == nil || benign(err) {
		return
	}
	o := g.o
	o.mu.Lock()
	if g.ks.gen != g || g.current != inst || g.ctx.Err() != nil {
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
	a.g.model[key] = v
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
		return nil
	}
	delete(a.g.model, key)
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
	o := a.g.o
	o.mu.Lock()
	defer o.mu.Unlock()
	g := a.g
	if g.current != a.inst || g.ks.gen != g {
		return nil
	}
	changed := !reflect.DeepEqual(g.model, next)
	g.model = next
	if changed {
		o.bumpKindLocked(g.ks)
	}
	if g.fresh.cov != Live {
		g.fresh = freshness{cov: Live}
		o.markKindLocked(g.ks)
	}
	g.backoff = o.cfg.BackoffMin
	return nil
}

func (a *modelAdapter) Resync() error { return nil }

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
	if watchList {
		return w, nil
	}
	return newRecordingWatch(w, l.note), nil
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

// recordingWatch forwards a watch, noting watch.Error events.
type recordingWatch struct {
	inner watch.Interface
	out   chan watch.Event
	stop  chan struct{}
	once  sync.Once
}

func newRecordingWatch(inner watch.Interface, note func(error)) *recordingWatch {
	w := &recordingWatch{inner: inner, out: make(chan watch.Event), stop: make(chan struct{})}
	go func() {
		defer close(w.out)
		for {
			select {
			case ev, ok := <-inner.ResultChan():
				if !ok {
					return
				}
				if ev.Type == watch.Error {
					note(apierrors.FromObject(ev.Object))
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

package observe

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/utils/clock"
)

// incarnations is process-wide: a new Observer for the same cluster always
// outranks an older one (spec §3.4).
var incarnations atomic.Uint64

// Owner holds a domain lease. The refcount is the number of distinct owners;
// a repeated Acquire by the same owner is idempotent.
type Owner struct{ Kind, ID string }

// HealthOwner is the Observer's own cluster-lifetime lease holder.
var HealthOwner = Owner{Kind: "health", ID: "observer"}

// Probe is one kind's served state from discovery.
type Probe struct {
	Served  bool
	Version string
}

// DomainSpec is one task: its parts and, through them, the kinds and
// snapshots it reads. Discovery is per kind (KindSpec.Probe); a part is
// absent when every counted source is absent, a task when every part is.
type DomainSpec struct {
	ID    string
	Parts []PartSpec
}

// PartSpec is one counted entity set. Counted and Deps name kinds or
// snapshots registered with the Observer.
type PartSpec struct {
	ID, Entity, Scope, Classifier string
	Counted, Deps                 []string
	// Facts names external inputs the evaluator reads (SetFact), e.g.
	// whether Flux is installed. They are captured with the objects, and a
	// change re-evaluates the part without touching its data watermark.
	Facts []string
	// Evaluate classifies one capture. It must be pure: same capture, same
	// result. Rows are built only when c.Rows is set.
	Evaluate func(c Capture) PartResult
}

// Capture is one part's atomic view: every listed source's objects (sorted
// by key) taken under one lock, at one time.
type Capture struct {
	Now     time.Time
	Objects map[string][]any
	Facts   map[string]any
	Rows    bool
}

// PartResult is what an evaluator returns.
type PartResult struct {
	Counts   *Counts
	Native   *NativeCounts
	Evidence *int
	Top      []Ref
	Rows     any
	Next     *time.Time
}

// Sink receives publications. Calls come from the Observer's loop goroutine,
// never under its lock.
type Sink interface {
	Summary(Summary)
	Rows(Rows)
}

// Config configures one Observer. Zero durations take the spec defaults.
type Config struct {
	Cluster   string
	Clock     clock.WithTicker
	Sink      Sink
	Kinds     []KindSpec
	Snapshots []SnapshotSpec
	Domains   []DomainSpec
	// NamespacesKind names a kind the Observer holds for its lifetime and
	// counts into Summary.Namespaces (nil until it is live).
	NamespacesKind     string
	Throttle           time.Duration // 1s
	InitialSyncTimeout time.Duration // 60s
	ReprobeEvery       time.Duration // 5m
	BackoffMin         time.Duration // 1s
	BackoffMax         time.Duration // 30s
}

type partKey struct{ task, part string }

type partState struct {
	spec PartSpec
	key  partKey
	obs  uint64 // advanced under o.mu by any counted/dependency mutation
	subs map[Owner]bool
	// Loop-owned (never touched under o.mu by other goroutines):
	ver           uint64
	last          *Part
	lastRows      any
	hasRows       bool
	boundaryFired bool      // its Next passed and it awaits re-evaluation
	evaluatedAt   time.Time // the capture time of its current version
	rowsPubVer    uint64    // the version last broadcast to subscribers
	rowsPub       bool      // rows were broadcast in this subscription period
}

type domainState struct {
	spec   DomainSpec
	owners map[Owner]bool
	active bool
	parts  []*partState
	// controller is the task's controller health (SetController): its own
	// identity, never added to a part count.
	controller *ControllerIssue
}

// Observer is one cluster's health observation (spec §2-§5).
type Observer struct {
	cfg         Config
	clk         clock.WithTicker
	incarnation uint64

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup

	mu           sync.Mutex
	kinds        map[string]*kindState
	snaps        map[string]*snapState
	domains      []*domainState
	byTask       map[string]*domainState
	dirty        map[partKey]bool
	summaryDirty bool
	conn         string
	facts        map[string]any
	latest       *Summary
	seq          uint64
	// stats: the Observer's own cost (Stats); written on the loop goroutine.
	statPublishes atomic.Uint64
	statEvalNanos atomic.Int64
	statLastEval  atomic.Int64
	started      bool

	wake chan struct{}
	reqs chan rowsReq
}

// New builds an Observer. It panics on a spec that names an unregistered
// source or repeats an id - a programming error, not a runtime condition.
func New(cfg Config) *Observer {
	if cfg.Throttle <= 0 {
		cfg.Throttle = time.Second
	}
	if cfg.InitialSyncTimeout <= 0 {
		cfg.InitialSyncTimeout = 60 * time.Second
	}
	if cfg.ReprobeEvery <= 0 {
		cfg.ReprobeEvery = 5 * time.Minute
	}
	if cfg.BackoffMin <= 0 {
		cfg.BackoffMin = time.Second
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 30 * time.Second
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	o := &Observer{cfg: cfg, clk: clk, incarnation: incarnations.Add(1),
		kinds: map[string]*kindState{}, snaps: map[string]*snapState{}, byTask: map[string]*domainState{},
		dirty: map[partKey]bool{}, done: make(chan struct{}), wake: make(chan struct{}, 1), reqs: make(chan rowsReq)}
	for _, k := range cfg.Kinds {
		if _, dup := o.kinds[k.Name]; dup {
			panic("observe: duplicate kind " + k.Name)
		}
		o.kinds[k.Name] = &kindState{spec: k}
	}
	for _, s := range cfg.Snapshots {
		if _, dup := o.snaps[s.Name]; dup || o.kinds[s.Name] != nil {
			panic("observe: duplicate source " + s.Name)
		}
		o.snaps[s.Name] = &snapState{spec: s}
	}
	if cfg.NamespacesKind != "" && o.kinds[cfg.NamespacesKind] == nil {
		panic("observe: unregistered namespaces kind " + cfg.NamespacesKind)
	}
	for _, d := range cfg.Domains {
		if o.byTask[d.ID] != nil {
			panic("observe: duplicate domain " + d.ID)
		}
		ds := &domainState{spec: d, owners: map[Owner]bool{}}
		for _, p := range d.Parts {
			ps := &partState{spec: p, key: partKey{d.ID, p.ID}, subs: map[Owner]bool{}}
			ds.parts = append(ds.parts, ps)
			for _, name := range append(append([]string{}, p.Counted...), p.Deps...) {
				switch {
				case o.kinds[name] != nil:
					o.kinds[name].parts = append(o.kinds[name].parts, ps.key)
				case o.snaps[name] != nil:
					o.snaps[name].parts = append(o.snaps[name].parts, ps.key)
				default:
					panic(fmt.Sprintf("observe: part %s/%s reads unregistered %q", d.ID, p.ID, name))
				}
			}
		}
		o.domains = append(o.domains, ds)
		o.byTask[d.ID] = ds
	}
	return o
}

// Incarnation identifies this Observer (spec §3.4).
func (o *Observer) Incarnation() uint64 { return o.incarnation }

// Done closes once the Final summary is published and every goroutine of
// this Observer - snapshot reads included - has returned.
func (o *Observer) Done() <-chan struct{} { return o.done }

// Latest returns the last published summary.
func (o *Observer) Latest() (Summary, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.latest == nil {
		return Summary{}, false
	}
	return *o.latest, true
}

// Start runs the Observer until ctx ends: it takes the health lease on every
// domain and starts the publish loop. Idempotent.
func (o *Observer) Start(ctx context.Context) {
	o.mu.Lock()
	if o.started {
		o.mu.Unlock()
		return
	}
	o.started = true
	o.ctx, o.cancel = context.WithCancel(ctx)
	for _, d := range o.domains {
		o.acquireLocked(d, HealthOwner)
	}
	if o.cfg.NamespacesKind != "" {
		o.refSourceLocked(o.cfg.NamespacesKind)
	}
	o.summaryDirty = true
	o.mu.Unlock()
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.loop()
	}()
	go func() {
		o.wg.Wait()
		close(o.done)
	}()
	o.signal()
}

// goSafe runs f as a tracked goroutine.
func (o *Observer) goSafe(f func()) {
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		f()
	}()
}

func (o *Observer) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// SetConnection records the connection word (health spec §2). It never sets
// kind freshness (spec §3.1).
func (o *Observer) SetConnection(word string) {
	o.mu.Lock()
	if o.conn == word {
		o.mu.Unlock()
		return
	}
	o.conn = word
	o.summaryDirty = true
	o.mu.Unlock()
	o.signal()
}

// Connection returns the connection word last handed to the Observer.
func (o *Observer) Connection() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.conn
}

// SetController records a task's controller issue (nil when healthy). It is
// never a part count; a change publishes with a new Seq. Unknown tasks are
// ignored.
func (o *Observer) SetController(task string, issue *ControllerIssue) {
	o.mu.Lock()
	d := o.byTask[task]
	if d == nil || reflect.DeepEqual(d.controller, issue) {
		o.mu.Unlock()
		return
	}
	if issue != nil {
		c := *issue
		issue = &c
	}
	d.controller = issue
	o.summaryDirty = true
	o.mu.Unlock()
	o.signal()
}

// SetFact records an external input. A changed value re-evaluates the parts
// that declare it (their version advances if the result changes); an equal
// value is a no-op.
func (o *Observer) SetFact(name string, v any) {
	o.mu.Lock()
	if old, ok := o.facts[name]; ok && reflect.DeepEqual(old, v) {
		o.mu.Unlock()
		return
	}
	if o.facts == nil {
		o.facts = map[string]any{}
	}
	o.facts[name] = v
	for _, d := range o.domains {
		for _, p := range d.parts {
			for _, f := range p.spec.Facts {
				if f == name {
					o.dirty[p.key] = true
				}
			}
		}
	}
	o.mu.Unlock()
	o.signal()
}

// ---- leases ---------------------------------------------------------------

// Acquire takes owner's lease on a domain and returns an idempotent release.
// A repeated Acquire by the same owner adds no reference.
func (o *Observer) Acquire(task string, owner Owner) (release func()) {
	o.mu.Lock()
	d := o.byTask[task]
	if d == nil || !o.started {
		o.mu.Unlock()
		return func() {}
	}
	o.acquireLocked(d, owner)
	o.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			o.releaseLocked(d, owner)
			o.mu.Unlock()
		})
	}
}

func (o *Observer) acquireLocked(d *domainState, owner Owner) {
	if d.owners[owner] {
		return
	}
	d.owners[owner] = true
	if !d.active && o.ctx.Err() == nil {
		d.active = true
		for _, n := range o.sourcesOf(d) {
			o.refSourceLocked(n)
		}
		o.markDomainLocked(d)
	}
}

func (o *Observer) releaseLocked(d *domainState, owner Owner) {
	if !d.owners[owner] {
		return
	}
	delete(d.owners, owner)
	for _, p := range d.parts {
		delete(p.subs, owner)
	}
	if len(d.owners) == 0 && d.active {
		o.deactivateLocked(d)
	}
}

func (o *Observer) deactivateLocked(d *domainState) {
	d.active = false
	for _, n := range o.sourcesOf(d) {
		o.unrefSourceLocked(n)
	}
	o.markDomainLocked(d)
}

// SubscribeRows asks for a part's rows on every version change, owned by
// owner (who should hold the domain lease). The returned func unsubscribes.
func (o *Observer) SubscribeRows(task, part string, owner Owner) (unsubscribe func()) {
	o.mu.Lock()
	ps := o.partLocked(task, part)
	if ps == nil {
		o.mu.Unlock()
		return func() {}
	}
	ps.subs[owner] = true
	o.dirty[ps.key] = true // the next tick builds its rows
	o.mu.Unlock()
	o.signal()
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			delete(ps.subs, owner)
			o.mu.Unlock()
		})
	}
}

func (o *Observer) partLocked(task, part string) *partState {
	d := o.byTask[task]
	if d == nil {
		return nil
	}
	for _, p := range d.parts {
		if p.spec.ID == part {
			return p
		}
	}
	return nil
}

func (o *Observer) sourcesOf(d *domainState) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range d.parts {
		for _, n := range append(append([]string{}, p.spec.Counted...), p.spec.Deps...) {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// refSourceLocked adds an active domain's reference to a source. A kind's
// first reference starts its discovery (or, without a probe, its single
// authoritative generation); later references share it.
func (o *Observer) refSourceLocked(name string) {
	if ks := o.kinds[name]; ks != nil {
		ks.refs++
		if ks.refs == 1 {
			o.activateKind(ks)
		}
		return
	}
	ss := o.snaps[name]
	ss.refs++
	if ss.refs == 1 {
		o.startSnap(ss)
	}
}

func (o *Observer) unrefSourceLocked(name string) {
	if ks := o.kinds[name]; ks != nil {
		ks.refs--
		if ks.refs == 0 {
			o.deactivateKind(ks)
		}
		return
	}
	ss := o.snaps[name]
	ss.refs--
	if ss.refs == 0 {
		o.stopSnap(ss)
	}
}

// Refresh asks a snapshot for an immediate read (single-flight).
func (o *Observer) Refresh(snapshot string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if ss := o.snaps[snapshot]; ss != nil && ss.gen != nil {
		select {
		case ss.gen.refresh <- struct{}{}:
		default:
		}
	}
}

// ---- dirty marking (caller holds o.mu) -----------------------------------

func (o *Observer) bumpKindLocked(ks *kindState) {
	if ks.spec.Name == o.cfg.NamespacesKind {
		o.summaryDirty = true
	}
	for _, k := range ks.parts {
		o.partOf(k).obs++
		o.dirty[k] = true
	}
	o.signal()
}

func (o *Observer) markKindLocked(ks *kindState) {
	if ks.spec.Name == o.cfg.NamespacesKind {
		o.summaryDirty = true
	}
	for _, k := range ks.parts {
		o.dirty[k] = true
	}
	o.signal()
}

func (o *Observer) bumpSnapLocked(ss *snapState) {
	for _, k := range ss.parts {
		o.partOf(k).obs++
		o.dirty[k] = true
	}
	o.signal()
}

func (o *Observer) markSnapLocked(ss *snapState) {
	for _, k := range ss.parts {
		o.dirty[k] = true
	}
	o.signal()
}

func (o *Observer) markDomainLocked(d *domainState) {
	for _, p := range d.parts {
		o.dirty[p.key] = true
	}
	o.summaryDirty = true
	o.signal()
}

func (o *Observer) partOf(k partKey) *partState {
	for _, p := range o.byTask[k.task].parts {
		if p.key == k {
			return p
		}
	}
	panic("observe: unknown part")
}

// Stats are the Observer's own cost counters (1c spec §7): summaries
// published, and the wall time spent capturing and evaluating parts on its
// single loop goroutine - an upper bound on the CPU evaluation uses.
type Stats struct {
	Publishes uint64        // summaries emitted
	EvalTime  time.Duration // cumulative capture and evaluation time
	LastEval  time.Duration // capture and evaluation time of the latest publish cycle
}

// Stats returns the cost counters so far.
func (o *Observer) Stats() Stats {
	return Stats{
		Publishes: o.statPublishes.Load(),
		EvalTime:  time.Duration(o.statEvalNanos.Load()),
		LastEval:  time.Duration(o.statLastEval.Load()),
	}
}

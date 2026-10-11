package observe

import (
	"context"
	"fmt"
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

// Probe is a domain's served state from discovery.
type Probe struct {
	Served  bool
	Version string
}

// DomainSpec is one task: its parts, the kinds and snapshots they read, and
// an optional discovery probe (nil = always served, e.g. core kinds).
type DomainSpec struct {
	ID    string
	Probe func(ctx context.Context) (Probe, error)
	Parts []PartSpec
}

// PartSpec is one counted entity set. Counted and Deps name kinds or
// snapshots registered with the Observer.
type PartSpec struct {
	ID, Entity, Scope, Classifier string
	Counted, Deps                 []string
	// Evaluate classifies one capture. It must be pure: same capture, same
	// result. Rows are built only when c.Rows is set.
	Evaluate func(c Capture) PartResult
}

// Capture is one part's atomic view: every listed kind's objects (sorted by
// key) taken under one lock, at one time.
type Capture struct {
	Now     time.Time
	Objects map[string][]any
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
	Cluster            string
	Clock              clock.WithTicker
	Sink               Sink
	Kinds              []KindSpec
	Snapshots          []SnapshotSpec
	Domains            []DomainSpec
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
	dom  *domainState
	obs  uint64 // advanced under o.mu by any counted/dependency mutation
	subs map[Owner]bool
	// Loop-owned (never touched under o.mu by other goroutines):
	ver           uint64
	last          *Part
	lastRows      any
	hasRows       bool
	boundaryFired bool      // its Next passed and it awaits re-evaluation
	evaluatedAt   time.Time // the capture time of its current version
}

type domainState struct {
	spec   DomainSpec
	owners map[Owner]bool
	gen    *domainGen
	parts  []*partState
}

type domainGen struct {
	ctx      context.Context
	cancel   context.CancelFunc
	probed   bool // a probe has succeeded in this generation
	served   bool
	version  string
	acquired bool
	probeErr string // the last failed probe, when none has succeeded
	reprobe  chan struct{}
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
	latest       *Summary
	seq          uint64
	started      bool

	wake chan struct{}
	reqs chan rowsReq
}

// New builds an Observer. It panics on a spec that names an unregistered
// kind or repeats an id - a programming error, not a runtime condition.
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
	for _, d := range cfg.Domains {
		if o.byTask[d.ID] != nil {
			panic("observe: duplicate domain " + d.ID)
		}
		ds := &domainState{spec: d, owners: map[Owner]bool{}}
		for _, p := range d.Parts {
			ps := &partState{spec: p, key: partKey{d.ID, p.ID}, dom: ds, subs: map[Owner]bool{}}
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

// Done closes after the Final publish.
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
	o.summaryDirty = true
	o.mu.Unlock()
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		o.loop()
	}()
	// Done closes once every goroutine of this Observer has exited.
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

// ---- leases ---------------------------------------------------------------

// Acquire takes owner's lease on a domain and returns an idempotent release.
// A repeated Acquire by the same owner returns an equivalent release and
// does not add a reference.
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
	if d.gen == nil && o.ctx.Err() == nil {
		o.startDomainLocked(d)
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
	if len(d.owners) == 0 && d.gen != nil {
		o.stopDomainLocked(d)
	}
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

// ---- domain generations and probes ---------------------------------------

func (o *Observer) startDomainLocked(d *domainState) {
	ctx, cancel := context.WithCancel(o.ctx)
	g := &domainGen{ctx: ctx, cancel: cancel, reprobe: make(chan struct{}, 1)}
	d.gen = g
	o.markDomainLocked(d)
	if d.spec.Probe == nil {
		g.probed, g.served = true, true
		o.acquireSourcesLocked(d, "")
		return
	}
	o.goSafe(func() { o.probeLoop(d, g) })
}

func (o *Observer) stopDomainLocked(d *domainState) {
	g := d.gen
	g.cancel()
	if g.acquired {
		o.releaseSourcesLocked(d)
	}
	d.gen = nil
	o.markDomainLocked(d)
}

// probeLoop probes at once, then every ReprobeEvery or on a NotFound list.
// A failed probe confirms nothing: the last known served set stands and the
// failure is retried with backoff.
func (o *Observer) probeLoop(d *domainState, g *domainGen) {
	backoff := o.cfg.BackoffMin
	for {
		res, err := d.spec.Probe(g.ctx)
		wait := o.cfg.ReprobeEvery
		o.mu.Lock()
		if d.gen != g || g.ctx.Err() != nil {
			o.mu.Unlock()
			return // released or replaced while probing: the result is discarded
		}
		if err != nil {
			if !g.probed {
				g.probeErr = err.Error()
				o.markDomainLocked(d)
			}
			wait = backoff
			backoff = min(backoff*2, o.cfg.BackoffMax)
		} else {
			backoff = o.cfg.BackoffMin
			o.applyProbeLocked(d, g, res)
		}
		o.mu.Unlock()
		t := o.clk.NewTimer(wait)
		select {
		case <-g.ctx.Done():
			t.Stop()
			return
		case <-t.C():
		case <-g.reprobe:
			t.Stop()
		}
	}
}

func (o *Observer) applyProbeLocked(d *domainState, g *domainGen, res Probe) {
	g.probeErr = ""
	changed := !g.probed || g.served != res.Served || (res.Served && g.version != res.Version)
	g.probed = true
	if !changed {
		return
	}
	if g.acquired {
		o.releaseSourcesLocked(d) // a version change starts new kind generations
	}
	g.served, g.version = res.Served, res.Version
	if res.Served {
		o.acquireSourcesLocked(d, res.Version)
	}
	o.markDomainLocked(d)
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

func (o *Observer) acquireSourcesLocked(d *domainState, version string) {
	for _, n := range o.sourcesOf(d) {
		if ks := o.kinds[n]; ks != nil {
			ks.refs++
			if ks.gen == nil {
				o.startKind(ks, version)
			} else if version != "" && ks.gen.version != version {
				o.stopKind(ks)
				o.startKind(ks, version)
			}
		} else if ss := o.snaps[n]; ss != nil {
			ss.refs++
			if ss.gen == nil {
				o.startSnap(ss)
			}
		}
	}
	d.gen.acquired = true
}

func (o *Observer) releaseSourcesLocked(d *domainState) {
	for _, n := range o.sourcesOf(d) {
		if ks := o.kinds[n]; ks != nil {
			ks.refs--
			if ks.refs == 0 {
				o.stopKind(ks)
			}
		} else if ss := o.snaps[n]; ss != nil {
			ss.refs--
			if ss.refs == 0 {
				o.stopSnap(ss)
			}
		}
	}
	d.gen.acquired = false
}

// reprobeKindLocked asks every domain reading ks to probe again (a NotFound
// list may mean the CRD or its version went away).
func (o *Observer) reprobeKindLocked(ks *kindState) {
	for _, d := range o.domains {
		if d.gen == nil || d.spec.Probe == nil {
			continue
		}
		reads := false
		for _, n := range o.sourcesOf(d) {
			reads = reads || n == ks.spec.Name
		}
		if reads {
			select {
			case d.gen.reprobe <- struct{}{}:
			default:
			}
		}
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
	for _, k := range ks.parts {
		o.partOf(k).obs++
		o.dirty[k] = true
	}
	o.signal()
}

func (o *Observer) markKindLocked(ks *kindState) {
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

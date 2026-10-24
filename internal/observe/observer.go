package observe

import (
	"context"
	"fmt"
	"reflect"
	"strings"
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

// Consumer is one reader's own demand for a shared kind (roadmap C3): the
// kind is acquired when any consumer's demand is proven, and absent only
// when every consumer proves it has none. Demand returns true for proven
// demand, false for proven absence, and an error when it cannot tell.
type Consumer struct {
	Name   string
	Demand func(ctx context.Context) (bool, error)
}

// Demand is one consumer's demand for a shared kind, as last probed.
type Demand string

const (
	DemandYes     Demand = "yes"
	DemandNo      Demand = "no"
	DemandUnknown Demand = "unknown"
)

// ConsumerDemand is one consumer's demand, published on a kind's coverage.
type ConsumerDemand struct {
	Name   string `json:"name"`
	Demand Demand `json:"demand"`
}

// ClassDemand: a part's own consumer cannot tell whether it needs the kind
// (C3): the kind is unread for that part, whatever another consumer holds.
const ClassDemand = "demand"

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
	// Ledger: the evaluator emits PartResult.Roles on every evaluation, and
	// the Observer keeps a role ledger and transition stream for the part
	// (The Line slice 2b). Evidence-only parts leave it off.
	Ledger bool
	// Consumer names the part's own consumer of the shared kinds it reads
	// (KindSpec.Consumers, C3). The part sees such a kind only through its
	// consumer's demand: yes is the kind as it is, no reads absent, unknown
	// reads unread - in its objects, completeness, coverage and ledger
	// alike. "" reads the kind as it is.
	Consumer string
}

// Capture is one part's atomic view: every listed source's objects (sorted
// by key) taken under one lock, at one time.
type Capture struct {
	Now     time.Time
	Objects map[string][]any
	// Complete names the sources whose objects here are every object
	// there is: live, with nothing left out by a budget or a cap. Only a
	// complete source proves an absent object departed.
	Complete map[string]bool
	// Current names the sources whose read is Live: their objects are as
	// the API serves them now, though a budget may have left others out
	// (coverage S7a, Codex's implementation review finding 3). A confirmed
	// absence is complete but not current; a stale read is neither. Only a
	// source both complete and current proves an object of a served kind
	// absent.
	Current map[string]bool
	// AsOf holds, for a stale source, when it was last live: its retained
	// objects are dated evidence (coverage S9 §3), never current.
	AsOf map[string]time.Time
	// Capped names the sources a budget or a cap left objects out of,
	// whatever their freshness (coverage S11b, Codex's implementation
	// review finding 1): a stale source that was cut is never whole.
	Capped map[string]bool
	// Windows holds, for a capped kind with a Rank (KindSpec.Rank), the
	// rank of its first left-out object: every object that ranks before it
	// is here, so a question confined to those has a complete answer
	// (coverage S2a §3). Absent when the kind is complete, unranked, or
	// its window is unknown.
	Windows map[string]string
	Facts   map[string]any
	Rows    bool
	// RoleCap bounds PartResult.Roles: an evaluator appends at most RoleCap
	// entries, then sets RolesCapped and stops appending. 0 when the part
	// keeps no ledger.
	RoleCap int
}

// PartResult is what an evaluator returns.
type PartResult struct {
	Counts   *Counts
	Native   *NativeCounts
	Evidence *int
	Alerts   *AlertCounts // an alerts part's counts (coverage S8a §6)
	Top      []Ref
	Rows     any
	Next     *time.Time
	// Roles is every evaluated object's role, for a part with a ledger;
	// RolesCapped reports that it stopped at Capture.RoleCap.
	Roles       []RoleEntry
	RolesCapped bool
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
	// RoleCap bounds each ledger part's role list and ledger (50,000).
	RoleCap int
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
	boundaryFired bool        // its Next passed and it awaits re-evaluation
	evaluatedAt   time.Time   // the capture time of its current version
	capturedAt    time.Time   // the capture time of its latest evaluation
	rowsPubVer    uint64      // the version last broadcast to subscribers
	rowsPub       bool        // rows were broadcast in this subscription period
	ledger        *partLedger // the role ledger (Ledger parts only)
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
	kindGenSeq   uint64 // kind generation identities
	snaps        map[string]*snapState
	domains      []*domainState
	byTask       map[string]*domainState
	dirty        map[partKey]bool
	summaryDirty bool
	conn         string
	// connUsable is whether the connection word allows observation
	// ("connected", or "connected · <reason>"); connEpoch advances on every
	// flip (slice 2b §3). Unset words read as usable.
	connUsable bool
	connEpoch  uint64
	// tsink receives transition batches (nil when the Sink does not
	// implement TransitionSink); tseq numbers them. Loop goroutine only.
	tsink  TransitionSink
	tseq   uint64
	facts  map[string]any
	latest *Summary
	seq    uint64
	// stats: the Observer's own cost (Stats); written on the loop goroutine.
	statPublishes atomic.Uint64
	statEvalNanos atomic.Int64
	statLastEval  atomic.Int64
	started       bool

	wake     chan struct{}
	reqs     chan rowsReq
	roleReqs chan rolesReq // RolesNow, served on the loop (The Line slice 9)
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
	if cfg.RoleCap <= 0 {
		cfg.RoleCap = DefaultRoleCap
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.RealClock{}
	}
	o := &Observer{cfg: cfg, clk: clk, incarnation: incarnations.Add(1),
		kinds: map[string]*kindState{}, snaps: map[string]*snapState{}, byTask: map[string]*domainState{},
		dirty: map[partKey]bool{}, done: make(chan struct{}), wake: make(chan struct{}, 1), reqs: make(chan rowsReq), roleReqs: make(chan rolesReq), connUsable: true}
	o.tsink, _ = cfg.Sink.(TransitionSink)
	for _, k := range cfg.Kinds {
		if _, dup := o.kinds[k.Name]; dup {
			panic("observe: duplicate kind " + k.Name)
		}
		if len(k.Consumers) > 0 && k.Probe != nil {
			panic("observe: kind " + k.Name + " has both a Probe and Consumers")
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
			if p.Ledger {
				ps.ledger = newPartLedger()
			}
			ds.parts = append(ds.parts, ps)
			for _, name := range append(append([]string{}, p.Counted...), p.Deps...) {
				switch {
				case o.kinds[name] != nil:
					o.kinds[name].parts = append(o.kinds[name].parts, ps.key)
					if p.Consumer != "" && len(o.kinds[name].spec.Consumers) > 0 && !hasConsumer(o.kinds[name].spec.Consumers, p.Consumer) {
						panic(fmt.Sprintf("observe: part %s/%s names consumer %q, which kind %q does not have", d.ID, p.ID, p.Consumer, name))
					}
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

func hasConsumer(cs []Consumer, name string) bool {
	for _, c := range cs {
		if c.Name == name {
			return true
		}
	}
	return false
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
// kind freshness (spec §3.1). A flip between usable and down advances the
// connection epoch and re-evaluates every part, so each ledger records the
// loss and, on recovery, reconciles (slice 2b §3); a controller-note change
// within "connected · <reason>" is neither.
func (o *Observer) SetConnection(word string) {
	o.mu.Lock()
	if o.conn == word {
		o.mu.Unlock()
		return
	}
	o.conn = word
	o.summaryDirty = true
	if usable := connectionUsable(word); usable != o.connUsable {
		o.connUsable = usable
		o.connEpoch++
		for _, d := range o.domains {
			for _, p := range d.parts {
				o.dirty[p.key] = true
			}
		}
	}
	o.mu.Unlock()
	o.signal()
}

// connectionUsable: the connection allows observation. "connected · <reason>"
// is a degraded but connected cluster (fleet connectionWord).
func connectionUsable(word string) bool {
	return word == "" || word == "connected" || strings.HasPrefix(word, "connected · ")
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

// RekeySnapshot gives a snapshot a new generation carrying key, unless its
// generation already carries it (coverage S8a §4). The new generation has
// no items, coverage checking and no as-of time; every outcome of the ended
// one - items, errors, truncation and freshness - is discarded by apply's
// generation check. A snapshot that is not running starts with the key
// when it next starts.
func (o *Observer) RekeySnapshot(snapshot, key string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ss := o.snaps[snapshot]
	if ss == nil {
		return
	}
	if ss.key == key && (ss.gen == nil || ss.gen.key == key) {
		return
	}
	ss.key = key
	if ss.gen != nil {
		o.stopSnap(ss)
		o.startSnap(ss)
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
	o.notifyKindLocked(ks)
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
	o.notifyKindLocked(ks)
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
	EvalTime  time.Duration // cumulative capture and evaluation WALL time (not CPU time)
	LastEval  time.Duration // capture and evaluation wall time of the latest publish cycle
}

// Stats returns the cost counters so far.
func (o *Observer) Stats() Stats {
	return Stats{
		Publishes: o.statPublishes.Load(),
		EvalTime:  time.Duration(o.statEvalNanos.Load()),
		LastEval:  time.Duration(o.statLastEval.Load()),
	}
}

// Cluster is the cluster this Observer observes.
func (o *Observer) Cluster() string { return o.cfg.Cluster }

// HasPart reports whether task/part is one of this Observer's parts.
func (o *Observer) HasPart(task, part string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.partLocked(task, part) != nil
}

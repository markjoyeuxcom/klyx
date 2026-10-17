// Package healthnotify turns the Observer's transition stream into health
// notifications (The Line slice 10, parent §8.6). Each partition's non-ok
// evidence is kept apart and folded per UID; an episode opens when a UID's
// fold becomes fault, and only one opened by a change under continuous
// coverage notifies. Faults present at connect or after a gap are listed in
// the app, never notified; a recovery is a continuous change into ok, and
// notifies only an episode whose onset was sent. One notification per UID
// and role per 30 minutes; items coalesce per cluster over 60 seconds;
// nothing is sent while the app shows that cluster. Apply only records:
// sending happens on Flush, after the engine's lock is released.
package healthnotify

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

// Timings and bounds (slice 10 note §3). Evidence is bounded per partition
// by the Observer's own RoleCap, and episodes by the UIDs with evidence, so
// the only engine-local bound is de-duplication, which expires by itself.
const (
	DedupeWindow   = 30 * time.Minute
	BurstWindow    = 60 * time.Second
	BaselineWindow = 2 * time.Second // a connect's partitions, listed as one
	MaxDedupe      = 10_000          // unexpired de-duplication entries per cluster
	MaxListItems   = 20              // objects named per listing
)

// Settings are fleet.yaml's notifications block.
type Settings struct {
	Enabled    bool
	Scope      string // ScopeProtected (the default) or ScopeAll
	Recoveries bool
}

const (
	ScopeProtected = "protected"
	ScopeAll       = "all"
)

// Object is one notified object: enough to say it and to open it pinned.
type Object struct {
	Cluster, Task, Part, GVR, Namespace, Name, UID string
	Kind, Label                                    string
}

// Notification is one OS notification: one object, or several.
type Notification struct {
	Cluster            string
	Title, Body        string
	Single             *Object // set when it names one object; several open the cluster
	Count              int
	Faults, Recoveries int
}

// Listing reasons.
const (
	ListConnect  = "connect"   // faults present at connect
	ListAfterCap = "after-cap" // faults present at a baseline after a cap
	ListAfterGap = "after-gap" // faults found after a gap in coverage
	ListDropped  = "dropped"   // items dropped by a full de-duplication map
)

// Baseline is one in-app listing: faults present at connect or after a gap,
// or items de-duplication had no room for. Never an OS notification.
type Baseline struct {
	Cluster string
	Reason  string
	Count   int
	Items   []Object // at most MaxListItems
}

// Hooks are the engine's outputs and the facts it reads. Send and List are
// called without the engine's lock held; Protected and Place with it.
type Hooks struct {
	Send      func(Notification) error
	List      func(Baseline)
	Protected func(cluster string) bool
	Place     func(cluster string) string // "prd · North Europe"; the cluster's name when empty
}

// Engine is the episode machine. Safe for concurrent use: batches arrive on
// each cluster's Observer loop, settings and focus from the app, Flush on a
// tick.
type Engine struct {
	clk          func() time.Time
	hooks        Hooks
	partitionCap int

	flushMu  sync.Mutex // one Flush at a time: an outcome is recorded before the next dispatch
	mu       sync.Mutex
	settings Settings
	focus    struct {
		cluster string
		focused bool
	}
	clusters map[string]*clusterState
	nextEp   uint64
}

// New returns an engine. partitionCap is the Observer's RoleCap (0: its
// default): a partition never commits more entries than that.
func New(clk func() time.Time, h Hooks, partitionCap int) *Engine {
	if clk == nil {
		clk = time.Now
	}
	if partitionCap <= 0 {
		partitionCap = observe.DefaultRoleCap
	}
	return &Engine{clk: clk, hooks: h, partitionCap: partitionCap, settings: Settings{Scope: ScopeProtected}, clusters: map[string]*clusterState{}}
}

// SetSettings applies new settings. Onsets before enabling never notify;
// disabling cancels every pending item at once.
func (e *Engine) SetSettings(s Settings) {
	if s.Scope != ScopeAll {
		s.Scope = ScopeProtected
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.settings = s
	if !s.Enabled {
		for _, cs := range e.clusters {
			cs.clearPending()
		}
	}
}

// SetFocus records whether the app is focused, and on which cluster.
func (e *Engine) SetFocus(cluster string, focused bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.focus.cluster, e.focus.focused = cluster, focused
}

// ---- state --------------------------------------------------------------

const (
	fault       = "fault"
	ok          = "ok"
	unknownRole = "unknown"
)

type partKey struct{ task, part, source string }

// partition is one (task, part, source)'s evidence: the UIDs whose role
// there is not ok. Ok and absent are not kept - either way the partition
// holds nothing against a recovery.
type partition struct {
	roles   map[string]string
	lost    bool // coverage lost at a loss, restored by after-gap or baseline
	overCap bool // more non-ok entries than the Observer commits: lost until its baseline
}

// fold states, in precedence order.
const (
	foldFault = iota
	foldCoverage
	foldUnknown
	foldQuiet
	foldClear
)

type opening int

const (
	openedOnset    opening = iota // by a change under continuous coverage: eligible to notify
	openedBaseline                // present at a replacement: listed, never notified
	openedAfterGap                // found after a gap: listed, never notified
)

type episode struct {
	id       uint64
	obj      Object
	openedAt time.Time
	opened   opening
	fold     int
	sent     bool // its onset was successfully sent
}

type pendingItem struct {
	recovery bool
	ep       uint64 // the episode that queued it
	obj      Object
	at       time.Time // when it was observed
}

type dedupeKey struct{ uid, role string }

type listing struct {
	b    Baseline
	at   time.Time
	uids map[string]bool // each object once, across the checkpoint's parts
}

type clusterState struct {
	inc      uint64
	parts    map[partKey]*partition
	episodes map[string]*episode
	dedupe   map[dedupeKey]time.Time
	// pending items in queue order. What each still owes is indexed, so an
	// episode's end invalidates its work in O(1); invalid items are
	// compacted away once they are the majority.
	pending  []pendingItem
	owedOn   map[uint64]bool   // episodes whose onset is still owed
	owedRec  map[string]uint64 // per UID, the episode whose recovery is still owed
	dead     int
	windowAt time.Time
	listings []listing
	dropped  int
}

func newClusterState(inc uint64, dedupe map[dedupeKey]time.Time) *clusterState {
	if dedupe == nil {
		dedupe = map[dedupeKey]time.Time{}
	}
	return &clusterState{inc: inc, parts: map[partKey]*partition{}, episodes: map[string]*episode{}, dedupe: dedupe,
		owedOn: map[uint64]bool{}, owedRec: map[string]uint64{}}
}

// clusterLocked returns the cluster's state for inc. A newer incarnation
// replaces it - its episodes close silently, its evidence and pending items
// go - keeping only de-duplication and listings not yet shown.
func (e *Engine) clusterLocked(name string, inc uint64) *clusterState {
	cs := e.clusters[name]
	if cs == nil {
		cs = newClusterState(inc, nil)
		e.clusters[name] = cs
	} else if cs.inc != inc {
		next := newClusterState(inc, cs.dedupe)
		next.listings = cs.listings
		cs = next
		e.clusters[name] = cs
	}
	return cs
}

func (cs *clusterState) part(k partKey) *partition {
	p := cs.parts[k]
	if p == nil {
		p = &partition{roles: map[string]string{}}
		cs.parts[k] = p
	}
	return p
}

// set records uid's role in p: ok and gone remove it.
func set(p *partition, uid, role string) {
	if role == ok || role == observe.StateGone {
		delete(p.roles, uid)
		return
	}
	p.roles[uid] = role
}

// capCheck: a partition never holds more than the Observer commits. The
// bound is on the state a batch leaves - a full turnover's delta carries up
// to twice the cap - and past it the partition is lost until its baseline,
// as the Observer's capped partition is.
func (e *Engine) capCheck(p *partition) {
	if len(p.roles) > e.partitionCap {
		p.overCap, p.lost = true, true
	}
}

// fold is a UID's state over every partition that holds it (§2.2).
func (cs *clusterState) fold(uid string) int {
	best := foldClear
	for _, p := range cs.parts {
		r, held := p.roles[uid]
		if !held {
			continue
		}
		f := foldQuiet
		switch {
		case r == fault && !p.lost:
			f = foldFault
		case r == fault:
			f = foldCoverage
		case r == unknownRole || r == observe.StateUnevaluated || p.lost:
			f = foldUnknown
		}
		if f < best {
			best = f
		}
	}
	return best
}

// ---- applying batches ---------------------------------------------------

// Apply runs one committed batch through the machine (§2). It only records
// state: nothing is sent or listed here.
func (e *Engine) Apply(b observe.TransitionBatch) {
	if b.Mode == observe.ModeSampled {
		return // no UID identity to notify or open (§2.1)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if old := e.clusters[b.Cluster]; old != nil && b.Incarnation < old.inc {
		return // an earlier incarnation's late batch
	}
	cs := e.clusterLocked(b.Cluster, b.Incarnation)
	if b.Kind == observe.BatchEnd {
		// the incarnation's episodes close silently; its evidence and
		// pending items go
		next := newClusterState(cs.inc, cs.dedupe)
		next.listings = cs.listings
		e.clusters[b.Cluster] = next
		return
	}
	p := cs.part(partKey{b.Task, b.Part, b.Source})
	touched := map[string]*observe.Transition{}
	// touch records a UID to settle, with the item that moved it (nil when
	// only its partition's coverage did)
	touch := func(uid string, it *observe.Transition) {
		if it != nil {
			touched[uid] = it
		} else if _, seen := touched[uid]; !seen {
			touched[uid] = nil
		}
	}
	switch b.Kind {
	case observe.BatchLoss:
		p.lost = true
		for uid := range p.roles {
			touch(uid, nil)
		}
	case observe.BatchBaseline:
		for uid := range p.roles {
			touch(uid, nil)
		}
		p.roles, p.lost, p.overCap = map[string]string{}, false, false
		for i := range b.Items {
			it := &b.Items[i]
			if it.Key == "" {
				continue
			}
			set(p, it.Key, it.To)
			touch(it.Key, it)
		}
		e.capCheck(p)
	case observe.BatchChanges, observe.BatchAfterGap:
		wasOver := p.overCap
		for i := range b.Items {
			it := &b.Items[i]
			if it.Key == "" || it.Restated || it.Derived {
				continue // restated and derived items change nothing (slice 2b §8)
			}
			set(p, it.Key, it.To)
			touch(it.Key, it)
		}
		e.capCheck(p)
		if b.Kind == observe.BatchAfterGap && !p.overCap {
			p.lost = false
			for uid := range p.roles {
				touch(uid, nil) // its retained role decides: an empty delta never resumes an unknown
			}
		}
		if p.overCap && !wasOver {
			for uid := range p.roles {
				touch(uid, nil) // the partition went over its cap: lost, as the Observer's capped one
			}
		}
	default:
		return
	}
	uids := make([]string, 0, len(touched))
	for uid := range touched {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	for _, uid := range uids {
		e.settleLocked(cs, b, uid, touched[uid])
	}
}

// settleLocked applies a UID's new fold to its episode (§2.3).
func (e *Engine) settleLocked(cs *clusterState, b observe.TransitionBatch, uid string, it *observe.Transition) {
	f := cs.fold(uid)
	ep := cs.episodes[uid]
	switch {
	case ep == nil && f == foldFault:
		if it == nil {
			return // only an item opens an episode
		}
		e.nextEp++
		ep = &episode{id: e.nextEp, obj: objectOf(b, *it), openedAt: e.observedAt(b), fold: f}
		cs.episodes[uid] = ep
		// a new episode ends, for good, any recovery still pending for the
		// UID's earlier one - even if this one closes before dispatch
		cs.dropRecovery(uid)
		switch b.Kind {
		case observe.BatchChanges:
			ep.opened = openedOnset
			if e.eligibleLocked(cs, b.Cluster) {
				e.enqueueLocked(cs, pendingItem{ep: ep.id, obj: ep.obj, at: ep.openedAt})
			}
		case observe.BatchBaseline:
			ep.opened = openedBaseline
		default:
			ep.opened = openedAfterGap
		}
	case ep == nil:
	case f != foldClear:
		ep.fold = f // fault resumes; coverage or unknown suspend; quiet stays open
	default:
		delete(cs.episodes, uid)
		// its onset will never go: pending work ends with the episode
		cs.dropOnset(ep.id)
		// a continuous change into ok recovers; gone deletes; a baseline or
		// an after-gap clears silently (its moment is unknown)
		if b.Kind == observe.BatchChanges && it != nil && it.To == ok && ep.sent && e.settings.Recoveries && e.eligibleLocked(cs, b.Cluster) {
			obj := ep.obj
			obj.Label = it.Label
			e.enqueueLocked(cs, pendingItem{recovery: true, ep: ep.id, obj: obj, at: e.observedAt(b)})
		}
	}
	// a checkpoint - a connect, an after-cap baseline, an after-gap - lists
	// the faults it holds once in the app, a resumed episode's too (§4)
	if open := cs.episodes[uid]; open != nil && f == foldFault && (b.Kind == observe.BatchBaseline || b.Kind == observe.BatchAfterGap) {
		reason := ListAfterGap
		if b.Kind == observe.BatchBaseline {
			reason = ListConnect
			if b.AfterCap {
				reason = ListAfterCap
			}
		}
		o := open.obj
		if it != nil {
			o.Label = it.Label
		}
		e.listLocked(cs, b.Cluster, reason, o)
	}
}

// owed says whether a pending item is still owed.
func (cs *clusterState) owed(it pendingItem) bool {
	if it.recovery {
		ep, ok := cs.owedRec[it.obj.UID]
		return ok && ep == it.ep
	}
	return cs.owedOn[it.ep]
}

// dropOnset ends an episode's pending onset; dropRecovery a UID's pending
// recovery. Each is O(1): the item stays in the queue, no longer owed.
func (cs *clusterState) dropOnset(ep uint64) {
	if cs.owedOn[ep] {
		delete(cs.owedOn, ep)
		cs.died()
	}
}

func (cs *clusterState) dropRecovery(uid string) {
	if _, ok := cs.owedRec[uid]; ok {
		delete(cs.owedRec, uid)
		cs.died()
	}
}

// died counts an item no longer owed and compacts the queue once such items
// are the majority (past 32), so it holds at most twice what is owed plus 32.
func (cs *clusterState) died() {
	cs.dead++
	if cs.dead <= 32 || cs.dead*2 <= len(cs.pending) {
		return
	}
	kept := cs.pending[:0]
	for _, it := range cs.pending {
		if cs.owed(it) {
			kept = append(kept, it)
		}
	}
	clear(cs.pending[len(kept):])
	cs.pending, cs.dead = kept, 0
}

func (cs *clusterState) owedCount() int { return len(cs.owedOn) + len(cs.owedRec) }

func (cs *clusterState) clearPending() {
	cs.pending, cs.dead = nil, 0
	clear(cs.owedOn)
	clear(cs.owedRec)
}

// observedAt is the batch's capture time, else now.
func (e *Engine) observedAt(b observe.TransitionBatch) time.Time {
	if b.At.IsZero() {
		return e.clk()
	}
	return b.At
}

// eligibleLocked: enabled, and the cluster in scope.
func (e *Engine) eligibleLocked(cs *clusterState, cluster string) bool {
	if !e.settings.Enabled {
		return false
	}
	return e.settings.Scope == ScopeAll || (e.hooks.Protected != nil && e.hooks.Protected(cluster))
}

func role(it pendingItem) string {
	if it.recovery {
		return ok
	}
	return fault
}

// enqueueLocked queues an item unless de-duplication withholds it.
func (e *Engine) enqueueLocked(cs *clusterState, it pendingItem) {
	now := e.clk()
	if at, seen := cs.dedupe[dedupeKey{it.obj.UID, role(it)}]; seen && now.Sub(at) < DedupeWindow {
		return // withheld: this episode's onset does not count as sent
	}
	// the window starts with the first owed item: dead items left queued by
	// O(1) invalidation never shorten a new item's 60 seconds
	if cs.owedCount() == 0 {
		cs.windowAt = now
	}
	if it.recovery {
		cs.dropRecovery(it.obj.UID) // one owed per UID: the latest
		cs.owedRec[it.obj.UID] = it.ep
	} else {
		cs.owedOn[it.ep] = true
	}
	cs.pending = append(cs.pending, it)
}

func objectOf(b observe.TransitionBatch, it observe.Transition) Object {
	gvr := it.GVR
	if gvr == "" {
		gvr = b.GVR
	}
	return Object{Cluster: b.Cluster, Task: b.Task, Part: b.Part, GVR: gvr, Namespace: it.NS, Name: it.Name, UID: it.Key, Kind: kindOf(gvr), Label: it.Label}
}

func (e *Engine) listLocked(cs *clusterState, cluster, reason string, o Object) {
	n := len(cs.listings)
	if n == 0 || cs.listings[n-1].b.Reason != reason {
		cs.listings = append(cs.listings, listing{b: Baseline{Cluster: cluster, Reason: reason}, at: e.clk(), uids: map[string]bool{}})
		n++
	}
	if cs.listings[n-1].uids[o.UID] {
		return
	}
	cs.listings[n-1].uids[o.UID] = true
	l := &cs.listings[n-1].b
	l.Count++
	if len(l.Items) < MaxListItems {
		l.Items = append(l.Items, o)
	}
}

// ---- delivery -----------------------------------------------------------

type outgoing struct {
	cs     *clusterState // the state that dispatched it: a replaced one is never touched
	n      Notification
	onsets map[string]uint64 // uid -> the episode whose onset this carries
}

// Flush delivers what is due: listings once their 2 seconds have passed, and
// each cluster's items once their 60 seconds have. It composes under the
// lock, then sends and lists after releasing it, and records each send's
// outcome against the episode that queued it.
func (e *Engine) Flush() {
	e.flushMu.Lock()
	defer e.flushMu.Unlock()
	e.mu.Lock()
	now := e.clk()
	names := make([]string, 0, len(e.clusters))
	for n := range e.clusters {
		names = append(names, n)
	}
	sort.Strings(names)
	var lists []Baseline
	var sends []outgoing
	for _, name := range names {
		cs := e.clusters[name]
		if cs.dropped > 0 {
			cs.listings = append(cs.listings, listing{b: Baseline{Cluster: name, Reason: ListDropped, Count: cs.dropped}, at: now.Add(-BaselineWindow)})
			cs.dropped = 0
		}
		kept := cs.listings[:0]
		for i, l := range cs.listings {
			// an earlier listing is complete; the last waits out its window
			if i < len(cs.listings)-1 || now.Sub(l.at) >= BaselineWindow {
				lists = append(lists, l.b)
				continue
			}
			kept = append(kept, l)
		}
		cs.listings = kept
		for k, at := range cs.dedupe {
			if now.Sub(at) >= DedupeWindow {
				delete(cs.dedupe, k)
			}
		}
		if cs.owedCount() == 0 && len(cs.pending) > 0 {
			cs.clearPending() // only dead items: nothing is owed
		}
		if cs.owedCount() > 0 && now.Sub(cs.windowAt) >= BurstWindow {
			if out, any := e.dispatchLocked(name, cs, now); any {
				sends = append(sends, out)
			}
		}
	}
	e.mu.Unlock()

	for _, l := range lists {
		if e.hooks.List != nil {
			e.hooks.List(l)
		}
	}
	for _, out := range sends {
		var err error = fmt.Errorf("no sender")
		if e.hooks.Send != nil {
			err = e.hooks.Send(out.n)
		}
		if err != nil {
			e.failed(out)
		}
	}
}

// failed withdraws a failed send's onsets: each still-open episode is
// not sent after all, and a recovery queued for it while the call was in
// flight is removed. It only removes, and only from the state that
// dispatched it - an ended or replaced state is never touched.
func (e *Engine) failed(out outgoing) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, cs := range e.clusters {
		if cs != out.cs {
			continue
		}
		for uid, id := range out.onsets {
			if ep := cs.episodes[uid]; ep != nil && ep.id == id {
				ep.sent = false
			}
			if rec, ok := cs.owedRec[uid]; ok && rec == id {
				cs.dropRecovery(uid)
			}
		}
	}
}

// dispatchLocked re-checks a cluster's pending items against the current
// state and settings (§3.3), consumes their de-duplication allowance and
// composes what remains.
func (e *Engine) dispatchLocked(name string, cs *clusterState, now time.Time) (outgoing, bool) {
	items := make([]pendingItem, 0, len(cs.pending)-cs.dead)
	for _, it := range cs.pending {
		if cs.owed(it) {
			items = append(items, it)
		}
	}
	cs.clearPending()
	if !e.eligibleLocked(cs, name) {
		return outgoing{}, false
	}
	type keptItem struct {
		pendingItem
		qual string
	}
	var kept []keptItem
	for _, it := range items {
		ep := cs.episodes[it.obj.UID]
		qual := ""
		if it.recovery {
			if !e.settings.Recoveries || ep != nil {
				continue // recoveries turned off, or the UID is in a new episode
			}
		} else {
			if ep == nil || ep.id != it.ep {
				continue // recovered, deleted, cleared or ended since
			}
			// observed under continuous coverage, so it goes - as history
			// when the verdict is no longer current
			switch at := it.at.Local().Format("15:04"); ep.fold {
			case foldCoverage:
				qual = " · observed " + at + "; coverage lost since"
			case foldUnknown:
				qual = " · observed " + at + "; now unknown"
			case foldQuiet:
				qual = " · observed " + at + "; not a fault now"
			}
		}
		k := dedupeKey{it.obj.UID, role(it)}
		if at, seen := cs.dedupe[k]; seen && now.Sub(at) < DedupeWindow {
			continue
		}
		if len(cs.dedupe) >= MaxDedupe {
			cs.dropped++ // never evict an unexpired guard
			continue
		}
		cs.dedupe[k] = now
		kept = append(kept, keptItem{it, qual})
	}
	if len(kept) == 0 || (e.focus.focused && e.focus.cluster == name) {
		return outgoing{}, false // nothing left, or the app shows it: consumed, not sent
	}
	out := outgoing{cs: cs, onsets: map[string]uint64{}}
	place := name
	if e.hooks.Place != nil {
		if p := e.hooks.Place(name); p != "" {
			place = p
		}
	}
	n := Notification{Cluster: name, Title: place, Count: len(kept)}
	var names []string
	for _, k := range kept {
		if k.recovery {
			n.Recoveries++
		} else {
			n.Faults++
			out.onsets[k.obj.UID] = k.ep
			// counted as sent from dispatch: a recovery while the call is in
			// flight queues as usual, and a failure withdraws both
			cs.episodes[k.obj.UID].sent = true
		}
		if len(names) < 3 {
			l := line(k.obj) + k.qual
			if k.recovery {
				l = "Recovered: " + l
			}
			names = append(names, l)
		}
	}
	if len(kept) == 1 {
		o := kept[0].obj
		n.Single, n.Body = &o, names[0]
	} else {
		var head []string
		if n.Faults > 0 {
			head = append(head, plural(n.Faults, "new fault", "new faults"))
		}
		if n.Recoveries > 0 {
			head = append(head, plural(n.Recoveries, "recovery", "recoveries"))
		}
		n.Body = fmt.Sprintf("%s on %s: %s", strings.Join(head, " and "), place, strings.Join(names, "; "))
		if more := len(kept) - len(names); more > 0 {
			n.Body += fmt.Sprintf("; +%d more", more)
		}
	}
	out.n = n
	return out, true
}

func line(o Object) string {
	name := o.Name
	if o.Namespace != "" {
		name = o.Namespace + "/" + o.Name
	}
	if o.Label == "" {
		return fmt.Sprintf("%s %s", o.Kind, name)
	}
	return fmt.Sprintf("%s %s: %s", o.Kind, name, o.Label)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// kindOf names a kind from its GVR's resource.
func kindOf(gvr string) string {
	res := gvr[strings.LastIndex(gvr, "/")+1:]
	if k, known := kinds[res]; known {
		return k
	}
	return res
}

var kinds = map[string]string{
	"pods": "Pod", "nodes": "Node", "deployments": "Deployment", "statefulsets": "StatefulSet", "daemonsets": "DaemonSet",
	"kustomizations": "Kustomization", "helmreleases": "HelmRelease", "gitrepositories": "GitRepository",
	"ocirepositories": "OCIRepository", "buckets": "Bucket", "helmrepositories": "HelmRepository", "helmcharts": "HelmChart",
	"applications": "Application", "applicationsets": "ApplicationSet", "certificates": "Certificate", "issuers": "Issuer",
	"clusterissuers": "ClusterIssuer", "externalsecrets": "ExternalSecret", "secretstores": "SecretStore",
	"clustersecretstores": "ClusterSecretStore", "gateways": "Gateway", "httproutes": "HTTPRoute",
	"nodeclaims": "NodeClaim", "nodepools": "NodePool",
	"fluxinstances": "FluxInstance", "resourcesets": "ResourceSet", "resourcesetinputproviders": "ResourceSetInputProvider",
	"imagerepositories": "ImageRepository", "imagepolicies": "ImagePolicy", "imageupdateautomations": "ImageUpdateAutomation",
	"artifactgenerators": "ArtifactGenerator", "externalartifacts": "ExternalArtifact", "receivers": "Receiver",
}

package observe

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
)

// The role ledger and transition stream (The Line slice 2b,
// docs/superpowers/specs/2026-10-14-klyx-the-line-slice2b-ledger.md). A
// ledger part keeps, per counted source, the roles of its last committed
// evaluation and the continuity identity it was committed under. Every
// evaluation - a publish or a RowsNow bootstrap - diffs against it, commits,
// and emits the batches at once on the loop goroutine: nothing is ever
// pending, so delivery is exactly once and Final has nothing to settle.

// DefaultRoleCap bounds a ledger part's role list and each partition.
const DefaultRoleCap = 50000

type partitionState int

const (
	partitionLive   partitionState = iota // committed under continuous coverage
	partitionFrozen                       // a loss was emitted; held for the after-gap
)

// ledgerEntry is one key's committed state: a pointer into the evaluation's
// own role slice (which the committed ledger then retains), never a copy -
// a 50,000-object part keeps one slice and a map of pointers.
type ledgerEntry struct {
	*RoleEntry
	unevaluated bool // present in the source but not evaluated: a state, not a role
}

func (e ledgerEntry) state() string {
	if e.unevaluated {
		return StateUnevaluated
	}
	return e.Role
}

// partition is one counted source's ledger within a part.
type partition struct {
	state       partitionState
	entries     map[string]ledgerEntry
	ident       ContinuityIdentity
	committedAt time.Time
	readAt      *time.Time // sampled: the read the commit used
	lossAt      time.Time  // frozen: when the loss was emitted
}

// partLedger is one part's ledger: a partition per counted source, and the
// sources whose capped episode is open (its loss already emitted).
type partLedger struct {
	parts  map[string]*partition
	capped map[string]bool
}

func newPartLedger() *partLedger {
	return &partLedger{parts: map[string]*partition{}, capped: map[string]bool{}}
}

// captured is what captureLocked records for the ledger beside the objects,
// under the same lock: each source's continuity, the connection and the
// capture time.
type captured struct {
	at         time.Time
	ids        map[string]SourceIdentity
	connEpoch  uint64
	connUsable bool
	readAt     map[string]*time.Time // snapshots: the read the capture holds
	truncated  map[string]bool       // snapshots: a capped read
}

// servedGVR inserts a generation's probed version into a KindSpec GVR. A CRD
// kind's spec GVR is group/resource (no version). A spec that already names
// its version - group/version/resource, or version/resource in the core
// group - is served as it is, even when a probe gates it (Gateway API's
// Services and EndpointSlices are probed but fully versioned): inserting the
// version again would serve "v1/v1/services".
func servedGVR(spec, version string) string {
	if version == "" || versioned(spec) {
		return spec
	}
	i := strings.LastIndex(spec, "/")
	if i < 0 {
		return version + "/" + spec
	}
	return spec[:i] + "/" + version + "/" + spec[i+1:]
}

var apiVersion = regexp.MustCompile(`^v[0-9]+((alpha|beta)[0-9]+)?$`)

// versioned: the spec already names its version.
func versioned(spec string) bool {
	parts := strings.Split(spec, "/")
	return len(parts) == 3 || (len(parts) == 2 && apiVersion.MatchString(parts[0]))
}

// sourceIdentityLocked is one source's continuity now. Caller holds o.mu.
func (o *Observer) sourceIdentityLocked(name string, cov Coverage) SourceIdentity {
	id := SourceIdentity{Source: name, Coverage: cov}
	if ks := o.kinds[name]; ks != nil {
		// The last served GVR, kept across an absence; empty before any
		// generation started, never the unversioned descriptor of a probed kind.
		if g := ks.gen; g != nil {
			id.Generation, id.Epoch, id.Relisting = g.id, g.epoch, g.relisting
			ks.servedGVR = servedGVR(ks.spec.GVR, g.version) // refreshed from the live generation
		}
		// A core kind's version is "": its descriptor is already complete.
		// Before any generation started a probed kind has none: no GVR,
		// never its unversioned descriptor.
		id.GVR = ks.servedGVR
		return id
	}
	if ss := o.snaps[name]; ss != nil && ss.gen != nil {
		id.Generation, id.Epoch = ss.gen.id, ss.gen.epoch
	}
	return id
}

// partitionIdentity is a partition's continuity: its own source, the part's
// dependencies and the connection.
func partitionIdentity(spec PartSpec, source string, cp captured) ContinuityIdentity {
	id := ContinuityIdentity{ConnEpoch: cp.connEpoch, ConnUsable: cp.connUsable}
	id.Sources = append(id.Sources, cp.ids[source])
	for _, d := range spec.Deps {
		id.Sources = append(id.Sources, cp.ids[d])
	}
	return id
}

// liveForLedger: the connection is usable, the source is Live and not
// awaiting a relist, and every dependency is too, or absent.
func liveForLedger(id ContinuityIdentity) bool {
	if !id.ConnUsable {
		return false
	}
	for i, s := range id.Sources {
		if i > 0 && s.Coverage == Absent {
			continue
		}
		if s.Coverage != Live || s.Relisting {
			return false
		}
	}
	return true
}

func sameIdentity(a, b ContinuityIdentity) bool {
	if a.ConnEpoch != b.ConnEpoch || a.ConnUsable != b.ConnUsable || len(a.Sources) != len(b.Sources) {
		return false
	}
	for i := range a.Sources {
		if a.Sources[i] != b.Sources[i] {
			return false
		}
	}
	return true
}

// lossOf names why continuity from prev to cur ended.
func lossOf(prev, cur ContinuityIdentity) *Loss {
	own := cur.Sources[0]
	l := &Loss{Reason: LossRelist, Coverage: own.Coverage}
	if !cur.ConnUsable || cur.ConnEpoch != prev.ConnEpoch {
		l.Reason = LossConnection
		return l
	}
	for i, s := range cur.Sources {
		var p SourceIdentity
		if i < len(prev.Sources) {
			p = prev.Sources[i]
		}
		switch {
		case s.Generation != p.Generation:
			l.Reason = LossGeneration
			return l
		case s.Coverage != Live && !(i > 0 && s.Coverage == Absent):
			l.Reason, l.Coverage = LossCoverage, s.Coverage
			return l
		}
	}
	return l
}

// membershipOf is a watched source's authoritative membership: the UIDs of
// its captured objects, each mapped to its index in objs (its namespace and
// name are read only for the few members the evaluator skipped). ok is false
// when any object has no accessible metadata or UID.
func membershipOf(objs []any) (map[string]int, bool) {
	m := make(map[string]int, len(objs))
	for i, obj := range objs {
		a, err := meta.Accessor(obj)
		if err != nil || a.GetUID() == "" {
			return nil, false
		}
		m[string(a.GetUID())] = i
	}
	return m, true
}

// commitLedger diffs one evaluation against p's ledger, commits it and emits
// the batches (slice 2b §4-5). Loop goroutine only.
func (o *Observer) commitLedger(p *partState, cp captured, c Capture, res PartResult, ver uint64) {
	lg := p.ledger
	if lg == nil || o.tsink == nil {
		return
	}
	// Group by source as indices into the evaluation's own slice: the
	// ledger points into it rather than copying each entry.
	bySource := map[string][]int{}
	if len(p.spec.Counted) > 1 {
		for i := range res.Roles {
			bySource[res.Roles[i].Source] = append(bySource[res.Roles[i].Source], i)
		}
	}
	for _, src := range p.spec.Counted {
		idx, all := bySource[src], len(p.spec.Counted) == 1
		o.commitPartition(p, lg, src, cp, c, res.Roles, idx, all, res.RolesCapped, ver)
	}
}

func (o *Observer) commitPartition(p *partState, lg *partLedger, src string, cp captured, c Capture, roles []RoleEntry, idx []int, all, capped bool, ver uint64) {
	id := partitionIdentity(p.spec, src, cp)
	pt := lg.parts[src]
	base := TransitionBatch{Source: src, GVR: id.Sources[0].GVR, Mode: ModeWatch, PartVer: ver, Identity: id, At: cp.at}
	_, sampled := o.snaps[src]
	if sampled {
		base.Mode = ModeSampled
		base.ReadAt = copyTime(cp.readAt[src])
		if pt != nil {
			base.PrevReadAt = copyTime(pt.readAt)
		}
	}
	emitLoss := func(l *Loss, prevAt *time.Time) {
		b := base
		b.Kind, b.Loss, b.PrevAt = BatchLoss, l, prevAt
		o.emit(p, b)
	}

	// A capability loss is never a deletion and never a recovery.
	if id.Sources[0].Coverage == Absent {
		if pt != nil {
			emitLoss(&Loss{Reason: LossAbsent, Coverage: Absent}, copyTime(&pt.committedAt))
			delete(lg.parts, src)
		}
		return
	}
	live := liveForLedger(id)

	var entries map[string]ledgerEntry
	var membershipUnknown bool
	if !capped && live {
		var st entriesStatus
		entries, st = o.entriesOf(src, id, c, roles, idx, all, sampled && cp.truncated[src])
		capped = st == entriesCapped
		// Unknown membership cannot tell a deletion from a skipped object:
		// continuity is not proven, so the partition is not live (review of
		// 5fcb5f2) until membership is readable again.
		membershipUnknown = st == entriesMembershipUnknown
		live = live && !membershipUnknown
	}
	if capped {
		if !lg.capped[src] {
			var prev *time.Time
			if pt != nil {
				prev = copyTime(&pt.committedAt)
			}
			emitLoss(&Loss{Reason: LossCapped, Coverage: id.Sources[0].Coverage}, prev)
			lg.capped[src] = true
		}
		delete(lg.parts, src)
		return
	}

	commit := func(kind string, items []Transition, prevAt *time.Time) {
		b := base
		b.Kind, b.Items, b.PrevAt = kind, items, prevAt
		if kind == BatchBaseline {
			b.AfterCap = lg.capped[src]
			delete(lg.capped, src)
		}
		if kind != BatchChanges || len(items) > 0 {
			o.emit(p, b)
		}
		lg.parts[src] = &partition{state: partitionLive, entries: entries, ident: id, committedAt: cp.at, readAt: copyTime(cp.readAt[src])}
	}
	derived := sampled && pt != nil && pt.readAt != nil && cp.readAt[src] != nil && pt.readAt.Equal(*cp.readAt[src])

	switch {
	case pt == nil:
		if live {
			commit(BatchBaseline, baselineItems(entries), nil)
		}
	case pt.state == partitionLive && !live:
		l := lossOf(pt.ident, id)
		if membershipUnknown {
			l = &Loss{Reason: LossMembership, Coverage: id.Sources[0].Coverage}
		}
		emitLoss(l, copyTime(&pt.committedAt))
		pt.state, pt.lossAt = partitionFrozen, cp.at
	case pt.state == partitionLive && !sameIdentity(pt.ident, id):
		// A gap between two Live captures: the loss, then the after-gap, in
		// one commit.
		emitLoss(lossOf(pt.ident, id), copyTime(&pt.committedAt))
		commit(BatchAfterGap, diffEntries(pt.entries, entries, false), copyTime(&pt.committedAt))
	case pt.state == partitionLive:
		commit(BatchChanges, diffEntries(pt.entries, entries, derived), copyTime(&pt.committedAt))
	case live: // frozen, now live again
		commit(BatchAfterGap, diffEntries(pt.entries, entries, false), copyTime(&pt.lossAt))
	}
}

type entriesStatus int

const (
	entriesOK                entriesStatus = iota
	entriesCapped                          // over the cap, or a truncated sampled read
	entriesMembershipUnknown               // a captured object has no readable UID
)

// entriesOf builds the partition's new entries: the evaluated roles, plus
// every member the evaluator did not evaluate. A key in the old ledger that
// is neither evaluated nor a member is gone. When membership is unknown no
// entries are built: the caller suspends the partition instead of claiming
// either a deletion or continuity.
func (o *Observer) entriesOf(src string, id ContinuityIdentity, c Capture, roles []RoleEntry, idx []int, all, truncated bool) (map[string]ledgerEntry, entriesStatus) {
	n := len(idx)
	if all {
		n = len(roles)
	}
	if truncated || n > o.cfg.RoleCap {
		return nil, entriesCapped
	}
	entries := make(map[string]ledgerEntry, n)
	if all {
		for i := range roles {
			entries[roles[i].Key] = ledgerEntry{RoleEntry: &roles[i]}
		}
	} else {
		for _, i := range idx {
			entries[roles[i].Key] = ledgerEntry{RoleEntry: &roles[i]}
		}
	}
	if _, isKind := o.kinds[src]; !isKind {
		return entries, entriesOK // a complete read: a missing key is gone
	}
	objs := c.Objects[src]
	members, known := membershipOf(objs)
	if !known {
		return nil, entriesMembershipUnknown
	}
	for uid, i := range members {
		if _, ok := entries[uid]; ok {
			continue
		}
		a, _ := meta.Accessor(objs[i]) // known readable: membershipOf checked it
		entries[uid] = ledgerEntry{RoleEntry: &RoleEntry{Source: src, GVR: id.Sources[0].GVR, Key: uid, NS: a.GetNamespace(), Name: a.GetName()}, unevaluated: true}
	}
	if len(entries) > o.cfg.RoleCap {
		return nil, entriesCapped
	}
	return entries, entriesOK
}

func itemOf(e ledgerEntry, from, to string) Transition {
	t := Transition{Key: e.Key, NS: e.NS, Name: e.Name, GVR: e.GVR, From: from, To: to}
	if to != StateGone && to != StateUnevaluated {
		t.Label, t.Reason, t.Since, t.Boundary, t.Condition = e.Label, e.Reason, e.Since, e.Boundary, e.Condition
	}
	return t
}

// baselineItems lists every entry as first seen.
func baselineItems(entries map[string]ledgerEntry) []Transition {
	items := make([]Transition, 0, len(entries))
	for _, e := range entries {
		items = append(items, itemOf(e, StateAbsent, e.state()))
	}
	sortItems(items)
	return items
}

// diffEntries lists every key whose state differs, plus restatements: the
// same role with a different proven Since. derived marks a time-only change
// from the same sampled read.
func diffEntries(old, cur map[string]ledgerEntry, derived bool) []Transition {
	var items []Transition
	for k, n := range cur {
		o, ok := old[k]
		switch {
		case !ok:
			items = append(items, itemOf(n, StateAbsent, n.state()))
		case o.state() != n.state():
			t := itemOf(n, o.state(), n.state())
			t.Derived = derived
			items = append(items, t)
		case !n.unevaluated && !sameTime(o.Since, n.Since):
			t := itemOf(n, o.state(), n.state())
			t.Restated = true
			items = append(items, t)
		}
	}
	for k, o := range old {
		if _, ok := cur[k]; !ok {
			items = append(items, itemOf(o, o.state(), StateGone))
		}
	}
	sortItems(items)
	return items
}

func sortItems(items []Transition) {
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
}

func sameTime(a, b *time.Time) bool {
	switch {
	case a == nil || b == nil:
		return a == nil && b == nil
	default:
		return a.Equal(*b)
	}
}

// emit numbers a batch and hands it to the sink. Loop goroutine only.
func (o *Observer) emit(p *partState, b TransitionBatch) {
	o.tseq++
	b.Cluster, b.Incarnation, b.Seq = o.cfg.Cluster, o.incarnation, o.tseq
	if p != nil {
		b.Task, b.Part = p.key.task, p.key.part
	}
	o.tsink.Transitions(b)
}

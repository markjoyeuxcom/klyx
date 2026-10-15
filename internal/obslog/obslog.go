// Package obslog is the observation log (The Line slice 2b §6,
// docs/superpowers/specs/2026-10-14-klyx-the-line-slice2b-ledger.md): it
// consumes the Observer's transition stream, keeps a bounded per-cluster
// history, and holds a proof of how long each non-ok object has held its
// role - from the rule when the rule proves it, from Klyx's own continuous
// observation when it saw the change, and "start unknown" otherwise. A proof
// is never derived from when observation began. Memory only.
package obslog

import (
	"sort"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

// Bounds (§7).
const (
	MaxEntries = 20000
	MaxAge     = 24 * time.Hour
	MaxProofs  = 20000
)

// Proof sources, in precedence order (§6.2).
const (
	ProofRule     = "rule"     // a proven rule Since
	ProofObserved = "observed" // a change seen under continuous watch coverage: began in (NotBefore, Since]
	ProofSampled  = "sampled"  // a change between two reads: began in (NotBefore, Since]
	ProofDerived  = "derived"  // a time-only change from the same read: start unknown
	ProofUnknown  = "unknown"  // first seen at a baseline, an after-gap or a replacement
)

// Proof is how long one non-ok object has held its role.
type Proof struct {
	Task, Part, Source string
	Key, NS, Name, GVR string
	Role, Label        string
	Reason             string
	Basis              string     // ProofRule ... ProofUnknown
	Since              *time.Time // rule: the start; observed/sampled: the latest it can have begun
	NotBefore          *time.Time // observed/sampled: it began after this
	FirstSeen          time.Time  // when this proof's role was first seen
	Boundary           *time.Time // derived: the threshold the role crossed
	ReadAt             *time.Time // derived/sampled: the read it came from
	Condition          *observe.Condition
	Suspended          bool // a loss holds: coverage is not live
	AfterGap           bool // downgraded at an after-gap: seen before and after a gap
}

// Entry is one logged event: an item, a loss, a baseline, an after-gap (one
// marker, even with no changed items) or an end.
type Entry struct {
	At          time.Time
	PrevAt      *time.Time
	PrevReadAt  *time.Time // sampled: the read the previous commit used
	Boundary    *time.Time // an item's time-only threshold (a derived change)
	Incarnation uint64
	Task, Part  string
	Source, GVR string
	Kind, Mode  string
	Key, NS     string
	Name        string
	From, To    string
	Label       string
	Reason      string
	ReadAt      *time.Time
	Restated    bool
	Derived     bool
	Loss        *observe.Loss
	Count       int // a baseline: objects; Faults: of them, non-ok
	Faults      int
}

// PartitionState is one source partition's continuity, for the queue's
// coverage rows.
type PartitionState struct {
	Task, Part, Source string
	Live               bool
	Since              time.Time     // live since (its last baseline or after-gap), or lost at
	Loss               *observe.Loss // when not live
	PartVer            uint64        // the part version of the batch that set this state
}

type partitionKey struct{ task, part, source string }

type cluster struct {
	incarnation uint64
	ended       bool
	// entries[head:] are the live entries, oldest first: an amortized deque,
	// so a push and an age or count trim are O(1) - never a copy of the
	// whole log per entry.
	entries []Entry
	head    int
	proofs  map[string]*Proof
	// proofsCapped are non-ok keys whose proof was not kept (the cap), each
	// with its partition so a replacement or drop clears them. Bounded by
	// maxProofs too.
	proofsCapped map[string]partitionKey
	// overflow are partitions with non-ok keys in neither proofs nor
	// proofsCapped (both at their cap): a bounded, conservative disclosure
	// that the partition's proof coverage is incomplete.
	overflow   map[partitionKey]bool
	partitions map[partitionKey]*PartitionState
}

// Log is the observation log for every cluster.
type Log struct {
	mu       sync.Mutex
	clusters map[string]*cluster
	now      func() time.Time // the expiry clock
	// Bounds, overridable in tests.
	maxEntries, maxProofs int
	maxAge                time.Duration
}

// New returns an empty log with the §7 bounds.
func New() *Log {
	return &Log{clusters: map[string]*cluster{}, now: time.Now, maxEntries: MaxEntries, maxProofs: MaxProofs, maxAge: MaxAge}
}

func newCluster(inc uint64) *cluster {
	return &cluster{incarnation: inc, proofs: map[string]*Proof{}, proofsCapped: map[string]partitionKey{},
		overflow: map[partitionKey]bool{}, partitions: map[partitionKey]*PartitionState{}}
}

// live is the retained entries, oldest first.
func (c *cluster) live() []Entry { return c.entries[c.head:] }

// dropPartitionProofs removes a partition's proofs and capped markers.
func (c *cluster) dropPartitionProofs(pk partitionKey) {
	for k, p := range c.proofs {
		if p.Task == pk.task && p.Part == pk.part && p.Source == pk.source {
			delete(c.proofs, k)
		}
	}
	for k, cp := range c.proofsCapped {
		if cp == pk {
			delete(c.proofsCapped, k)
		}
	}
	delete(c.overflow, pk)
}

// Apply consumes one batch, whole, under the log's lock. A batch from an
// older incarnation than the newest seen is ignored; a newer incarnation
// ends the older one's proofs and keeps its entries.
func (l *Log) Apply(b observe.TransitionBatch) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clusters[b.Cluster]
	switch {
	case c == nil:
		c = newCluster(b.Incarnation)
		l.clusters[b.Cluster] = c
	case b.Incarnation < c.incarnation:
		return
	case b.Incarnation > c.incarnation:
		old, head := c.entries, c.head
		c = newCluster(b.Incarnation)
		c.entries, c.head = old, head // history is kept; proofs and partitions start afresh
		l.clusters[b.Cluster] = c
	case c.ended:
		return
	}
	pk := partitionKey{b.Task, b.Part, b.Source}
	switch b.Kind {
	case observe.BatchEnd:
		l.add(c, entryOf(b))
		l.endProofs(c)
		// Coverage ends with the Observer: every partition is terminally not
		// live. Only the incarnation fence and the bounded history remain.
		for _, ps := range c.partitions {
			ps.Live, ps.Since, ps.Loss = false, b.At, &observe.Loss{Reason: LossEnded}
		}
		c.ended = true
	case observe.BatchLoss:
		l.add(c, entryOf(b))
		c.partitions[pk] = &PartitionState{Task: b.Task, Part: b.Part, Source: b.Source, Since: b.At, Loss: b.Loss, PartVer: b.PartVer}
		if b.Loss != nil && (b.Loss.Reason == observe.LossCapped || b.Loss.Reason == observe.LossAbsent) {
			c.dropPartitionProofs(pk)
		} else {
			for _, p := range c.proofs {
				if p.Task == b.Task && p.Part == b.Part && p.Source == b.Source {
					p.Suspended = true
				}
			}
		}
	case observe.BatchBaseline:
		e := entryOf(b)
		e.Count = len(b.Items)
		for _, it := range b.Items {
			if nonOK(it.To) {
				e.Faults++
			}
		}
		l.add(c, e)
		// A replacement checkpoint: the partition's proofs and capped
		// markers end first (§5.2).
		c.dropPartitionProofs(pk)
		for _, it := range b.Items {
			l.firstSight(c, b, it)
		}
		c.partitions[pk] = &PartitionState{Task: b.Task, Part: b.Part, Source: b.Source, Live: true, Since: b.At, PartVer: b.PartVer}
	case observe.BatchAfterGap:
		// Every observed, sampled or derived proof is downgraded: the start
		// is no longer proven. Rule proofs stay while their Since holds.
		for _, p := range c.proofs {
			if p.Task == b.Task && p.Part == b.Part && p.Source == b.Source {
				p.Suspended = false
				if p.Basis != ProofRule {
					p.Basis, p.Since, p.NotBefore, p.AfterGap = ProofUnknown, nil, nil, true
				}
			}
		}
		// One marker records the coverage's return and its gap bounds, even
		// when nothing changed; items follow it.
		e := entryOf(b)
		e.Count = len(b.Items)
		l.add(c, e)
		l.addItems(c, b)
		for _, it := range b.Items {
			l.apply(c, b, it, true)
		}
		c.partitions[pk] = &PartitionState{Task: b.Task, Part: b.Part, Source: b.Source, Live: true, Since: b.At, PartVer: b.PartVer}
	case observe.BatchChanges:
		l.addItems(c, b)
		for _, it := range b.Items {
			l.apply(c, b, it, false)
		}
	}
}

// LossEnded is the partition loss after the Observer's end batch.
const LossEnded = "ended"

// addItems logs a batch's items. Items beyond the entry bound would be
// trimmed at once, so only the last maxEntries are logged: a batch's work is
// bounded by the log, not by the batch.
func (l *Log) addItems(c *cluster, b observe.TransitionBatch) {
	items := b.Items
	if len(items) > l.maxEntries {
		items = items[len(items)-l.maxEntries:]
	}
	for _, it := range items {
		l.add(c, itemEntry(b, it))
	}
}

// nonOK: a role that keeps a proof.
func nonOK(state string) bool {
	switch state {
	case "ok", observe.StateGone, observe.StateUnevaluated, observe.StateAbsent, "":
		return false
	}
	return true
}

// firstSight records a key seen with no observed change: its start is the
// rule's when proven, unknown otherwise.
func (l *Log) firstSight(c *cluster, b observe.TransitionBatch, it observe.Transition) {
	if !nonOK(it.To) {
		return
	}
	p := proofOf(b, it)
	if it.Since != nil {
		p.Basis, p.Since = ProofRule, copyTime(it.Since)
	} else {
		p.Basis = ProofUnknown
	}
	l.keep(c, it.Key, p)
}

// apply updates one key's proof from a delta item.
func (l *Log) apply(c *cluster, b observe.TransitionBatch, it observe.Transition, afterGap bool) {
	old := c.proofs[it.Key]
	switch {
	case !nonOK(it.To):
		delete(c.proofs, it.Key) // ok, gone or unevaluated: the episode's proof closes
		delete(c.proofsCapped, it.Key)
	case it.Restated:
		// No role changed: a restatement only replaces the rule's start.
		// Without a stored proof (it was capped), it starts one that is
		// rule-backed or unknown - never an observed start.
		if old == nil {
			l.firstSight(c, b, it)
			return
		}
		if it.Since != nil {
			old.Basis, old.Since, old.NotBefore = ProofRule, copyTime(it.Since), nil
		} else {
			old.Basis, old.Since, old.NotBefore = ProofUnknown, nil, nil
		}
	case afterGap || (old != nil && old.Role == it.To):
		// Seen after a gap, or unchanged: never an observed start.
		if old == nil || old.Role != it.To {
			l.firstSight(c, b, it)
		}
	default:
		p := proofOf(b, it)
		switch {
		case it.Since != nil:
			p.Basis, p.Since = ProofRule, copyTime(it.Since)
		case it.Derived:
			p.Basis, p.Boundary, p.ReadAt = ProofDerived, copyTime(it.Boundary), copyTime(b.ReadAt)
		case b.Mode == observe.ModeSampled:
			p.Basis, p.NotBefore, p.Since, p.ReadAt = ProofSampled, copyTime(b.PrevReadAt), copyTime(b.ReadAt), copyTime(b.ReadAt)
		default:
			p.Basis, p.NotBefore, p.Since = ProofObserved, copyTime(b.PrevAt), copyTime(&b.At)
		}
		l.keep(c, it.Key, p)
	}
}

func proofOf(b observe.TransitionBatch, it observe.Transition) *Proof {
	return &Proof{Task: b.Task, Part: b.Part, Source: b.Source, Key: it.Key, NS: it.NS, Name: it.Name, GVR: it.GVR,
		Role: it.To, Label: it.Label, Reason: it.Reason, FirstSeen: b.At, Condition: it.Condition, Boundary: copyTime(it.Boundary)}
}

// keep stores a proof under the per-cluster cap; beyond it the key is
// marked (with its partition, bounded by the same cap) so its query reads
// "start unknown (proof capped)".
func (l *Log) keep(c *cluster, key string, p *Proof) {
	if _, ok := c.proofs[key]; !ok && len(c.proofs) >= l.maxProofs {
		pk := partitionKey{p.Task, p.Part, p.Source}
		if _, marked := c.proofsCapped[key]; marked || len(c.proofsCapped) < l.maxProofs {
			c.proofsCapped[key] = pk
		} else {
			c.overflow[pk] = true // markers exhausted: disclose the partition instead
		}
		return
	}
	delete(c.proofsCapped, key)
	c.proofs[key] = p
}

func (l *Log) endProofs(c *cluster) {
	c.proofs = map[string]*Proof{}
	c.proofsCapped = map[string]partitionKey{}
	c.overflow = map[partitionKey]bool{}
}

func entryOf(b observe.TransitionBatch) Entry {
	return Entry{At: b.At, PrevAt: copyTime(b.PrevAt), PrevReadAt: copyTime(b.PrevReadAt), Incarnation: b.Incarnation, Task: b.Task, Part: b.Part,
		Source: b.Source, GVR: b.GVR, Kind: b.Kind, Mode: b.Mode, ReadAt: copyTime(b.ReadAt), Loss: b.Loss}
}

func itemEntry(b observe.TransitionBatch, it observe.Transition) Entry {
	e := entryOf(b)
	e.Key, e.NS, e.Name, e.From, e.To, e.Label, e.Reason = it.Key, it.NS, it.Name, it.From, it.To, it.Label, it.Reason
	e.Restated, e.Derived, e.Boundary = it.Restated, it.Derived, copyTime(it.Boundary)
	if it.GVR != "" {
		e.GVR = it.GVR
	}
	return e
}

// add appends an entry and trims the oldest beyond the count bound and the
// age window. Amortized O(1).
func (l *Log) add(c *cluster, e Entry) {
	c.entries = append(c.entries, e)
	for len(c.entries)-c.head > l.maxEntries {
		c.entries[c.head] = Entry{}
		c.head++
	}
	l.expire(c)
}

// expire drops entries older than the age window against the log's clock,
// and compacts the deque once its dead prefix outgrows the live part. An
// ended cluster whose history has all expired keeps only its incarnation
// fence.
func (l *Log) expire(c *cluster) {
	cutoff := l.now().Add(-l.maxAge)
	for c.head < len(c.entries) && c.entries[c.head].At.Before(cutoff) {
		c.entries[c.head] = Entry{}
		c.head++
	}
	if c.head > 0 && c.head >= len(c.entries)-c.head {
		c.entries = append([]Entry(nil), c.entries[c.head:]...)
		c.head = 0
	}
	if c.ended && len(c.entries) == 0 {
		c.entries, c.partitions = nil, map[partitionKey]*PartitionState{}
	}
}

// ProofOf returns a key's current proof. capped reports that the key's
// proof may have been dropped by the cap - marked by key, or (once the
// markers are exhausted too) because some partition's proof coverage is
// incomplete - so a non-ok key's start reads "start unknown (proof capped)".
func (l *Log) ProofOf(clusterName, key string) (p Proof, ok, capped bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clusters[clusterName]
	if c == nil {
		return Proof{}, false, false
	}
	l.expire(c)
	if pr := c.proofs[key]; pr != nil {
		return *pr, true, false
	}
	_, capped = c.proofsCapped[key]
	return Proof{}, false, capped || len(c.overflow) > 0
}

// Recent returns up to limit entries, newest first.
func (l *Log) Recent(clusterName string, limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clusters[clusterName]
	if c == nil {
		return nil
	}
	l.expire(c)
	live := c.live()
	n := len(live)
	if limit > n || limit <= 0 {
		limit = n
	}
	out := make([]Entry, 0, limit)
	for i := n - 1; i >= n-limit; i-- {
		out = append(out, live[i])
	}
	return out
}

// Partitions returns a part's source partitions, by source.
func (l *Log) Partitions(clusterName, task, part string) []PartitionState {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clusters[clusterName]
	if c == nil {
		return nil
	}
	l.expire(c)
	var out []PartitionState
	for k, s := range c.partitions {
		if k.task == task && k.part == part {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// PartitionSnapshot returns every partition state of a cluster with the
// incarnation they belong to (The Line slice 3 note §2.3): a consumer seeds
// its continuity only when that incarnation is the one it attached to. ok is
// false when the log holds nothing for the cluster.
func (l *Log) PartitionSnapshot(clusterName string) (incarnation uint64, states []PartitionState, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clusters[clusterName]
	if c == nil {
		return 0, nil, false
	}
	l.expire(c)
	for _, s := range c.partitions {
		states = append(states, *s)
	}
	sort.Slice(states, func(i, j int) bool {
		a, b := states[i], states[j]
		if a.Task != b.Task {
			return a.Task < b.Task
		}
		if a.Part != b.Part {
			return a.Part < b.Part
		}
		return a.Source < b.Source
	})
	return c.incarnation, states, true
}

// OldestEntry is the trim bound: an age older than it reads "at least since
// <bound> (log trimmed)". ok is false for an empty log.
func (l *Log) OldestEntry(clusterName string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clusters[clusterName]
	if c == nil {
		return time.Time{}, false
	}
	l.expire(c)
	if len(c.live()) == 0 {
		return time.Time{}, false
	}
	return c.live()[0].At, true
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

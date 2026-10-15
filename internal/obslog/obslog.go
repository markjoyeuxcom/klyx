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

// Entry is one logged event: an item, a loss, a baseline or an end.
type Entry struct {
	At          time.Time
	PrevAt      *time.Time
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
}

type partitionKey struct{ task, part, source string }

type cluster struct {
	incarnation  uint64
	ended        bool
	entries      []Entry // oldest first
	proofs       map[string]*Proof
	proofsCapped map[string]bool // keys whose proof was not kept (the cap)
	partitions   map[partitionKey]*PartitionState
}

// Log is the observation log for every cluster.
type Log struct {
	mu       sync.Mutex
	clusters map[string]*cluster
	// Bounds, overridable in tests.
	maxEntries, maxProofs int
	maxAge                time.Duration
}

// New returns an empty log with the §7 bounds.
func New() *Log {
	return &Log{clusters: map[string]*cluster{}, maxEntries: MaxEntries, maxProofs: MaxProofs, maxAge: MaxAge}
}

func newCluster(inc uint64) *cluster {
	return &cluster{incarnation: inc, proofs: map[string]*Proof{}, proofsCapped: map[string]bool{},
		partitions: map[partitionKey]*PartitionState{}}
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
		l.endProofs(c)
		old := c.entries
		c = newCluster(b.Incarnation)
		c.entries = old
		l.clusters[b.Cluster] = c
	case c.ended:
		return
	}
	pk := partitionKey{b.Task, b.Part, b.Source}
	switch b.Kind {
	case observe.BatchEnd:
		l.add(c, entryOf(b))
		l.endProofs(c)
		c.ended = true
	case observe.BatchLoss:
		l.add(c, entryOf(b))
		c.partitions[pk] = &PartitionState{Task: b.Task, Part: b.Part, Source: b.Source, Since: b.At, Loss: b.Loss}
		drop := b.Loss != nil && (b.Loss.Reason == observe.LossCapped || b.Loss.Reason == observe.LossAbsent)
		for k, p := range c.proofs {
			if p.Task == b.Task && p.Part == b.Part && p.Source == b.Source {
				if drop {
					delete(c.proofs, k)
				} else {
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
		// A replacement checkpoint: the partition's proofs end first (§5.2).
		for k, p := range c.proofs {
			if p.Task == b.Task && p.Part == b.Part && p.Source == b.Source {
				delete(c.proofs, k)
			}
		}
		for _, it := range b.Items {
			l.firstSight(c, b, it)
		}
		c.partitions[pk] = &PartitionState{Task: b.Task, Part: b.Part, Source: b.Source, Live: true, Since: b.At}
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
		for _, it := range b.Items {
			l.add(c, itemEntry(b, it))
			l.apply(c, b, it, true)
		}
		c.partitions[pk] = &PartitionState{Task: b.Task, Part: b.Part, Source: b.Source, Live: true, Since: b.At}
	case observe.BatchChanges:
		for _, it := range b.Items {
			l.add(c, itemEntry(b, it))
			l.apply(c, b, it, false)
		}
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
	case it.Restated && old != nil:
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
// marked so its query reads "start unknown (proof capped)".
func (l *Log) keep(c *cluster, key string, p *Proof) {
	if _, ok := c.proofs[key]; !ok && len(c.proofs) >= l.maxProofs {
		c.proofsCapped[key] = true
		return
	}
	delete(c.proofsCapped, key)
	c.proofs[key] = p
}

func (l *Log) endProofs(c *cluster) {
	c.proofs = map[string]*Proof{}
	c.proofsCapped = map[string]bool{}
}

func entryOf(b observe.TransitionBatch) Entry {
	return Entry{At: b.At, PrevAt: copyTime(b.PrevAt), Incarnation: b.Incarnation, Task: b.Task, Part: b.Part,
		Source: b.Source, GVR: b.GVR, Kind: b.Kind, Mode: b.Mode, ReadAt: copyTime(b.ReadAt), Loss: b.Loss}
}

func itemEntry(b observe.TransitionBatch, it observe.Transition) Entry {
	e := entryOf(b)
	e.Key, e.NS, e.Name, e.From, e.To, e.Label, e.Reason = it.Key, it.NS, it.Name, it.From, it.To, it.Label, it.Reason
	e.Restated, e.Derived = it.Restated, it.Derived
	if it.GVR != "" {
		e.GVR = it.GVR
	}
	return e
}

// add appends an entry and trims the oldest beyond the count and age bounds.
func (l *Log) add(c *cluster, e Entry) {
	c.entries = append(c.entries, e)
	cut := 0
	for cut < len(c.entries) && (len(c.entries)-cut > l.maxEntries || e.At.Sub(c.entries[cut].At) > l.maxAge) {
		cut++
	}
	if cut > 0 {
		c.entries = append([]Entry(nil), c.entries[cut:]...)
	}
}

// ProofOf returns a key's current proof. capped reports a non-ok key whose
// proof was not kept (the cap): its start reads unknown.
func (l *Log) ProofOf(clusterName, key string) (p Proof, ok, capped bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clusters[clusterName]
	if c == nil {
		return Proof{}, false, false
	}
	if pr := c.proofs[key]; pr != nil {
		return *pr, true, false
	}
	return Proof{}, false, c.proofsCapped[key]
}

// Recent returns up to limit entries, newest first.
func (l *Log) Recent(clusterName string, limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clusters[clusterName]
	if c == nil {
		return nil
	}
	n := len(c.entries)
	if limit > n || limit <= 0 {
		limit = n
	}
	out := make([]Entry, 0, limit)
	for i := n - 1; i >= n-limit; i-- {
		out = append(out, c.entries[i])
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
	var out []PartitionState
	for k, s := range c.partitions {
		if k.task == task && k.part == part {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// OldestEntry is the trim bound: an age older than it reads "at least since
// <bound> (log trimmed)". ok is false for an empty log.
func (l *Log) OldestEntry(clusterName string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.clusters[clusterName]
	if c == nil || len(c.entries) == 0 {
		return time.Time{}, false
	}
	return c.entries[0].At, true
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

package appbridge

import (
	"sync"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// The Line's tap on the health sink (slice 3 note §2.1): it runs on each
// Observer's loop goroutine, so it only records the latest state into
// bounded per-cluster slots and wakes the service worker - never subscribing,
// bootstrapping, computing or blocking.

// lineParts are the Observer parts the line reads.
var lineParts = []partRef{
	{healthsrc.TaskFlux, "reconciliations"},
	{healthsrc.TaskFlux, "sources"},
	{healthsrc.TaskArgo, "applications"},
}

type partRef struct{ task, part string }

func (p partRef) key() string { return p.task + "/" + p.part }

func isLinePart(task, part string) bool {
	for _, p := range lineParts {
		if p.task == task && p.part == part {
			return true
		}
	}
	return false
}

// partitionRef is one source partition of a line part (slice 2b): a counted
// kind within it.
type partitionRef struct{ task, part, source string }

// Partition states (note §2.3).
const (
	partitionUnknown   = ""
	partitionLost      = "lost"
	partitionAbsent    = "absent"
	partitionAvailable = "available"
)

// tapPartition is a partition's latest state, fenced by incarnation, with
// the sticky history-gap bit.
type tapPartition struct {
	inc   uint64
	state string
	ver   uint64 // the PartVer of the batch that set the state
	gap   bool   // a loss arrived since the worker last drained
}

type tapRows struct {
	rows        observe.Rows
	consumed    bool
	overwritten bool // an unconsumed, different Part.Ver was replaced: a history gap
}

type tapCluster struct {
	summary    *observe.Summary
	rows       map[string]*tapRows
	partitions map[partitionRef]*tapPartition
}

type lineTap struct {
	mu       sync.Mutex
	clusters map[string]*tapCluster
	wake     chan struct{}
}

func newLineTap() *lineTap {
	return &lineTap{clusters: map[string]*tapCluster{}, wake: make(chan struct{}, 1)}
}

func (t *lineTap) cluster(name string) *tapCluster {
	c := t.clusters[name]
	if c == nil {
		c = &tapCluster{rows: map[string]*tapRows{}, partitions: map[partitionRef]*tapPartition{}}
		t.clusters[name] = c
	}
	return c
}

func (t *lineTap) signal() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *lineTap) OnSummary(s observe.Summary) {
	t.mu.Lock()
	c := t.cluster(s.Cluster)
	if h := c.summary; h == nil || s.Incarnation > h.Incarnation || (s.Incarnation == h.Incarnation && s.Seq > h.Seq) {
		cp := s
		c.summary = &cp
	}
	t.mu.Unlock()
	t.signal()
}

func (t *lineTap) OnRows(r observe.Rows) {
	if !isLinePart(r.Task, r.Part.ID) {
		return
	}
	t.mu.Lock()
	c := t.cluster(r.Cluster)
	k := partRef{r.Task, r.Part.ID}.key()
	held := c.rows[k]
	switch {
	case held != nil && r.Incarnation < held.rows.Incarnation:
		t.mu.Unlock()
		return
	case held != nil && r.Incarnation == held.rows.Incarnation && !held.consumed && held.rows.Part.Ver != r.Part.Ver:
		c.rows[k] = &tapRows{rows: r, overwritten: true}
	case held != nil && r.Incarnation == held.rows.Incarnation && !held.consumed:
		c.rows[k] = &tapRows{rows: r, overwritten: held.overwritten}
	default:
		c.rows[k] = &tapRows{rows: r}
	}
	t.mu.Unlock()
	t.signal()
}

func (t *lineTap) OnTransitions(b observe.TransitionBatch) {
	if b.Kind == observe.BatchEnd || !isLinePart(b.Task, b.Part) {
		return // Final arrives as the summary
	}
	t.mu.Lock()
	c := t.cluster(b.Cluster)
	pr := partitionRef{b.Task, b.Part, b.Source}
	p := c.partitions[pr]
	if p == nil || b.Incarnation > p.inc {
		p = &tapPartition{inc: b.Incarnation}
		c.partitions[pr] = p
	} else if b.Incarnation < p.inc {
		t.mu.Unlock()
		return
	}
	switch b.Kind {
	case observe.BatchLoss:
		p.gap = true
		p.state, p.ver = partitionLost, b.PartVer
		if b.Loss != nil && b.Loss.Reason == observe.LossAbsent {
			p.state = partitionAbsent
		}
	case observe.BatchBaseline, observe.BatchAfterGap:
		p.state, p.ver = partitionAvailable, b.PartVer
	}
	t.mu.Unlock()
	t.signal()
}

// drained is one worker drain of a cluster's slots.
type drained struct {
	summary    *observe.Summary
	rows       map[string]observe.Rows // only rows not yet consumed
	overwrite  map[string]bool
	partitions map[partitionRef]tapPartition // every partition's latest state; gap bits as of this drain
}

// drain hands the worker everything new, marks the rows consumed and clears
// the sticky bits it has now seen.
func (t *lineTap) drain() map[string]drained {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]drained, len(t.clusters))
	for name, c := range t.clusters {
		d := drained{rows: map[string]observe.Rows{}, overwrite: map[string]bool{}, partitions: map[partitionRef]tapPartition{}}
		if c.summary != nil {
			s := *c.summary
			d.summary = &s
		}
		for k, r := range c.rows {
			if !r.consumed {
				d.rows[k] = r.rows
				d.overwrite[k] = r.overwritten
				r.consumed, r.overwritten = true, false
			}
		}
		for pr, p := range c.partitions {
			d.partitions[pr] = *p
			p.gap = false
		}
		out[name] = d
	}
	return out
}

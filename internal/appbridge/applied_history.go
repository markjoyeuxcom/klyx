package appbridge

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/gitops/flux"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/line"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/obslog"
)

// The applied-revision history (The Line slice 8d, slice 8 note §5): each
// Flux unit's typed applied value, recorded as predicate batches in the
// observation log under task "units", part "applied", one key per unit UID.
// Revision proximity reads it: "applied a1b9e3c, change observed 20:28".
//
// An observed change needs two known values of the same UID. The writer
// keeps unknown values out of the log: unknown -> known is written as a
// restatement (the log starts it at first sight, basis unknown, never an
// observed change); known -> unknown ends the key's proof; a known value
// that changes to another is the only observed change.

const (
	unitsTask        = "units"
	unitsPartApplied = "applied"
	unitsMode        = "units"
)

func appliedKey(uid string) string { return "applied/" + uid }

type appliedCluster struct {
	inc    uint64
	prev   map[string]string // key -> "<kind>:<value>", known values only
	units  map[string]string // "<Kind>/<ns>/<name>" -> UID ("" when not single)
	live   bool
	seen   bool
	ended  bool
	lastAt time.Time
}

type appliedHistory struct {
	mu  sync.Mutex
	log *obslog.Log
	st  map[string]*appliedCluster
}

func newAppliedHistory(log *obslog.Log) *appliedHistory {
	return &appliedHistory{log: log, st: map[string]*appliedCluster{}}
}

// AppliedHistory registers the history on the health sink and returns it: a
// package-level accessor, so nothing is bound to the webview.
func AppliedHistory(sink *HealthSink) *appliedHistory {
	h := newAppliedHistory(sink.ObservationLog())
	sink.AddListener(h)
	return h
}

func (h *appliedHistory) OnSummary(observe.Summary) {}

func (h *appliedHistory) OnRows(r observe.Rows) {
	if r.Task != healthsrc.TaskFlux || r.Part.ID != "reconciliations" {
		return
	}
	rs, ok := r.Rows.(healthsrc.RowSet[flux.Resource])
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.st[r.Cluster]
	if c == nil || r.Incarnation > c.inc {
		c = &appliedCluster{inc: r.Incarnation}
		h.st[r.Cluster] = c
	} else if r.Incarnation < c.inc || c.ended {
		return
	}
	at := r.EvaluatedAt
	if at.Before(c.lastAt) {
		at = c.lastAt
	}
	b := observe.TransitionBatch{Cluster: r.Cluster, Incarnation: r.Incarnation, Task: unitsTask, Part: unitsPartApplied,
		Source: unitsPartApplied, Mode: unitsMode, At: at}
	if !ok || !unitsReadable(r.Part) {
		if c.live {
			loss := b
			loss.Kind, loss.Loss = observe.BatchLoss, &observe.Loss{Reason: observe.LossCoverage}
			prev := c.lastAt
			loss.PrevAt = &prev
			h.apply(loss)
			c.live = false
		}
		c.units = nil
		return
	}
	cur := map[string]string{}
	units := map[string]string{}
	for _, row := range rs.Rows {
		uid := row.Ref.UID
		if uid == "" {
			continue
		}
		id := string(row.Data.Kind) + "/" + row.Data.Namespace + "/" + row.Data.Name
		if _, dup := units[id]; dup {
			units[id] = "" // not a single row: nothing is attributed to it
		} else {
			units[id] = uid
		}
		if kind, value, ok := line.AppliedValue(row.Data); ok {
			cur[appliedKey(uid)] = kind + ":" + value
		}
	}
	c.units = units
	switch {
	case !c.seen:
		b.Kind, b.Items = observe.BatchBaseline, appliedItems(nil, cur)
	case !c.live:
		b.Kind, b.Items = observe.BatchAfterGap, appliedItems(c.prev, cur)
	default:
		b.Kind, b.Items = observe.BatchChanges, appliedItems(c.prev, cur)
		prev := c.lastAt
		b.PrevAt = &prev
		if len(b.Items) == 0 {
			return
		}
	}
	h.apply(b)
	c.prev, c.live, c.seen, c.lastAt = cur, true, true, at
}

// appliedItems diffs two known-value maps: a value that appears (unknown ->
// known, or first sight) is a restatement - never an observed start - a
// value that changes is a change, and one that goes ends the key's proof.
func appliedItems(prev, cur map[string]string) []observe.Transition {
	var items []observe.Transition
	for k, v := range cur {
		old, ok := prev[k]
		switch {
		case !ok:
			items = append(items, observe.Transition{Key: k, Name: k, From: observe.StateAbsent, To: v, Restated: true})
		case old != v:
			items = append(items, observe.Transition{Key: k, Name: k, From: old, To: v})
		}
	}
	for k, v := range prev {
		if _, ok := cur[k]; !ok {
			items = append(items, observe.Transition{Key: k, Name: k, From: v, To: observe.StateGone})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}

// OnTransitions: a loss of the Observer's reconciliations partition breaks
// every key's continuity; the Observer's end leaves only the fence.
func (h *appliedHistory) OnTransitions(b observe.TransitionBatch) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.st[b.Cluster]
	if c == nil || b.Incarnation != c.inc {
		return
	}
	if b.Kind == observe.BatchEnd {
		h.st[b.Cluster] = &appliedCluster{inc: c.inc, ended: true}
		return
	}
	if b.Task != healthsrc.TaskFlux || b.Part != "reconciliations" || b.Kind != observe.BatchLoss || !c.live {
		return
	}
	at := b.At
	if at.Before(c.lastAt) {
		at = c.lastAt
	}
	prev := c.lastAt
	h.apply(observe.TransitionBatch{Cluster: b.Cluster, Incarnation: b.Incarnation, Task: unitsTask, Part: unitsPartApplied,
		Source: unitsPartApplied, Mode: unitsMode, Kind: observe.BatchLoss, At: at, PrevAt: &prev,
		Loss: &observe.Loss{Reason: observe.LossCoverage}})
	c.live = false
	c.units = nil
}

// unitsReadable: every reconciliation kind is live; otherwise a missing row
// could be a gap, not a gone unit.
func unitsReadable(p observe.Part) bool {
	if len(p.Kinds) == 0 {
		return false
	}
	for _, k := range p.Kinds {
		if k.Coverage != observe.Live {
			return false
		}
	}
	return true
}

func (h *appliedHistory) apply(b observe.TransitionBatch) {
	if h.log != nil {
		h.log.Apply(b)
	}
}

// AppliedChange is a unit's current applied value and when its change was
// observed: Basis observed only for a known value that changed to another
// in Klyx's sight; unknown for a first sighting, an after-gap or a value
// that appeared from unknown. LiveSince is the partition's current live
// span start: proximity needs continuous coverage from the change on.
type AppliedChange struct {
	Kind, Value string
	ObservedAt  *time.Time
	Basis       string // observed | unknown
	LiveSince   time.Time
}

// UnitFor resolves a unit to its UID from the latest rows: none when the
// rows are not readable or the name is not a single row.
func (h *appliedHistory) UnitFor(cluster, kind, ns, name string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.st[cluster]
	if c == nil || c.units == nil {
		return "", false
	}
	uid := c.units[kind+"/"+ns+"/"+name]
	return uid, uid != ""
}

// Change reads a unit's applied value back from the log.
func (h *appliedHistory) Change(cluster, uid string) (AppliedChange, bool) {
	if h.log == nil {
		return AppliedChange{}, false
	}
	var live *obslog.PartitionState
	for _, ps := range h.log.Partitions(cluster, unitsTask, unitsPartApplied) {
		if ps.Source == unitsPartApplied {
			p := ps
			live = &p
		}
	}
	if live == nil || !live.Live {
		return AppliedChange{}, false
	}
	p, ok, _ := h.log.ProofOf(cluster, appliedKey(uid))
	if !ok || p.Suspended {
		return AppliedChange{}, false
	}
	kind, value, found := strings.Cut(p.Role, ":")
	if !found {
		return AppliedChange{}, false
	}
	out := AppliedChange{Kind: kind, Value: value, Basis: obslog.ProofUnknown, LiveSince: live.Since}
	if p.Basis == obslog.ProofObserved && p.Since != nil && !p.Since.Before(live.Since) {
		at := *p.Since
		out.ObservedAt, out.Basis = &at, obslog.ProofObserved
	}
	return out, true
}

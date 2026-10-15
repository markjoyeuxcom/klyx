package appbridge

import (
	"sort"
	"strconv"
	"time"

	"github.com/moomora/klyx/internal/line"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/obslog"
)

// The line's own predicate history (The Line slice 3 note §5; parent §8.2):
// a role proof is not a line predicate's age, so the line records its own
// predicates as batches into the observation log under Task "line", sharing
// its bounds, wording and incarnation fence.
//
//   - Partitions: each unit kind's failing predicates (Kustomizations,
//     HelmReleases, Argo applications), the station's held predicate and
//     its state. Each partition reads its own input parts and kinds; a gap
//     breaks only the partitions whose inputs need the gapped kind.
//   - Commits follow every accepted change, one row set at a time in
//     capture order, never the presentation's coalesced recompute.
//   - Evidence times are capture times: a partition's commit is dated by
//     the newest capture among its own input parts, never an unrelated
//     part's or the worker's clock, and only a real change commits, so a
//     quiet commit never advances a proof's lower bound. Evidence captured
//     before a partition's last change but arriving after it shows that
//     change came from superseded inputs: a break.
//   - A break - unreadable inputs, a gap, overwritten rows, a lost
//     connection, out-of-order evidence - is a loss; read again, the
//     partition is re-baselined at once (loss and after-gap in one commit),
//     so no proof outlives it.
//   - Held and state are tied to the build's reference (key and R): a
//     reference change ends their proofs ("reference changed") and they are
//     baselined afresh under the new reference.

const (
	lineTask      = "line"
	linePartUnits = "units"
	linePartHeld  = "held"
	linePartState = "state"
	lineMode      = "line"
	keyHeld       = "line/held"
	keyState      = "line/state"
	predFault     = "fault"
	predOK        = "ok"
	predHeld      = "held"
)

// Unit partitions: the unit kinds.
const (
	unitsKustomizations = "kustomizations"
	unitsHelmReleases   = "helmreleases"
	unitsApplications   = "applications"
)

var unitPartitions = []string{unitsKustomizations, unitsHelmReleases, unitsApplications}

func unitPartitionOf(u line.Unit) string {
	switch u.Origin {
	case "HelmRelease":
		return unitsHelmReleases
	case "Application":
		return unitsApplications
	}
	return unitsKustomizations
}

func unitPredKey(u line.Unit) string {
	return "line/unit/" + u.UID + "#" + strconv.Itoa(u.SourceIndex)
}

type histPartition struct {
	prev     map[string]string
	live     bool
	seen     bool
	lastAt   time.Time // the evidence time of the last real change
	lastPart string    // the input part whose row set made it ("" for a lifecycle commit)
}

type histStation struct {
	inc   uint64
	parts map[string]*histPartition // by part/source
	ref   string                    // the reference held and state are under
}

// histCommit is one partition commit's evidence.
type histCommit struct {
	readable bool       // every input kind is read
	broken   bool       // continuity broke since the last commit
	at       time.Time  // the newest capture among the partition's input parts
	trigger  *time.Time // the capture time of the row set that prompted it, when an input
	part     string     // that row set's part
}

type lineHistory struct {
	log *obslog.Log
	st  map[string]*histStation
}

func newLineHistory(log *obslog.Log) *lineHistory {
	return &lineHistory{log: log, st: map[string]*histStation{}}
}

func (h *lineHistory) station(cluster string, inc uint64) *histStation {
	hs := h.st[cluster]
	if hs == nil || hs.inc != inc {
		hs = &histStation{inc: inc, parts: map[string]*histPartition{}}
		h.st[cluster] = hs
	}
	return hs
}

// committed reports whether a station's history has its baseline.
func (h *lineHistory) committed(cluster string, inc uint64) bool {
	hs := h.st[cluster]
	return hs != nil && hs.inc == inc && len(hs.parts) > 0
}

// commit records one partition's predicates.
func (h *lineHistory) commit(cluster string, inc uint64, part, source string, preds map[string]string, c histCommit) {
	hs := h.station(cluster, inc)
	k := part + "/" + source
	hp := hs.parts[k]
	if hp == nil {
		hp = &histPartition{}
		hs.parts[k] = hp
	}
	h.step(cluster, inc, part, source, hp, preds, c)
}

// reference records the reference a station's held and state are under. A
// change ends their proofs: both are tied to the key and R.
func (h *lineHistory) reference(cluster string, inc uint64, ref string, at time.Time) {
	hs := h.station(cluster, inc)
	if ref == hs.ref {
		return
	}
	for _, part := range []string{linePartHeld, linePartState} {
		k := part + "/" + lineTask
		if hp := hs.parts[k]; hp != nil && hp.live {
			when := at
			if when.Before(hp.lastAt) {
				when = hp.lastAt
			}
			h.apply(observe.TransitionBatch{Cluster: cluster, Incarnation: inc, Task: lineTask, Part: part, Source: lineTask,
				Mode: lineMode, Kind: observe.BatchLoss, At: when, Loss: &observe.Loss{Reason: "reference changed"}})
		}
		delete(hs.parts, k) // baselined afresh under the new reference
	}
	hs.ref = ref
}

// step commits one partition.
func (h *lineHistory) step(cluster string, inc uint64, part, source string, hp *histPartition, cur map[string]string, c histCommit) {
	at := c.at
	if at.Before(hp.lastAt) {
		at = hp.lastAt // evidence times only move forward
	}
	// Evidence captured before the last change, arriving after it, shows
	// that change was computed from superseded inputs; another part captured
	// at the same instant has no order against it. Either way continuity is
	// unproven.
	broken := c.broken || (c.trigger != nil && (c.trigger.Before(hp.lastAt) || (c.trigger.Equal(hp.lastAt) && c.part != hp.lastPart)))
	b := observe.TransitionBatch{Cluster: cluster, Incarnation: inc, Task: lineTask, Part: part, Source: source, Mode: lineMode, At: at}
	if hp.live && (!c.readable || broken) {
		loss := b
		loss.Kind, loss.Loss = observe.BatchLoss, &observe.Loss{Reason: observe.LossCoverage}
		prev := hp.lastAt
		loss.PrevAt = &prev
		h.apply(loss)
		hp.live = false
	}
	if !c.readable {
		return
	}
	switch {
	case !hp.seen:
		b.Kind = observe.BatchBaseline
		b.Items = diffPreds(nil, cur)
	case !hp.live:
		b.Kind = observe.BatchAfterGap
		b.Items = diffPreds(hp.prev, cur)
	default:
		b.Kind = observe.BatchChanges
		b.Items = diffPreds(hp.prev, cur)
		prev := hp.lastAt
		b.PrevAt = &prev
	}
	if b.Kind == observe.BatchChanges && len(b.Items) == 0 {
		hp.prev = cur // nothing changed: the evidence watermark stays
		return
	}
	h.apply(b)
	hp.prev, hp.live, hp.seen, hp.lastAt, hp.lastPart = cur, true, true, at, ""
	if c.trigger != nil {
		hp.lastPart = c.part
	}
}

func (h *lineHistory) apply(b observe.TransitionBatch) {
	if h.log != nil {
		h.log.Apply(b)
	}
}

func diffPreds(prev, cur map[string]string) []observe.Transition {
	var items []observe.Transition
	for k, v := range cur {
		old, ok := prev[k]
		switch {
		case !ok:
			items = append(items, observe.Transition{Key: k, Name: k, From: observe.StateAbsent, To: v})
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

// stationProofs are a station's ages, read back from the line's own history.
type stationProofs struct {
	failing *LineProofDTO
	held    *LineProofDTO
}

// proofsOf reads a station's failing and held proofs for the computed line.
func (h *lineHistory) proofsOf(l line.Line, sl line.StationLine) *stationProofs {
	p := &stationProofs{}
	for _, u := range sl.Units {
		if u.Failing && l.Key != nil && u.Key == *l.Key {
			p.failing = earlierProof(p.failing, h.proof(sl.Cluster, unitPredKey(u)))
		}
	}
	if sl.Held {
		p.held = h.proof(sl.Cluster, keyHeld)
	}
	return p
}

// proof reads one predicate's proof back from the log.
func (h *lineHistory) proof(cluster, key string) *LineProofDTO {
	if h.log == nil {
		return nil
	}
	p, ok, capped := h.log.ProofOf(cluster, key)
	if !ok {
		if capped {
			return &LineProofDTO{Basis: obslog.ProofUnknown, Capped: true}
		}
		return nil
	}
	d := &LineProofDTO{Basis: p.Basis, AfterGap: p.AfterGap}
	if p.Suspended {
		// A loss holds: the start is no longer proven to continue.
		fs := p.FirstSeen
		return &LineProofDTO{Basis: obslog.ProofUnknown, FirstSeen: &fs, AfterGap: true}
	}
	switch p.Basis {
	case obslog.ProofRule, obslog.ProofObserved, obslog.ProofSampled:
		d.Since, d.NotBefore = copyTimePtr(p.Since), copyTimePtr(p.NotBefore)
	default:
		fs := p.FirstSeen
		d.FirstSeen = &fs
	}
	return d
}

// earlierProof keeps the proof with the earlier start: a dated start before
// a first sighting, the earliest of each.
func earlierProof(a, b *LineProofDTO) *LineProofDTO {
	at := func(p *LineProofDTO) *time.Time {
		if p.Since != nil {
			return p.Since
		}
		return p.FirstSeen
	}
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case at(a) == nil:
		return b
	case at(b) == nil:
		return a
	case at(b).Before(*at(a)):
		return b
	}
	return a
}

func copyTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

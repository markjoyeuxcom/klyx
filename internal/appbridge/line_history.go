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
//   - Each unit's failing predicate is committed on every accepted change -
//     each drain or bootstrap result that touches the station - never only
//     when the presentation recomputes, so coalescing cannot hide a flip.
//     Units are partitioned by kind (Kustomizations, HelmReleases, Argo
//     applications): a gap breaks only the partitions whose inputs need the
//     gapped kind.
//   - The held predicate (tied to the key and R) is committed when the line
//     recomputes, and ends on a reference change.
//
// Evidence times are the rows' capture times, never the worker's clock, and
// only a real change commits: a quiet recompute never advances a proof's
// lower bound. A break - unreadable inputs, a gap, overwritten rows, a lost
// connection - is a loss; read again, it is re-baselined at once (loss and
// after-gap in one commit), so no proof outlives it.

const (
	lineTask      = "line"
	linePartUnits = "units"
	linePartHeld  = "held"
	lineMode      = "line"
	keyHeld       = "line/held"
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
	prev   map[string]string
	live   bool
	seen   bool
	lastAt time.Time // the capture time of the last commit
}

type histStation struct {
	inc   uint64
	units map[string]*histPartition
	held  histPartition
	ref   string
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
		hs = &histStation{inc: inc, units: map[string]*histPartition{}}
		h.st[cluster] = hs
	}
	return hs
}

// committed reports whether a station's unit history has its baseline.
func (h *lineHistory) committed(cluster string, inc uint64) bool {
	hs := h.st[cluster]
	return hs != nil && hs.inc == inc && len(hs.units) > 0
}

// commitUnits records one unit partition's failing predicates.
func (h *lineHistory) commitUnits(cluster string, inc uint64, part string, preds map[string]string, readable, broken bool, at time.Time) {
	hs := h.station(cluster, inc)
	hp := hs.units[part]
	if hp == nil {
		hp = &histPartition{}
		hs.units[part] = hp
	}
	h.step(cluster, inc, linePartUnits, part, hp, preds, readable, broken, at)
}

// commitHeld records the station's held predicate under the reference ref.
// A reference change ends every held proof: held is tied to R.
func (h *lineHistory) commitHeld(cluster string, inc uint64, held bool, ref string, readable, broken bool, at time.Time) {
	hs := h.station(cluster, inc)
	if hs.held.live && ref != hs.ref {
		h.apply(observe.TransitionBatch{Cluster: cluster, Incarnation: inc, Task: lineTask, Part: linePartHeld, Source: lineTask,
			Mode: lineMode, Kind: observe.BatchLoss, At: at, Loss: &observe.Loss{Reason: "reference changed"}})
		hs.held = histPartition{}
	}
	hs.ref = ref
	v := map[string]string{keyHeld: predOK}
	if held {
		v[keyHeld] = predHeld
	}
	h.step(cluster, inc, linePartHeld, lineTask, &hs.held, v, readable, broken, at)
}

// step commits one partition at the capture time at.
func (h *lineHistory) step(cluster string, inc uint64, part, source string, hp *histPartition, cur map[string]string, readable, broken bool, at time.Time) {
	if at.Before(hp.lastAt) {
		at = hp.lastAt // capture times only move forward
	}
	b := observe.TransitionBatch{Cluster: cluster, Incarnation: inc, Task: lineTask, Part: part, Source: source, Mode: lineMode, At: at}
	if hp.live && (!readable || broken) {
		loss := b
		loss.Kind, loss.Loss = observe.BatchLoss, &observe.Loss{Reason: observe.LossCoverage}
		prev := hp.lastAt
		loss.PrevAt = &prev
		h.apply(loss)
		hp.live = false
	}
	if !readable {
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
	hp.prev, hp.live, hp.seen, hp.lastAt = cur, true, true, at
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

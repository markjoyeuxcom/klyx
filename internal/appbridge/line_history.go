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
// predicates - each eligible unit's failing predicate and the station's state
// in one partition, the held predicate (tied to the key and R) in another -
// as batches into the observation log under Task "line". It shares the log's
// bounds, wording and incarnation fence. Line coverage is continuous only
// while the station reads ok with no gap, no overwritten rows and no
// connection loss between commits; any break is a loss, after which every
// predicate's start is unknown.

const (
	lineTask      = "line"
	linePartUnits = "units"
	linePartHeld  = "held"
	lineMode      = "line"
	keyState      = "line/state"
	keyHeld       = "line/held"
	predFault     = "fault"
	predOK        = "ok"
	predHeld      = "held"
)

func unitPredKey(u line.Unit) string {
	return "line/unit/" + u.UID + "#" + strconv.Itoa(u.SourceIndex)
}

type histPartition struct {
	prev   map[string]string
	live   bool
	seen   bool
	lastAt time.Time
}

type histStation struct {
	inc   uint64
	units histPartition
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

// stationProofs are a station's ages, read back from the line's own
// history in the same worker step that wrote it.
type stationProofs struct {
	failing *LineProofDTO
	held    *LineProofDTO
}

// commit records every station's predicates and returns their proofs.
func (h *lineHistory) commit(l line.Line, stations map[string]*lineStation, now time.Time) map[string]*stationProofs {
	out := map[string]*stationProofs{}
	ref := ""
	if l.Key != nil {
		ref = l.Key.String() + "@" + l.R
	}
	for _, sls := range [][]line.StationLine{l.Stations, l.Siding} {
		for _, sl := range sls {
			st := stations[sl.Cluster]
			if st == nil || st.inc == 0 {
				continue
			}
			hs := h.st[sl.Cluster]
			if hs == nil || hs.inc != st.inc {
				hs = &histStation{inc: st.inc}
				h.st[sl.Cluster] = hs
			}
			readable := sl.Read == line.ReadOK && st.usable && !st.ended
			broken := st.histGap
			st.histGap = false // this commit has seen it

			units := map[string]string{keyState: sl.State}
			for _, u := range sl.Units {
				if l.Key == nil || u.Key != *l.Key {
					continue
				}
				v := predOK
				if u.Failing {
					v = predFault
				}
				units[unitPredKey(u)] = v
			}
			h.step(sl.Cluster, hs.inc, linePartUnits, &hs.units, units, readable, broken, now)

			// A reference change ends every held proof: held is tied to R.
			if hs.held.live && ref != hs.ref {
				h.apply(observe.TransitionBatch{Cluster: sl.Cluster, Incarnation: hs.inc, Task: lineTask, Part: linePartHeld, Source: lineTask,
					Mode: lineMode, Kind: observe.BatchLoss, At: now, Loss: &observe.Loss{Reason: "reference changed"}})
				hs.held = histPartition{}
			}
			hs.ref = ref
			held := map[string]string{keyHeld: predOK}
			if sl.Held {
				held[keyHeld] = predHeld
			}
			h.step(sl.Cluster, hs.inc, linePartHeld, &hs.held, held, readable, broken, now)

			p := &stationProofs{}
			for _, u := range sl.Units {
				if u.Failing && l.Key != nil && u.Key == *l.Key {
					p.failing = earlierProof(p.failing, h.proof(sl.Cluster, unitPredKey(u)))
				}
			}
			if sl.Held {
				p.held = h.proof(sl.Cluster, keyHeld)
			}
			out[sl.Cluster] = p
		}
	}
	return out
}

// step commits one partition. Unreadable: a loss, then frozen. Readable
// after a break (a gap, overwritten rows, a lost connection since the last
// commit): the loss and the after-gap in one commit - like the Observer's gap
// between two live captures - so no proof outlives the break. Otherwise a
// baseline (first sight), an after-gap (after a loss) or changes.
func (h *lineHistory) step(cluster string, inc uint64, part string, hp *histPartition, cur map[string]string, readable, broken bool, now time.Time) {
	b := observe.TransitionBatch{Cluster: cluster, Incarnation: inc, Task: lineTask, Part: part, Source: lineTask, Mode: lineMode, At: now}
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
	if b.Kind != observe.BatchChanges || len(b.Items) > 0 {
		h.apply(b)
	}
	hp.prev, hp.live, hp.seen, hp.lastAt = cur, true, true, now
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

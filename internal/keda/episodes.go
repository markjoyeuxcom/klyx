package keda

import "time"

// EpisodeKind is one of §3.1's timed states.
type EpisodeKind uint8

const (
	EpPausing     EpisodeKind = 1 << iota // a full pause requested, not completed
	EpResuming                            // Paused=True with no pause annotation
	EpAwaitingHPA                         // not fully paused, Ready=True, no controlled HPA in a complete read
)

// Grace is each episode's allowance before it reads past due (§3.1).
const Grace = 2 * time.Minute

// Episode is one state's current stretch: Start, and whether the tracker
// saw it begin (Observed) or Start is only a lower bound - the first
// observation on an initial list, after a gap, or of an object the
// snapshots cannot prove stayed in the state (§3.1). Never the object's
// creation time.
type Episode struct {
	Start    time.Time `json:"start"`
	Observed bool      `json:"observed,omitempty"`
}

// Deadline is when the episode's grace runs out.
func (e Episode) Deadline() time.Time { return e.Start.Add(Grace) }

// Continuity identifies one unbroken observation of every kind an
// episode's state reads (the scalers' kind, and the HPAs' for the
// ScaledObjects): any relist, recorded error or new generation of either
// changes it, and every open episode restarts as a lower bound.
type Continuity struct {
	Generation, Epoch       uint64
	HPAGeneration, HPAEpoch uint64
}

// Obs is one scaler in a snapshot: the episode states it is in, and its
// resourceVersion, which changes on every write of it.
type Obs struct {
	States EpisodeKind
	RV     string
}

type trackedObs struct {
	obs Obs
	eps map[EpisodeKind]Episode
}

// Tracker keeps each scaler's episodes, keyed by UID (§3.1), from
// level-triggered, newest-wins snapshots. As S4's tracker: a state that
// held in the previous snapshot carries on only when the snapshot proves
// it stayed - the scaler not written since (its resourceVersion
// unchanged), or the snapshots contiguous (one change at most across the
// kinds read) - otherwise it restarts as a lower bound. A state first seen
// after it did not hold has an observed start. A new UID is a new scaler.
type Tracker struct {
	cont  Continuity
	began bool
	last  map[string]trackedObs
}

// Observe applies one complete, live snapshot taken at now under c and
// returns every scaler's open episodes. contiguous says the kinds changed
// once at most, in total, since the previous snapshot.
func (t *Tracker) Observe(c Continuity, now time.Time, obs map[string]Obs, contiguous bool) map[string]map[EpisodeKind]Episode {
	gap := !t.began || c != t.cont
	next := make(map[string]trackedObs, len(obs))
	out := map[string]map[EpisodeKind]Episode{}
	for uid, o := range obs {
		prev, had := t.last[uid]
		tr := trackedObs{obs: o}
		for _, k := range []EpisodeKind{EpPausing, EpResuming, EpAwaitingHPA} {
			if o.States&k == 0 {
				continue
			}
			var ep Episode
			pe, held := prev.eps[k]
			switch {
			case gap || !had:
				ep = Episode{Start: now} // a lower bound: the start was not observed
			case held && (prev.obs.RV == o.RV || contiguous):
				ep = pe // provably the same stretch
			case held:
				ep = Episode{Start: now} // written between snapshots: it may have left the state meanwhile
			default:
				ep = Episode{Start: now, Observed: true} // seen entering the state
			}
			if tr.eps == nil {
				tr.eps = map[EpisodeKind]Episode{}
			}
			tr.eps[k] = ep
		}
		next[uid] = tr
		if tr.eps != nil {
			out[uid] = tr.eps
		}
	}
	t.cont, t.began, t.last = c, true, next
	return out
}

// Break records a continuity loss: the next Observe restarts every
// episode as a lower bound.
func (t *Tracker) Break() { t.began = false }

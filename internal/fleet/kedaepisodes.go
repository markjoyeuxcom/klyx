package fleet

import (
	"context"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/hpa"
	"github.com/moomora/klyx/internal/keda"
	"github.com/moomora/klyx/internal/observe"
)

// kedaObserver is what the KEDA episode tracker needs of the Observer.
type kedaObserver interface {
	WatchKind(kind string, f func(observe.KindState)) (cancel func())
	VisitKind(kind string, visit func(obj any) bool) (st observe.KindState, complete, ok bool)
	SetFact(name string, v any)
}

// kedaVersion is the version every running operator agrees on (S9 §1.8).
func (c *ClusterConn) kedaVersion() keda.Version {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps.KEDA.Version()
}

// startKEDAEpisodes keeps FactKEDAEpisodes (coverage S9 §3.1): each
// scaler's pausing, resuming and awaiting-HPA episodes, from the scaler
// kinds' and the HPA kind's states. A scaler kind that is not live and
// complete breaks its tracker's continuity, and the next observation
// restarts every episode as a lower bound; a relist or error of the HPA
// kind changes the ScaledObjects' continuity the same way. The episodes are
// as S4's: carried on only when a scaler was not written between two
// snapshots, or the snapshots are contiguous (one change at most across the
// kinds read).
func startKEDAEpisodes(ctx context.Context, h kedaObserver, now func() time.Time, version func() keda.Version) {
	var mu sync.Mutex
	var soTr, sjTr keda.Tracker
	var lastSO, lastSJ, lastHPA uint64
	visited := false
	update := func(observe.KindState) {
		if ctx.Err() != nil {
			return
		}
		v := version()
		controlled := map[string]bool{}
		hst, hComplete, hOK := h.VisitKind(healthsrc.KindHPAs, func(obj any) bool {
			if x, ok := obj.(*hpa.HPA); ok {
				if so, ok := x.KEDAOwner(); ok {
					controlled[so.UID] = true
				}
			}
			return true
		})
		hpaProven := hOK && hComplete && hst.Coverage == observe.Live && !hst.Truncated
		soObs := map[string]keda.Obs{}
		sost, soComplete, soOK := h.VisitKind(healthsrc.KindScaledObjects, func(obj any) bool {
			if so, ok := obj.(*keda.ScaledObject); ok {
				absent := hpaProven && !controlled[so.UID]
				soObs[so.UID] = keda.Obs{States: keda.States(so, v, absent), RV: so.RV}
			}
			return true
		})
		sjObs := map[string]keda.Obs{}
		sjst, sjComplete, sjOK := h.VisitKind(healthsrc.KindScaledJobs, func(obj any) bool {
			if j, ok := obj.(*keda.ScaledJob); ok {
				sjObs[j.UID] = keda.Obs{States: keda.JobStates(j, v), RV: j.RV}
			}
			return true
		})
		mu.Lock()
		defer mu.Unlock()
		out := map[string]map[keda.EpisodeKind]keda.Episode{}
		// deliveries are newest-wins: contiguous only when the kinds read
		// changed once at most, in total, since the last visit
		if soOK && soComplete && sost.Coverage == observe.Live && !sost.Truncated {
			moved := (sost.Seq - lastSO) + (hst.Seq - lastHPA)
			cont := keda.Continuity{Generation: sost.Generation, Epoch: sost.Epoch, HPAGeneration: hst.Generation, HPAEpoch: hst.Epoch}
			for uid, eps := range soTr.Observe(cont, now(), soObs, visited && moved <= 1) {
				out[uid] = eps
			}
		} else {
			soTr.Break()
		}
		if sjOK && sjComplete && sjst.Coverage == observe.Live && !sjst.Truncated {
			cont := keda.Continuity{Generation: sjst.Generation, Epoch: sjst.Epoch}
			for uid, eps := range sjTr.Observe(cont, now(), sjObs, visited && sjst.Seq-lastSJ <= 1) {
				out[uid] = eps
			}
		} else {
			sjTr.Break()
		}
		lastSO, lastSJ, lastHPA, visited = sost.Seq, sjst.Seq, hst.Seq, true
		h.SetFact(healthsrc.FactKEDAEpisodes, out)
	}
	h.WatchKind(healthsrc.KindScaledObjects, update)
	h.WatchKind(healthsrc.KindScaledJobs, update)
	h.WatchKind(healthsrc.KindHPAs, update)
}

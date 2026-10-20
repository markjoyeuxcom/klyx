package fleet

import (
	"context"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

// serverMinor is the API server's minor version from the GitVersion read at
// connect (0 when unknown): the pool rules allow for it and one older.
func (c *ClusterConn) serverMinor() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return healthsrc.MinorOf(c.snapVersion)
}

// startDRAEpisodes keeps FactDRAEpisodes (coverage S11b §3.2): each
// incomplete pool's first observation, keyed by pool and generation, on
// the KEDA and LoadBalancer trackers' continuity rule. A read that is not
// complete and live, or a new generation or epoch of the slices kind (a
// relist: what happened between is not known), restarts every episode, so
// a start is a lower bound. Otherwise an episode continues when no change
// of the kind was skipped (deliveries are newest-wins), or when its pool's
// slices are provably the same (every UID and resourceVersion), so a busy
// kind does not keep restarting a stuck pool.
func startDRAEpisodes(ctx context.Context, h lbObserver, now func() time.Time, minor func() int) {
	type episode struct {
		start time.Time
		print string
	}
	var mu sync.Mutex
	eps := map[string]episode{}
	var lastSeq, lastGen, lastEpoch uint64
	visited := false
	h.WatchKind(healthsrc.KindResourceSlices, func(observe.KindState) {
		if ctx.Err() != nil {
			return
		}
		var objs []any
		st, complete, ok := h.VisitKind(healthsrc.KindResourceSlices, func(obj any) bool {
			objs = append(objs, obj)
			return true
		})
		mu.Lock()
		defer mu.Unlock()
		if !ok || !complete || st.Coverage != observe.Live || st.Truncated {
			eps, visited = map[string]episode{}, false
			return
		}
		gap := !visited || st.Generation != lastGen || st.Epoch != lastEpoch
		contiguous := !gap && st.Seq <= lastSeq+1
		lastSeq, lastGen, lastEpoch, visited = st.Seq, st.Generation, st.Epoch, true
		t := now()
		next := map[string]episode{}
		out := map[string]time.Time{}
		for key, print := range healthsrc.IncompletePools(healthsrc.SlicesOf(objs), minor()) {
			e := episode{start: t, print: print}
			if prev, had := eps[key]; had && !gap && (contiguous || prev.print == print) {
				e.start = prev.start // provably the same stretch
			}
			next[key], out[key] = e, e.start
		}
		eps = next
		h.SetFact(healthsrc.FactDRAEpisodes, out)
	})
}

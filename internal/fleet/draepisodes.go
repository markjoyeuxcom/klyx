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
// incomplete pool's first observation, keyed by pool and generation. An
// episode lasts only across contiguous complete, live reads of the slices
// (the KEDA tracker's continuity rule); a relist, a gap or a capped read
// restarts every episode, so a start is a lower bound.
func startDRAEpisodes(ctx context.Context, h lbObserver, now func() time.Time, minor func() int) {
	var mu sync.Mutex
	starts := map[string]time.Time{}
	var lastSeq uint64
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
			starts, visited = map[string]time.Time{}, false
			return
		}
		contiguous := visited && st.Seq <= lastSeq+1
		lastSeq, visited = st.Seq, true
		if !contiguous {
			starts = map[string]time.Time{}
		}
		t := now()
		next := map[string]time.Time{}
		for _, key := range healthsrc.IncompletePools(healthsrc.SlicesOf(objs), minor()) {
			if s, ok := starts[key]; ok {
				next[key] = s
			} else {
				next[key] = t
			}
		}
		starts = next
		out := make(map[string]time.Time, len(next))
		for k, v := range next {
			out[k] = v
		}
		h.SetFact(healthsrc.FactDRAEpisodes, out)
	})
}

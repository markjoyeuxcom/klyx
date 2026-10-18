package fleet

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/lb"
	"github.com/moomora/klyx/internal/observe"
)

// lbObserver is what the episode tracker needs of the Observer.
type lbObserver interface {
	WatchKind(kind string, f func(observe.KindState)) (cancel func())
	VisitKind(kind string, visit func(obj any) bool) (st observe.KindState, complete, ok bool)
	SetFact(name string, v any)
}

// startLBEpisodes keeps FactLBEpisodes (coverage S4 §3): each pending
// LoadBalancer Service's current episode, from the Services kind's states.
// A state that is not live and complete breaks continuity; the tracker
// then restarts every episode as a lower bound, and the fact keeps the
// last episodes while the kind's evidence is stale. seen reports, from
// each complete live read, whether any LoadBalancer Service exists.
func startLBEpisodes(ctx context.Context, h lbObserver, now func() time.Time, seen func(bool)) {
	var mu sync.Mutex
	var tr lb.Tracker
	var lastSeq uint64
	visited := false
	h.WatchKind(healthsrc.KindServices, func(observe.KindState) {
		if ctx.Err() != nil {
			return
		}
		states := map[string]lb.Obs{}
		st, complete, ok := h.VisitKind(healthsrc.KindServices, func(obj any) bool {
			if s, ok := obj.(*corev1.Service); ok {
				states[string(s.UID)] = lb.Obs{State: lb.StateOf(s), RV: s.ResourceVersion}
			}
			return true
		})
		mu.Lock()
		defer mu.Unlock()
		if !ok || !complete || st.Coverage != observe.Live || st.Truncated {
			tr.Break()
			visited = false
			return
		}
		// deliveries are newest-wins: contiguous only when the kind changed
		// once at most since the last visit (Codex's S4 review, finding 2)
		contiguous := visited && st.Seq <= lastSeq+1
		lastSeq, visited = st.Seq, true
		h.SetFact(healthsrc.FactLBEpisodes, tr.Observe(lb.Continuity{Generation: st.Generation, Epoch: st.Epoch}, now(), states, contiguous))
		lbSeen := false
		for _, s := range states {
			lbSeen = lbSeen || s.State != lb.StateOther
		}
		if seen != nil {
			seen(lbSeen)
		}
	})
}

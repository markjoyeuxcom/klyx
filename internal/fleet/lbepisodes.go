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
// last episodes while the kind's evidence is stale.
func startLBEpisodes(ctx context.Context, h lbObserver, now func() time.Time) {
	var mu sync.Mutex
	var tr lb.Tracker
	h.WatchKind(healthsrc.KindServices, func(observe.KindState) {
		if ctx.Err() != nil {
			return
		}
		states := map[string]lb.SvcState{}
		st, complete, ok := h.VisitKind(healthsrc.KindServices, func(obj any) bool {
			if s, ok := obj.(*corev1.Service); ok {
				states[string(s.UID)] = lb.StateOf(s)
			}
			return true
		})
		mu.Lock()
		defer mu.Unlock()
		if !ok || !complete || st.Coverage != observe.Live || st.Truncated {
			tr.Break()
			return
		}
		h.SetFact(healthsrc.FactLBEpisodes, tr.Observe(lb.Continuity{Generation: st.Generation, Epoch: st.Epoch}, now(), states))
	})
}

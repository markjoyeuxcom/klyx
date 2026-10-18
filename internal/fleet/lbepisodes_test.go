package fleet

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/lb"
	"github.com/moomora/klyx/internal/observe"
)

type fakeLBObserver struct {
	mu    sync.Mutex
	f     func(observe.KindState)
	st    observe.KindState
	objs  []any
	facts []map[string]lb.Episode
}

func (o *fakeLBObserver) WatchKind(_ string, f func(observe.KindState)) func() { o.f = f; return func() {} }
func (o *fakeLBObserver) VisitKind(_ string, visit func(any) bool) (observe.KindState, bool, bool) {
	for _, x := range o.objs {
		visit(x)
	}
	return o.st, true, true
}
func (o *fakeLBObserver) SetFact(_ string, v any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.facts = append(o.facts, v.(map[string]lb.Episode))
}

// Coverage S4 §3: the tracker driver publishes episodes from live states,
// breaks continuity when the kind leaves live, and restarts on a relist.
func TestS4LBEpisodesDriver(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	pending := &corev1.Service{ObjectMeta: metav1.ObjectMeta{UID: "u1"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	allocated := pending.DeepCopy()
	allocated.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}}
	o := &fakeLBObserver{st: observe.KindState{Kind: healthsrc.KindServices, Generation: 1, Epoch: 1, Coverage: observe.Live, CountKnown: true}, objs: []any{allocated}}
	startLBEpisodes(context.Background(), o, clock)
	o.f(o.st)
	now = now.Add(time.Minute)
	o.objs = []any{pending}
	o.f(o.st)
	if last := o.facts[len(o.facts)-1]; !last["u1"].Observed || !last["u1"].Start.Equal(now) {
		t.Fatalf("an observed loss of the address: %+v", last)
	}
	n := len(o.facts)
	o.st.Coverage = observe.Stale
	o.f(o.st)
	if len(o.facts) != n {
		t.Fatal("a stale kind publishes nothing: the last episodes stand")
	}
	o.st.Coverage, o.st.Epoch = observe.Live, 2
	now = now.Add(time.Minute)
	o.f(o.st)
	if last := o.facts[len(o.facts)-1]; last["u1"].Observed || !last["u1"].Start.Equal(now) {
		t.Fatalf("after the break, a lower bound: %+v", last)
	}
}

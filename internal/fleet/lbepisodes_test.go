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

func (o *fakeLBObserver) WatchKind(_ string, f func(observe.KindState)) func() {
	o.f = f
	return func() {}
}
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
	pending := &corev1.Service{ObjectMeta: metav1.ObjectMeta{UID: "u1", ResourceVersion: "2"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	allocated := pending.DeepCopy()
	allocated.ResourceVersion = "1"
	allocated.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}}
	o := &fakeLBObserver{st: observe.KindState{Kind: healthsrc.KindServices, Generation: 1, Epoch: 1, Seq: 1, Coverage: observe.Live, CountKnown: true}, objs: []any{allocated}}
	var seen bool
	startLBEpisodes(context.Background(), o, clock, func(b bool) { seen = b })
	o.f(o.st)
	now = now.Add(time.Minute)
	o.objs = []any{pending}
	o.st.Seq = 2
	o.f(o.st)
	if last := o.facts[len(o.facts)-1]; !last["u1"].Observed || !last["u1"].Start.Equal(now) {
		t.Fatalf("an observed loss of the address: %+v", last)
	}
	if !seen {
		t.Fatal("a LoadBalancer Service is seen")
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

// Codex's S4 implementation review, finding 2: deliveries are newest-wins.
// A Service pending since 09:00 is given an address and loses it (two
// writes) before the next delivery at 09:20: the old episode must not
// carry on, so it is not a fault at once.
func TestS4ReviewCoalescedAllocationEndsOldEpisode(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{UID: "u1", ResourceVersion: "10"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	o := &fakeLBObserver{st: observe.KindState{Kind: healthsrc.KindServices, Generation: 1, Epoch: 1, Seq: 1, Coverage: observe.Live, CountKnown: true}, objs: []any{svc}}
	startLBEpisodes(context.Background(), o, func() time.Time { return now }, nil)
	o.f(o.st)
	now = now.Add(20 * time.Minute)
	lost := svc.DeepCopy()
	lost.ResourceVersion = "12" // allocated at 11, lost at 12: seq 2 and 3, one delivery
	o.objs, o.st.Seq = []any{lost}, 3
	o.f(o.st)
	ep := o.facts[len(o.facts)-1]["u1"]
	if got := lb.ServiceRule(lost, lb.AllocationOf(lost), ep, now, ""); !ep.Start.Equal(now) || got.Label == "No address" {
		t.Fatalf("the new episode inherited the old one: start=%s eval=%+v", ep.Start, got)
	}
	// unwritten, the same Service stays one stretch whatever else changed
	o.st.Seq = 9
	now = now.Add(time.Minute)
	o.f(o.st)
	if ep2 := o.facts[len(o.facts)-1]["u1"]; !ep2.Start.Equal(ep.Start) {
		t.Fatalf("an unwritten Service keeps its episode: %+v", ep2)
	}
}

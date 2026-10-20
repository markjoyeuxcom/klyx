package fleet

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/observe"
)

type fakeDRAObserver struct {
	mu    sync.Mutex
	f     func(observe.KindState)
	st    observe.KindState
	objs  []any
	facts []map[string]time.Time
}

func (o *fakeDRAObserver) WatchKind(_ string, f func(observe.KindState)) func() {
	o.f = f
	return func() {}
}
func (o *fakeDRAObserver) VisitKind(_ string, visit func(any) bool) (observe.KindState, bool, bool) {
	for _, x := range o.objs {
		visit(x)
	}
	return o.st, true, true
}
func (o *fakeDRAObserver) SetFact(_ string, v any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.facts = append(o.facts, v.(map[string]time.Time))
}

func draEpisodeSlice(name, rv string, count int64) *dra.Sized[dra.Slice] {
	s := dra.Slice{Name: name, UID: "u-" + name, RV: rv, Driver: "d", Pool: "p", Generation: 1, Count: count, AllNodes: true, Devices: []dra.Device{{Name: name}}}
	return &dra.Sized[dra.Slice]{V: s}
}

// Coverage S11b §3.2: an incomplete pool's episode, on the KEDA and
// LoadBalancer trackers' continuity rule.
func TestDRAEpisodesDriver(t *testing.T) {
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	key := dra.EpisodeKey("d", "p", 1)
	o := &fakeDRAObserver{st: observe.KindState{Kind: healthsrc.KindResourceSlices, Generation: 1, Epoch: 1, Seq: 1, Coverage: observe.Live, CountKnown: true},
		objs: []any{draEpisodeSlice("a", "10", 2)}}
	startDRAEpisodes(context.Background(), o, clock, func() int { return 37 })
	last := func() map[string]time.Time { return o.facts[len(o.facts)-1] }
	start := now
	o.f(o.st)
	if !last()[key].Equal(start) {
		t.Fatalf("the first read starts the episode: %v", last())
	}
	step := func(what string, mut func(), want time.Time) {
		t.Helper()
		now = now.Add(time.Minute)
		mut()
		o.f(o.st)
		if got := last()[key]; !got.Equal(want) {
			t.Fatalf("%s: start %v, want %v", what, got, want)
		}
	}
	step("a contiguous read keeps it", func() { o.st.Seq++ }, start)
	step("skipped changes elsewhere, the pool unchanged, keep it", func() { o.st.Seq += 5 }, start)
	step("skipped changes and the pool's slice changed restart it", func() { o.st.Seq += 5; o.objs = []any{draEpisodeSlice("a", "11", 2)} }, now.Add(time.Minute))
	start = now
	step("a contiguous change of the pool keeps it", func() { o.st.Seq++; o.objs = []any{draEpisodeSlice("a", "12", 2)} }, start)
	step("a new epoch (a relist) restarts it, the pool unchanged", func() { o.st.Seq++; o.st.Epoch++ }, now.Add(time.Minute))
	start = now
	step("a new generation restarts it", func() { o.st.Seq++; o.st.Generation++ }, now.Add(time.Minute))
	n := len(o.facts)
	now = now.Add(time.Minute)
	o.st.Coverage = observe.Stale
	o.f(o.st)
	if len(o.facts) != n {
		t.Fatal("a stale read publishes nothing: the last episodes stand")
	}
	step("after a stale read, a lower bound", func() { o.st.Coverage = observe.Live; o.st.Seq++ }, now.Add(time.Minute))
	now = now.Add(time.Minute)
	o.st.Seq++
	o.objs = []any{draEpisodeSlice("a", "12", 2), draEpisodeSlice("b", "1", 2)}
	o.f(o.st)
	if _, ok := last()[key]; ok || len(last()) != 0 {
		t.Fatalf("a complete pool has no episode: %v", last())
	}
}

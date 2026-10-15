package fleetindex

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

func bigObs(n int) *fakeObs {
	obs := newFakeObs(1)
	objs := make([]any, n)
	for i := range objs {
		objs[i] = meta1(fmt.Sprintf("0b6c1a2e-5f3d-4c1e-9a7b-%012d", i), fmt.Sprintf("team-%d", i%40), fmt.Sprintf("web-%d-7d9f8c6b5-x2k4p", i),
			map[string]string{"app.kubernetes.io/name": "web", "pod-template-hash": "7d9f8c6b5", "team": fmt.Sprintf("t%d", i%40)})
	}
	obs.set("pods", "v1/pods", observe.Live, objs...)
	return obs
}

func visitOnce(b testing.TB, c *coord, gen uint64, obs *fakeObs) {
	c.visit(gen, obs, planned{ref: refPods, class: ClassPods, observer: "pods"})
}

// §2.4: the steady-state visit of a 50,000-object kind - lookups, no
// allocation per unchanged object - holds the Observer's lock briefly.
func BenchmarkVisitSteadyState(b *testing.B) {
	obs := bigObs(50_000)
	ix := New(Settings{Kinds: []KindRef{refPods}, Limits: Limits{ClusterEntries: 1e6, ClusterBytes: 1 << 40, FleetEntries: 1e6, FleetBytes: 1 << 40, PodPercent: 100}})
	c := ix.coordFor("a")
	c.store.begin(1)
	visitOnce(b, c, 1, obs)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		visitOnce(b, c, 1, obs)
	}
}

// The visit gates of §2.4, as a test: a steady-state visit of 50,000
// objects allocates nothing per object and stays well under 50 ms.
func TestVisitCost(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing")
	}
	obs := bigObs(50_000)
	ix := New(Settings{Kinds: []KindRef{refPods}, Limits: Limits{ClusterEntries: 1e6, ClusterBytes: 1 << 40, FleetEntries: 1e6, FleetBytes: 1 << 40, PodPercent: 100}})
	c := ix.coordFor("a")
	c.store.begin(1)
	start := time.Now()
	visitOnce(t, c, 1, obs)
	t.Logf("first fill: %v", time.Since(start))
	allocs := testing.AllocsPerRun(3, func() { visitOnce(t, c, 1, obs) })
	start = time.Now()
	visitOnce(t, c, 1, obs)
	d := time.Since(start)
	t.Logf("steady state: %v, %.0f allocations", d, allocs)
	if allocs > 100 {
		t.Fatalf("a steady-state visit allocates %v times", allocs)
	}
	if d > 50*time.Millisecond {
		t.Fatalf("a steady-state visit took %v", d)
	}
}

// §5: a 60,000-object Observer kind at the default caps: Pods stop at
// their ceiling, freeze, and the retained heap stays under the charge.
func TestObserverKindPeakMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates")
	}
	obs := bigObs(60_000)
	base := heap()
	ix := New(Settings{Kinds: []KindRef{refPods}})
	c := ix.coordFor("a")
	c.store.begin(1)
	visitOnce(t, c, 1, obs)
	after := heap()
	_, charged, _, _ := ix.budget.Used("a")
	st := c.store.status()["/pods"]
	growth := int64(after) - int64(base)
	t.Logf("pods %s at %d entries; retained %d KiB, charged %d KiB", st.State, st.Entries, growth>>10, charged>>10)
	if st.State != StateCapped || st.Entries != DefaultLimits.ClusterEntries*DefaultLimits.PodPercent/100 && charged < DefaultLimits.ClusterBytes*int64(DefaultLimits.PodPercent)/100-entryOverhead*4 {
		t.Fatalf("pods: %+v", st)
	}
	if growth > charged {
		t.Fatalf("retained %d > charged %d", growth, charged)
	}
	// repeated visits at the cap never rotate or refill
	before := fmt.Sprint(len(entryUIDsOf(c, "/pods")))
	visitOnce(t, c, 1, obs)
	if fmt.Sprint(len(entryUIDsOf(c, "/pods"))) != before {
		t.Fatal("a frozen kind changed on a later visit")
	}
	runtime.KeepAlive(obs)
}

func entryUIDsOf(c *coord, key string) map[string]*Entry {
	c.store.mu.RLock()
	defer c.store.mu.RUnlock()
	return c.store.kinds[key].entries
}

// §2.3 across clusters: one cluster's Pods stop at the fleet's Pod ceiling,
// leaving the other clusters' other kinds their share.
func TestFleetContention(t *testing.T) {
	ix := New(Settings{Kinds: []KindRef{refPods, refConfigMaps}, Limits: Limits{ClusterEntries: 1000, ClusterBytes: 1 << 30, FleetEntries: 1000, FleetBytes: 1 << 30, PodPercent: 80}})
	a, b := ix.coordFor("a"), ix.coordFor("b")
	a.store.begin(1)
	b.store.begin(2)
	visitOnce(t, a, 1, bigObs(2000))
	if st := a.store.status()["/pods"]; st.State != StateCapped || st.Entries != 800 {
		t.Fatalf("a's pods %+v", st)
	}
	if !read(t, b.store, 2, "/configmaps", ClassOther, sources(0, 150)) {
		t.Fatal("b's other kinds keep the fleet's headroom")
	}
	visitOnce(t, b, 2, bigObs(10))
	if st := b.store.status()["/pods"]; st.State != StateCapped || st.Entries != 0 {
		t.Fatalf("b's pods start past the fleet's Pod ceiling: %+v", st)
	}
}

// A label-only growth at a cap is refused, and the kind freezes.
func TestLabelGrowthAtCap(t *testing.T) {
	src := sources(0, 1)[0]
	b := NewBudget(Limits{ClusterEntries: 10, ClusterBytes: kindOverhead + estimate(src), FleetEntries: 10, FleetBytes: 1 << 30, PodPercent: 100})
	c := newClusterStore("a", b, func() time.Time { return t0 })
	c.begin(1)
	if !read(t, c, 1, "configmaps", ClassOther, []Source{src}) {
		t.Fatal("fits exactly")
	}
	grown := src
	grown.Labels = map[string]string{"app": "x", "tier": "a-much-longer-label-value"}
	if c.put(1, "configmaps", 0, grown) != putCapped || c.status()["configmaps"].State != StateCapped {
		t.Fatal("growth past the cap freezes the kind")
	}
	if e := c.kinds["configmaps"].entries[src.UID]; len(e.Labels) != 1 {
		t.Fatal("the frozen entry keeps its admitted labels")
	}
}

func TestParseKinds(t *testing.T) {
	got, warnings := ParseKinds([]string{"configmaps", "v1/secrets", "apps/deployments", "batch/v1/jobs", "core/v1/pods", "example.com/widgets", "apps/v1/deployments", "bad kind", "a/b/c/d", "Apps/Deployments"})
	var keys []string
	for _, k := range got {
		keys = append(keys, k.Key()+"@"+k.Version+":"+k.Kind)
	}
	want := "[/configmaps@v1:ConfigMap /secrets@v1:Secret apps/deployments@v1:Deployment batch/jobs@v1:Job /pods@v1:Pod example.com/widgets@:widgets]"
	if fmt.Sprint(keys) != want {
		t.Fatalf("kinds %v", keys)
	}
	if len(warnings) != 3 {
		t.Fatalf("warnings %v", warnings)
	}
	if got, _ := ParseKinds([]string{}); got == nil || len(got) != 0 {
		t.Fatal("an explicit empty list is no kinds, never the defaults")
	}
	s, _ := FromConfig(nil, false, 0, 64, 0, 0)
	if s.Kinds != nil || s.Limits.ClusterBytes != 64<<20 || s.Limits.ClusterEntries != DefaultLimits.ClusterEntries {
		t.Fatalf("from config %+v", s)
	}
	if ix := New(Settings{Kinds: []KindRef{}}); len(ix.s.Kinds) != 0 {
		t.Fatal("New keeps an empty list empty")
	}
}

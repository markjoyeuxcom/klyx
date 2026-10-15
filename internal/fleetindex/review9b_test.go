package fleetindex

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

// The design review of 647f21c (slice 9b): each finding, as a regression.

// roleGate is a fake Observer whose role read can be held, and can run a
// hook inside the call.
type roleGate struct {
	*fakeObs
	entered, release chan struct{}
	once             sync.Once
	onRead           func()
}

func (g *roleGate) RolesNow(ctx context.Context, qs []observe.RoleQuery) ([]observe.RoleView, bool) {
	if g.onRead != nil {
		g.onRead()
	}
	if g.entered != nil {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-ctx.Done():
			return nil, false
		case <-g.release:
		}
	}
	out := make([]observe.RoleView, len(qs))
	for i := range out {
		out[i] = observe.RoleView{State: observe.RoleCurrent, Role: "ok"}
	}
	return out, true
}

// gatedIndex: one cluster attached to obs, n pods indexed under generation
// 1 and incarnation 1.
func gatedIndex(t *testing.T, obs Observed, n int) *Index {
	t.Helper()
	ix := New(testSettings(refPods))
	ix.SetClusters([]string{"a"})
	c := ix.coordFor("a")
	c.obs, c.ran, c.usable, c.word, c.gen = obs, true, true, "connected", 1
	c.store.begin(1, 1)
	m, _ := c.store.beginRead(1, "/pods", ClassPods)
	c.store.describe(1, "/pods", ClassPods, kindDesc{ref: refPods, gvr: "v1/pods", observer: "pods"})
	for i := 0; i < n; i++ {
		if c.store.put(1, "/pods", m, Source{UID: fmt.Sprint(i), Name: fmt.Sprintf("web-%03d", i), Namespace: "ns"}) != putApplied {
			t.Fatal("put")
		}
	}
	c.store.endRead(1, "/pods", m, KindStatus{State: StateLive})
	return ix
}

// Finding 4: a cancelled search never waits behind another.
func TestCancelledSearchDoesNotWait(t *testing.T) {
	g := &roleGate{fakeObs: newFakeObs(1), entered: make(chan struct{}), release: make(chan struct{})}
	ix := gatedIndex(t, g, 1)
	defer ix.Stop()
	first := make(chan error, 1)
	go func() { _, err := ix.Search(context.Background(), "web", 200); first <- err }()
	<-g.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := ix.Search(ctx, "web", 200); err != context.Canceled || time.Since(start) > time.Second {
		t.Fatalf("a cancelled search: %v after %v", err, time.Since(start))
	}
	close(g.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

// Finding 2: a hit carries the generation and incarnation its entry was
// read under; an entry not yet re-read by a new attachment is never given
// the new Observer's role.
func TestHitsKeepTheIdentityTheyWereIndexedUnder(t *testing.T) {
	ix := gatedIndex(t, newFakeObs(1), 1)
	defer ix.Stop()
	c := ix.clusters["a"]
	c.mu.Lock()
	c.obs, c.gen = &roleGate{fakeObs: newFakeObs(2)}, 2
	c.mu.Unlock()
	c.store.begin(2, 2)
	res, err := ix.Search(context.Background(), "web", 200)
	if err != nil || len(res.Hits) != 1 {
		t.Fatal(err, res)
	}
	h := res.Hits[0]
	if h.Incarnation != 1 || h.Gen != 1 || h.Role.State != observe.RoleUnverified {
		t.Fatalf("an old entry retagged: %+v", h)
	}
	// re-read in the new generation, it carries the new identity
	m, _ := c.store.beginRead(2, "/pods", ClassPods)
	c.store.put(2, "/pods", m, Source{UID: "0", Name: "web-000", Namespace: "ns"})
	res, _ = ix.Search(context.Background(), "web", 200)
	if h := res.Hits[0]; h.Incarnation != 2 || h.Gen != 2 || h.Role.State != observe.RoleCurrent {
		t.Fatalf("a re-read entry: %+v", h)
	}
}

// Finding 3: an Observer replaced during the role read never lends its
// answer.
func TestRolesFenceAnObserverSwapDuringTheCall(t *testing.T) {
	g := &roleGate{fakeObs: newFakeObs(1)}
	ix := gatedIndex(t, g, 1)
	defer ix.Stop()
	c := ix.clusters["a"]
	g.onRead = func() {
		c.mu.Lock()
		c.obs = newFakeObs(2)
		c.mu.Unlock()
	}
	res, err := ix.Search(context.Background(), "web", 200)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.Hits[0].Role; r.State != observe.RoleUnverified || !strings.Contains(r.Reason, "changed") {
		t.Fatalf("an answer from a replaced Observer: %+v", r)
	}
}

// Finding 5: a search's working set - its top-K, hits, role queries and
// coverage - fits the reservation it admitted, held while it waits on
// roles; the reservation is returned on Release.
func TestSearchWorkingSetFitsItsReservation(t *testing.T) {
	g := &roleGate{fakeObs: newFakeObs(1), entered: make(chan struct{}), release: make(chan struct{})}
	ix := gatedIndex(t, g, 5000)
	defer ix.Stop()
	_, _, _, base := ix.budget.Used("a")
	reserve := searchReserve(MaxHits, ix.Coverage())
	done := make(chan Result, 1)
	before := heapNow()
	go func() {
		res, _ := ix.Search(context.Background(), "web", MaxHits)
		done <- res
	}()
	<-g.entered
	growth := int64(heapNow()) - int64(before)
	_, _, _, held := ix.budget.Used("a")
	t.Logf("working set %d KiB held, %d KiB reserved", growth>>10, reserve>>10)
	if growth > reserve {
		t.Fatalf("a search holds %d bytes, reserved %d", growth, reserve)
	}
	if held-base != reserve {
		t.Fatalf("the reservation is admitted: %d", held-base)
	}
	close(g.release)
	res := <-done
	if len(res.Hits) != MaxHits || res.Total != 5000 {
		t.Fatalf("%d hits of %d", len(res.Hits), res.Total)
	}
	res.Release()
	res.Release() // idempotent
	if _, _, _, after := ix.budget.Used("a"); after != base {
		t.Fatalf("released %d, held %d", base, after)
	}
}

// Finding 5: a query longer than any name searches nothing - before it is
// normalized, so its size never becomes scratch.
func TestOversizedQuerySearchesNothing(t *testing.T) {
	var calls atomic.Int32
	ix := gatedIndex(t, newFakeObs(1), 1)
	defer ix.Stop()
	q := strings.Repeat("X", 1<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	res, err := ix.Search(context.Background(), q, MaxHits)
	runtime.ReadMemStats(&after)
	if err != nil || len(res.Hits) != 0 || res.Total != 0 || len(res.Coverage) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if b := after.TotalAlloc - before.TotalAlloc; b > 64<<10 {
		t.Fatalf("an oversized query allocated %d bytes", b)
	}
	res.Release()
	_ = calls.Load()
}

// The budget refuses a search it cannot hold, and says so.
func TestSearchRefusedOverBudget(t *testing.T) {
	ix := New(Settings{Kinds: []KindRef{refPods}, Limits: Limits{ClusterEntries: 10, ClusterBytes: 1 << 20, FleetEntries: 10, FleetBytes: 100 << 10, PodPercent: 100}})
	defer ix.Stop()
	if _, err := ix.Search(context.Background(), "web", MaxHits); err != ErrSearchBudget {
		t.Fatalf("err %v", err)
	}
}

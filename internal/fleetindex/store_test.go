package fleetindex

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func sources(from, to int) []Source {
	var out []Source
	for i := from; i < to; i++ {
		out = append(out, Source{UID: fmt.Sprintf("u%03d", i), Namespace: "ns", Name: fmt.Sprintf("o%03d", i), Labels: map[string]string{"app": "x"}, Created: t0})
	}
	return out
}

func uids(c *clusterStore, kind string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for uid := range c.kinds[kind].entries {
		out = append(out, uid)
	}
	sort.Strings(out)
	return out
}

func read(t *testing.T, c *clusterStore, gen uint64, kind string, class Class, srcs []Source) bool {
	t.Helper()
	mark, ok := c.beginRead(gen, kind, class)
	if !ok {
		return false
	}
	for _, s := range srcs {
		if c.put(gen, kind, mark, s) == putCapped {
			return false
		}
	}
	return c.endRead(gen, kind, mark, KindStatus{State: StateLive})
}

// A complete read is a mark-and-sweep: what it did not see is released,
// what it saw unchanged costs nothing more.
func TestStoreMarkAndSweep(t *testing.T) {
	b := NewBudget(DefaultLimits)
	c := newClusterStore("a", b, func() time.Time { return t0 })
	c.begin(1, 1)
	if !read(t, c, 1, "configmaps", ClassOther, sources(0, 10)) {
		t.Fatal("first read")
	}
	_, bytes1, _, _ := b.Used("a")
	if !read(t, c, 1, "configmaps", ClassOther, sources(0, 10)) {
		t.Fatal("same read")
	}
	if _, bytes2, _, _ := b.Used("a"); bytes2 != bytes1 {
		t.Fatalf("an unchanged re-read changed the charge: %d -> %d", bytes1, bytes2)
	}
	if !read(t, c, 1, "configmaps", ClassOther, sources(5, 12)) {
		t.Fatal("third read")
	}
	if got := uids(c, "configmaps"); len(got) != 7 || got[0] != "u005" || got[6] != "u011" {
		t.Fatalf("after the sweep %v", got)
	}
	if e, _, _, _ := b.Used("a"); e != 7 {
		t.Fatalf("budget entries %d", e)
	}
	st := c.status()["configmaps"]
	if st.State != StateLive || st.Entries != 7 {
		t.Fatalf("status %+v", st)
	}
	// a watch removal releases
	if !c.remove(1, "configmaps", "u005") {
		t.Fatal("remove")
	}
	if e, _, _, _ := b.Used("a"); e != 6 {
		t.Fatalf("after remove %d", e)
	}
	// a grown label set is admitted for its growth
	s := sources(6, 7)[0]
	s.Labels = map[string]string{"app": "x", "tier": "frontend"}
	_, before, _, _ := b.Used("a")
	if c.put(1, "configmaps", 0, s) != putApplied {
		t.Fatal("growth")
	}
	if _, after, _, _ := b.Used("a"); after <= before {
		t.Fatal("growth charged")
	}
}

// §2.3: at a cap a kind freezes - it never rotates, refills or applies
// deletes within the generation; other kinds go on; a new generation
// re-reads it in place.
func TestStoreFreezesAtCap(t *testing.T) {
	b := NewBudget(Limits{ClusterEntries: 10, ClusterBytes: 1 << 30, FleetEntries: 100, FleetBytes: 1 << 30, PodPercent: 100})
	c := newClusterStore("a", b, func() time.Time { return t0 })
	c.begin(1, 1)
	if read(t, c, 1, "configmaps", ClassOther, sources(0, 15)) {
		t.Fatal("a read past the cap completes")
	}
	held := uids(c, "configmaps")
	st := c.status()["configmaps"]
	if len(held) != 10 || st.State != StateCapped || st.AsOf == nil || !st.AsOf.Equal(t0) || st.Entries != 10 {
		t.Fatalf("held %d, status %+v", len(held), st)
	}
	// frozen: deletes, inserts and growth are ignored; nothing rotates
	c.remove(1, "configmaps", "u000")
	c.put(1, "configmaps", 0, sources(20, 21)[0])
	if _, ok := c.beginRead(1, "configmaps", ClassOther); ok {
		t.Fatal("a frozen kind is not re-read in its generation")
	}
	if got := uids(c, "configmaps"); fmt.Sprint(got) != fmt.Sprint(held) {
		t.Fatalf("a frozen kind changed: %v", got)
	}
	// a vacancy elsewhere is not refilled by the frozen kind
	b.Release("a", 5, 0)
	c.put(1, "configmaps", 0, sources(30, 31)[0])
	if len(uids(c, "configmaps")) != 10 {
		t.Fatal("a vacancy was refilled")
	}
	b.Admit("a", ClassOther, 5, 0) // restore the accounting the test borrowed
	// a new generation re-reads in place: unchanged entries cost nothing
	c.begin(2, 1)
	if _, ok := c.beginRead(2, "configmaps", ClassOther); !ok {
		t.Fatal("a new generation thaws")
	}
}

// §2.7: a cancelled generation's late results are dropped; a down
// connection keeps last-good entries, stale.
func TestStoreGenerationFence(t *testing.T) {
	b := NewBudget(DefaultLimits)
	c := newClusterStore("a", b, func() time.Time { return t0 })
	c.begin(1, 1)
	mark, _ := c.beginRead(1, "configmaps", ClassOther)
	c.put(1, "configmaps", mark, sources(0, 1)[0])
	c.staleAll(t0.Add(time.Minute)) // the connection went down
	if c.put(1, "configmaps", mark, sources(1, 2)[0]) != putStale || c.remove(1, "configmaps", "u000") || c.endRead(1, "configmaps", mark, KindStatus{State: StateLive}) {
		t.Fatal("a cancelled generation's results applied")
	}
	st := c.status()["configmaps"]
	if st.State != StateStale || st.Entries != 1 || !st.AsOf.Equal(t0.Add(time.Minute)) {
		t.Fatalf("stale status %+v", st)
	}
	// recovery: a new generation reads in place
	c.begin(3, 1)
	if !read(t, c, 3, "configmaps", ClassOther, sources(0, 3)) || len(uids(c, "configmaps")) != 3 {
		t.Fatal("recovery read")
	}
	// release drops everything
	c.release()
	if e, by, _, _ := b.Used("a"); e != 0 || by != 0 {
		t.Fatalf("released %d %d", e, by)
	}
}

// Pods stop first: a Pod-heavy read leaves room for the other kinds.
func TestStorePodsCappedFirst(t *testing.T) {
	b := NewBudget(Limits{ClusterEntries: 10, ClusterBytes: 1 << 30, FleetEntries: 100, FleetBytes: 1 << 30, PodPercent: 80})
	c := newClusterStore("a", b, func() time.Time { return t0 })
	c.begin(1, 1)
	if read(t, c, 1, "pods", ClassPods, sources(0, 20)) {
		t.Fatal("pods past their ceiling")
	}
	if !read(t, c, 1, "configmaps", ClassOther, sources(100, 102)) {
		t.Fatal("the other kinds keep their share")
	}
	if c.status()["pods"].State != StateCapped || len(uids(c, "pods")) != 8 {
		t.Fatalf("pods %+v", c.status()["pods"])
	}
}

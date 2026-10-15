package fleetindex

import (
	"sync"
	"testing"
)

// The Line slice 9a: one budget, all or nothing, across kinds and clusters.
func TestBudget(t *testing.T) {
	b := NewBudget(Limits{ClusterEntries: 10, ClusterBytes: 1000, FleetEntries: 15, FleetBytes: 1500})
	if !b.Admit("a", ClassOther, 10, 500) {
		t.Fatal("within both limits")
	}
	if b.Admit("a", ClassOther, 1, 1) {
		t.Fatal("the cluster's entry limit")
	}
	if !b.Admit("b", ClassOther, 5, 900) || b.Admit("b", ClassOther, 0, 101) {
		t.Fatal("the cluster's byte limit")
	}
	if b.Admit("c", ClassOther, 1, 1) {
		t.Fatal("the fleet's entry limit")
	}
	if ce, cb, fe, fb := b.Used("a"); ce != 10 || cb != 500 || fe != 15 || fb != 1400 {
		t.Fatalf("usage %d %d %d %d", ce, cb, fe, fb)
	}
	b.Release("a", 4, 200)
	if !b.Admit("c", ClassOther, 4, 100) {
		t.Fatal("released room is room again")
	}
	if !b.Admit("b", ClassOther, 0, -100) {
		t.Fatal("a shrinking entry always fits")
	}
}

func TestBudgetIsAtomic(t *testing.T) {
	b := NewBudget(Limits{ClusterEntries: 1000, ClusterBytes: 1 << 30, FleetEntries: 1000, FleetBytes: 1 << 30, PodPercent: 100})
	var wg sync.WaitGroup
	admitted := make(chan bool, 4000)
	for i := 0; i < 4000; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			admitted <- b.Admit([]string{"a", "b"}[i%2], Class(i%2), 1, 1)
		}(i)
	}
	wg.Wait()
	close(admitted)
	n := 0
	for ok := range admitted {
		if ok {
			n++
		}
	}
	if _, _, fe, _ := b.Used("a"); n != 1000 || fe != 1000 {
		t.Fatalf("admitted %d, fleet %d", n, fe)
	}
}

// §14.11: Pods are capped first - they fill at most PodPercent of every
// limit, so the rest is always left to the other kinds.
func TestBudgetPodsCappedFirst(t *testing.T) {
	b := NewBudget(Limits{ClusterEntries: 100, ClusterBytes: 1 << 20, FleetEntries: 150, FleetBytes: 1 << 20, PodPercent: 80})
	n := 0
	for b.Admit("a", ClassPods, 1, 1) {
		n++
	}
	if n != 80 {
		t.Fatalf("pods admitted %d, want 80", n)
	}
	m := 0
	for b.Admit("a", ClassOther, 1, 1) {
		m++
	}
	if m != 20 {
		t.Fatalf("other kinds admitted %d after pods, want the 20 left", m)
	}
	// the fleet ceiling: cluster b's pods stop at 80% of the fleet (120),
	// with a already holding 100
	k := 0
	for b.Admit("b", ClassPods, 1, 1) {
		k++
	}
	if k != 20 {
		t.Fatalf("b's pods admitted %d, want 20 (fleet pod ceiling 120)", k)
	}
	if !b.Admit("b", ClassOther, 30, 1) || b.Admit("b", ClassOther, 1, 1) {
		t.Fatal("other kinds fill the fleet's last 30, then stop")
	}
	// bytes too
	c := NewBudget(Limits{ClusterEntries: 100, ClusterBytes: 1000, FleetEntries: 100, FleetBytes: 1000, PodPercent: 80})
	if !c.Admit("a", ClassPods, 1, 800) || c.Admit("a", ClassPods, 1, 1) || !c.Admit("a", ClassOther, 1, 200) {
		t.Fatal("the pod byte ceiling")
	}
}

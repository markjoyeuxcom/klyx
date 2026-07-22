package workloads

import "testing"

func simNode(name, zone string, cpu, mem float64) SimNode {
	return SimNode{Name: name, Zone: zone, CPUAlloc: cpu, MemAlloc: mem, Schedulable: true}
}

func simPod(ns, name, node string, cpu, mem float64) SimPod {
	return SimPod{Namespace: ns, Name: name, Node: node, CPUReq: cpu, MemReq: mem}
}

func TestSimulateLoss_ZoneLossFits(t *testing.T) {
	nodes := []SimNode{
		simNode("a1", "zone-a", 4, 8e9),
		simNode("b1", "zone-b", 4, 8e9),
	}
	pods := []SimPod{
		simPod("x", "p1", "a1", 1, 1e9),
		simPod("x", "p2", "b1", 1, 1e9), // survivor baseline: b1 free drops to 3cpu
	}
	r := SimulateLoss(nodes, pods, LossTarget{Kind: "zone", Name: "zone-a"})
	if r.LostNodes != 1 || r.SurvivorNodes != 1 || r.Displaced != 1 {
		t.Fatalf("counts: %+v", r)
	}
	if r.Rescheduled != 1 || len(r.Unplaced) != 0 {
		t.Fatalf("must fit: %+v", r)
	}
}

func TestSimulateLoss_ShortfallReportsLargestFirst(t *testing.T) {
	nodes := []SimNode{
		simNode("a1", "zone-a", 8, 32e9),
		simNode("b1", "zone-b", 2, 4e9),
	}
	pods := []SimPod{
		simPod("x", "big", "a1", 4, 8e9),
		simPod("x", "mid", "a1", 2, 2e9),
		simPod("x", "small", "a1", 1, 1e9),
	}
	r := SimulateLoss(nodes, pods, LossTarget{Kind: "zone", Name: "zone-a"})
	// b1 has 2cpu/4Gi free: FFD places big(4cpu) -> unplaced, mid(2cpu) -> fits,
	// small(1cpu) -> no cpu left, unplaced.
	if r.Rescheduled != 1 || len(r.Unplaced) != 2 {
		t.Fatalf("packing: %+v", r)
	}
	if r.Unplaced[0].Name != "big" || r.CPUShortfall != 5 {
		t.Fatalf("shortfall order/sum: %+v", r)
	}
}

func TestSimulateLoss_DaemonSetAndUnschedulableRules(t *testing.T) {
	nodes := []SimNode{
		simNode("a1", "zone-a", 4, 8e9),
		{Name: "b1", Zone: "zone-b", CPUAlloc: 4, MemAlloc: 8e9, Schedulable: false}, // cordoned: not a survivor
		simNode("c1", "zone-c", 4, 8e9),
	}
	pods := []SimPod{
		{Namespace: "kube-system", Name: "cilium-x", Node: "a1", CPUReq: 1, MemReq: 1e9, DaemonSet: true},
		simPod("x", "app", "a1", 1, 1e9),
	}
	r := SimulateLoss(nodes, pods, LossTarget{Kind: "node", Name: "a1"})
	if r.SurvivorNodes != 1 {
		t.Fatalf("cordoned node must not count as survivor: %+v", r)
	}
	// The DaemonSet pod dies with the node - only the app pod is displaced.
	if r.Displaced != 1 || r.Rescheduled != 1 {
		t.Fatalf("daemonset exclusion: %+v", r)
	}
}

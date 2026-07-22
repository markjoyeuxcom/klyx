package workloads

import "sort"

// The survivability simulation: "if this zone/pool/node goes away, do the
// displaced pods fit on what's left?" - a REQUESTS-based bin-pack, which is
// the scheduler-relevant question (the scheduler places by requests, not
// usage). Deliberately conservative and stated as such in the UI:
//   - DaemonSet pods on lost nodes are excluded (they don't reschedule),
//   - zero-request pods place trivially (requests-based means no signal),
//   - affinity/anti-affinity, taints, and topology spread are NOT modeled -
//     the result is an upper bound on schedulability, never a guarantee.

// SimNode is one node's scheduling capacity for the simulation.
type SimNode struct {
	Name        string
	Pool        string
	Zone        string
	CPUAlloc    float64
	MemAlloc    float64
	Schedulable bool // Ready && !Unschedulable
}

// SimPod is one running pod's requests footprint.
type SimPod struct {
	Namespace string
	Name      string
	Node      string
	CPUReq    float64
	MemReq    float64
	DaemonSet bool
}

// LossTarget names what disappears: one node, a zone, or a pool.
type LossTarget struct {
	Kind string // "node" | "zone" | "pool"
	Name string
}

// PodRef identifies one unplaced pod with its requests (for the shortfall list).
type PodRef struct {
	Namespace string
	Name      string
	CPUReq    float64
	MemReq    float64
}

// HeadroomResult is the simulation outcome.
type HeadroomResult struct {
	LostNodes     int
	SurvivorNodes int
	Displaced     int // non-DaemonSet pods needing a new home
	Rescheduled   int
	Unplaced      []PodRef // pods that do NOT fit - the shortfall, largest first
	CPUShortfall  float64  // sum of unplaced cpu requests
	MemShortfall  float64
}

func (t LossTarget) matches(n SimNode) bool {
	switch t.Kind {
	case "node":
		return n.Name == t.Name
	case "zone":
		return n.Zone == t.Name
	case "pool":
		return n.Pool == t.Name
	default:
		return false
	}
}

// SimulateLoss bin-packs the displaced pods onto the surviving schedulable
// nodes, first-fit-decreasing by CPU with a best-fit survivor pick (most
// remaining CPU). Survivor capacity starts at allocatable minus the requests
// of the pods ALREADY on it (all of them, DaemonSets included - they keep
// consuming).
func SimulateLoss(nodes []SimNode, pods []SimPod, lose LossTarget) HeadroomResult {
	out := HeadroomResult{}

	lost := map[string]bool{}
	type survivor struct {
		name    string
		cpuFree float64
		memFree float64
	}
	var survivors []*survivor
	for _, n := range nodes {
		if lose.matches(n) {
			lost[n.Name] = true
			out.LostNodes++
			continue
		}
		if !n.Schedulable {
			continue
		}
		survivors = append(survivors, &survivor{name: n.Name, cpuFree: n.CPUAlloc, memFree: n.MemAlloc})
		out.SurvivorNodes++
	}
	byName := map[string]*survivor{}
	for _, s := range survivors {
		byName[s.name] = s
	}

	var displaced []SimPod
	for _, p := range pods {
		if lost[p.Node] {
			if p.DaemonSet {
				continue // DaemonSet pods die with their node
			}
			displaced = append(displaced, p)
			continue
		}
		if s := byName[p.Node]; s != nil {
			s.cpuFree -= p.CPUReq
			s.memFree -= p.MemReq
		}
	}
	out.Displaced = len(displaced)

	// First-fit-decreasing by CPU (mem as tiebreak) onto the survivor with the
	// most remaining CPU that fits both dimensions.
	sort.Slice(displaced, func(i, j int) bool {
		if displaced[i].CPUReq != displaced[j].CPUReq {
			return displaced[i].CPUReq > displaced[j].CPUReq
		}
		return displaced[i].MemReq > displaced[j].MemReq
	})
	for _, p := range displaced {
		var best *survivor
		for _, s := range survivors {
			if s.cpuFree >= p.CPUReq && s.memFree >= p.MemReq {
				if best == nil || s.cpuFree > best.cpuFree {
					best = s
				}
			}
		}
		if best == nil {
			out.Unplaced = append(out.Unplaced, PodRef{Namespace: p.Namespace, Name: p.Name, CPUReq: p.CPUReq, MemReq: p.MemReq})
			out.CPUShortfall += p.CPUReq
			out.MemShortfall += p.MemReq
			continue
		}
		best.cpuFree -= p.CPUReq
		best.memFree -= p.MemReq
		out.Rescheduled++
	}
	return out
}

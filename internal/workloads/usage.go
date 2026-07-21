package workloads

import (
	"sort"
	"time"
)

// Usage is a workload's live resource usage (cpu cores, memory bytes). nil = no
// sample available; never a fabricated zero.
type Usage struct {
	CPU *float64
	Mem *float64
}

// UsageStatus reports whether the metrics source produced a usable result.
// Available=false carries a human Message; UpdatedAt stamps a produced result.
type UsageStatus struct {
	Available bool
	Message   string
	UpdatedAt time.Time
}

// Key is the stable workload identity "<Kind>/<Namespace>/<Name>" shared by the
// health list, the metrics map, and the UI row key.
func (w Workload) Key() string {
	return w.Kind + "/" + w.Namespace + "/" + w.Name
}

// NodeUsage is one node's pod-attributed live usage: the sum of the usage of
// the pods scheduled on it. Deliberately NOT whole-node usage - node OS and
// kubelet overhead are outside it - but it is the scheduling-relevant number
// (what pods consume of allocatable) and needs no extra metric series beyond
// the confirmed per-pod ones. nil = no sampled pod on that node.
type NodeUsage struct {
	Node        string
	CPU         *float64
	Mem         *float64
	SampledPods int // pods that contributed at least one sample
}

// AggregateNodeUsage groups per-pod usage (keyed "<namespace>/<pod>") by the
// pod's assigned node. Unscheduled pods (empty Node) are skipped. A node
// appears only when at least one of its pods produced a sample - a node with
// none is "no data", never zero. Sorted by node name.
func AggregateNodeUsage(pods []PodSummary, cpuByPod, memByPod map[string]float64) []NodeUsage {
	byNode := map[string]*NodeUsage{}
	for i := range pods {
		p := &pods[i]
		if p.Node == "" {
			continue
		}
		k := p.Namespace + "/" + p.Name
		cpu, cpuOK := cpuByPod[k]
		mem, memOK := memByPod[k]
		if !cpuOK && !memOK {
			continue
		}
		nu := byNode[p.Node]
		if nu == nil {
			nu = &NodeUsage{Node: p.Node}
			byNode[p.Node] = nu
		}
		nu.SampledPods++
		if cpuOK {
			if nu.CPU == nil {
				nu.CPU = new(float64)
			}
			*nu.CPU += cpu
		}
		if memOK {
			if nu.Mem == nil {
				nu.Mem = new(float64)
			}
			*nu.Mem += mem
		}
	}
	out := make([]NodeUsage, 0, len(byNode))
	for _, nu := range byNode {
		out = append(out, *nu)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// AggregateUsage sums per-pod usage (keyed "<namespace>/<pod>") over each
// workload's matched pods (reusing the join Assemble already performed via
// Workload.Pods — no second matching interpretation). Usage is sampled and
// approximate, so it is best-effort: a cell sums the pods that have a sample and
// is nil only when NONE do.
func AggregateUsage(ws []Workload, cpuByPod, memByPod map[string]float64) map[string]Usage {
	out := make(map[string]Usage, len(ws))
	for _, w := range ws {
		var cpu, mem float64
		var cpuAny, memAny bool
		for _, p := range w.Pods {
			k := w.Namespace + "/" + p.Name
			if v, ok := cpuByPod[k]; ok {
				cpu += v
				cpuAny = true
			}
			if v, ok := memByPod[k]; ok {
				mem += v
				memAny = true
			}
		}
		u := Usage{}
		if cpuAny {
			c := cpu
			u.CPU = &c
		}
		if memAny {
			m := mem
			u.Mem = &m
		}
		out[w.Key()] = u
	}
	return out
}

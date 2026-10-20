package extres

import (
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Fact is one vendor fact, read from a label (S11a §5). Key is the label's
// suffix (product, memory, replicas, ...), never a whole label key.
type Fact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Entry is one extended resource a node advertises (S11a §2).
type Entry struct {
	Name string
	// Capacity and Allocatable are nil when the key is absent.
	Capacity, Allocatable *Qty
	Accelerator           bool
	// Unit is the resource's unit when known (Unit); Partition names an AMD
	// partition's modes.
	Unit, Partition string
	// RenamedFrom is the base name of NVIDIA's default shared rename
	// (<name>.shared), whose labels the entry's facts come from.
	RenamedFrom string
	// Sharing is Klyx's wording of the resource's sharing, "" when the
	// labels say nothing of it.
	Sharing string
	Facts   []Fact
}

// NodeResources is a node's extended resources within the bounds.
type NodeResources struct {
	Entries []Entry
	// Omitted counts entries past MaxNodeEntries; FactsOmitted counts
	// resources whose facts were left out by MaxFactResources.
	Omitted, FactsOmitted int
	// MIGStrategy is the node-wide nvidia.com/mig.strategy label.
	MIGStrategy string
}

// OfNode reads a node's extended resources: every name in capacity or
// allocatable, accelerators first, then by name, cut at MaxNodeEntries.
func OfNode(n *corev1.Node) NodeResources {
	out := NodeResources{MIGStrategy: clipValue(n.Labels["nvidia.com/mig.strategy"])}
	names := map[corev1.ResourceName]bool{}
	for k := range n.Status.Capacity {
		if IsExtended(k) {
			names[k] = true
		}
	}
	for k := range n.Status.Allocatable {
		if IsExtended(k) {
			names[k] = true
		}
	}
	order := make([]string, 0, len(names))
	for k := range names {
		order = append(order, string(k))
	}
	sortAccelFirst(order)
	if len(order) > MaxNodeEntries {
		out.Omitted = len(order) - MaxNodeEntries
		order = order[:MaxNodeEntries]
	}
	withFacts := 0
	for _, name := range order {
		e := Entry{Name: name, Accelerator: Accelerator(name), Unit: Unit(name, out.MIGStrategy), Partition: Partition(name)}
		if q, ok := n.Status.Capacity[corev1.ResourceName(name)]; ok {
			v := qtyOf(q)
			e.Capacity = &v
		}
		if q, ok := n.Status.Allocatable[corev1.ResourceName(name)]; ok {
			v := qtyOf(q)
			e.Allocatable = &v
		}
		facts, renamed := vendorFacts(n.Labels, name)
		e.RenamedFrom = renamed
		if strings.HasPrefix(name, "nvidia.com/") {
			e.Sharing = sharing(facts, e.Unit)
		}
		if len(facts) > 0 {
			if withFacts < MaxFactResources {
				if len(facts) > MaxFactsPerResource {
					facts = facts[:MaxFactsPerResource]
				}
				e.Facts = facts
				withFacts++
			} else {
				out.FactsOmitted++
			}
		}
		out.Entries = append(out.Entries, e)
	}
	return out
}

// sortAccelFirst orders accelerators first, then by name.
func sortAccelFirst(names []string) {
	sort.Slice(names, func(i, j int) bool {
		ai, aj := Accelerator(names[i]), Accelerator(names[j])
		if ai != aj {
			return ai
		}
		return names[i] < names[j]
	})
}

func clipValue(s string) string {
	if len(s) > MaxFactValue {
		return s[:MaxFactValue]
	}
	return s
}

// The NVIDIA per-resource label suffixes, in display order (GPU Feature
// Discovery writes <resource-name>.<suffix>).
var nvidiaSuffixes = []string{"product", "count", "memory", "replicas", "sharing-strategy"}

// The AMD node labeller's kinds, in display order; the labeller writes
// amd.com/gpu.<kind> (beta.amd.com/ too), or amd.com/gpu.<kind>.<value> =
// <count> when the node's GPUs differ.
var amdKinds = []string{"product-name", "vram", "family", "driver-version", "compute-memory-partition"}

// vendorFacts reads a resource's facts from the node's labels: NVIDIA's are
// the resource's own (or, for the default <base>.shared rename, its base's);
// AMD's are the node's, shared by every amd.com/ resource.
func vendorFacts(labels map[string]string, name string) ([]Fact, string) {
	switch {
	case strings.HasPrefix(name, "nvidia.com/"):
		if f := nvidiaFacts(labels, name); len(f) > 0 {
			return f, ""
		}
		if base, ok := strings.CutSuffix(name, ".shared"); ok {
			if f := nvidiaFacts(labels, base); len(f) > 0 {
				return f, base
			}
		}
	case strings.HasPrefix(name, "amd.com/"):
		return amdFacts(labels), ""
	}
	return nil, ""
}

func nvidiaFacts(labels map[string]string, name string) []Fact {
	var out []Fact
	for _, s := range nvidiaSuffixes {
		if v, ok := labels[name+"."+s]; ok {
			out = append(out, Fact{Key: s, Value: clipValue(v)})
		}
	}
	return out
}

func amdFacts(labels map[string]string) []Fact {
	var out []Fact
	for _, k := range amdKinds {
		v, ok := labels["amd.com/gpu."+k]
		if !ok {
			v, ok = labels["beta.amd.com/gpu."+k]
		}
		if !ok && k == "compute-memory-partition" {
			v, ok = labels["amd.com/compute-memory-partition"] // the documented spelling
		}
		if ok {
			out = append(out, Fact{Key: k, Value: clipValue(v)})
			continue
		}
		if mixed := amdMixed(labels, k); mixed != "" {
			out = append(out, Fact{Key: k, Value: mixed})
		}
	}
	return out
}

// amdMixed reads the per-value form: "mixed: 16G ×2, 32G ×1".
func amdMixed(labels map[string]string, kind string) string {
	var parts []string
	for _, prefix := range []string{"amd.com/gpu." + kind + ".", "beta.amd.com/gpu." + kind + "."} {
		for k, count := range labels {
			if v, ok := strings.CutPrefix(k, prefix); ok && v != "" {
				parts = append(parts, v+" ×"+count)
			}
		}
		if len(parts) > 0 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return clipValue("mixed: " + strings.Join(parts, ", "))
}

// sharing words an NVIDIA resource's sharing from its own facts (S11a §5):
// a replica count above 1 says how many shares a unit is advertised as; a
// strategy or a -SHARED product without one says only that it is shared.
// Klyx never infers a replica count.
func sharing(facts []Fact, unit string) string {
	get := func(k string) string {
		for _, f := range facts {
			if f.Key == k {
				return f.Value
			}
		}
		return ""
	}
	if unit == "" {
		unit = "device"
	}
	strategy := get("sharing-strategy")
	if r, err := strconv.Atoi(get("replicas")); err == nil && r > 1 {
		how := strategy
		if how == "" || how == "none" {
			how = "sharing"
		}
		return "shared (" + how + "): each " + unit + " is advertised " + strconv.Itoa(r) + " times, so these count shares of a " + unit
	}
	if strategy == "time-slicing" || strategy == "mps" || strings.HasSuffix(get("product"), "-SHARED") {
		return "shared (replica count not reported)"
	}
	return ""
}

// Gap is one extended resource whose allocatable reads below its capacity
// (S11a §3). The figures are the kubelet's own, as written.
type Gap struct {
	Name                  string
	Allocatable, Capacity string
}

// Gaps reads every extended key of a node, before any display cut: each
// resource with both keys present and allocatable below capacity.
func Gaps(n *corev1.Node) []Gap {
	var out []Gap
	for k, c := range n.Status.Capacity {
		if !IsExtended(k) {
			continue
		}
		a, ok := n.Status.Allocatable[k]
		if ok && a.Cmp(c) < 0 {
			out = append(out, Gap{Name: string(k), Allocatable: a.String(), Capacity: c.String()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

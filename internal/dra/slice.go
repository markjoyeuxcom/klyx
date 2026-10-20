package dra

import (
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The API's bounds on a slice (resource.k8s.io/v1, the larger of 1.34's
// and 1.35-1.37's: 1.34 allows 32 counter sets and 16 consumptions per
// device, 1.35+ 8 and 2): a list past them marks the projection Truncated
// rather than cut silently (S11b §2.0).
const (
	maxDevices         = 128
	maxCounterSets     = 32
	maxCounters        = 32
	maxConsumptions    = 16
	maxTaintsPerDevice = 16
)

// ProjectSlice keeps the fields the pool rules read (S11b §2.0's ledger):
// the pool identity, the access form, counter sets with their counter
// names, and per device its name, access form, consumed sets and counters,
// compatibility-group use and taint effects. Attributes, capacities,
// quantities, selectors' content and taint keys and values are dropped.
func ProjectSlice(u *unstructured.Unstructured) Slice {
	o := u.Object
	s := Slice{Name: u.GetName(), UID: string(u.GetUID()), RV: u.GetResourceVersion(),
		Driver:       str(o, "spec", "driver"),
		Pool:         str(o, "spec", "pool", "name"),
		Generation:   i64(o, "spec", "pool", "generation"),
		Count:        i64(o, "spec", "pool", "resourceSliceCount"),
		NodeName:     str(o, "spec", "nodeName"),
		AllNodes:     boolean(o, "spec", "allNodes"),
		NodeSelector: present(o, "spec", "nodeSelector"),
		PerDevice:    boolean(o, "spec", "perDeviceNodeSelection"),
	}
	sets, _, _ := unstructured.NestedSlice(o, "spec", "sharedCounters")
	if len(sets) > maxCounterSets {
		s.Truncated = true
	}
	for _, x := range sets {
		m, _ := x.(map[string]any)
		cs := CounterSet{Name: str(m, "name"), Counters: keys(m, "counters")}
		if len(cs.Counters) > maxCounters {
			s.Truncated = true
		}
		s.CounterSets = append(s.CounterSets, cs)
	}
	devs, _, _ := unstructured.NestedSlice(o, "spec", "devices")
	if len(devs) > maxDevices {
		s.Truncated = true
	}
	for _, x := range devs {
		m, _ := x.(map[string]any)
		d := Device{Name: str(m, "name"), NodeName: str(m, "nodeName"), AllNodes: boolean(m, "allNodes"), NodeSelector: present(m, "nodeSelector")}
		cons, _, _ := unstructured.NestedSlice(m, "consumesCounters")
		if len(cons) > maxConsumptions {
			s.Truncated = true
		}
		for _, y := range cons {
			cm, _ := y.(map[string]any)
			d.Consumes = append(d.Consumes, Consumption{Set: str(cm, "counterSet"), Counters: keys(cm, "counters"), CompatGroups: present(cm, "compatibilityGroups")})
		}
		taints, _, _ := unstructured.NestedSlice(m, "taints")
		if len(taints) > maxTaintsPerDevice {
			s.Truncated = true
		}
		for _, y := range taints {
			tm, _ := y.(map[string]any)
			d.TaintEffects = append(d.TaintEffects, str(tm, "effect"))
		}
		s.Devices = append(s.Devices, d)
	}
	return s
}

func str(m map[string]any, path ...string) string {
	v, _, _ := unstructured.NestedString(m, path...)
	return v
}

func boolean(m map[string]any, path ...string) bool {
	v, _, _ := unstructured.NestedBool(m, path...)
	return v
}

func present(m map[string]any, path ...string) bool {
	v, ok, _ := unstructured.NestedFieldNoCopy(m, path...)
	return ok && v != nil
}

func i64(m map[string]any, path ...string) int64 {
	v, ok, _ := unstructured.NestedFieldNoCopy(m, path...)
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

func keys(m map[string]any, field string) []string {
	c, _, _ := unstructured.NestedMap(m, field)
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Package dra reads dynamic resource allocation (coverage S11b): pools of
// ResourceSlices, ResourceClaims and DeviceClasses. Pure: no client, no
// clock.
//
// A pool is read as the scheduler reads it, per node, under every scheduler
// the cluster may run (S11b §3.2): the server's minor or one older, and the
// partitionable-devices feature on and off where the pool uses it. The
// rules are the scheduler's own (k8s.io/dynamic-resource-allocation
// structured/internal/stable GatherPools at v0.33.13 to v0.37.1), held to
// its allocator by an oracle (testdata/pools).
package dra

import (
	"sort"
	"strconv"
)

// CounterSet is one shared counter set: its name and its counters' names.
type CounterSet struct {
	Name     string
	Counters []string
}

// Consumption is one device's use of a counter set: the set's name and
// the counters it consumes. CompatGroups: the 1.37 alpha field is set.
type Consumption struct {
	Set          string
	Counters     []string
	CompatGroups bool
}

// Device is one device of a slice, with its own access form under
// per-device selection.
type Device struct {
	Name         string
	NodeName     string
	AllNodes     bool
	NodeSelector bool
	Consumes     []Consumption
	TaintEffects []string
}

// Slice is a projected ResourceSlice: every field a pool rule reads.
type Slice struct {
	Name, UID         string
	Driver, Pool      string
	Generation, Count int64
	NodeName          string
	AllNodes          bool
	NodeSelector      bool
	PerDevice         bool
	CounterSets       []CounterSet
	Devices           []Device
	// Truncated: a list was longer than the API allows, so the projection
	// cannot vouch for it.
	Truncated bool
}

func (s Slice) usesCompat() bool {
	for _, d := range s.Devices {
		for _, c := range d.Consumes {
			if c.CompatGroups {
				return true
			}
		}
	}
	return false
}

func (s Slice) usesCounters() bool {
	if len(s.CounterSets) > 0 {
		return true
	}
	for _, d := range s.Devices {
		if len(d.Consumes) > 0 {
			return true
		}
	}
	return false
}

// Family is a scheduler's pool rules: 1.33, 1.34, or 1.35 to 1.37.
type Family int

const (
	F133 Family = iota
	F134
	F135
)

// FamilyOf is the rule family of a scheduler minor.
func FamilyOf(minor int) Family {
	switch {
	case minor <= 33:
		return F133
	case minor == 34:
		return F134
	}
	return F135
}

// Variant is one scheduler the cluster may run.
type Variant struct {
	Minor int
	// Partitionable: the partitionable-devices feature is on.
	Partitionable bool
}

func (v Variant) String() string {
	g := "off"
	if v.Partitionable {
		g = "on"
	}
	return "1." + strconv.Itoa(v.Minor) + " (partitionable devices " + g + ")"
}

// The outcomes of one view under one variant.
const (
	OutNone          = "none"     // no slice serves the view, or the scheduler ignores them
	OutObsolete      = "obsolete" // 1.35+: a newer generation exists elsewhere
	OutUpdating      = "updating" // incomplete
	OutInvalid       = "invalid"
	OutComplete      = "complete"
	OutUnestablished = "unestablished"
)

// Outcome is one view's outcome under one variant.
type Outcome struct {
	Kind string
	// Devices are the devices the view can allocate (complete only).
	Devices []string
	// Have and Want: the slices present and resourceSliceCount (updating).
	Have, Want int64
	// Rule is the failed rule (invalid) or why not established.
	Rule string
}

// sees reports whether a slice serves the view's node ("" is a node no
// slice names) under a variant, and whether that needs a node selector
// evaluated (unknown).
func sees(s Slice, node string, v Variant) (visible, unknown bool) {
	f := FamilyOf(v.Minor)
	switch f {
	case F133:
		if !v.Partitionable && (len(s.CounterSets) > 0 || s.PerDevice) {
			return false, false
		}
	case F134:
		if !v.Partitionable && s.PerDevice {
			return false, false
		}
	case F135:
		if !v.Partitionable && (s.PerDevice || len(s.CounterSets) > 0) {
			return false, false
		}
		if len(s.CounterSets) > 0 {
			return true, false // every node sees a counter slice
		}
	}
	switch {
	case s.NodeName != "" || s.AllNodes || s.NodeSelector:
		if s.NodeSelector {
			return false, true
		}
		return s.AllNodes || (node != "" && s.NodeName == node), false
	case s.PerDevice:
		selector := false
		for _, d := range s.Devices {
			if d.AllNodes || (node != "" && d.NodeName == node) {
				return true, false
			}
			selector = selector || d.NodeSelector
		}
		return false, selector
	}
	return false, false // no access form: an unknown extension, skipped
}

// devicesFor are the devices of a visible slice the view's node can use.
func devicesFor(s Slice, node string, v Variant) []string {
	var out []string
	for _, d := range s.Devices {
		if s.PerDevice && !(d.AllNodes || (node != "" && d.NodeName == node)) {
			continue
		}
		if !v.Partitionable && len(d.Consumes) > 0 {
			continue // a device consuming counters is not allocatable without the feature
		}
		out = append(out, d.Name)
	}
	return out
}

// ViewOutcome is one view's outcome under one variant. slices are every
// slice of the pool (one driver and pool name), in the capture's order.
func ViewOutcome(slices []Slice, node string, v Variant) Outcome {
	for _, s := range slices {
		if s.Truncated {
			return Outcome{Kind: OutUnestablished, Rule: "a slice's lists exceed the API's bounds"}
		}
		if s.usesCompat() {
			return Outcome{Kind: OutUnestablished, Rule: "completeness depends on the scheduler's compatibility-groups support, which Klyx cannot read"}
		}
	}
	f := FamilyOf(v.Minor)
	if f != F135 {
		for _, s := range slices {
			if s.usesCounters() {
				return Outcome{Kind: OutUnestablished, Rule: "counter-based partitionable devices were alpha before 1.35, in another shape"}
			}
		}
	}
	var visible []Slice
	for _, s := range slices {
		vis, unknown := sees(s, node, v)
		if unknown {
			return Outcome{Kind: OutUnestablished, Rule: "a node selector would have to be evaluated"}
		}
		if vis {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return Outcome{Kind: OutNone}
	}
	// the highest generation among the visible slices, as addSlice keeps it
	gen := visible[0].Generation
	for _, s := range visible[1:] {
		if s.Generation > gen {
			gen = s.Generation
		}
	}
	var atGen []Slice
	for _, s := range visible {
		if s.Generation == gen {
			atGen = append(atGen, s)
		}
	}
	want := atGen[0].Count
	for _, s := range atGen[1:] {
		if s.Count != want {
			return Outcome{Kind: OutUnestablished, Rule: "the pool's slices disagree on its slice count"}
		}
	}
	if f != F135 {
		if int64(len(atGen)) != want {
			return Outcome{Kind: OutUpdating, Have: int64(len(atGen)), Want: want}
		}
		if dup := duplicate(atGen); dup != "" {
			return Outcome{Kind: OutInvalid, Rule: "duplicate device name " + dup}
		}
		return complete(atGen, node, v)
	}
	// 1.35+: the view falls short; look across the cluster
	var all []Slice
	if int64(len(atGen)) != want {
		for _, s := range slices {
			switch {
			case s.Generation > gen:
				return Outcome{Kind: OutObsolete}
			case s.Generation == gen:
				all = append(all, s)
			}
		}
		if int64(len(all)) != want {
			return Outcome{Kind: OutUpdating, Have: int64(len(all)), Want: want}
		}
	}
	return build(atGen, all, node, v)
}

// build is buildPool's validation (1.35+): device names among the view's
// device slices, then the counter sets and every device's consumption.
func build(view, all []Slice, node string, v Variant) Outcome {
	var deviceSlices, counterSlices []Slice
	if v.Partitionable {
		for _, s := range view {
			if len(s.CounterSets) > 0 {
				counterSlices = append(counterSlices, s)
			} else {
				deviceSlices = append(deviceSlices, s)
			}
		}
	} else {
		deviceSlices = view
	}
	if dup := duplicate(deviceSlices); dup != "" {
		return Outcome{Kind: OutInvalid, Rule: "duplicate device name " + dup}
	}
	if !v.Partitionable {
		return complete(deviceSlices, node, v)
	}
	sets := map[string]map[string]bool{}
	for _, s := range counterSlices {
		for _, cs := range s.CounterSets {
			if sets[cs.Name] != nil {
				return Outcome{Kind: OutInvalid, Rule: "duplicate counter set name " + cs.Name}
			}
			names := map[string]bool{}
			for _, c := range cs.Counters {
				names[c] = true
			}
			sets[cs.Name] = names
		}
	}
	if rule := consumption(sets, view); rule != "" {
		return Outcome{Kind: OutInvalid, Rule: rule}
	}
	if all != nil && len(all) != len(view) {
		inView := map[string]bool{}
		for _, s := range view {
			inView[s.Name] = true
		}
		var rest []Slice
		for _, s := range all {
			if !inView[s.Name] {
				rest = append(rest, s)
			}
		}
		if rule := consumption(sets, rest); rule != "" {
			return Outcome{Kind: OutInvalid, Rule: rule}
		}
	}
	return complete(deviceSlices, node, v)
}

func consumption(sets map[string]map[string]bool, slices []Slice) string {
	for _, s := range slices {
		for _, d := range s.Devices {
			for _, c := range d.Consumes {
				set, ok := sets[c.Set]
				if !ok {
					return "counter set " + c.Set + " not found"
				}
				for _, n := range c.Counters {
					if !set[n] {
						return "counter " + n + " not found in counter set " + c.Set
					}
				}
			}
		}
	}
	return ""
}

func duplicate(slices []Slice) string {
	seen := map[string]bool{}
	for _, s := range slices {
		for _, d := range s.Devices {
			if seen[d.Name] {
				return d.Name
			}
			seen[d.Name] = true
		}
	}
	return ""
}

func complete(slices []Slice, node string, v Variant) Outcome {
	out := Outcome{Kind: OutComplete}
	for _, s := range slices {
		out.Devices = append(out.Devices, devicesFor(s, node, v)...)
	}
	sort.Strings(out.Devices)
	if len(out.Devices) == 0 {
		out.Kind = OutNone
	}
	return out
}

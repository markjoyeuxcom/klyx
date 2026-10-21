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
	Name, UID string
	// RV is the slice's resourceVersion: the episode tracker's proof that
	// a pool did not change between two reads
	RV                string
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
	// Devices are the devices the view can allocate (complete only), and N
	// their count; a read pool keeps the count alone (a pool row carries
	// counts, never device names).
	Devices []string
	N       int
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
	return newPoolIndex(slices).outcome(node, v)
}

// poolIndex is one pool's slices indexed for its views (S11b §8: a pool of
// 2,000 node-local slices reads in linear time). What every view shares is
// read once: the pool-wide reasons no view is established, each
// generation's slices, and each generation's counter sets with the
// consumptions they fail. A view reads only the slices its node may see,
// in the capture's order, so every outcome and rule is the one a scan of
// every slice gives (the pool oracle holds it to the allocator's).
type poolIndex struct {
	slices []Slice
	// blocked: a pool-wide reason no view is established
	blocked  string
	counters bool
	// global: the slices any node may see, or whose visibility a selector
	// decides; local: the slices only the named node may see. Both in the
	// capture's order.
	global []int
	local  map[string][]int
	maxGen int64
	byGen  map[int64][]int
	gens   map[int64]*genSets
}

// genSets are one generation's counter sets (1.35+, the feature on: every
// view sees every counter slice) and each slice's first failing
// consumption against them.
type genSets struct {
	sets map[string]map[string]bool
	rule string
	fail map[int]string
}

func newPoolIndex(slices []Slice) *poolIndex {
	ix := &poolIndex{slices: slices, local: map[string][]int{}, byGen: map[int64][]int{}, gens: map[int64]*genSets{}}
	for i, s := range slices {
		if ix.blocked == "" {
			if s.Truncated {
				ix.blocked = "a slice's lists exceed the API's bounds"
			} else if s.usesCompat() {
				ix.blocked = "completeness depends on the scheduler's compatibility-groups support, which Klyx cannot read"
			}
		}
		ix.counters = ix.counters || s.usesCounters()
		if i == 0 || s.Generation > ix.maxGen {
			ix.maxGen = s.Generation
		}
		ix.byGen[s.Generation] = append(ix.byGen[s.Generation], i)
		// the access forms as sees reads them
		switch {
		case len(s.CounterSets) > 0, s.AllNodes, s.NodeSelector:
			ix.global = append(ix.global, i)
		case s.NodeName != "":
			ix.local[s.NodeName] = append(ix.local[s.NodeName], i)
		case s.PerDevice:
			anywhere := false
			for _, d := range s.Devices {
				anywhere = anywhere || d.AllNodes || d.NodeSelector
			}
			if anywhere {
				ix.global = append(ix.global, i)
				continue
			}
			seen := map[string]bool{}
			for _, d := range s.Devices {
				if d.NodeName != "" && !seen[d.NodeName] {
					seen[d.NodeName] = true
					ix.local[d.NodeName] = append(ix.local[d.NodeName], i)
				}
			}
		}
		// no access form: no node sees it
	}
	return ix
}

func (ix *poolIndex) outcome(node string, v Variant) Outcome {
	if ix.blocked != "" {
		return Outcome{Kind: OutUnestablished, Rule: ix.blocked}
	}
	f := FamilyOf(v.Minor)
	if f != F135 && ix.counters {
		return Outcome{Kind: OutUnestablished, Rule: "counter-based partitionable devices were alpha before 1.35, in another shape"}
	}
	// the slices this node may see, merged back into the capture's order
	var visible []int
	g, l := ix.global, ix.local[node]
	if node == "" {
		l = nil
	}
	for i, j := 0, 0; i < len(g) || j < len(l); {
		var k int
		if j == len(l) || (i < len(g) && g[i] < l[j]) {
			k, i = g[i], i+1
		} else {
			k, j = l[j], j+1
		}
		vis, unknown := sees(ix.slices[k], node, v)
		if unknown {
			return Outcome{Kind: OutUnestablished, Rule: "a node selector would have to be evaluated"}
		}
		if vis {
			visible = append(visible, k)
		}
	}
	if len(visible) == 0 {
		return Outcome{Kind: OutNone}
	}
	// the highest generation among the visible slices, as addSlice keeps it
	gen := ix.slices[visible[0]].Generation
	for _, k := range visible[1:] {
		if g := ix.slices[k].Generation; g > gen {
			gen = g
		}
	}
	var atGen []int
	for _, k := range visible {
		if ix.slices[k].Generation == gen {
			atGen = append(atGen, k)
		}
	}
	want := ix.slices[atGen[0]].Count
	for _, k := range atGen[1:] {
		if ix.slices[k].Count != want {
			return Outcome{Kind: OutUnestablished, Rule: "the pool's slices disagree on its slice count"}
		}
	}
	if f != F135 {
		if int64(len(atGen)) != want {
			return Outcome{Kind: OutUpdating, Have: int64(len(atGen)), Want: want}
		}
		if dup := ix.duplicate(atGen); dup != "" {
			return Outcome{Kind: OutInvalid, Rule: "duplicate device name " + dup}
		}
		return ix.complete(atGen, node, v)
	}
	// 1.35+: the view falls short; look across the cluster
	var all []int
	if int64(len(atGen)) != want {
		if ix.maxGen > gen {
			return Outcome{Kind: OutObsolete}
		}
		all = ix.byGen[gen]
		if int64(len(all)) != want {
			return Outcome{Kind: OutUpdating, Have: int64(len(all)), Want: want}
		}
	}
	return ix.build(atGen, all, gen, node, v)
}

// build is buildPool's validation (1.35+): device names among the view's
// device slices, then the counter sets and every device's consumption.
func (ix *poolIndex) build(view, all []int, gen int64, node string, v Variant) Outcome {
	var deviceSlices []int
	if v.Partitionable {
		for _, k := range view {
			if len(ix.slices[k].CounterSets) == 0 {
				deviceSlices = append(deviceSlices, k)
			}
		}
	} else {
		deviceSlices = view
	}
	if dup := ix.duplicate(deviceSlices); dup != "" {
		return Outcome{Kind: OutInvalid, Rule: "duplicate device name " + dup}
	}
	if !v.Partitionable {
		return ix.complete(deviceSlices, node, v)
	}
	gs := ix.genSets(gen)
	if gs.rule != "" {
		return Outcome{Kind: OutInvalid, Rule: gs.rule}
	}
	for _, k := range view {
		if r := gs.fail[k]; r != "" {
			return Outcome{Kind: OutInvalid, Rule: r}
		}
	}
	if all != nil && len(all) != len(view) && len(gs.fail) > 0 {
		inView := map[string]bool{}
		for _, k := range view {
			inView[ix.slices[k].Name] = true
		}
		for _, k := range all {
			if r := gs.fail[k]; r != "" && !inView[ix.slices[k].Name] {
				return Outcome{Kind: OutInvalid, Rule: r}
			}
		}
	}
	return ix.complete(deviceSlices, node, v)
}

func (ix *poolIndex) genSets(gen int64) *genSets {
	if gs, ok := ix.gens[gen]; ok {
		return gs
	}
	gs := &genSets{sets: map[string]map[string]bool{}, fail: map[int]string{}}
	ix.gens[gen] = gs
	for _, k := range ix.byGen[gen] {
		for _, cs := range ix.slices[k].CounterSets {
			if gs.sets[cs.Name] != nil {
				gs.rule = "duplicate counter set name " + cs.Name
				return gs
			}
			names := map[string]bool{}
			for _, c := range cs.Counters {
				names[c] = true
			}
			gs.sets[cs.Name] = names
		}
	}
	for _, k := range ix.byGen[gen] {
		if r := consumption(gs.sets, ix.slices[k]); r != "" {
			gs.fail[k] = r
		}
	}
	return gs
}

func consumption(sets map[string]map[string]bool, s Slice) string {
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
	return ""
}

func (ix *poolIndex) duplicate(ks []int) string {
	seen := map[string]bool{}
	for _, k := range ks {
		for _, d := range ix.slices[k].Devices {
			if seen[d.Name] {
				return d.Name
			}
			seen[d.Name] = true
		}
	}
	return ""
}

func (ix *poolIndex) complete(ks []int, node string, v Variant) Outcome {
	out := Outcome{Kind: OutComplete}
	for _, k := range ks {
		out.Devices = append(out.Devices, devicesFor(ix.slices[k], node, v)...)
	}
	sort.Strings(out.Devices)
	out.N = len(out.Devices)
	if out.N == 0 {
		out.Kind = OutNone
	}
	return out
}

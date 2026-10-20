package dra

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// The index (S11b §8) against the scan it replaced: every view of random
// pools, under every minor from 1.33 to 1.37 with the feature on and off,
// reads the same outcome, rule, counts and devices.

// refOutcome is the pool reading before the index (a scan of every slice
// per view), kept as the index's reference.
// slice of the pool (one driver and pool name), in the capture's order.
func refOutcome(slices []Slice, node string, v Variant) Outcome {
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
		if dup := refDuplicate(atGen); dup != "" {
			return Outcome{Kind: OutInvalid, Rule: "duplicate device name " + dup}
		}
		return refComplete(atGen, node, v)
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
	return refBuild(atGen, all, node, v)
}

// build is buildPool's validation (1.35+): device names among the view's
// device slices, then the counter sets and every device's consumption.
func refBuild(view, all []Slice, node string, v Variant) Outcome {
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
	if dup := refDuplicate(deviceSlices); dup != "" {
		return Outcome{Kind: OutInvalid, Rule: "duplicate device name " + dup}
	}
	if !v.Partitionable {
		return refComplete(deviceSlices, node, v)
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
	if rule := refConsumption(sets, view); rule != "" {
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
		if rule := refConsumption(sets, rest); rule != "" {
			return Outcome{Kind: OutInvalid, Rule: rule}
		}
	}
	return refComplete(deviceSlices, node, v)
}

func refConsumption(sets map[string]map[string]bool, slices []Slice) string {
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

func refDuplicate(slices []Slice) string {
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

func refComplete(slices []Slice, node string, v Variant) Outcome {
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

func randomPool(r *rand.Rand) []Slice {
	nodes := []string{"n1", "n2", "n3"}
	var out []Slice
	n := 1 + r.Intn(5)
	for i := 0; i < n; i++ {
		s := Slice{Name: fmt.Sprintf("s%d", i), Driver: "d", Pool: "p", Generation: int64(1 + r.Intn(2)), Count: int64(1 + r.Intn(3))}
		switch r.Intn(8) {
		case 0:
			s.AllNodes = true
		case 1:
			s.NodeSelector = r.Intn(4) == 0
			s.AllNodes = !s.NodeSelector
		case 2, 3:
			s.PerDevice = true
		case 4:
			s.CounterSets = []CounterSet{{Name: fmt.Sprintf("cs%d", r.Intn(2)), Counters: []string{"a", "b"}[:1+r.Intn(2)]}}
			if r.Intn(2) == 0 {
				s.AllNodes = true
			} else {
				s.NodeName = nodes[r.Intn(3)]
			}
		case 5:
			// no access form
		default:
			s.NodeName = nodes[r.Intn(3)]
		}
		for d := 0; d < r.Intn(3); d++ {
			dev := Device{Name: fmt.Sprintf("g%d", r.Intn(4))}
			if s.PerDevice {
				switch r.Intn(5) {
				case 0:
					dev.AllNodes = true
				case 1:
					dev.NodeSelector = r.Intn(3) == 0
				default:
					dev.NodeName = nodes[r.Intn(3)]
				}
			}
			if r.Intn(4) == 0 {
				dev.Consumes = []Consumption{{Set: fmt.Sprintf("cs%d", r.Intn(3)), Counters: []string{"a", "b", "c"}[r.Intn(3):]}}
			}
			s.Devices = append(s.Devices, dev)
		}
		s.Truncated = r.Intn(40) == 0
		out = append(out, s)
	}
	return out
}

func TestIndexReadsAsTheScan(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	compared := 0
	kinds := map[string]int{}
	for i := 0; i < 20000; i++ {
		slices := randomPool(r)
		ix := newPoolIndex(slices)
		for _, node := range []string{"n1", "n2", "n3", "n9", ""} {
			for minor := 33; minor <= 37; minor++ {
				for _, gate := range []bool{false, true} {
					v := Variant{Minor: minor, Partitionable: gate}
					want, got := refOutcome(slices, node, v), ix.outcome(node, v)
					got.N = 0
					if !reflect.DeepEqual(want, got) {
						t.Fatalf("pool %+v, view %q, %s: the scan %+v, the index %+v", slices, node, v, want, got)
					}
					compared++
					kinds[want.Kind]++
				}
			}
		}
	}
	// every outcome is exercised
	for _, k := range []string{OutNone, OutObsolete, OutUpdating, OutInvalid, OutComplete, OutUnestablished} {
		if kinds[k] == 0 {
			t.Errorf("no random pool reads %s: %v", k, kinds)
		}
	}
	t.Logf("%d views compared: %v", compared, kinds)
}

func TestKeptKeepsTheWorstViews(t *testing.T) {
	var views []View
	for i := 0; i < 20; i++ {
		views = append(views, View{Node: fmt.Sprintf("n%02d", i), Agreed: true, Kind: OutComplete})
	}
	views[17] = View{Node: "n17", Agreed: true, Kind: OutInvalid}
	views[3] = View{Node: "n03", Agreed: true, Kind: OutUpdating}
	views[9] = View{Node: "n09", Agreed: false}
	p := PoolRead{Views: views, Obsolete: make([]string, 20)}
	k := p.Kept(4)
	got := []string{}
	for _, v := range k.Views {
		got = append(got, v.Node)
	}
	if !reflect.DeepEqual(got, []string{"n17", "n03", "n09", "n00"}) || k.ViewsOmitted != 16 || len(k.Obsolete) != 4 || k.ObsoleteOmitted != 16 {
		t.Fatalf("the worst first, the rest counted: %v, %d omitted; obsolete %d, %d omitted", got, k.ViewsOmitted, len(k.Obsolete), k.ObsoleteOmitted)
	}
	if len(p.Views) != 20 || p.Views[0].Node != "n00" {
		t.Fatal("Kept copies: the read is unchanged")
	}
	if p2 := (PoolRead{Views: views[:3]}).Kept(4); p2.ViewsOmitted != 0 || len(p2.Views) != 3 {
		t.Fatal("within the bound, nothing is cut")
	}
}

var _ = sort.Strings

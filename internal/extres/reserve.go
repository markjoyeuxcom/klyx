package extres

import (
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// MaxRowsBytes bounds the reservations rows per cluster (S11a §2.0).
const MaxRowsBytes = 2 << 20

// Figures are one resource's reservations on a node (S11a §4).
type Figures struct {
	// Requested counts against the node: the node advertises the name and
	// no DRA mapping serves the pair.
	Requested Qty
	// ServedByDRA: a pair in status.extendedResourceClaimStatus, or an
	// implicit DeviceClass name.
	ServedByDRA Qty
	// NotAdvertised: an extended name absent from the node's allocatable
	// (a cluster-level resource, or DRA-served without a mapping).
	NotAdvertised Qty
}

func zeroFigures() Figures { return Figures{Known(0), Known(0), Known(0)} }

// Reserved is one entry of a node's row.
type Reserved struct {
	Name       string
	Advertised bool
	Figures
}

// NodeRow is one node's reservations, within the bounds: the names the
// node advertises first, then the other requested names by amount.
type NodeRow struct {
	Node    string
	UID     types.UID
	Entries []Reserved
	// Omitted counts entries past MaxRowEntries; each kept entry is exact.
	Omitted int
	// Untracked: more than MaxTracked distinct names the node does not
	// advertise were requested on it, so their DRA-served and not-advertised
	// figures are not established. Advertised entries stay exact.
	Untracked bool
}

// Total is one accelerator name across the cluster, computed from every
// node and pod before any cut (S11a §2.0).
type Total struct {
	Name                             string
	Capacity, Allocatable, Requested Qty
}

// Result is the reservations rows and the totals.
type Result struct {
	Rows []NodeRow
	// RowsDropped counts nodes whose rows the per-cluster bound left out.
	RowsDropped int
	Totals      []Total
	// TotalsOmitted counts accelerator names past MaxTotals.
	TotalsOmitted int
}

type nodeState struct {
	node       *corev1.Node
	advertised map[string]*Figures // names in the node's allocatable
	tracked    map[string]*Figures // other requested names, at most MaxTracked
	untracked  bool
}

func (s *nodeState) add(name string, class int, q Qty) {
	f := s.advertised[name]
	if f == nil {
		f = s.tracked[name]
		if f == nil {
			if len(s.tracked) >= MaxTracked {
				s.untracked = true
				return
			}
			z := zeroFigures()
			f = &z
			s.tracked[name] = f
		}
	}
	switch class {
	case classCounted:
		f.Requested = f.Requested.Add(q)
	case classDRA:
		f.ServedByDRA = f.ServedByDRA.Add(q)
	default:
		f.NotAdvertised = f.NotAdvertised.Add(q)
	}
}

const (
	classCounted = iota
	classDRA
	classNotAdvertised
)

// Reserve computes every node's reservations and the cluster's accelerator
// totals. Pods count when bound to a node in the read and not terminal (a
// terminating pod counts until it is terminal).
func Reserve(nodes []*corev1.Node, pods []*corev1.Pod) Result {
	states := map[string]*nodeState{}
	for _, n := range nodes {
		s := &nodeState{node: n, advertised: map[string]*Figures{}, tracked: map[string]*Figures{}}
		for k := range n.Status.Allocatable {
			if IsExtended(k) {
				z := zeroFigures()
				s.advertised[string(k)] = &z
			}
		}
		states[n.Name] = s
	}
	for _, p := range pods {
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		s := states[p.Spec.NodeName]
		if s == nil || !requestsExtended(p) {
			continue
		}
		reservePod(s, p)
	}
	var res Result
	for _, n := range nodes {
		if row, ok := rowOf(states[n.Name]); ok {
			res.Rows = append(res.Rows, row)
		}
	}
	res.Rows, res.RowsDropped = boundRows(res.Rows)
	res.Totals, res.TotalsOmitted = totals(nodes, states)
	return res
}

// requestsExtended reports whether any container or the overhead names an
// extended resource: most pods request only cpu and memory, and skip the
// per-class aggregation (S11a §8, TestLoad).
func requestsExtended(p *corev1.Pod) bool {
	for _, cs := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
		for i := range cs {
			for name := range cs[i].Resources.Requests {
				if IsDRAExtended(name) {
					return true
				}
			}
		}
	}
	for name := range p.Spec.Overhead {
		if IsExtended(name) {
			return true
		}
	}
	return false
}

// reservePod classifies each (container, resource) pair before aggregating
// (the kubelet removes pairs before computing the pod's request), then
// aggregates each class by the init/sidecar formula, then adds overhead.
func reservePod(s *nodeState, p *corev1.Pod) {
	mapped := map[[2]string]bool{}
	if st := p.Status.ExtendedResourceClaimStatus; st != nil {
		for _, m := range st.RequestMappings {
			mapped[[2]string{m.ContainerName, m.ResourceName}] = true
		}
	}
	classOf := func(container string, name corev1.ResourceName) int {
		switch {
		case mapped[[2]string{container, string(name)}], strings.HasPrefix(string(name), DeviceClassPrefix):
			return classDRA
		case s.advertised[string(name)] != nil:
			return classCounted
		}
		return classNotAdvertised
	}
	// only the classes the pod's pairs fall in are aggregated
	var present [classNotAdvertised + 1]bool
	for _, cs := range [][]corev1.Container{p.Spec.Containers, p.Spec.InitContainers} {
		for i := range cs {
			for name := range cs[i].Resources.Requests {
				if IsDRAExtended(name) {
					present[classOf(cs[i].Name, name)] = true
				}
			}
		}
	}
	for class := classCounted; class <= classNotAdvertised; class++ {
		if !present[class] {
			continue
		}
		pick := func(c corev1.Container) qmap {
			m := qmap{}
			for name, q := range c.Resources.Requests {
				if IsDRAExtended(name) && classOf(c.Name, name) == class {
					m[string(name)] = qtyOf(q)
				}
			}
			return m
		}
		for name, q := range aggregate(p, pick) {
			s.add(name, class, q)
		}
	}
	// overhead names containers' pairs never: it counts against the node
	// when the node advertises the name, else it is not advertised
	for name, q := range p.Spec.Overhead {
		if !IsExtended(name) {
			continue
		}
		class := classNotAdvertised
		if s.advertised[string(name)] != nil {
			class = classCounted
		}
		s.add(string(name), class, qtyOf(q))
	}
}

type qmap map[string]Qty

func (m qmap) add(o qmap) {
	for k, v := range o {
		if cur, ok := m[k]; ok {
			m[k] = cur.Add(v)
		} else {
			m[k] = v
		}
	}
}

func (m qmap) maxWith(o qmap) {
	for k, v := range o {
		if cur, ok := m[k]; ok {
			m[k] = cur.max(v)
		} else {
			m[k] = v
		}
	}
}

func (m qmap) copy() qmap {
	out := qmap{}
	out.add(m)
	return out
}

// aggregate is component-helpers' PodRequests container aggregation:
// regular and sidecar containers summed; each regular init container plus
// the sidecars declared before it; the maximum of the two.
func aggregate(p *corev1.Pod, pick func(corev1.Container) qmap) qmap {
	reqs := qmap{}
	for _, c := range p.Spec.Containers {
		reqs.add(pick(c))
	}
	restartable, inits := qmap{}, qmap{}
	for _, c := range p.Spec.InitContainers {
		r := pick(c)
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			reqs.add(r)
			restartable.add(r)
			r = restartable.copy()
		} else {
			tmp := r.copy()
			tmp.add(restartable)
			r = tmp
		}
		inits.maxWith(r)
	}
	reqs.maxWith(inits)
	return reqs
}

// rowOf is a node's row: every name it advertises (accelerators first, then
// by name), then the other requested names by amount, then by name.
func rowOf(s *nodeState) (NodeRow, bool) {
	if len(s.advertised) == 0 && len(s.tracked) == 0 && !s.untracked {
		return NodeRow{}, false
	}
	row := NodeRow{Node: s.node.Name, UID: s.node.UID, Untracked: s.untracked}
	adv := make([]string, 0, len(s.advertised))
	for k := range s.advertised {
		adv = append(adv, k)
	}
	sortAccelFirst(adv)
	for _, k := range adv {
		row.Entries = append(row.Entries, Reserved{Name: k, Advertised: true, Figures: *s.advertised[k]})
	}
	other := make([]string, 0, len(s.tracked))
	for k := range s.tracked {
		other = append(other, k)
	}
	amount := func(k string) int64 {
		f := s.tracked[k]
		var v int64
		if f.ServedByDRA.OK {
			v += f.ServedByDRA.V
		}
		if f.NotAdvertised.OK && v <= 1<<62 {
			v += f.NotAdvertised.V
		}
		return v
	}
	sort.Slice(other, func(i, j int) bool {
		ai, aj := amount(other[i]), amount(other[j])
		if ai != aj {
			return ai > aj
		}
		return other[i] < other[j]
	})
	for _, k := range other {
		row.Entries = append(row.Entries, Reserved{Name: k, Figures: *s.tracked[k]})
	}
	if len(row.Entries) > MaxRowEntries {
		row.Omitted = len(row.Entries) - MaxRowEntries
		row.Entries = row.Entries[:MaxRowEntries]
	}
	return row, true
}

// RowSize estimates a row's encoded size for the per-cluster bound.
func RowSize(r NodeRow) int {
	n := 96 + len(r.Node) + len(r.UID)
	for _, e := range r.Entries {
		n += 112 + len(e.Name)
	}
	return n
}

// boundRows keeps rows within MaxRowsBytes: nodes advertising an
// accelerator first, then by name. The rest are dropped and counted.
func boundRows(rows []NodeRow) ([]NodeRow, int) {
	accel := func(r NodeRow) bool {
		for _, e := range r.Entries {
			if e.Advertised && Accelerator(e.Name) {
				return true
			}
		}
		return false
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ai, aj := accel(rows[i]), accel(rows[j])
		if ai != aj {
			return ai
		}
		return rows[i].Node < rows[j].Node
	})
	size := 0
	for i, r := range rows {
		size += RowSize(r)
		if size > MaxRowsBytes {
			return rows[:i], len(rows) - i
		}
	}
	return rows, 0
}

// totals sums each accelerator name over every node and every counted pair,
// before any cut.
func totals(nodes []*corev1.Node, states map[string]*nodeState) ([]Total, int) {
	byName := map[string]*Total{}
	for _, n := range nodes {
		for k, q := range n.Status.Capacity {
			if !IsExtended(k) || !Accelerator(string(k)) {
				continue
			}
			t := totalOf(byName, string(k))
			t.Capacity = t.Capacity.Add(qtyOf(q))
		}
		for k, q := range n.Status.Allocatable {
			if !IsExtended(k) || !Accelerator(string(k)) {
				continue
			}
			t := totalOf(byName, string(k))
			t.Allocatable = t.Allocatable.Add(qtyOf(q))
			if f := states[n.Name].advertised[string(k)]; f != nil {
				t.Requested = t.Requested.Add(f.Requested)
			}
		}
	}
	out := make([]Total, 0, len(byName))
	for _, t := range byName {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Allocatable, out[j].Allocatable
		if a.OK != b.OK {
			return a.OK
		}
		if a.V != b.V {
			return a.V > b.V
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > MaxTotals {
		return out[:MaxTotals], len(out) - MaxTotals
	}
	return out, 0
}

func totalOf(m map[string]*Total, name string) *Total {
	t := m[name]
	if t == nil {
		t = &Total{Name: name, Capacity: Known(0), Allocatable: Known(0), Requested: Known(0)}
		m[name] = t
	}
	return t
}

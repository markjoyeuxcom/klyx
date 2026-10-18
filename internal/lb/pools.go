package lb

import (
	"math/big"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/moomora/klyx/internal/health"
)

// Cilium's pool and Service conditions (lbipam.go 44-49).
const (
	CondPoolConflict     = "cilium.io/PoolConflict"
	CondIPsTotal         = "cilium.io/IPsTotal"
	CondIPsAvailable     = "cilium.io/IPsAvailable"
	CondIPsUsed          = "cilium.io/IPsUsed"
	CondRequestSatisfied = "cilium.io/IPAMRequestSatisfied"
)

// Read is how a part read one kind.
type Read int

const (
	ReadNone     Read = iota // not read, or not served
	ReadStale                // last-good evidence, not current
	ReadComplete             // live and complete
)

// Count is one exact count with its state: State is "" when Value holds
// it, else "missing", "malformed" or "stale" (for an earlier generation).
type Count struct {
	Value *big.Int
	State string
}

func (c Count) String() string {
	if c.Value == nil {
		return c.State
	}
	return c.Value.String()
}

// CiliumCounts are a pool's three counts, from their conditions' messages
// (S4 §5): their status is Unknown by design, the number is the message.
type CiliumCounts struct{ Total, Available, Used Count }

// Known reports whether all three are current and exact.
func (c CiliumCounts) Known() bool {
	return c.Total.State == "" && c.Available.State == "" && c.Used.State == ""
}

// CountsOf parses a Cilium pool's counts.
func CountsOf(p *CiliumPool) CiliumCounts {
	return CiliumCounts{Total: countOf(p, CondIPsTotal), Available: countOf(p, CondIPsAvailable), Used: countOf(p, CondIPsUsed)}
}

func countOf(p *CiliumPool, condType string) Count {
	c := p.cond(condType)
	if c == nil {
		return Count{State: "missing"}
	}
	if c.ObservedGeneration != p.Generation {
		return Count{State: "stale"}
	}
	return parseCount(c.Message)
}

// parseCount reads an exact non-negative integer: decimal digits only.
func parseCount(s string) Count {
	if s == "" || len(s) > 64 {
		return Count{State: "malformed"}
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return Count{State: "malformed"}
		}
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return Count{State: "malformed"}
	}
	return Count{Value: v}
}

func (p *CiliumPool) cond(t string) *Cond {
	for i := range p.Conds {
		if p.Conds[i].Type == t {
			return &p.Conds[i]
		}
	}
	return nil
}

// Conflict is the pool's PoolConflict condition and whether it is current
// (observed at the pool's generation); nil when absent.
func (p *CiliumPool) Conflict() (c *Cond, current bool) {
	c = p.cond(CondPoolConflict)
	return c, c != nil && c.ObservedGeneration == p.Generation
}

// CiliumPoolRule judges one Cilium pool (S4 §5, revision 2): each condition
// on its own freshness, and a current conflict before the counts.
func CiliumPoolRule(p *CiliumPool) health.NativeEval {
	if p.Disabled {
		return health.NativeEval{Role: health.Paused, Label: "Disabled"}
	}
	if c, current := p.Conflict(); current && c.Status == string(metav1.ConditionTrue) {
		e := health.NativeEval{Included: true, Role: health.Fault, Label: "Conflict",
			Reason: health.CapReason("Conflicts with another pool: disabled by Cilium", c.Message)}
		e.Cond = health.CondAt(c.Type, c.Status, c.At)
		return e
	}
	n := CountsOf(p)
	if !n.Known() {
		label := "Counts not reported yet"
		for _, c := range []Count{n.Total, n.Available, n.Used} {
			switch c.State {
			case "stale":
				label = "Counts for an earlier change"
			case "malformed":
				if label != "Counts for an earlier change" {
					label = "Counts unreadable"
				}
			}
		}
		return health.NativeEval{Role: health.Unknown, Label: label}
	}
	if n.Available.Value.Sign() == 0 && n.Total.Value.Sign() > 0 {
		return health.NativeEval{Included: true, Role: health.Degraded, Label: "Exhausted",
			Reason: health.CapReason("Exhausted", "0 of "+n.Total.Value.String()+" available")}
	}
	return health.NativeEval{Role: health.OK, Label: n.Available.Value.String() + " of " + n.Total.Value.String() + " available"}
}

// Instances are the MetalLB instance namespaces (S4 §5, revision 2): the
// namespace of a "controller" ConfigurationState, which MetalLB writes in
// its own, else a detected controller workload's, which a --namespace flag
// or METALLB_NAMESPACE can move (FromWorkload says so).
type Instances struct {
	Namespaces   map[string]bool
	FromWorkload map[string]bool
	// Known: the evidence was read (a ConfigurationState read or the
	// capability's workload read). Without it no namespace is excluded.
	Known bool
}

// Has reports whether ns is a known instance namespace.
func (in Instances) Has(ns string) bool { return in.Namespaces[ns] }

// InstancesOf derives the instance namespaces from the ConfigurationStates
// (with their read) and the detected controller workloads' namespaces
// (with whether that list was read).
func InstancesOf(states []*ConfigState, statesRead Read, workloads []string, workloadsRead bool) Instances {
	in := Instances{Namespaces: map[string]bool{}, FromWorkload: map[string]bool{}}
	for _, s := range states {
		if s.Role() == "controller" {
			in.Namespaces[s.Namespace] = true
		}
	}
	for _, ns := range workloads {
		if !in.Namespaces[ns] {
			in.Namespaces[ns], in.FromWorkload[ns] = true, true
		}
	}
	in.Known = statesRead == ReadComplete || workloadsRead
	return in
}

// Selection is how the advertisements in a pool's namespace select it.
type Selection struct {
	// By names the advertisements that prove selection, as "L2 name".
	By []string
	// Unresolved names advertisements whose selection of this pool could
	// not be evaluated (a cut list, a selector past the cap, labels cut).
	Unresolved []string
}

// Selects evaluates one advertisement's selection of a pool with MetalLB's
// own semantics (config.go 1225-1260): no names and no selectors selects
// every pool; otherwise a named pool, or one whose labels match any
// selector through metav1.LabelSelectorAsSelector. proven is a match from
// retained data; unresolved means it may select it through what was cut.
func Selects(a *Advertisement, p *MetalLBPool) (proven, unresolved bool) {
	if a.Namespace != p.Namespace {
		return false, false // MetalLB reads both only in its own namespace
	}
	if a.AllPools() {
		return true, false
	}
	for _, n := range a.Pools {
		if n == p.Name {
			return true, false
		}
	}
	for i := range a.Selectors {
		s, err := metav1.LabelSelectorAsSelector(&a.Selectors[i])
		if err != nil {
			unresolved = true // MetalLB refuses it too: the configuration is invalid
			continue
		}
		if s.Matches(labels.Set(p.Labels)) {
			if p.LabelsCut && !matchesOnRetained(&a.Selectors[i]) {
				unresolved = true
				continue
			}
			return true, false
		}
		if p.LabelsCut && len(a.Selectors[i].MatchExpressions)+len(a.Selectors[i].MatchLabels) > 0 {
			unresolved = true // a cut label could have matched
		}
	}
	if a.PoolsCut || a.SelectorsCut {
		unresolved = true
	}
	return false, unresolved
}

// matchesOnRetained: a match on retained labels proves selection only when
// no requirement could be falsified by a cut label (NotIn, DoesNotExist).
func matchesOnRetained(s *metav1.LabelSelector) bool {
	for _, r := range s.MatchExpressions {
		if r.Operator == metav1.LabelSelectorOpNotIn || r.Operator == metav1.LabelSelectorOpDoesNotExist {
			return false
		}
	}
	return true
}

// SelectionOf evaluates every advertisement against one pool.
func SelectionOf(ads []*Advertisement, p *MetalLBPool) Selection {
	var s Selection
	for _, a := range ads {
		proven, unresolved := Selects(a, p)
		switch {
		case proven:
			s.By = append(s.By, a.Kind+" "+a.Name)
		case unresolved:
			s.Unresolved = append(s.Unresolved, a.Kind+" "+a.Name)
		}
	}
	return s
}

// MetalLBPoolInput is a MetalLB pool's evidence, from one capture.
type MetalLBPoolInput struct {
	Pool      *MetalLBPool
	Instances Instances
	// Ads are the L2 and BGP advertisements read; AdsRead is the worse of
	// the two kinds' reads.
	Ads     []*Advertisement
	AdsRead Read
	// States are the pool's namespace's ConfigurationStates; StatesRead
	// their kind's read (ReadNone also when MetalLB predates the kind).
	States     []*ConfigState
	StatesRead Read
}

// The advertisement limit, always said (S4 §5).
const (
	LimitAdvertisement = "From the cluster's configuration objects, not the speakers' announcement state."
	LimitConfigState   = "MetalLB's configuration result is not reported; an invalid configuration would leave the speakers on an earlier one."
)

// InvalidStates are the namespace's Invalid ConfigurationStates.
func InvalidStates(states []*ConfigState, ns string) []*ConfigState {
	var out []*ConfigState
	for _, s := range states {
		if s.Namespace == ns && s.Result == "Invalid" {
			out = append(out, s)
		}
	}
	return out
}

// MetalLBPoolRule judges one MetalLB pool (S4 §5, revisions 2 and 3). The
// advertisement verdict is configuration evidence and comes before the
// counts, so a missing count never hides it (as built: the Cilium rule's
// revision 2 ordering applied here too).
func MetalLBPoolRule(in MetalLBPoolInput) health.NativeEval {
	p := in.Pool
	if in.Instances.Known && !in.Instances.Has(p.Namespace) {
		return health.NativeEval{Role: health.Unknown, Label: "No MetalLB instance here",
			Reason: health.CapReason("No MetalLB instance found in its namespace", "MetalLB reads pools only in its own")}
	}
	if bad := InvalidStates(in.States, p.Namespace); len(bad) > 0 {
		return health.NativeEval{Role: health.Unknown, Label: "Configuration invalid",
			Reason: health.CapReason("MetalLB's configuration is invalid", "it may still run an earlier valid one")}
	}
	if in.AdsRead != ReadComplete {
		return health.NativeEval{Role: health.Unknown, Label: "Advertisements unverified"}
	}
	sel := SelectionOf(in.Ads, p)
	if len(sel.By) == 0 {
		if len(sel.Unresolved) > 0 {
			return health.NativeEval{Role: health.Unknown, Label: "Selection unresolved",
				Reason: health.CapReason("An advertisement's selection could not be evaluated", strings.Join(sel.Unresolved, ", "))}
		}
		return health.NativeEval{Included: true, Role: health.Degraded, Label: "No advertisement",
			Reason: health.CapReason("No advertisement selects it", LimitAdvertisement)}
	}
	if !p.Status.Reported() {
		return health.NativeEval{Role: health.Unknown, Label: "Counts not reported"}
	}
	if exhausted(p.Status) {
		return health.NativeEval{Included: true, Role: health.Degraded, Label: "Exhausted",
			Reason: health.CapReason("Exhausted", "assigned and none available in every family it reports")}
	}
	return health.NativeEval{Role: health.OK, Label: availableLabel(p.Status)}
}

// exhausted: in every family the pool reports addresses for (assigned or
// available above zero), none is available, and some are assigned.
func exhausted(s PoolStatus) bool {
	type fam struct{ a, v *int64 }
	assigned := false
	for _, f := range []fam{{s.AssignedV4, s.AvailableV4}, {s.AssignedV6, s.AvailableV6}} {
		if f.a == nil || f.v == nil {
			if f.v != nil && *f.v > 0 {
				return false
			}
			continue
		}
		if *f.a+*f.v == 0 {
			continue // the pool has no addresses of this family
		}
		if *f.v > 0 {
			return false
		}
		assigned = assigned || *f.a > 0
	}
	return assigned
}

func availableLabel(s PoolStatus) string {
	n := new(big.Int)
	known := false
	for _, v := range []*int64{s.AvailableV4, s.AvailableV6} {
		if v != nil {
			n.Add(n, big.NewInt(*v)) // two int64 counts can overflow one
			known = true
		}
	}
	if !known {
		return "Available not reported"
	}
	return n.String() + " available"
}

// ConfigStateRule judges one ConfigurationState (S4 §5).
func ConfigStateRule(c *ConfigState) health.NativeEval {
	switch c.Result {
	case "Invalid":
		return health.NativeEval{Included: true, Role: health.Fault, Label: "Configuration invalid",
			Reason: health.CapReason("Configuration invalid", c.Error)}
	case "Valid":
		return health.NativeEval{Role: health.OK, Label: "Valid"}
	}
	return health.NativeEval{Role: health.Unknown, Label: "Not reported"}
}

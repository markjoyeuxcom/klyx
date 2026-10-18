package gwapi

import (
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/moomora/klyx/internal/health"
)

// ListenerSet is one ListenerSet as read (coverage S7 §4): a set of
// listeners merged into one parent Gateway's.
type ListenerSet struct {
	Namespace, Name, UID string
	Created              time.Time
	Generation           int64
	Parent               ParentRef
	Listeners            []Listener

	AcceptedCond, ProgrammedCond Cond
	// Stale: the spec changed and the controller has not observed it yet
	// (as GatewayRef's).
	Stale              bool
	ObservedGeneration int64
}

// ParentRef is a ListenerSet's spec.parentRef with the API's defaults
// applied: group gateway.networking.k8s.io, kind Gateway, and the
// ListenerSet's own namespace.
type ParentRef struct {
	Group, Kind, Namespace, Name string
}

// IsGateway reports whether the reference names a Gateway.
func (p ParentRef) IsGateway() bool { return p.Group == Group && p.Kind == "Gateway" }

// Key is the parent's namespace/name.
func (p ParentRef) Key() string { return p.Namespace + "/" + p.Name }

// ParseListenerSet maps a ListenerSet.
func ParseListenerSet(u *unstructured.Unstructured) ListenerSet {
	stale, gen, observed := gatewayStaleness(u)
	ls := ListenerSet{Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()), Created: u.GetCreationTimestamp().Time,
		Generation: gen, Stale: stale, ObservedGeneration: observed,
		Listeners:    parseListeners(u.Object),
		AcceptedCond: condOf(statusConds(u.Object), "Accepted"), ProgrammedCond: condOf(statusConds(u.Object), "Programmed")}
	p, _, _ := unstructured.NestedMap(u.Object, "spec", "parentRef")
	ls.Parent.Group, _ = p["group"].(string)
	ls.Parent.Kind, _ = p["kind"].(string)
	ls.Parent.Namespace, _ = p["namespace"].(string)
	ls.Parent.Name, _ = p["name"].(string)
	if _, set := p["group"]; !set {
		ls.Parent.Group = Group
	}
	if ls.Parent.Kind == "" {
		ls.Parent.Kind = "Gateway"
	}
	if ls.Parent.Namespace == "" {
		ls.Parent.Namespace = ls.Namespace
	}
	return ls
}

// ParentState is what Klyx's read establishes about a ListenerSet's parent
// Gateway (S7 §4, §8).
type ParentState string

const (
	// ParentFound: the parent Gateway is in the read.
	ParentFound ParentState = "found"
	// ParentNotFound: absent from a Live, complete, untruncated read - the
	// only read that proves absence.
	ParentNotFound ParentState = "not found"
	// ParentNotEstablished: absent from any other read (not served,
	// forbidden, checking, stale or truncated by the budget).
	ParentNotEstablished ParentState = "not established"
	// ParentUnsupported: the reference names a kind other than Gateway.
	ParentUnsupported ParentState = "unsupported"
)

// ParentLookup is a ListenerSet's parent as established, with the Gateway
// when found. Current: the Gateway is from a Live read, so its
// configuration is as the API serves it now; a parent found only in a
// stale read establishes no current claim about it. NotServed: the
// Gateways API is confirmed not served.
type ParentLookup struct {
	State     ParentState
	Gateway   *GatewayRef
	Current   bool
	NotServed bool
}

// ParentRead is how the Gateways were read (S7 §8, Codex's S7a
// implementation review finding 3): Absent, confirmed not served; Current,
// a Live read; Complete, nothing left out. Only a complete, current read
// proves a parent missing.
type ParentRead struct {
	Absent, Current, Complete bool
}

// LookupParent finds a ListenerSet's parent among the Gateways read.
func LookupParent(ls ListenerSet, gateways []GatewayRef, read ParentRead) ParentLookup {
	if !ls.Parent.IsGateway() {
		return ParentLookup{State: ParentUnsupported}
	}
	if read.Absent {
		return ParentLookup{State: ParentNotEstablished, NotServed: true}
	}
	for i := range gateways {
		if gateways[i].Namespace == ls.Parent.Namespace && gateways[i].Name == ls.Parent.Name {
			return ParentLookup{State: ParentFound, Gateway: &gateways[i], Current: read.Current}
		}
	}
	if read.Current && read.Complete {
		return ParentLookup{State: ParentNotFound}
	}
	return ParentLookup{State: ParentNotEstablished}
}

// ListenerSetRule is a ListenerSet's verdict (S7 §4). Its own conditions
// decide first, in the Gateway's order: Accepted False is a Fault named with
// its reason, then Programmed False, also a Fault; Unknown, pending, absent
// and stale conditions read as GatewayRule reads them. A failure its
// controller reports is judged whatever the parent's read. Then the parent:
// absent from a complete read is a Fault, and any read that cannot prove it
// leaves the ListenerSet Unknown. Healthy, its listeners' conditions apply
// as a Gateway's do (Degraded).
func ListenerSetRule(ls ListenerSet, parent ParentLookup) health.NativeEval {
	role, label, by, current := listenerSetRole(ls.AcceptedCond, ls.ProgrammedCond, ls.Generation)
	e := health.NativeEval{Included: role == health.Fault || role == health.Degraded, Role: role, Label: label}
	gatewayDated(&e, by, current, ls.AcceptedCond, ls.ProgrammedCond, ls.Generation)
	if role == health.Fault {
		return e
	}
	switch parent.State {
	case ParentNotFound:
		return health.NativeEval{Included: true, Role: health.Fault, Label: "parent Gateway not found"}
	case ParentUnsupported:
		if role != health.Degraded {
			return health.NativeEval{Role: health.Unknown, Label: "parent is a " + clipLabel(ls.Parent.Kind) + ", not a Gateway"}
		}
		return e
	case ParentNotEstablished:
		if role != health.Degraded {
			return health.NativeEval{Role: health.Unknown, Label: "parent Gateway not established"}
		}
		return e
	}
	if role != health.OK {
		return e
	}
	return withListenerIssues(e, ListenerIssues(ls.Listeners, ls.Generation))
}

// listenerSetRole is gatewayRole with a not-programmed ListenerSet a Fault,
// and each negative named with its reason.
func listenerSetRole(accepted, programmed Cond, generation int64) (health.Kind, string, gatewayBasis, bool) {
	role, label, by, current := gatewayRole(accepted, programmed, generation)
	switch {
	case by == byProgrammed && role == health.Degraded && current:
		role = health.Fault
	}
	if role == health.Fault || role == health.Degraded {
		c := accepted
		if by == byProgrammed {
			c = programmed
		}
		if c.Reason != "" {
			label += ": " + clipLabel(c.Reason)
		}
	}
	return role, label, by, current
}

// Allowance values.
const (
	AllowedYes = "yes"
	AllowedNo  = "no"
)

// Allowance is Klyx's own reading of a parent Gateway's allowedListeners
// for one ListenerSet (S7 §4): evidence beside the controller's Accepted
// condition, never its verdict.
type Allowance struct {
	// From is the allowance in force: None, Same, All or Selector, the
	// API's default (None) applied; as written when Klyx does not read it.
	From string
	// Allowed is AllowedYes, AllowedNo, or "" when it cannot be
	// established: the parent or the namespace's labels are not
	// established, or the selector cannot be read.
	Allowed string
	Text    string
	// Disagrees: the controller's current Accepted condition says
	// otherwise; Text says both, and the controller's stays the verdict.
	Disagrees bool
}

// NamespaceLabels is a namespace's labels as read: Known only when the
// namespace is in a complete, current read (S7 §8).
type NamespaceLabels struct {
	Known  bool
	Labels map[string]string
}

// ListenerSetAllowance evaluates the parent's allowedListeners against the
// ListenerSet's namespace, and compares it with the controller's current
// Accepted condition.
func ListenerSetAllowance(ls ListenerSet, parent ParentLookup, ns NamespaceLabels) Allowance {
	switch {
	case parent.State == ParentUnsupported:
		return Allowance{Text: "the parent is a " + clipLabel(ls.Parent.Kind) + ", not a Gateway"}
	case parent.NotServed:
		return Allowance{Text: "the parent Gateway is not established: Gateways are not served"}
	case parent.State != ParentFound || parent.Gateway == nil:
		return Allowance{Text: "the parent Gateway is " + string(parent.State)}
	}
	gw := parent.Gateway
	al := gw.AllowedListeners
	a := Allowance{From: al.From}
	if a.From == "" {
		a.From = "None"
	}
	if !parent.Current {
		// the parent's configuration is its last good read: no current
		// allowance, and so no disagreement, is established from it
		a.Text = "the Gateways read is not current: the parent's allowedListeners (" + clipLabel(a.From) + " when last read) is not established"
		return a
	}
	switch a.From {
	case "None":
		a.Allowed = AllowedNo
		if al.Set {
			a.Text = "the Gateway allows no ListenerSets (allowedListeners None)"
		} else {
			a.Text = "the Gateway sets no allowedListeners, so it allows no ListenerSets"
		}
	case "Same":
		a.Text = "the Gateway allows ListenerSets from its own namespace (Same)"
		a.Allowed = AllowedYes
		if ls.Namespace != gw.Namespace {
			a.Allowed = AllowedNo
			a.Text += "; this one is in " + ls.Namespace
		}
	case "All":
		a.Allowed, a.Text = AllowedYes, "the Gateway allows ListenerSets from every namespace (All)"
	case "Selector":
		a.Allowed, a.Text = selectorAllows(al, ls.Namespace, ns)
	default:
		a.Text = "allowedListeners from " + clipLabel(al.From) + " is not a value Klyx reads"
	}
	acc := condState(ls.AcceptedCond, ls.Generation)
	if acc.stale || a.Allowed == "" {
		return a
	}
	switch {
	case acc.status == "True" && a.Allowed == AllowedNo:
		a.Disagrees = true
		a.Text = "the controller accepted it although " + a.Text
	case acc.status == "False" && ls.AcceptedCond.Reason == "NotAllowed" && a.Allowed == AllowedYes:
		a.Disagrees = true
		a.Text = "the controller reported NotAllowed although " + a.Text
	}
	return a
}

func selectorAllows(al NamespaceAllowance, namespace string, ns NamespaceLabels) (string, string) {
	if al.SelectorInvalid || al.Selector == nil {
		return "", "the Gateway allows ListenerSets by namespace selector, but its selector cannot be read"
	}
	sel, err := metav1.LabelSelectorAsSelector(al.Selector)
	if err != nil {
		return "", "the Gateway allows ListenerSets by namespace selector, but its selector cannot be read"
	}
	shown := clipLabel(metav1.FormatLabelSelector(al.Selector))
	if !ns.Known {
		return "", "the Gateway allows ListenerSets by namespace selector (" + shown + "); the labels of namespace " + namespace + " are not established"
	}
	if sel.Matches(labels.Set(ns.Labels)) {
		return AllowedYes, "namespace " + namespace + " matches the Gateway's selector (" + shown + ")"
	}
	return AllowedNo, "namespace " + namespace + " does not match the Gateway's selector (" + shown + ")"
}

// SortByPrecedence orders ListenerSets as their parent merges them (S7
// §1): creation time, oldest first, then namespace/name. The parent's own
// listeners come before them all.
func SortByPrecedence(sets []ListenerSet) {
	sort.SliceStable(sets, func(i, j int) bool {
		if !sets[i].Created.Equal(sets[j].Created) {
			return sets[i].Created.Before(sets[j].Created)
		}
		if sets[i].Namespace != sets[j].Namespace {
			return sets[i].Namespace < sets[j].Namespace
		}
		return sets[i].Name < sets[j].Name
	})
}

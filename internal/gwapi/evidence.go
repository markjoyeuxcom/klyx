package gwapi

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// Coverage S7b §6: the evidence that explains a controller's status for the
// hard cases - cross-namespace references, listeners' allowedRoutes and the
// features a class declares. Each explains the status and never replaces
// it; a disagreement is said only on complete, current evidence.

// ---- ReferenceGrants -------------------------------------------------------

// GrantFrom and GrantTo are a ReferenceGrant's from and to entries.
type GrantFrom struct{ Group, Kind, Namespace string }
type GrantTo struct{ Group, Kind, Name string }

// ReferenceGrant is one ReferenceGrant: it lives in the namespace of the
// objects it lets others reference.
type ReferenceGrant struct {
	Namespace, Name string
	From            []GrantFrom
	To              []GrantTo
}

// ParseReferenceGrant reads a ReferenceGrant.
func ParseReferenceGrant(u *unstructured.Unstructured) ReferenceGrant {
	g := ReferenceGrant{Namespace: u.GetNamespace(), Name: u.GetName()}
	from, _, _ := unstructured.NestedSlice(u.Object, "spec", "from")
	for _, x := range from {
		if m, ok := x.(map[string]any); ok {
			f := GrantFrom{}
			f.Group, _ = m["group"].(string)
			f.Kind, _ = m["kind"].(string)
			f.Namespace, _ = m["namespace"].(string)
			g.From = append(g.From, f)
		}
	}
	to, _, _ := unstructured.NestedSlice(u.Object, "spec", "to")
	for _, x := range to {
		if m, ok := x.(map[string]any); ok {
			t := GrantTo{}
			t.Group, _ = m["group"].(string)
			t.Kind, _ = m["kind"].(string)
			t.Name, _ = m["name"].(string)
			g.To = append(g.To, t)
		}
	}
	return g
}

// GrantsRead is the ReferenceGrants read, with how it was read.
type GrantsRead struct {
	Grants []ReferenceGrant
	Read   ParentRead
}

// GrantEvidence is whether a cross-namespace reference is granted: Allowed
// "yes" (By names the grant, from a current read), "no" (only from a
// complete, current read), or "" when the grants are not established - a
// grant retained from a read that is not current is last observed.
type GrantEvidence struct {
	Allowed string
	By      string
	Text    string
}

// GrantFor finds the ReferenceGrant letting an object (from) reference
// another (to) across namespaces (S7 §6): a grant in the target's namespace
// whose from names the referrer's group, kind and namespace, and whose to
// names the target's group and kind, and its name when set. A grant to one
// kind never covers another: a Gateway's grant is not a ListenerSet's.
func GrantFor(gr GrantsRead, from GrantFrom, toNS string, to GrantTo) GrantEvidence {
	what := fmt.Sprintf("%s %s/%s from %s %s", to.Kind, toNS, to.Name, from.Kind, from.Namespace)
	if gr.Read.Absent {
		return GrantEvidence{Text: what + ": ReferenceGrants are not served"}
	}
	for _, g := range gr.Grants {
		if g.Namespace != toNS || !grantsFrom(g, from) {
			continue
		}
		for _, t := range g.To {
			if t.Group == to.Group && t.Kind == to.Kind && (t.Name == "" || t.Name == to.Name) {
				by := g.Namespace + "/" + g.Name
				if !gr.Read.Current {
					return GrantEvidence{By: by, Text: what + ": ReferenceGrant " + by + " allowed it when last read; the ReferenceGrants read is not current"}
				}
				return GrantEvidence{Allowed: AllowedYes, By: by, Text: what + ": granted by ReferenceGrant " + by}
			}
		}
	}
	if gr.Read.Current && gr.Read.Complete {
		return GrantEvidence{Allowed: AllowedNo, Text: what + ": no ReferenceGrant in " + toNS + " allows it"}
	}
	return GrantEvidence{Text: what + ": the ReferenceGrants read is not complete and current"}
}

func grantsFrom(g ReferenceGrant, from GrantFrom) bool {
	for _, f := range g.From {
		if f.Group == from.Group && f.Kind == from.Kind && f.Namespace == from.Namespace {
			return true
		}
	}
	return false
}

// BackendGrant is one cross-namespace backend's grant evidence, by its rule
// and its place in the rule.
type BackendGrant struct {
	Rule, Index int
	Backend     string // namespace/name
	GrantEvidence
}

// BackendGrants is the grant evidence of every backend in another namespace
// than the route's (S7 §6).
func (r Route) BackendGrants(gr GrantsRead) []BackendGrant {
	var out []BackendGrant
	from := GrantFrom{Group: Group, Kind: r.Kind, Namespace: r.Namespace}
	for _, rule := range r.Rules {
		for i, b := range rule.Backends {
			if b.Namespace == r.Namespace {
				continue
			}
			ev := GrantFor(gr, from, b.Namespace, GrantTo{Group: b.Group, Kind: b.Kind, Name: b.Name})
			out = append(out, BackendGrant{Rule: rule.Index, Index: i, Backend: b.Namespace + "/" + b.Name, GrantEvidence: ev})
		}
	}
	return out
}

// CertificateGrants is the grant evidence of every listener certificateRef
// in another namespace than the listener's owner - a Gateway, or a
// ListenerSet, whose grants are its own (S7 §1, §6).
func CertificateGrants(ownerKind, ownerNS string, listeners []Listener, gr GrantsRead) []string {
	var out []string
	from := GrantFrom{Group: Group, Kind: ownerKind, Namespace: ownerNS}
	for _, l := range listeners {
		for _, c := range l.CertificateRefs {
			if c.Namespace == ownerNS {
				continue
			}
			ev := GrantFor(gr, from, c.Namespace, GrantTo{Group: c.Group, Kind: c.Kind, Name: c.Name})
			out = append(out, "listener "+l.Name+": "+ev.Text)
		}
	}
	return out
}

// ---- allowedRoutes ---------------------------------------------------------

// conventionalKinds are the route kinds a listener protocol conventionally
// admits (S7 §6): said as a convention, because the mapping is the
// implementation's.
var conventionalKinds = map[string][]string{
	"HTTP": {KindHTTPRoute, KindGRPCRoute}, "HTTPS": {KindHTTPRoute, KindGRPCRoute},
	"TLS": {KindTLSRoute}, "TCP": {KindTCPRoute}, "UDP": {KindUDPRoute},
}

// listenerKinds are the kinds a listener admits and where that list came
// from: its controller's supportedKinds, its declared allowedRoutes.kinds,
// or the protocol's convention. A kind is its group and kind, the group's
// default applied at parsing (Codex's S7b review, finding 5).
func listenerKinds(l Listener) ([]RouteKind, string) {
	if l.Status != nil && len(l.Status.SupportedKinds) > 0 {
		return l.Status.SupportedKinds, "its controller reports"
	}
	if l.AllowedRoutes != nil && len(l.AllowedRoutes.Kinds) > 0 {
		return l.AllowedRoutes.Kinds, "it declares"
	}
	var out []RouteKind
	for _, k := range conventionalKinds[l.Protocol] {
		out = append(out, RouteKind{Group: Group, Kind: k})
	}
	return out, "its protocol " + l.Protocol + " conventionally admits"
}

// kindNames names kinds as the listener lists them: a Gateway API kind by
// its kind, another group's with its group, and the core group's as core.
func kindNames(ks []RouteKind) []string {
	out := make([]string, 0, len(ks))
	for _, k := range ks {
		switch k.Group {
		case Group:
			out = append(out, clipLabel(k.Kind))
		case "":
			out = append(out, "core/"+clipLabel(k.Kind))
		default:
			out = append(out, clipLabel(k.Group)+"/"+clipLabel(k.Kind))
		}
	}
	return out
}

// RouteAllowance is Klyx's own reading of the target listeners'
// allowedRoutes for one attachment (S7 §6): evidence, never the verdict.
type RouteAllowance struct {
	Allowed string // yes, no, or "" when not established
	Text    string
}

// targetListeners are the parent listeners an attachment targets: the named
// section, the listeners on its port, or every one.
func targetListeners(ref RouteParentRef, ls []Listener) []Listener {
	var out []Listener
	for _, l := range ls {
		if ref.SectionName != "" && l.Name != ref.SectionName {
			continue
		}
		if ref.Port != 0 && l.Port != ref.Port {
			continue
		}
		out = append(out, l)
	}
	return out
}

// RouteAllowedBy evaluates the target listeners' allowedRoutes for a route
// (S7 §6): namespaces Same (the listener's owner's), All, or Selector over
// the route namespace's labels (known only from a complete, current read);
// kinds from supportedKinds, the declared kinds, or the protocol's
// convention. Allowed "yes" when one target listener admits the route.
func RouteAllowedBy(routeKind, routeNS string, ae AttachmentEval, ns NamespaceLabels) RouteAllowance {
	p := ae.Parent
	if p.State != ParentFound {
		return RouteAllowance{Text: "the parent is " + string(p.State)}
	}
	if !p.Current {
		return RouteAllowance{Text: "the parent's listeners are last observed: " + p.ControllerWhy}
	}
	owner := ae.Ref.Namespace
	targets := targetListeners(ae.Ref, p.Listeners)
	if len(targets) == 0 {
		return RouteAllowance{Allowed: AllowedNo, Text: "no listener of the parent matches the reference"}
	}
	var reasons []string
	unknown := false
	for _, l := range targets {
		nsOK, nsText := routeNamespaceAllowed(l, owner, routeNS, ns)
		kinds, src := listenerKinds(l)
		kindOK := containsKind(kinds, routeKind)
		switch {
		case nsOK == AllowedYes && kindOK:
			return RouteAllowance{Allowed: AllowedYes, Text: "listener " + l.Name + " admits it: " + nsText + "; " + routeKind + " is among the kinds " + src}
		case nsOK == "":
			unknown = true
			reasons = append(reasons, "listener "+l.Name+": "+nsText)
		case !kindOK:
			reasons = append(reasons, "listener "+l.Name+": "+routeKind+" is not among the kinds "+src+" ("+strings.Join(kindNames(kinds), ", ")+")")
		default:
			reasons = append(reasons, "listener "+l.Name+": "+nsText)
		}
	}
	if unknown {
		return RouteAllowance{Text: strings.Join(reasons, "; ")}
	}
	return RouteAllowance{Allowed: AllowedNo, Text: strings.Join(reasons, "; ")}
}

// containsKind reports whether the Gateway API's kind is among kinds, by
// group and kind.
func containsKind(kinds []RouteKind, kind string) bool {
	for _, k := range kinds {
		if k.Group == Group && k.Kind == kind {
			return true
		}
	}
	return false
}

// routeNamespaceAllowed evaluates a listener's allowedRoutes.namespaces for
// the route's namespace; Same is the listener owner's namespace.
func routeNamespaceAllowed(l Listener, owner, routeNS string, ns NamespaceLabels) (string, string) {
	from := "Same"
	var a NamespaceAllowance
	if l.AllowedRoutes != nil {
		a = l.AllowedRoutes.Namespaces
		if a.From != "" {
			from = a.From
		}
	}
	switch from {
	case "All":
		return AllowedYes, "routes from every namespace (All)"
	case "Same":
		if routeNS == owner {
			return AllowedYes, "routes from its own namespace (Same)"
		}
		return AllowedNo, "routes from " + owner + " only (Same); this one is in " + routeNS
	case "Selector":
		if a.SelectorInvalid || a.Selector == nil {
			return "", "its namespace selector cannot be read"
		}
		sel, err := metav1.LabelSelectorAsSelector(a.Selector)
		if err != nil {
			return "", "its namespace selector cannot be read"
		}
		shown := clipLabel(metav1.FormatLabelSelector(a.Selector))
		if !ns.Known {
			return "", "routes by namespace selector (" + shown + "); the labels of namespace " + routeNS + " are not established"
		}
		if sel.Matches(labels.Set(ns.Labels)) {
			return AllowedYes, "namespace " + routeNS + " matches its selector (" + shown + ")"
		}
		return AllowedNo, "namespace " + routeNS + " does not match its selector (" + shown + ")"
	}
	return "", "allowedRoutes from " + clipLabel(from) + " is not a value Klyx reads"
}

// ---- supported features ----------------------------------------------------

// FeatureNote says when a class's controller lists features but not the one
// named (S7 §6). A class listing no features says nothing.
func FeatureNote(class *GatewayClass, feature string) string {
	if class == nil || !class.FeaturesReported {
		return ""
	}
	for _, f := range class.SupportedFeatures {
		if f == feature {
			return ""
		}
	}
	return "the class's controller does not declare support for " + feature
}

// ClassOf finds a class by name among those read; a class from a read that
// is not current is last observed, and establishes nothing.
func (ix RouteIndex) ClassOf(name string) *GatewayClass {
	if !ix.ClassesRead.Current {
		return nil
	}
	for i := range ix.Classes {
		if ix.Classes[i].Name == name {
			return &ix.Classes[i]
		}
	}
	return nil
}

// ---- the attachment's evidence ---------------------------------------------

// AttachmentEvidence is what explains one attachment's status (S7 §6), with
// the disagreements Klyx's own readings have with it.
type AttachmentEvidence struct {
	Allowance     RouteAllowance
	Grants        []BackendGrant
	Features      []string
	Disagreements []string
}

// Evidence gathers an attachment's evidence. Disagreements are said only
// where Klyx's reading is established: a route the controller refused for
// its listeners (NotAllowedByListeners, NoMatchingParent) that Klyx reads as
// admitted; ResolvedRefs True with a backend no ReferenceGrant allows (on a
// complete, current grants read); RefNotPermitted with every cross-namespace
// backend granted.
func Evidence(r Route, ae AttachmentEval, ix RouteIndex, gr GrantsRead, ns NamespaceLabels) AttachmentEvidence {
	ev := AttachmentEvidence{Allowance: RouteAllowedBy(r.Kind, r.Namespace, ae, ns), Grants: r.BackendGrants(gr)}
	if ae.Parent.Gateway != nil && ae.Parent.Current {
		class := ix.ClassOf(ae.Parent.Gateway.ClassName)
		for _, f := range []string{r.Kind, map[bool]string{true: "ListenerSet"}[ae.Ref.IsListenerSet()]} {
			if n := FeatureNote(class, f); f != "" && n != "" {
				ev.Features = append(ev.Features, n)
			}
		}
	}
	if st := ae.Status; st != nil && !condState(st.Accepted, r.Generation).stale {
		reason := st.Accepted.Reason
		if st.Accepted.Status == "False" && (reason == "NotAllowedByListeners" || reason == "NoMatchingParent") && ev.Allowance.Allowed == AllowedYes {
			ev.Disagreements = append(ev.Disagreements, "the controller reported "+reason+" although "+ev.Allowance.Text)
		}
	}
	if st := ae.Status; st != nil && !condState(st.ResolvedRefs, r.Generation).stale {
		var denied, granted int
		for _, g := range ev.Grants {
			switch g.Allowed {
			case AllowedNo:
				denied++
			case AllowedYes:
				granted++
			}
		}
		switch {
		case st.ResolvedRefs.Status == "True" && denied > 0:
			ev.Disagreements = append(ev.Disagreements, fmt.Sprintf("ResolvedRefs is True, but no ReferenceGrant allows %d cross-namespace backend%s", denied, map[bool]string{true: "s"}[denied > 1]))
		case st.ResolvedRefs.Status == "False" && st.ResolvedRefs.Reason == "RefNotPermitted" && len(ev.Grants) > 0 && granted == len(ev.Grants):
			ev.Disagreements = append(ev.Disagreements, "the controller reported RefNotPermitted although a ReferenceGrant allows every cross-namespace backend")
		}
	}
	return ev
}

// ListenerFeatureNotes says, for a listener owner's listeners, what its
// class does not declare (S7 §6): a TLS listener terminating without
// "TLSRouteModeTerminate".
func ListenerFeatureNotes(class *GatewayClass, ls []Listener) []string {
	var out []string
	for _, l := range ls {
		if l.Protocol == "TLS" && (l.TLSMode == "" || l.TLSMode == "Terminate") {
			if n := FeatureNote(class, "TLSRouteModeTerminate"); n != "" {
				out = append(out, "listener "+l.Name+": "+n)
			}
		}
	}
	return out
}

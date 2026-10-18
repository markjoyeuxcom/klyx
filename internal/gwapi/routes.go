package gwapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

// Coverage S7b §5: routes of every standard kind, each attachment - one
// parentRef - kept separately.

// Route kinds.
const (
	KindHTTPRoute = "HTTPRoute"
	KindGRPCRoute = "GRPCRoute"
	KindTLSRoute  = "TLSRoute"
	KindTCPRoute  = "TCPRoute"
	KindUDPRoute  = "UDPRoute"
)

// routeKinds are the route kinds in presentation order, with their
// resources.
var routeKinds = []struct{ Kind, Resource string }{
	{KindHTTPRoute, "httproutes"}, {KindGRPCRoute, "grpcroutes"}, {KindTLSRoute, "tlsroutes"},
	{KindTCPRoute, "tcproutes"}, {KindUDPRoute, "udproutes"},
}

// RouteKinds returns the route kinds in presentation order.
func RouteKinds() []string {
	out := make([]string, len(routeKinds))
	for i, k := range routeKinds {
		out[i] = k.Kind
	}
	return out
}

// RouteResource is a route kind's resource ("" for another kind).
func RouteResource(kind string) string {
	for _, k := range routeKinds {
		if k.Kind == kind {
			return k.Resource
		}
	}
	return ""
}

// RouteKindOf is a route resource's kind ("" for another resource).
func RouteKindOf(resource string) string {
	for _, k := range routeKinds {
		if k.Resource == resource {
			return k.Kind
		}
	}
	return ""
}

// Transport is the protocol a route kind's backends are reached over (S7
// §5): UDP for UDPRoute, TCP for every other kind.
func Transport(kind string) string {
	if kind == KindUDPRoute {
		return "UDP"
	}
	return "TCP"
}

// L4 reports whether a route kind matches nothing (TCPRoute and UDPRoute) or
// only by SNI (TLSRoute): its rules are backends alone.
func L4(kind string) bool {
	return kind == KindTLSRoute || kind == KindTCPRoute || kind == KindUDPRoute
}

// RouteParentRef is one parentRef with the API's defaults applied: group
// gateway.networking.k8s.io unless written (an empty group is the core
// group), kind Gateway, the route's namespace; section and port as written
// ("" and 0 when unset). Its Key is the attachment's identity (S7 §5): two
// references differing only by section or by port are two attachments.
type RouteParentRef struct {
	Group, Kind, Namespace, Name, SectionName string
	Port                                      int32
}

// Key is the canonical attachment identity, every field kept.
func (p RouteParentRef) Key() string {
	return p.Group + "/" + p.Kind + "/" + p.Namespace + "/" + p.Name + "#" + p.SectionName + ":" + strconv.Itoa(int(p.Port))
}

// Label names the attachment for display: the parent, and its section or
// port when set ("infra/eg · https", "infra/eg · :8443").
func (p RouteParentRef) Label() string {
	s := p.Namespace + "/" + p.Name
	if p.Kind != "Gateway" {
		s = p.Kind + " " + s
	}
	switch {
	case p.SectionName != "" && p.Port != 0:
		s += " · " + p.SectionName + ":" + strconv.Itoa(int(p.Port))
	case p.SectionName != "":
		s += " · " + p.SectionName
	case p.Port != 0:
		s += " · :" + strconv.Itoa(int(p.Port))
	}
	return s
}

// IsGateway and IsListenerSet name the parent kinds Klyx reads.
func (p RouteParentRef) IsGateway() bool     { return p.Group == Group && p.Kind == "Gateway" }
func (p RouteParentRef) IsListenerSet() bool { return p.Group == Group && p.Kind == "ListenerSet" }

// parseParentRef reads one parentRef (or a status entry's parentRef) with
// the defaults applied against the route's namespace.
func parseParentRef(m map[string]any, routeNS string) RouteParentRef {
	p := RouteParentRef{Group: Group, Kind: "Gateway", Namespace: routeNS}
	if g, ok := m["group"].(string); ok {
		p.Group = g
	}
	if k, _ := m["kind"].(string); k != "" {
		p.Kind = k
	}
	if ns, _ := m["namespace"].(string); ns != "" {
		p.Namespace = ns
	}
	p.Name, _ = m["name"].(string)
	p.SectionName, _ = m["sectionName"].(string)
	if n, ok, _ := unstructured.NestedNumberAsFloat64(m, "port"); ok {
		p.Port = int32(n)
	}
	return p
}

// ParentStatus is one status.parents entry: the reference it reports on,
// the controller that wrote it, and its conditions.
type ParentStatus struct {
	Ref            RouteParentRef
	ControllerName string
	Accepted       Cond
	ResolvedRefs   Cond
	Conditions     []TypedCond
}

// RouteRuleSpec is one spec.rules entry as returned (every one is kept,
// whatever the version's schema allows): its name, its matches by kind,
// and its backends.
type RouteRuleSpec struct {
	Index    int
	Name     string
	Matches  []Match // capped for display (MatchCount is the whole)
	Backends []Backend
	// MatchCount is how many matches the rule has; Matches may hold fewer.
	MatchCount int
}

// Route is one route of any kind as read (S7 §5).
type Route struct {
	Kind, Namespace, Name, UID string
	APIVersion                 string
	Generation                 int64
	// Hostnames are capped for display; HostnameCount is the whole.
	Hostnames     []string
	HostnameCount int
	ParentRefs    []RouteParentRef
	Rules         []RouteRuleSpec
	Parents       []ParentStatus
	// Fingerprints identify each rule, by index, taken over the whole rule
	// before anything is capped.
	Fingerprints []string
}

// Display caps (S7 §8): identity and comparison facts are kept whole.
const (
	maxHostnamesShown = 16
	maxMatchesShown   = 8
)

// ParseRoute reads a route of the given kind.
func ParseRoute(kind string, u *unstructured.Unstructured) Route {
	ns := u.GetNamespace()
	r := Route{Kind: kind, Namespace: ns, Name: u.GetName(), UID: string(u.GetUID()), APIVersion: u.GetAPIVersion(), Generation: u.GetGeneration(),
		Fingerprints: KindRuleFingerprints(kind, u)}
	hosts, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "hostnames")
	r.HostnameCount = len(hosts)
	if len(hosts) > maxHostnamesShown {
		hosts = hosts[:maxHostnamesShown]
	}
	r.Hostnames = hosts
	refs, _, _ := unstructured.NestedSlice(u.Object, "spec", "parentRefs")
	for _, x := range refs {
		if m, ok := x.(map[string]any); ok {
			r.ParentRefs = append(r.ParentRefs, parseParentRef(m, ns))
		}
	}
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
	for i, x := range rules {
		rm, ok := x.(map[string]any)
		if !ok {
			r.Rules = append(r.Rules, RouteRuleSpec{Index: i})
			continue
		}
		rule := RouteRuleSpec{Index: i}
		rule.Name, _ = rm["name"].(string)
		matches, _ := rm["matches"].([]any)
		rule.MatchCount = len(matches)
		for _, mt := range matches {
			if len(rule.Matches) == maxMatchesShown {
				break
			}
			if mm, ok := mt.(map[string]any); ok {
				rule.Matches = append(rule.Matches, parseMatch(kind, mm, i))
			}
		}
		brefs, _ := rm["backendRefs"].([]any)
		for _, b := range brefs {
			if bm, ok := b.(map[string]any); ok {
				rule.Backends = append(rule.Backends, parseBackend(bm, ns, i))
			}
		}
		r.Rules = append(r.Rules, rule)
	}
	parents, _, _ := unstructured.NestedSlice(u.Object, "status", "parents")
	for _, x := range parents {
		pm, ok := x.(map[string]any)
		if !ok {
			continue
		}
		ps := ParentStatus{}
		if ref, ok := pm["parentRef"].(map[string]any); ok {
			ps.Ref = parseParentRef(ref, ns)
		}
		ps.ControllerName, _ = pm["controllerName"].(string)
		conds, _ := pm["conditions"].([]any)
		ps.Conditions = typedConds(conds)
		ps.Accepted, ps.ResolvedRefs = condOf(conds, "Accepted"), condOf(conds, "ResolvedRefs")
		r.Parents = append(r.Parents, ps)
	}
	return r
}

// parseMatch reads one match by kind: HTTPRoute's path and method, or
// GRPCRoute's service and method. The L4 kinds have none.
func parseMatch(kind string, mm map[string]any, rule int) Match {
	m := Match{Rule: rule}
	switch kind {
	case KindGRPCRoute:
		if gm, ok := mm["method"].(map[string]any); ok {
			m.GRPCService, _ = gm["service"].(string)
			m.GRPCMethod, _ = gm["method"].(string)
		}
	default:
		m.Method, _ = mm["method"].(string)
		if pth, ok := mm["path"].(map[string]any); ok {
			m.PathType, _ = pth["type"].(string)
			m.PathValue, _ = pth["value"].(string)
		}
	}
	return m
}

// parseBackend reads one backendRef with the API's defaults: kind Service,
// the route's namespace.
func parseBackend(bm map[string]any, routeNS string, rule int) Backend {
	be := Backend{Rule: rule}
	be.Group, _ = bm["group"].(string)
	be.Name, _ = bm["name"].(string)
	be.Kind, _ = bm["kind"].(string)
	if be.Kind == "" {
		be.Kind = "Service"
	}
	be.Namespace, _ = bm["namespace"].(string)
	if be.Namespace == "" {
		be.Namespace = routeNS
	}
	if p, ok, _ := unstructured.NestedNumberAsFloat64(bm, "port"); ok {
		be.Port = int32(p)
	}
	if w, ok, _ := unstructured.NestedNumberAsFloat64(bm, "weight"); ok {
		be.Weight, be.WeightSet = int32(w), true
	}
	return be
}

// KindRuleFingerprints hashes each rule by index. An HTTPRoute's are
// RuleFingerprints, unchanged (its metric history joins on them); another
// kind's hash its kind with its matches (GRPCRoute) or its backends alone
// (the L4 kinds) (S7 §5).
func KindRuleFingerprints(kind string, u *unstructured.Unstructured) []string {
	if kind == KindHTTPRoute {
		return RuleFingerprints(u)
	}
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		var matches []any
		if kind == KindGRPCRoute {
			matches, _, _ = unstructured.NestedSlice(rm, "matches")
		}
		base := ruleFingerprint(map[string]any{"matches": matches, "backendRefs": rm["backendRefs"]}, u.GetNamespace())
		sum := sha256.Sum256([]byte(kind + "\x00" + base))
		out = append(out, hex.EncodeToString(sum[:]))
	}
	return out
}

// ---- parents and their controllers ----------------------------------------

// RouteIndex is what an attachment is judged against: the Gateways, the
// ListenerSets and the GatewayClasses read, each with how it was read.
type RouteIndex struct {
	Gateways         []GatewayRef
	GatewaysRead     ParentRead
	ListenerSets     []ListenerSet
	ListenerSetsRead ParentRead
	Classes          []GatewayClass
	ClassesRead      ParentRead
}

// AttachmentParent is an attachment's parent as established (S7 §5, §8),
// with the controller expected to judge it.
type AttachmentParent struct {
	State       ParentState
	Gateway     *GatewayRef  // the Gateway: the parent, or a ListenerSet's parent
	ListenerSet *ListenerSet // when the parent is a ListenerSet
	// Listeners are the parent's own: a Gateway's, or a ListenerSet's.
	Listeners []Listener
	// Controller is the expected controller (the class's controllerName);
	// ControllerWhy says why it is not established when it is "".
	Controller    string
	ControllerWhy string
}

func (ix RouteIndex) gateway(ns, name string) (*GatewayRef, ParentState) {
	if ix.GatewaysRead.Absent {
		return nil, ParentNotEstablished
	}
	for i := range ix.Gateways {
		if ix.Gateways[i].Namespace == ns && ix.Gateways[i].Name == name {
			return &ix.Gateways[i], ParentFound
		}
	}
	if ix.GatewaysRead.Current && ix.GatewaysRead.Complete {
		return nil, ParentNotFound
	}
	return nil, ParentNotEstablished
}

func (ix RouteIndex) class(name string) (string, string) {
	if ix.ClassesRead.Absent {
		return "", "GatewayClasses are not served"
	}
	for _, c := range ix.Classes {
		if c.Name == name {
			if c.ControllerName == "" {
				return "", "the GatewayClass " + name + " names no controller"
			}
			return c.ControllerName, ""
		}
	}
	if ix.ClassesRead.Current && ix.ClassesRead.Complete {
		return "", "the GatewayClass " + name + " does not exist"
	}
	return "", "the GatewayClass " + name + " is not in a complete, current read"
}

// Resolve establishes an attachment's parent and its expected controller.
func (ix RouteIndex) Resolve(ref RouteParentRef) AttachmentParent {
	var p AttachmentParent
	switch {
	case ref.IsGateway():
		p.Gateway, p.State = ix.gateway(ref.Namespace, ref.Name)
		if p.Gateway != nil {
			p.Listeners = p.Gateway.Listeners
		}
	case ref.IsListenerSet():
		p.State = ParentNotEstablished
		if !ix.ListenerSetsRead.Absent {
			for i := range ix.ListenerSets {
				if ix.ListenerSets[i].Namespace == ref.Namespace && ix.ListenerSets[i].Name == ref.Name {
					p.ListenerSet, p.State = &ix.ListenerSets[i], ParentFound
					p.Listeners = ix.ListenerSets[i].Listeners
				}
			}
			if p.ListenerSet == nil && ix.ListenerSetsRead.Current && ix.ListenerSetsRead.Complete {
				p.State = ParentNotFound
			}
		}
		if p.ListenerSet != nil && p.ListenerSet.Parent.IsGateway() {
			p.Gateway, _ = ix.gateway(p.ListenerSet.Parent.Namespace, p.ListenerSet.Parent.Name)
		}
	default:
		p.State = ParentUnsupported
		p.ControllerWhy = "the parent is a " + ref.Kind + ", which Klyx does not read"
		return p
	}
	switch {
	case p.Gateway == nil:
		p.ControllerWhy = "the parent Gateway is not established"
	case p.Gateway.ClassName == "":
		p.ControllerWhy = "the parent Gateway names no GatewayClass"
	default:
		p.Controller, p.ControllerWhy = ix.class(p.Gateway.ClassName)
	}
	return p
}

// StatusFor returns the status entries reporting on ref: the expected
// controller's (nil when it wrote none, or when the controller is not
// established), and every other entry.
func (r Route) StatusFor(ref RouteParentRef, controller string) (*ParentStatus, []ParentStatus) {
	var mine *ParentStatus
	var others []ParentStatus
	for i := range r.Parents {
		ps := r.Parents[i]
		if ps.Ref.Key() != ref.Key() {
			continue
		}
		if controller != "" && ps.ControllerName == controller && mine == nil {
			mine = &r.Parents[i]
			continue
		}
		others = append(others, ps)
	}
	return mine, others
}

// ---- backends --------------------------------------------------------------

// ServicePort is one port of a Service: its name, number and protocol
// (defaulting to TCP).
type ServicePort struct {
	Name     string
	Port     int32
	Protocol string
}

// SelectServicePort chooses the Service port a backendRef targets (S7 §5,
// revision 2): the port whose number is the backendRef's and whose protocol
// is the route's transport, never the first numeric match. A same-number
// port of another protocol is named in other.
func SelectServicePort(ports []ServicePort, port int32, transport string) (sp ServicePort, ok bool, other string) {
	var protos []string
	for _, p := range ports {
		proto := p.Protocol
		if proto == "" {
			proto = "TCP"
		}
		if p.Port != port {
			continue
		}
		if proto == transport {
			return ServicePort{Name: p.Name, Port: p.Port, Protocol: proto}, true, ""
		}
		protos = append(protos, proto)
	}
	if len(protos) > 0 {
		sort.Strings(protos)
		other = fmt.Sprintf("port %d exists for %s only", port, joinWords(protos))
	}
	return ServicePort{}, false, other
}

func joinWords(ws []string) string {
	switch len(ws) {
	case 0:
		return ""
	case 1:
		return ws[0]
	}
	out := ""
	for i, w := range ws {
		switch {
		case i == 0:
			out = w
		case i == len(ws)-1:
			out += " and " + w
		default:
			out += ", " + w
		}
	}
	return out
}

// sliceCarries reports whether an EndpointSlice carries the Service port: a
// slice port of the same name and protocol, an unnamed Service port matching
// the slice's unnamed port of that protocol.
func sliceCarries(s discoveryv1.EndpointSlice, sp ServicePort) bool {
	for _, p := range s.Ports {
		name := ""
		if p.Name != nil {
			name = *p.Name
		}
		proto := "TCP"
		if p.Protocol != nil && *p.Protocol != "" {
			proto = string(*p.Protocol)
		}
		if name == sp.Name && proto == sp.Protocol {
			return true
		}
	}
	return false
}

// CountEndpointsFor counts the endpoints of the slices that carry the
// Service port (S7b §5): only those slices count, so a UDP backend never
// reads available from TCP endpoints.
func CountEndpointsFor(slices []discoveryv1.EndpointSlice, sp ServicePort) EndpointCount {
	var keep []discoveryv1.EndpointSlice
	for _, s := range slices {
		if sliceCarries(s, sp) {
			keep = append(keep, s)
		}
	}
	return CountEndpoints(keep)
}

// BackendReads is what backends are resolved against: Services by
// namespace/name with their ports, the EndpointSlices of a Service, and
// how each was read. A negative claim needs a complete, current read
// (S7b §8); a Service read but not current proves no missing port.
type BackendReads struct {
	Service                           func(ns, name string) ([]ServicePort, bool)
	ServicesComplete, ServicesCurrent bool
	Slices                            func(ns, name string) []discoveryv1.EndpointSlice
	SlicesComplete, SlicesCurrent     bool
	// ServicesError names why Services could not be read at all ("" when
	// they were).
	ServicesError string
}

// ResolveBackend fills one backend's state, transport and endpoints (S7b
// §5): the Service, then its port by number and the route's transport, then
// the EndpointSlices carrying that port.
func ResolveBackend(b *Backend, kind string, rd BackendReads) {
	b.Transport = Transport(kind)
	if b.Kind != "Service" || b.Group != "" {
		b.State = BackendUnsupported
		return
	}
	ports, found := rd.Service(b.Namespace, b.Name)
	switch {
	case rd.ServicesError != "":
		b.State, b.Error = BackendUnreadable, rd.ServicesError
		return
	case !found && rd.ServicesComplete && rd.ServicesCurrent:
		b.State = BackendNotFound
		return
	case !found:
		b.State, b.Error = BackendUnreadable, "the Services read is not complete and current"
		return
	}
	sp, ok, other := SelectServicePort(ports, b.Port, b.Transport)
	if !ok {
		if !rd.ServicesCurrent {
			b.State, b.Error = BackendUnreadable, "the Service's ports are from a read that is not current"
			return
		}
		b.State = BackendNoPort
		b.Error = fmt.Sprintf("Service has no %s port %d", b.Transport, b.Port)
		if other != "" {
			b.Error += " (" + other + ")"
		}
		return
	}
	b.State = BackendResolved
	ep := EndpointCount{Error: "EndpointSlices could not be read"}
	if rd.SlicesComplete && rd.SlicesCurrent {
		ep = CountEndpointsFor(rd.Slices(b.Namespace, b.Name), sp)
	} else if rd.Slices != nil && !rd.SlicesComplete {
		ep.Error = "the EndpointSlices read is not complete and current"
	}
	b.Endpoints = &ep
}

// ---- attachments -----------------------------------------------------------

// AttachmentEval is one attachment judged (S7b §5): its parent as
// established, the expected controller's status entry, every other
// controller's entry for the same reference (shown, never judged), and the
// verdict.
type AttachmentEval struct {
	Ref    RouteParentRef
	Parent AttachmentParent
	Status *ParentStatus
	Others []ParentStatus
	Eval   health.NativeEval
}

// Read reports whether the attachment's parent is a kind Klyx reads: a
// parent of another kind (a Service, for mesh routing) is not an attachment
// of the Gateway task.
func (ae AttachmentEval) Read() bool { return ae.Parent.State != ParentUnsupported }

// ResolveBackends fills every backend of a route once, whatever its parents.
func ResolveBackends(r *Route, rd BackendReads) {
	for ri := range r.Rules {
		for bi := range r.Rules[ri].Backends {
			ResolveBackend(&r.Rules[ri].Backends[bi], r.Kind, rd)
		}
	}
}

// Backends are every rule's backends, in rule order.
func (r Route) Backends() []Backend {
	var out []Backend
	for _, rule := range r.Rules {
		out = append(out, rule.Backends...)
	}
	return out
}

// NodeFor is the route as one attachment sees it: the parent's status entry
// (the expected controller's), its section, and every backend - the shape
// RouteRule and RuleVerdicts judge.
func (r Route) NodeFor(ae AttachmentEval) RouteNode {
	rn := RouteNode{Namespace: r.Namespace, Name: r.Name, UID: r.UID, Hostnames: r.Hostnames, ParentSectionName: ae.Ref.SectionName,
		Backends: r.Backends(), RuleCount: len(r.Rules), Fingerprints: r.Fingerprints, Generation: r.Generation}
	for _, rule := range r.Rules {
		rn.Matches = append(rn.Matches, rule.Matches...)
	}
	if ae.Status != nil {
		rn.ParentFound = true
		rn.AcceptedCond, rn.ResolvedRefsCond = ae.Status.Accepted, ae.Status.ResolvedRefs
		rn.Accepted, rn.ResolvedRefs = rn.AcceptedCond.Status == "True", rn.ResolvedRefsCond.Status == "True"
	}
	return rn
}

// EvaluateAttachment judges one parentRef of a route whose backends are
// resolved (S7b §5). In order:
//   - the parent: absent from a complete, current read is a Fault; any read
//     that cannot prove it, Unknown; a ListenerSet its controller did not
//     accept is a Fault ("parent ListenerSet not accepted");
//   - the controller: the parent Gateway's class's controllerName; when it
//     cannot be established the attachment is Unknown, every entry listed
//     and none chosen;
//   - then RouteRule over the expected controller's entry: Accepted,
//     ResolvedRefs, the section's listener, each rule's backends.
func EvaluateAttachment(r Route, ref RouteParentRef, ix RouteIndex) AttachmentEval {
	ae := AttachmentEval{Ref: ref, Parent: ix.Resolve(ref)}
	ae.Status, ae.Others = r.StatusFor(ref, ae.Parent.Controller)
	ae.Eval = attachmentRule(r, ae)
	return ae
}

func attachmentRule(r Route, ae AttachmentEval) health.NativeEval {
	kind := ae.Ref.Kind
	switch ae.Parent.State {
	case ParentUnsupported:
		return health.NativeEval{Role: health.Unknown, Label: "parent is a " + clipLabel(kind) + ", which Klyx does not read"}
	case ParentNotFound:
		return health.NativeEval{Included: true, Role: health.Fault, Label: "parent " + kind + " not found"}
	case ParentNotEstablished:
		return health.NativeEval{Role: health.Unknown, Label: "parent " + kind + " not established"}
	}
	if ls := ae.Parent.ListenerSet; ls != nil {
		if a := condState(ls.AcceptedCond, ls.Generation); a.status == "False" && !a.stale {
			return health.NativeEval{Included: true, Role: health.Fault, Label: "parent ListenerSet not accepted",
				Reason: health.CapReason(ls.AcceptedCond.Reason, ls.AcceptedCond.Message)}
		}
	}
	if ae.Parent.Controller == "" {
		return health.NativeEval{Role: health.Unknown, Label: "the controller judging this parent is not established"}
	}
	e := RouteRule(r.NodeFor(ae), ae.Parent.Listeners)
	if e.Label == "no status for this Gateway" && kind != "Gateway" {
		e.Label = "no status for this " + kind
	}
	return e
}

// Attachments evaluates every parentRef of a route Klyx reads.
func (r Route) Attachments(ix RouteIndex) []AttachmentEval {
	var out []AttachmentEval
	for _, ref := range r.ParentRefs {
		if ae := EvaluateAttachment(r, ref, ix); ae.Read() {
			out = append(out, ae)
		}
	}
	return out
}

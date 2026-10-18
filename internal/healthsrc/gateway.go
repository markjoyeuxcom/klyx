package healthsrc

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/observe"
)

// Gateway API kinds and the Gateways task (1c addendum §1, §1.3). Routes
// are their own part: a healthy Gateway with a broken route lights the
// task. Service existence and EndpointSlices are the routes part's evidence:
// the shared network kinds (shared.go, C3), read through the gateway
// consumer, so they are this part's only where the Gateway API is served.
//
// Coverage S7a: every Gateway API kind is read at the first of its own
// candidate versions that is served (gwapi.Versions), GatewayClasses,
// Gateways and ListenerSets are typed projections within a budget, and
// ListenerSets are their own part.
const (
	KindGatewayClasses   = "gatewayclasses"
	KindGateways         = "gateways"
	KindHTTPRoutes       = "httproutes"
	KindGRPCRoutes       = "grpcroutes"
	KindTLSRoutes        = "tlsroutes"
	KindTCPRoutes        = "tcproutes"
	KindUDPRoutes        = "udproutes"
	KindReferenceGrants  = "referencegrants"
	KindListenerSets     = "listenersets"
	TaskGateway          = "gateway"
	gatewayGroup         = gwapi.Group
	coreKindProbeVersion = "v1" // core kinds: a fixed generation identity
)

// The S7 budgets (§8), in JSON-encoded projection bytes. The design's
// Gateway and ListenerSet figures assumed listeners without their status;
// measured at about 1.15 KiB a listener (TestProjectionSizing), they hold
// about 85 ten-listener Gateways and 200 two-listener ListenerSets.
const (
	budgetGatewayClasses = 64 << 10
	budgetGateways       = 1 << 20
	budgetListenerSets   = 512 << 10
	// S7b §8
	budgetHTTPRoutes      = 1 << 20
	budgetGRPCRoutes      = 256 << 10
	budgetTLSRoutes       = 256 << 10
	budgetTCPRoutes       = 128 << 10
	budgetUDPRoutes       = 128 << 10
	budgetReferenceGrants = 128 << 10
)

// Gateway returns the Gateway API kinds and the Gateways task.
func Gateway(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	kinds := []observe.KindSpec{
		gwKind(d, KindGatewayClasses, "cluster-scoped", budgetGatewayClasses),
		gwKind(d, KindGateways, "all namespaces", budgetGateways),
		gwKind(d, KindListenerSets, "all namespaces", budgetListenerSets),
		gwKind(d, KindHTTPRoutes, "all namespaces", budgetHTTPRoutes),
		gwKind(d, KindGRPCRoutes, "all namespaces", budgetGRPCRoutes),
		gwKind(d, KindTLSRoutes, "all namespaces", budgetTLSRoutes),
		gwKind(d, KindTCPRoutes, "all namespaces", budgetTCPRoutes),
		gwKind(d, KindUDPRoutes, "all namespaces", budgetUDPRoutes),
		gwKind(d, KindReferenceGrants, "all namespaces", budgetReferenceGrants),
	}
	c := d.Cluster
	domains := []observe.DomainSpec{{ID: TaskGateway, Parts: []observe.PartSpec{
		{ID: "gateways", Entity: "Gateways", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindGateways}, Deps: []string{KindGatewayClasses}, Evaluate: evalGateways(c), Ledger: true},
		{ID: "listenersets", Entity: "ListenerSets", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindListenerSets}, Deps: []string{KindGateways, KindGatewayClasses, KindNamespaces}, Evaluate: evalListenerSets(c), Ledger: true},
		{ID: "routes", Entity: "routes with a failing attachment", Scope: "routes attached to a Gateway or ListenerSet", Classifier: "native",
			Counted:  []string{KindHTTPRoutes, KindGRPCRoutes, KindTLSRoutes, KindTCPRoutes, KindUDPRoutes},
			Deps:     []string{KindGateways, KindGatewayClasses, KindListenerSets, KindServices, KindEndpointSlices, KindReferenceGrants, KindNamespaces},
			Evaluate: evalRoutes(c), Ledger: true, Consumer: ConsumerGateway},
	}}}
	return kinds, domains
}

// gwKind is one Gateway API kind: read at the first of its candidates that
// discovery serves (S7 §2); with a budget, a typed projection measured
// against it.
func gwKind(d NativeDeps, resource, scope string, budget int) observe.KindSpec {
	ks := crdKind(d, resource, gatewayGroup, resource, gwapi.Versions(resource)[0], scope)
	ks.Probe = func(ctx context.Context) (observe.Probe, error) {
		return d.servedAmong(ctx, gatewayGroup, resource, gwapi.Versions(resource))
	}
	if budget > 0 {
		ks.Project, ks.Budget, ks.MaxObject, ks.Size = gwapi.Project(resource), budget, gwapi.MaxObject(resource), gwapi.SizeOf
	}
	return ks
}

// gwObjects are a kind's typed projections.
func gwObjects(objs []any) []*gwapi.Object {
	out := make([]*gwapi.Object, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.(*gwapi.Object))
	}
	return out
}

// gwRef is a typed projection's ref.
func gwRef(o *gwapi.Object, resource string) refInput {
	return refInput{gvr: o.APIVersion + "/" + resource, ns: o.Namespace, name: o.Name, uid: string(o.UID), generation: o.Generation, source: resource}
}

func evalGateways(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[gwapi.GatewayRef]
		for _, o := range gwObjects(c.Objects[KindGateways]) {
			g := *o.Gateway
			it := nativeItem{gwapi.GatewayVerdict(g.AcceptedCond, g.ProgrammedCond, g.Generation, g.Listeners), gwRef(o, KindGateways)}
			items = append(items, it)
			rows = append(rows, nativeRow(it, g))
		}
		r := nativeResult(c.RoleCap, cluster, TaskGateway, "need attention", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

// ListenerSetRow is one ListenerSet with what its inspector shows beside its
// verdict (S7 §4): how its parent was established, Klyx's own reading of
// the parent's allowedListeners, and its place in the parent's merge.
type ListenerSetRow struct {
	Set         gwapi.ListenerSet
	ParentState gwapi.ParentState
	// ParentCurrent: a found parent is from a Live read of the Gateways.
	ParentCurrent bool
	Allowance     gwapi.Allowance
	// Precedence is the set's place among the ListenerSets naming the same
	// parent in this read (1 = first after the Gateway's own listeners),
	// Siblings how many the read holds, and PrecedenceComplete whether the
	// read holds every ListenerSet (a truncated read can miss one that
	// ranks earlier).
	Precedence, Siblings int
	PrecedenceComplete   bool
}

// evalListenerSets judges each ListenerSet (S7 §4) against its parent in
// the gateways model - absent proves "not found" only from a complete,
// current read of a served kind - and evaluates a current parent's
// allowedListeners against the namespace's labels, established only from a
// complete, current namespaces read.
func evalListenerSets(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		gws := gwObjects(c.Objects[KindGateways])
		gateways := make([]gwapi.GatewayRef, len(gws))
		for i, o := range gws {
			gateways[i] = *o.Gateway
		}
		nsLabels := map[string]map[string]string{}
		for _, o := range c.Objects[KindNamespaces] {
			m := o.(*metav1.PartialObjectMetadata)
			nsLabels[m.Name] = m.Labels
		}
		objs := gwObjects(c.Objects[KindListenerSets])
		sets := make([]gwapi.ListenerSet, len(objs))
		for i, o := range objs {
			sets[i] = *o.ListenerSet
		}
		// each parent's ListenerSets in merge precedence
		byParent := map[string][]gwapi.ListenerSet{}
		for _, ls := range sets {
			k := ls.Parent.Group + "/" + ls.Parent.Kind + "/" + ls.Parent.Key()
			byParent[k] = append(byParent[k], ls)
		}
		place := map[string][2]int{} // UID -> precedence, siblings
		for _, group := range byParent {
			gwapi.SortByPrecedence(group)
			for i, ls := range group {
				place[ls.UID] = [2]int{i + 1, len(group)}
			}
		}
		// a confirmed absence is complete but not current (Capture.Current)
		read := gwapi.ParentRead{Absent: c.Complete[KindGateways] && !c.Current[KindGateways],
			Current: c.Current[KindGateways], Complete: c.Complete[KindGateways]}
		var items []nativeItem
		var rows []Row[ListenerSetRow]
		for i, o := range objs {
			ls := sets[i]
			lk := gwapi.LookupParent(ls, gateways, read)
			labels, present := nsLabels[ls.Namespace]
			ns := gwapi.NamespaceLabels{Known: present && c.Complete[KindNamespaces] && c.Current[KindNamespaces], Labels: labels}
			it := nativeItem{gwapi.ListenerSetRule(ls, lk), gwRef(o, KindListenerSets)}
			items = append(items, it)
			pl := place[ls.UID]
			rows = append(rows, nativeRow(it, ListenerSetRow{Set: ls, ParentState: lk.State, ParentCurrent: lk.Current, Allowance: gwapi.ListenerSetAllowance(ls, lk, ns),
				Precedence: pl[0], Siblings: pl[1], PrecedenceComplete: c.Complete[KindListenerSets]}))
		}
		r := nativeResult(c.RoleCap, cluster, TaskGateway, "need attention", items)
		if c.Rows {
			r.Rows = rowSet(rows)
		}
		return r
	}
}

// evalRoutes evaluates every attachment - one parentRef - of every route of
// the five kinds (coverage S7b §5): its parent established from complete,
// current reads only, the expected controller's status entry judged, each
// backend resolved through its Service port by number and transport. Each
// route is counted once by UID with its worst attachment
// (observe.ReduceAttachments); a route none of whose parents is a kind Klyx
// reads is not counted.
func evalRoutes(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		read := func(kind string) gwapi.ParentRead {
			return gwapi.ParentRead{Absent: c.Complete[kind] && !c.Current[kind], Current: c.Current[kind], Complete: c.Complete[kind]}
		}
		ix := gwapi.RouteIndex{GatewaysRead: read(KindGateways), ListenerSetsRead: read(KindListenerSets), ClassesRead: read(KindGatewayClasses)}
		for _, o := range gwObjects(c.Objects[KindGateways]) {
			ix.Gateways = append(ix.Gateways, *o.Gateway)
		}
		for _, o := range gwObjects(c.Objects[KindListenerSets]) {
			ix.ListenerSets = append(ix.ListenerSets, *o.ListenerSet)
		}
		for _, o := range gwObjects(c.Objects[KindGatewayClasses]) {
			ix.Classes = append(ix.Classes, *o.Class)
		}
		grants := gwapi.GrantsRead{Read: read(KindReferenceGrants)}
		for _, o := range gwObjects(c.Objects[KindReferenceGrants]) {
			grants.Grants = append(grants.Grants, *o.Grant)
		}
		nsLabels := map[string]map[string]string{}
		for _, o := range c.Objects[KindNamespaces] {
			m := o.(*metav1.PartialObjectMetadata)
			nsLabels[m.Name] = m.Labels
		}
		labelsOf := func(ns string) gwapi.NamespaceLabels {
			l, present := nsLabels[ns]
			return gwapi.NamespaceLabels{Known: present && c.Complete[KindNamespaces] && c.Current[KindNamespaces], Labels: l}
		}
		services := map[string][]gwapi.ServicePort{}
		for _, o := range c.Objects[KindGatewayServices] {
			m := o.(*corev1.Service)
			var ports []gwapi.ServicePort
			for _, p := range m.Spec.Ports {
				ports = append(ports, gwapi.ServicePort{Name: p.Name, Port: p.Port, Protocol: string(p.Protocol)})
			}
			services[m.Namespace+"/"+m.Name] = ports
		}
		slices := map[string][]discoveryv1.EndpointSlice{}
		for _, o := range c.Objects[KindGatewayEndpoints] {
			es := o.(*discoveryv1.EndpointSlice)
			key := es.Namespace + "/" + es.Labels[discoveryv1.LabelServiceName]
			slices[key] = append(slices[key], *es)
		}
		rd := gwapi.BackendReads{
			Service: func(ns, name string) ([]gwapi.ServicePort, bool) {
				p, ok := services[ns+"/"+name]
				return p, ok
			},
			ServicesComplete: c.Complete[KindGatewayServices], ServicesCurrent: c.Current[KindGatewayServices],
			Slices:         func(ns, name string) []discoveryv1.EndpointSlice { return slices[ns+"/"+name] },
			SlicesComplete: c.Complete[KindGatewayEndpoints], SlicesCurrent: c.Current[KindGatewayEndpoints],
		}
		if _, read := c.Objects[KindGatewayServices]; !read && !c.Complete[KindGatewayServices] {
			rd.ServicesError = "Services could not be read"
		}

		var atts []observe.Attachment
		uidOf := map[string]*nativeRouteRef{}
		facts := map[string]*RouteRow{} // route UID -> its own facts and evidence
		for _, kind := range gwapi.RouteKinds() {
			resource := gwapi.RouteResource(kind)
			for _, o := range gwObjects(c.Objects[resource]) {
				r := o.Route.Clone() // backends are resolved on a copy: the model is never written
				gwapi.ResolveBackends(&r, rd)
				uid := string(o.UID)
				row := &RouteRow{APIVersion: o.APIVersion, Hostnames: r.Hostnames, HostnameCount: r.HostnameCount}
				for _, ae := range r.Attachments(ix) {
					e := ae.Eval
					a := observe.Attachment{UID: uid, Namespace: r.Namespace, Name: r.Name, Kind: kind, Parent: ae.Ref.Key(), ParentLabel: ae.Ref.Label(),
						ParentRef: &observe.ParentRef{Group: ae.Ref.Group, Kind: ae.Ref.Kind, Namespace: ae.Ref.Namespace, Name: ae.Ref.Name, SectionName: ae.Ref.SectionName, Port: ae.Ref.Port},
						Role:      e.Role.String(), Label: e.Label, Reason: e.Reason, Included: e.Included}
					if g := ae.Parent.Gateway; g != nil {
						a.Gateway = g.Namespace + "/" + g.Name
					}
					atts = append(atts, a)
					ev := gwapi.Evidence(r, ae, ix, grants, labelsOf(r.Namespace))
					ar := AttachmentRow{Parent: a.Parent, Controller: ae.Parent.Controller, ControllerWhy: ae.Parent.ControllerWhy,
						Allowance: ev.Allowance, Grants: ev.Grants, Features: ev.Features, Disagreements: ev.Disagreements}
					for _, other := range ae.Others {
						ar.Others = append(ar.Others, OtherStatus{Controller: other.ControllerName, Accepted: other.Accepted.Status, ResolvedRefs: other.ResolvedRefs.Status})
					}
					row.Evidence = append(row.Evidence, ar)
				}
				if len(row.Evidence) == 0 {
					continue
				}
				uidOf[uid] = &nativeRouteRef{ref: gwRef(o, resource), fingerprints: r.Fingerprints}
				facts[uid] = row
			}
		}
		reduced := observe.ReduceAttachments(atts)
		for i := range reduced {
			reduced[i].Rules = uidOf[reduced[i].UID].fingerprints
		}
		nc := observe.NativeCounts{Word: "with a failing attachment"}
		var incl []observe.Attached
		for _, a := range reduced {
			nc.Total++
			nc.Roles.Total++
			switch a.Role {
			case "fault":
				nc.Roles.Fault++
			case "degraded":
				nc.Roles.Degraded++
			case "progressing":
				nc.Roles.Progressing++
			case "paused":
				nc.Roles.Paused++
			case "ok":
				nc.Roles.OK++
			default:
				nc.Roles.Unknown++
			}
			if a.Included {
				nc.Included++
				incl = append(incl, a)
			}
		}
		var top []observe.Ref
		for _, a := range sortAttachedWorstFirst(incl) {
			if len(top) == maxTop {
				break
			}
			ref := uidOf[a.UID].ref
			// Parent names the Gateway the worst attachment lands on: what a
			// Top link opens
			top = append(top, observe.Ref{Cluster: cluster, GVR: ref.gvr, Namespace: a.Namespace, Name: a.Name, UID: a.UID,
				Parent: a.Gateway, Role: a.Role, Label: a.Label, Reason: a.Reason, Generation: ref.generation, Route: TaskGateway})
		}
		res := observe.PartResult{Native: &nc, Top: top}
		// One ledger role per counted route (slice 2b §1.1): the reduced
		// attachment's worst role, partitioned by the route's kind.
		for _, a := range reduced {
			ref := uidOf[a.UID].ref
			res.Roles = appendRole(res.Roles, &res.RolesCapped, c.RoleCap, observe.RoleEntry{Source: ref.source, GVR: ref.gvr, Key: a.UID,
				NS: a.Namespace, Name: a.Name, Role: a.Role, Label: a.Label, Reason: a.Reason})
		}
		if c.Rows {
			// The reduced attachment carries its captured evaluation; the row
			// adds the route's identity, its own facts and each attachment's
			// evidence, in the attachments' order.
			rows := make([]Row[RouteRow], 0, len(reduced))
			for _, a := range reduced {
				ref := uidOf[a.UID].ref
				f := *facts[a.UID]
				f.Attached = a
				byParent := map[string]AttachmentRow{}
				for _, ar := range f.Evidence {
					byParent[ar.Parent] = ar
				}
				f.Evidence = f.Evidence[:0:0]
				for _, at := range a.Attachments {
					f.Evidence = append(f.Evidence, byParent[at.Parent])
				}
				rows = append(rows, Row[RouteRow]{Ref: ref.rowRef(), Data: f})
			}
			res.Rows = rowSet(rows)
		}
		return res
	}
}

// RouteRow is one route of the routes part (S7b §5): its reduced attachment
// (the counted verdict), its own facts, and each attachment's evidence in
// the attachments' order.
type RouteRow struct {
	observe.Attached
	APIVersion    string
	Hostnames     []string
	HostnameCount int
	Evidence      []AttachmentRow
}

// AttachmentRow is one attachment's evidence (S7b §5, §6): the controller
// expected to judge it (or why it is not established), the entries other
// controllers wrote for the same reference (shown, never judged), and Klyx's
// own readings beside the controller's status.
type AttachmentRow struct {
	Parent        string
	Controller    string
	ControllerWhy string
	Others        []OtherStatus
	Allowance     gwapi.RouteAllowance
	Grants        []gwapi.BackendGrant
	Features      []string
	Disagreements []string
}

// OtherStatus is another controller's status entry for the same reference.
type OtherStatus struct {
	Controller, Accepted, ResolvedRefs string
}

type nativeRouteRef struct {
	ref          refInput
	fingerprints []string
}

// sortAttachedWorstFirst orders counted routes for Top refs: role (verdict
// order), then namespace/name.
func sortAttachedWorstFirst(xs []observe.Attached) []observe.Attached {
	out := append([]observe.Attached(nil), xs...)
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := observe.RoleRank(out[i].Role), observe.RoleRank(out[j].Role); ri != rj {
			return ri < rj
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

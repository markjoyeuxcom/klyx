package healthsrc

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/observe"
)

// Gateway API kinds and the Gateways task (1c addendum §1, §1.3). Routes
// are their own part: a healthy Gateway with a broken route lights the
// task. Service existence and EndpointSlices are the routes part's evidence:
// the shared network kinds (shared.go, C3), read through the gateway
// consumer, so they are this part's only where the Gateway API is served.
const (
	KindGatewayClasses   = "gatewayclasses"
	KindGateways         = "gateways"
	KindHTTPRoutes       = "httproutes"
	TaskGateway          = "gateway"
	gatewayGroup         = "gateway.networking.k8s.io"
	coreKindProbeVersion = "v1" // core kinds: a fixed generation identity
)

// Gateway returns the Gateway API kinds and the Gateways task.
func Gateway(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	kinds := []observe.KindSpec{
		crdKind(d, KindGatewayClasses, gatewayGroup, "gatewayclasses", "v1", "cluster-scoped"),
		crdKind(d, KindGateways, gatewayGroup, "gateways", "v1", "all namespaces"),
		crdKind(d, KindHTTPRoutes, gatewayGroup, "httproutes", "v1", "all namespaces"),
	}
	c := d.Cluster
	domains := []observe.DomainSpec{{ID: TaskGateway, Parts: []observe.PartSpec{
		{ID: "gateways", Entity: "Gateways", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindGateways}, Deps: []string{KindGatewayClasses}, Evaluate: evalGateways(c), Ledger: true},
		{ID: "routes", Entity: "HTTPRoutes with a failing attachment", Scope: "routes attached to a Gateway", Classifier: "native",
			Counted: []string{KindHTTPRoutes}, Deps: []string{KindGateways, KindServices, KindEndpointSlices}, Evaluate: evalRoutes(c), Ledger: true,
			Consumer: ConsumerGateway},
	}}}
	return kinds, domains
}

func evalGateways(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []Row[gwapi.GatewayRef]
		for _, u := range unstructuredOf(c.Objects[KindGateways]) {
			g := gwapi.ParseGatewayRef(u)
			it := nativeItem{gwapi.GatewayRule(g.AcceptedCond, g.ProgrammedCond, g.Generation), refOf(u, KindGateways)}
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

// evalRoutes evaluates every route attachment (route x parent Gateway in the
// model), resolving each backend from the captured Service and EndpointSlice
// models exactly as the topology read does live, then counts each route
// once by UID with its worst attachment (observe.ReduceAttachments).
func evalRoutes(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		gateways := unstructuredOf(c.Objects[KindGateways])
		parents := make([]gwapi.GatewayRef, len(gateways))
		for i, u := range gateways {
			parents[i] = gwapi.ParseGatewayRef(u)
		}
		svcObjs, servicesRead := c.Objects[KindGatewayServices]
		services := map[string]bool{}
		for _, o := range svcObjs {
			m := o.(*corev1.Service)
			services[m.Namespace+"/"+m.Name] = true
		}
		sliceObjs, slicesRead := c.Objects[KindGatewayEndpoints]
		slices := map[string][]discoveryv1.EndpointSlice{}
		for _, o := range sliceObjs {
			es := o.(*discoveryv1.EndpointSlice)
			key := es.Namespace + "/" + es.Labels[discoveryv1.LabelServiceName]
			slices[key] = append(slices[key], *es)
		}
		resolve := func(rn *gwapi.RouteNode) {
			for bi := range rn.Backends {
				b := &rn.Backends[bi]
				switch {
				case b.Kind != "Service":
					b.State = gwapi.BackendUnsupported
				case !servicesRead:
					b.State, b.Error = gwapi.BackendUnreadable, "Services could not be read"
				case !services[b.Namespace+"/"+b.Name]:
					b.State = gwapi.BackendNotFound
				default:
					b.State = gwapi.BackendResolved
					ep := gwapi.EndpointCount{Error: "EndpointSlices could not be read"}
					if slicesRead {
						ep = gwapi.CountEndpoints(slices[b.Namespace+"/"+b.Name])
					}
					b.Endpoints = &ep
				}
			}
		}
		var atts []observe.Attachment
		uidOf := map[string]*nativeRouteRef{}
		rulesOf := map[string][]string{} // route UID -> its rule fingerprints
		for _, u := range unstructuredOf(c.Objects[KindHTTPRoutes]) {
			rulesOf[string(u.GetUID())] = gwapi.RuleFingerprints(u)
			for _, gw := range parents {
				rn, ok := gwapi.RouteForGateway(u, gw.Namespace, gw.Name)
				if !ok {
					continue
				}
				resolve(&rn)
				e := gwapi.RouteRule(rn, gw.Listeners)
				uid := string(u.GetUID())
				atts = append(atts, observe.Attachment{UID: uid, Namespace: u.GetNamespace(), Name: u.GetName(),
					Parent: gw.Namespace + "/" + gw.Name, Role: e.Role.String(), Label: e.Label, Reason: e.Reason, Included: e.Included})
				uidOf[uid] = &nativeRouteRef{ref: refOf(u, KindHTTPRoutes)}
			}
		}
		reduced := observe.ReduceAttachments(atts)
		for i := range reduced {
			reduced[i].Rules = rulesOf[reduced[i].UID]
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
			top = append(top, observe.Ref{Cluster: cluster, GVR: ref.gvr, Namespace: a.Namespace, Name: a.Name, UID: a.UID,
				Parent: a.Parent, Role: a.Role, Label: a.Label, Reason: a.Reason, Generation: ref.generation, Route: TaskGateway})
		}
		res := observe.PartResult{Native: &nc, Top: top}
		// One ledger role per counted route (slice 2b §1.1): the reduced
		// attachment's worst role. A route attached to no Gateway in this
		// capture is not evaluated - the Observer's membership names it
		// unevaluated, never gone.
		for _, a := range reduced {
			ref := uidOf[a.UID].ref
			res.Roles = appendRole(res.Roles, &res.RolesCapped, c.RoleCap, observe.RoleEntry{Source: KindHTTPRoutes, GVR: ref.gvr, Key: a.UID,
				NS: a.Namespace, Name: a.Name, Role: a.Role, Label: a.Label, Reason: a.Reason})
		}
		if c.Rows {
			// The reduced attachment carries its captured evaluation (role,
			// label, inclusion) itself; the row adds the route's identity.
			rows := make([]Row[observe.Attached], 0, len(reduced))
			for _, a := range reduced {
				ref := uidOf[a.UID].ref
				ref.ns, ref.name, ref.uid = a.Namespace, a.Name, a.UID
				rows = append(rows, Row[observe.Attached]{Ref: ref.rowRef(), Data: a})
			}
			res.Rows = rowSet(rows)
		}
		return res
	}
}

type nativeRouteRef struct{ ref refInput }

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

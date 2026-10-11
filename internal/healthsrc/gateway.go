package healthsrc

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/moomora/klyx/internal/gwapi"
	"github.com/moomora/klyx/internal/observe"
)

// Gateway API kinds and the Gateways task (1c addendum §1, §1.3). Routes
// are their own part: a healthy Gateway with a broken route lights the
// task. Service existence and EndpointSlices are the routes part's evidence
// - watched only where the Gateway API is served (their probes follow it),
// never on clusters without it.
const (
	KindGatewayClasses   = "gatewayclasses"
	KindGateways         = "gateways"
	KindHTTPRoutes       = "httproutes"
	KindGatewayServices  = "services"
	KindGatewayEndpoints = "endpointslices"
	TaskGateway          = "gateway"
	gatewayGroup         = "gateway.networking.k8s.io"
	coreKindProbeVersion = "v1" // core kinds: a fixed generation identity
)

// Gateway returns the Gateway API kinds and the Gateways task.
func Gateway(d NativeDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	whenGatewayAPI := func(ctx context.Context) (observe.Probe, error) {
		p, err := d.Served(ctx, gatewayGroup, "gateways", "v1")
		if err != nil || !p.Served {
			return observe.Probe{Served: false}, err
		}
		return observe.Probe{Served: true, Version: coreKindProbeVersion}, nil
	}
	services := metaKind(d.Meta, KindGatewayServices, schema.GroupVersionResource{Version: "v1", Resource: "services"}, "all namespaces", ProjectOwnerMeta)
	services.Probe = whenGatewayAPI
	typed := d.Typed
	endpoints := observe.KindSpec{Name: KindGatewayEndpoints, GVR: "discovery.k8s.io/v1/endpointslices", Scope: "all namespaces",
		ExpectedType: &discoveryv1.EndpointSlice{}, Project: ProjectEndpointSlice, Probe: whenGatewayAPI,
		NewListWatch: lw(typed, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return typed.DiscoveryV1().EndpointSlices("").List(ctx, o)
		}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return typed.DiscoveryV1().EndpointSlices("").Watch(ctx, o)
		})}
	kinds := []observe.KindSpec{
		crdKind(d, KindGatewayClasses, gatewayGroup, "gatewayclasses", "v1", "cluster-scoped"),
		crdKind(d, KindGateways, gatewayGroup, "gateways", "v1", "all namespaces"),
		crdKind(d, KindHTTPRoutes, gatewayGroup, "httproutes", "v1", "all namespaces"),
		services, endpoints,
	}
	c := d.Cluster
	domains := []observe.DomainSpec{{ID: TaskGateway, Parts: []observe.PartSpec{
		{ID: "gateways", Entity: "Gateways", Scope: "all namespaces", Classifier: "native",
			Counted: []string{KindGateways}, Deps: []string{KindGatewayClasses}, Evaluate: evalGateways(c)},
		{ID: "routes", Entity: "HTTPRoutes with a failing attachment", Scope: "routes attached to a Gateway", Classifier: "native",
			Counted: []string{KindHTTPRoutes}, Deps: []string{KindGateways, KindGatewayServices, KindGatewayEndpoints}, Evaluate: evalRoutes(c)},
	}}}
	return kinds, domains
}

// ProjectEndpointSlice keeps what CountEndpoints reads: identity, the
// service-name label, and per endpoint its ready condition and target ref.
func ProjectEndpointSlice(obj any) (any, error) {
	es, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil, errWant("*EndpointSlice", obj)
	}
	out := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: es.Name, Namespace: es.Namespace, UID: es.UID, Labels: map[string]string{discoveryv1.LabelServiceName: es.Labels[discoveryv1.LabelServiceName]}},
		AddressType: es.AddressType,
	}
	for _, e := range es.Endpoints {
		pe := discoveryv1.Endpoint{Conditions: discoveryv1.EndpointConditions{Ready: e.Conditions.Ready}}
		if e.TargetRef != nil {
			pe.TargetRef = &corev1.ObjectReference{Kind: e.TargetRef.Kind, Namespace: e.TargetRef.Namespace, Name: e.TargetRef.Name, UID: e.TargetRef.UID}
		}
		out.Endpoints = append(out.Endpoints, pe)
	}
	return out, nil
}

func evalGateways(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		var items []nativeItem
		var rows []gwapi.GatewayRef
		for _, u := range unstructuredOf(c.Objects[KindGateways]) {
			g := gwapi.ParseGatewayRef(u)
			items = append(items, nativeItem{gwapi.GatewayRule(g.AcceptedCond, g.ProgrammedCond, g.Generation), refOf(u, KindGateways)})
			rows = append(rows, g)
		}
		r := nativeResult(cluster, TaskGateway, "need attention", items)
		if c.Rows {
			r.Rows = rows
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
			m := o.(*metav1.PartialObjectMetadata)
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
		var rows []observe.Attached
		uidOf := map[string]*nativeRouteRef{}
		for _, u := range unstructuredOf(c.Objects[KindHTTPRoutes]) {
			for _, gw := range parents {
				rn, ok := gwapi.RouteForGateway(u, gw.Namespace, gw.Name)
				if !ok {
					continue
				}
				resolve(&rn)
				e := gwapi.RouteRule(rn, gw.Listeners)
				uid := string(u.GetUID())
				atts = append(atts, observe.Attachment{UID: uid, Namespace: u.GetNamespace(), Name: u.GetName(),
					Parent: gw.Namespace + "/" + gw.Name, Role: e.Role.String(), Label: e.Label, Included: e.Included})
				uidOf[uid] = &nativeRouteRef{ref: refOf(u, KindHTTPRoutes)}
			}
		}
		reduced := observe.ReduceAttachments(atts)
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
				Parent: a.Parent, Role: a.Role, Label: a.Label, Generation: ref.generation, Route: TaskGateway})
		}
		if c.Rows {
			rows = reduced
		}
		res := observe.PartResult{Native: &nc, Top: top}
		if c.Rows {
			res.Rows = rows
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

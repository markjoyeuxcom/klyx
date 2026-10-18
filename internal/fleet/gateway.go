package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gwapi"
)

const gwGroup = "gateway.networking.k8s.io"

const envoyGroup = "gateway.envoyproxy.io"

const ciliumGroup = "cilium.io"

// policyKinds is the M5-b-i precise policy set (display Kind + resource + group).
var policyKinds = []struct{ Kind, Group, Resource string }{
	{"ClientTrafficPolicy", envoyGroup, "clienttrafficpolicies"},
	{"BackendTrafficPolicy", envoyGroup, "backendtrafficpolicies"},
	{"SecurityPolicy", envoyGroup, "securitypolicies"},
	{"EnvoyExtensionPolicy", envoyGroup, "envoyextensionpolicies"},
	{"BackendTLSPolicy", gwGroup, "backendtlspolicies"},
}

// ciliumPolicyKinds is the M5-b-ii inferred Cilium policy set. Cluster marks the
// cluster-scoped CCNP, which routes broad/empty selectors to header context.
var ciliumPolicyKinds = []struct {
	Kind, Resource string
	Cluster        bool
}{
	{"CiliumNetworkPolicy", "ciliumnetworkpolicies", false},
	{"CiliumClusterwideNetworkPolicy", "ciliumclusterwidenetworkpolicies", true},
}

// policyCandidateVersions lists the versions a policy resource may be served at,
// preferred first. A resource's version is resolved per-resource (not via the
// group's preferred version, since BackendTLSPolicy lives at v1alpha3 while the
// gateway.networking.k8s.io group prefers v1).
var policyCandidateVersions = map[string][]string{
	envoyGroup:  {"v1alpha1"},
	gwGroup:     {"v1", "v1alpha3", "v1alpha2"},
	ciliumGroup: {"v2"},
}

// servedResourceGVR finds the served GVR for a (group, resource), probing the
// candidate versions in order. ok=false means the CRD is not installed.
func (c *ClusterConn) servedResourceGVR(group, resource string) (schema.GroupVersionResource, bool) {
	disc := c.discovery()
	for _, v := range policyCandidateVersions[group] {
		rl, err := disc.ServerResourcesForGroupVersion(group + "/" + v)
		if err != nil || rl == nil {
			continue
		}
		for _, r := range rl.APIResources {
			if r.Name == resource {
				return schema.GroupVersionResource{Group: group, Version: v, Resource: resource}, true
			}
		}
	}
	return schema.GroupVersionResource{}, false
}

// attachGatewayPolicies lists the five precise policy kinds and attaches them by
// targetRef. Two warning classes: not-installed (informational) and served-but-
// list-failed (operational).
func (c *ClusterConn) attachGatewayPolicies(ctx context.Context, topo *gwapi.Topology) {
	for _, pk := range policyKinds {
		gvr, ok := c.servedResourceGVR(pk.Group, pk.Resource)
		if !ok {
			topo.Warnings = append(topo.Warnings, pk.Kind+" CRD not installed")
			continue
		}
		list, err := c.dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			topo.Warnings = append(topo.Warnings, fmt.Sprintf("could not list %s: %v", pk.Kind, err))
			continue
		}
		var refs []gwapi.PolicyRef
		for i := range list.Items {
			u := &unstructured.Unstructured{Object: list.Items[i].Object}
			refs = append(refs, gwapi.BuildPolicyRefs(u, pk.Kind)...)
		}
		gwapi.AttachPolicies(topo, refs)
	}
}

// attachCiliumPolicies lists CNP/CCNP and attaches them by the inferred label heuristic.
// Reuses the served-resource discovery + two warning classes. Inferred=true throughout.
func (c *ClusterConn) attachCiliumPolicies(ctx context.Context, topo *gwapi.Topology) {
	for _, ck := range ciliumPolicyKinds {
		gvr, ok := c.servedResourceGVR(ciliumGroup, ck.Resource)
		if !ok {
			topo.Warnings = append(topo.Warnings, ck.Kind+" CRD not installed")
			continue
		}
		list, err := c.dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			topo.Warnings = append(topo.Warnings, fmt.Sprintf("could not list %s: %v", ck.Kind, err))
			continue
		}
		for i := range list.Items {
			u := &unstructured.Unstructured{Object: list.Items[i].Object}
			attachOneCiliumPolicy(u, ck.Kind, ck.Cluster, topo)
		}
	}
}

// attachOneCiliumPolicy classifies a policy's selector once, then maps class + kind to a
// PolicyMatchKind and routes it to ServiceNode.CNPs / Topology.ClusterPolicies / a warning.
func attachOneCiliumPolicy(u *unstructured.Unstructured, kind string, cluster bool, topo *gwapi.Topology) {
	sel, _, _ := unstructured.NestedMap(u.Object, "spec", "endpointSelector")
	class, labels, hasExpr := gwapi.ClassifyCiliumSelector(sel)
	polNS := u.GetNamespace()

	switch class {
	case gwapi.SelectorExpressionsOnly:
		topo.Warnings = append(topo.Warnings, fmt.Sprintf("%s %s/%s: matchExpressions-only selector not evaluated", kind, polNS, u.GetName()))
		return
	case gwapi.SelectorEmpty:
		if cluster {
			topo.ClusterPolicies = append(topo.ClusterPolicies, gwapi.CiliumPolicyRef(u, kind, gwapi.MatchClusterWide, "", "", false))
			return
		}
		// namespace-wide CNP: every Service in the policy's namespace.
		for ri := range topo.Routes {
			for si := range topo.Routes[ri].Services {
				s := &topo.Routes[ri].Services[si]
				if s.Namespace == polNS {
					s.CNPs = append(s.CNPs, gwapi.CiliumPolicyRef(u, kind, gwapi.MatchNamespaceWide, s.Namespace, s.Name, false))
				}
			}
		}
	case gwapi.SelectorLabels:
		for ri := range topo.Routes {
			for si := range topo.Routes[ri].Services {
				s := &topo.Routes[ri].Services[si]
				if !cluster && s.Namespace != polNS {
					continue // a namespaced CNP only governs its own namespace
				}
				if gwapi.LabelsSubset(labels, s.Selector) {
					s.CNPs = append(s.CNPs, gwapi.CiliumPolicyRef(u, kind, gwapi.MatchSelector, s.Namespace, s.Name, hasExpr))
				}
			}
		}
	}
}

// gwGVR resolves a Gateway API resource's version from its own candidates
// (coverage S7 §2, gwapi.Versions), never the group's preferred version. An
// error says the resource is not served, or is served only at a version
// Klyx does not read: either way nothing is read.
func (c *ClusterConn) gwGVR(resource string) (schema.GroupVersionResource, error) {
	v, served, err := servedResourceAmong(c.discovery(), gwGroup, resource, gwapi.Versions(resource))
	switch {
	case err != nil:
		return schema.GroupVersionResource{}, err
	case !served:
		return schema.GroupVersionResource{}, fmt.Errorf("%s/%s is %w", gwGroup, resource, errGWNotServed)
	}
	return schema.GroupVersionResource{Group: gwGroup, Version: v, Resource: resource}, nil
}

// listGW lists a Gateway API resource at its resolved version.
func (c *ClusterConn) listGW(ctx context.Context, resource string) (*unstructured.UnstructuredList, error) {
	gvr, err := c.gwGVR(resource)
	if err != nil {
		return nil, err
	}
	return c.dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
}

// GetGatewayTopology builds the per-Gateway data path. A core failure (the Gateway
// cannot be read) returns an error; soft issues accumulate in Topology.Warnings.
func (c *ClusterConn) GetGatewayTopology(ctx context.Context, namespace, name string) (gwapi.Topology, error) {
	gwGVR, err := c.gwGVR("gateways")
	if err != nil {
		return gwapi.Topology{}, fmt.Errorf("get gateway %s/%s: %w", namespace, name, err)
	}
	gwu, err := c.dyn.Resource(gwGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return gwapi.Topology{}, fmt.Errorf("get gateway %s/%s: %w", namespace, name, err)
	}
	topo := gwapi.Topology{Gateway: gwapi.ParseGateway(gwu)}
	c.readRoutes(ctx, gwu, &topo)
	c.attachGatewayPolicies(ctx, &topo)
	c.attachCiliumPolicies(ctx, &topo)
	return topo, nil
}

// errGWNotServed: discovery completely answered that a Gateway API
// resource is not served.
var errGWNotServed = errors.New("not served")

// readGWList lists a Gateway API resource: served=false when it is not
// served (nothing to say), an error when the list failed.
func (c *ClusterConn) readGWList(ctx context.Context, resource string) (*unstructured.UnstructuredList, bool, error) {
	gvr, err := c.gwGVR(resource)
	if errors.Is(err, errGWNotServed) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	list, err := c.dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
	return list, true, err
}

// readRoutes draws one lane per attachment landing on this Gateway -
// directly, or through one of its ListenerSets - of every route kind the
// cluster serves (coverage S7b §5, §9). Each lane carries the attachment's
// verdict (the Observer's rule: the expected controller's status, the
// section, the backends by port and transport) and its evidence. A list
// that fails leaves the routes incomplete, naming the kind: absent routes
// are then never proven unattached.
func (c *ClusterConn) readRoutes(ctx context.Context, gwu *unstructured.Unstructured, topo *gwapi.Topology) {
	gw := gwapi.ParseGatewayRef(gwu)
	// only this Gateway is read: any other parent is not established
	ix := gwapi.RouteIndex{Gateways: []gwapi.GatewayRef{gw}, GatewaysRead: gwapi.ParentRead{Current: true}}
	read := func(ok bool, err error) gwapi.ParentRead {
		switch {
		case !ok:
			return gwapi.ParentRead{Absent: true, Complete: true}
		case err != nil:
			return gwapi.ParentRead{}
		}
		return gwapi.ParentRead{Current: true, Complete: true}
	}
	// A failed class read leaves every lane's controller unestablished (its
	// verdict Unknown, never a Fault); a failed grant read leaves the grant
	// evidence unknown. Both are warnings: the lanes still draw.
	classes, served, err := c.readGWList(ctx, "gatewayclasses")
	ix.ClassesRead = read(served, err)
	if err != nil {
		topo.Warnings = append(topo.Warnings, "could not list GatewayClasses: "+err.Error())
	} else if classes != nil {
		for i := range classes.Items {
			ix.Classes = append(ix.Classes, gwapi.ParseGatewayClass(&classes.Items[i]))
		}
	}
	sets, served, err := c.readGWList(ctx, "listenersets")
	ix.ListenerSetsRead = read(served, err)
	setsFailed := err != nil
	if setsFailed {
		topo.Warnings = append(topo.Warnings, "could not list ListenerSets: "+err.Error())
	} else if sets != nil {
		for i := range sets.Items {
			ix.ListenerSets = append(ix.ListenerSets, gwapi.ParseListenerSet(&sets.Items[i]))
		}
	}
	grants := gwapi.GrantsRead{}
	gl, served, err := c.readGWList(ctx, "referencegrants")
	grants.Read = read(served, err)
	if err != nil {
		topo.Warnings = append(topo.Warnings, "could not list ReferenceGrants: "+err.Error())
	} else if gl != nil {
		for i := range gl.Items {
			grants.Grants = append(grants.Grants, gwapi.ParseReferenceGrant(&gl.Items[i]))
		}
	}
	lands := func(ref gwapi.RouteParentRef) bool {
		switch {
		case ref.IsGateway():
			return ref.Namespace == gw.Namespace && ref.Name == gw.Name
		case ref.IsListenerSet():
			for _, ls := range ix.ListenerSets {
				if ls.Namespace == ref.Namespace && ls.Name == ref.Name {
					return ls.Parent.IsGateway() && ls.Parent.Namespace == gw.Namespace && ls.Parent.Name == gw.Name
				}
			}
		}
		return false
	}
	nsLabels := map[string]gwapi.NamespaceLabels{}
	labelsOf := func(ns string) gwapi.NamespaceLabels {
		if l, ok := nsLabels[ns]; ok {
			return l
		}
		l := gwapi.NamespaceLabels{}
		if n, err := c.typed.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err == nil {
			l = gwapi.NamespaceLabels{Known: true, Labels: n.Labels}
		}
		nsLabels[ns] = l
		return l
	}
	topo.RoutesComplete = true
	var errs []string
	unsure := 0
	for _, kind := range gwapi.RouteKinds() {
		list, served, err := c.readGWList(ctx, gwapi.RouteResource(kind))
		switch {
		case !served:
			continue
		case err != nil:
			errs = append(errs, fmt.Sprintf("could not list %ss: %v", kind, err))
			continue
		}
		for i := range list.Items {
			r := gwapi.ParseRoute(kind, &list.Items[i])
			var landing []gwapi.RouteParentRef
			for _, ref := range r.ParentRefs {
				if lands(ref) {
					landing = append(landing, ref)
				} else if ref.IsListenerSet() && setsFailed {
					unsure++ // it may land through a ListenerSet nobody could read
				}
			}
			if len(landing) == 0 {
				continue
			}
			services, pods := c.resolveRouteBackends(ctx, &r, topo)
			for _, ref := range landing {
				ae := gwapi.EvaluateAttachment(r, ref, ix)
				rn := r.NodeFor(ae)
				rn.Kind, rn.Attachment, rn.AttachmentLabel, rn.ParentRef = kind, ref.Key(), ref.Label(), ref
				eval := ae.Eval
				rn.Verdict = &eval
				ev := gwapi.Evidence(r, ae, ix, grants, labelsOf(r.Namespace))
				rn.Evidence = &ev
				rn.Controller, rn.ControllerWhy, rn.OtherControllers = ae.Parent.Controller, ae.Parent.ControllerWhy, ae.Others
				rn.HostnameCount = r.HostnameCount
				for _, rule := range r.Rules {
					rn.RuleNames = append(rn.RuleNames, rule.Name)
				}
				rn.Services, rn.Pods = append([]gwapi.ServiceNode(nil), services...), pods
				topo.Routes = append(topo.Routes, rn)
			}
		}
	}
	if unsure > 0 {
		errs = append(errs, fmt.Sprintf("%d route attachments name a ListenerSet that could not be read; whether they land on this Gateway is unknown", unsure))
	}
	if len(errs) > 0 {
		// Coverage, not a warning alone: with a route list missing, nothing
		// can be claimed about which routes attach (none, or one that left).
		topo.RoutesComplete = false
		topo.RoutesError = strings.Join(errs, "; ")
		topo.Warnings = append(topo.Warnings, errs...)
	}
}

// resolveRouteBackends resolves every backend of a route, live, through its
// Service port by number and the route's transport, then the EndpointSlices
// carrying that port (S7b §5). It returns the Service nodes (for policy and
// Cilium inference) and the legacy pod count of the first resolved Service.
func (c *ClusterConn) resolveRouteBackends(ctx context.Context, r *gwapi.Route, topo *gwapi.Topology) ([]gwapi.ServiceNode, gwapi.PodCount) {
	var services []gwapi.ServiceNode
	pods, primary := gwapi.PodCount{}, false
	routeID := r.Kind + " " + r.Namespace + "/" + r.Name
	transport := gwapi.Transport(r.Kind)
	backends := 0
	for ri := range r.Rules {
		for bi := range r.Rules[ri].Backends {
			backends++
			b := &r.Rules[ri].Backends[bi]
			b.Transport = transport
			if b.Kind != "Service" || b.Group != "" {
				b.State = gwapi.BackendUnsupported
				topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s: backend %q is a %s, not a Service", routeID, b.Name, b.Kind))
				continue
			}
			sn := gwapi.ServiceNode{Namespace: b.Namespace, Name: b.Name, Port: b.Port}
			svc, err := c.typed.CoreV1().Services(b.Namespace).Get(ctx, b.Name, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					b.State = gwapi.BackendNotFound
					topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s: backend Service %s/%s not found", routeID, b.Namespace, b.Name))
				} else {
					b.State, b.Error = gwapi.BackendUnreadable, err.Error()
					topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s: backend Service %s/%s unreadable: %v", routeID, b.Namespace, b.Name, err))
				}
				services = append(services, sn) // Resolved=false
				continue
			}
			sn.Resolved = true
			sn.Type = string(svc.Spec.Type)
			sn.Selector = svc.Spec.Selector
			sn.Global = svc.Annotations["service.cilium.io/global"] == "true"
			services = append(services, sn)
			ports := make([]gwapi.ServicePort, 0, len(svc.Spec.Ports))
			for _, p := range svc.Spec.Ports {
				ports = append(ports, gwapi.ServicePort{Name: p.Name, Port: p.Port, Protocol: string(p.Protocol)})
			}
			sp, ok, other := gwapi.SelectServicePort(ports, b.Port, transport)
			if !ok {
				b.State = gwapi.BackendNoPort
				b.Error = fmt.Sprintf("Service has no %s port %d", transport, b.Port)
				if other != "" {
					b.Error += " (" + other + ")"
				}
				topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s: %s %s/%s", routeID, b.Error, b.Namespace, b.Name))
				continue
			}
			b.State = gwapi.BackendResolved
			ep := c.endpointCountFor(ctx, b.Namespace, b.Name, sp)
			b.Endpoints = &ep
			if !primary {
				pods, primary = gwapi.PodCount{Ready: ep.Ready, Total: ep.Total, Unknown: ep.Error != ""}, true
				if ep.Error != "" {
					topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s: EndpointSlices unavailable for %s", routeID, b.Name))
				}
			}
		}
	}
	if backends > 1 {
		topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s has %d backends; the lane shows the primary", routeID, backends))
	}
	return services, pods
}

// endpointCountFor counts the endpoints of a Service's EndpointSlices that
// carry its port (S7b §5).
func (c *ClusterConn) endpointCountFor(ctx context.Context, ns, svc string, sp gwapi.ServicePort) gwapi.EndpointCount {
	slices, err := c.typed.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + svc})
	if err != nil {
		return gwapi.EndpointCount{Error: err.Error()}
	}
	return gwapi.CountEndpointsFor(slices.Items, sp)
}

package fleet

import (
	"context"
	"fmt"

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

func (c *ClusterConn) gwGVR(resource string) schema.GroupVersionResource {
	v := preferredVersion(c.discovery(), gwGroup, "v1")
	return schema.GroupVersionResource{Group: gwGroup, Version: v, Resource: resource}
}

// gatewayAPIServed reports whether the Gateway API group is advertised.
func (c *ClusterConn) gatewayAPIServed() bool {
	groups, err := c.discovery().ServerGroups()
	if err != nil || groups == nil {
		return false
	}
	for _, g := range groups.Groups {
		if g.Name == gwGroup {
			return true
		}
	}
	return false
}

// ListGateways lists Gateways (refs) and whether the Gateway API is served.
// A list failure on a served API is a real error (auth, network, RBAC) and is
// returned as one - rendering it as "no Gateways" would be indistinguishable
// from a genuinely empty cluster. A failure when the group is not advertised
// is the expected not-installed case (and the fake-discovery fallback in
// tests) and stays an empty list.
func (c *ClusterConn) ListGateways(ctx context.Context) ([]gwapi.GatewayRef, bool, error) {
	served := c.gatewayAPIServed()
	list, err := c.dyn.Resource(c.gwGVR("gateways")).List(ctx, metav1.ListOptions{})
	if err != nil {
		if !served {
			return nil, false, nil
		}
		return nil, served, fmt.Errorf("list gateways: %w", err)
	}
	out := make([]gwapi.GatewayRef, 0, len(list.Items))
	for i := range list.Items {
		u := &unstructured.Unstructured{Object: list.Items[i].Object}
		out = append(out, gwapi.ParseGatewayRef(u))
	}
	return out, served, nil
}

// GetGatewayTopology builds the per-Gateway data path. A core failure (the Gateway
// cannot be read) returns an error; soft issues accumulate in Topology.Warnings.
func (c *ClusterConn) GetGatewayTopology(ctx context.Context, namespace, name string) (gwapi.Topology, error) {
	gwu, err := c.dyn.Resource(c.gwGVR("gateways")).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return gwapi.Topology{}, fmt.Errorf("get gateway %s/%s: %w", namespace, name, err)
	}
	topo := gwapi.Topology{Gateway: gwapi.ParseGateway(gwu)}

	hrList, err := c.dyn.Resource(c.gwGVR("httproutes")).List(ctx, metav1.ListOptions{})
	if err != nil {
		topo.Warnings = append(topo.Warnings, "could not list HTTPRoutes: "+err.Error())
		return topo, nil
	}
	for i := range hrList.Items {
		u := &unstructured.Unstructured{Object: hrList.Items[i].Object}
		rn, ok := gwapi.RouteForGateway(u, namespace, name)
		if !ok {
			continue
		}
		c.resolveBackends(ctx, &rn, &topo)
		topo.Routes = append(topo.Routes, rn)
	}
	c.attachGatewayPolicies(ctx, &topo)
	c.attachCiliumPolicies(ctx, &topo)
	return topo, nil
}

// resolveBackends fills a route's Services + primary Pods from the typed client,
// appending warnings for anything it can't resolve.
func (c *ClusterConn) resolveBackends(ctx context.Context, rn *gwapi.RouteNode, topo *gwapi.Topology) {
	for i, b := range rn.Backends {
		if b.Kind != "Service" {
			topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s/%s: backend %q is a %s, not a Service", rn.Namespace, rn.Name, b.Name, b.Kind))
			continue
		}
		sn := gwapi.ServiceNode{Namespace: b.Namespace, Name: b.Name, Port: b.Port}
		svc, err := c.typed.CoreV1().Services(b.Namespace).Get(ctx, b.Name, metav1.GetOptions{})
		if err != nil {
			topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s/%s: backend Service %s/%s not found", rn.Namespace, rn.Name, b.Namespace, b.Name))
			rn.Services = append(rn.Services, sn) // Resolved=false
			continue
		}
		sn.Resolved = true
		sn.Type = string(svc.Spec.Type)
		sn.Selector = svc.Spec.Selector
		sn.Global = svc.Annotations["service.cilium.io/global"] == "true"
		if len(svc.Spec.Ports) > 0 && sn.Port == 0 {
			sn.Port = svc.Spec.Ports[0].Port
		}
		rn.Services = append(rn.Services, sn)
		if i == 0 {
			rn.Pods = c.podCount(ctx, b.Namespace, b.Name, topo, rn)
		}
	}
	if len(rn.Backends) > 1 {
		topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s/%s has %d backends; the lane shows the primary", rn.Namespace, rn.Name, len(rn.Backends)))
	}
}

func (c *ClusterConn) podCount(ctx context.Context, ns, svc string, topo *gwapi.Topology, rn *gwapi.RouteNode) gwapi.PodCount {
	slices, err := c.typed.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: "kubernetes.io/service-name=" + svc})
	if err != nil {
		topo.Warnings = append(topo.Warnings, fmt.Sprintf("route %s/%s: EndpointSlices unavailable for %s", rn.Namespace, rn.Name, svc))
		return gwapi.PodCount{Unknown: true}
	}
	pc := gwapi.PodCount{}
	for i := range slices.Items {
		for _, e := range slices.Items[i].Endpoints {
			pc.Total++
			if e.Conditions.Ready != nil && *e.Conditions.Ready {
				pc.Ready++
			}
		}
	}
	return pc
}

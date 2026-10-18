package healthsrc

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/moomora/klyx/internal/lb"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/urlscrub"
)

// The shared network kinds (roadmap C3, coverage S3a §2): Services and
// EndpointSlices, declared once and gated by each consumer's own demand -
// the Gateway routes part's evidence where the Gateway API is served, and
// the admission webhooks' backends wherever admission webhooks are. One
// model and projection serve every reader.
const (
	KindServices       = "services"
	KindEndpointSlices = "endpointslices"
	// The consumers.
	ConsumerGateway       = "gateway"
	ConsumerAdmission     = "admission"
	ConsumerLoadBalancing = "loadbalancing" // coverage S4: Services only
	admissionGroup        = "admissionregistration.k8s.io"
)

// AddressPresent stands in a projected endpoint for its addresses: a rule
// reads whether an endpoint has one, never which.
const AddressPresent = "(address)"

// Deprecated names, kept for the Gateway task's readers.
const (
	KindGatewayServices  = KindServices
	KindGatewayEndpoints = KindEndpointSlices
)

// SharedNetworkKinds returns the shared Services and EndpointSlices kinds.
// The connection adds them once, beside the domains that read them.
func SharedNetworkKinds(d NativeDeps) []observe.KindSpec {
	served := func(group, resource string) func(context.Context) (bool, error) {
		return func(ctx context.Context) (bool, error) {
			p, err := d.Served(ctx, group, resource, "v1")
			if err != nil {
				return false, err
			}
			return p.Served, nil
		}
	}
	consumers := []observe.Consumer{
		{Name: ConsumerGateway, Demand: served(gatewayGroup, "gateways")},
		{Name: ConsumerAdmission, Demand: served(admissionGroup, "validatingwebhookconfigurations")},
	}
	// S4's consumer reads the core Services API, served on every supported
	// cluster: Services are watched wherever Klyx connects.
	always := func(context.Context) (bool, error) { return true, nil }
	svcConsumers := append(append([]observe.Consumer(nil), consumers...), observe.Consumer{Name: ConsumerLoadBalancing, Demand: always})
	typed := d.Typed
	services := observe.KindSpec{Name: KindServices, GVR: "v1/services", Scope: "all namespaces", ExpectedType: &corev1.Service{},
		Project: ProjectService, Consumers: svcConsumers, Version: coreKindProbeVersion,
		NewListWatch: lw(typed, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return typed.CoreV1().Services("").List(ctx, o)
		}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return typed.CoreV1().Services("").Watch(ctx, o)
		})}
	endpoints := observe.KindSpec{Name: KindEndpointSlices, GVR: "discovery.k8s.io/v1/endpointslices", Scope: "all namespaces",
		ExpectedType: &discoveryv1.EndpointSlice{}, Project: ProjectEndpointSlice, Consumers: consumers, Version: coreKindProbeVersion,
		NewListWatch: lw(typed, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return typed.DiscoveryV1().EndpointSlices("").List(ctx, o)
		}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return typed.DiscoveryV1().EndpointSlices("").Watch(ctx, o)
		})}
	return []observe.KindSpec{services, endpoints}
}

// ProjectService keeps what every reader reads (S3a §2): identity and
// owners (the Gateway routes part, the fleet index), and the spec fields
// that decide whether the API server can route to it - type, a headless
// ClusterIP, an ExternalName, the ports (name, number, protocol), whether
// a selector is set, and publishNotReadyAddresses. It is a *corev1.Service
// so meta.Accessor readers keep working.
//
// A LoadBalancer Service also keeps what S4's rule reads (S4 §2): its
// class, externalTrafficPolicy, families and their policy, the deprecated
// loadBalancerIP, the allowlisted address-request annotations, and its
// status - every ingress entry (ip, hostname, ipMode; per port its number,
// protocol and whether an error is set) and its conditions (message
// scrubbed and clipped). The lists are kept whole so a rule classifies
// over all of them; only displays are capped.
func ProjectService(obj any) (any, error) {
	s, ok := obj.(*corev1.Service)
	if !ok {
		return nil, errWant("*Service", obj)
	}
	out := &corev1.Service{ObjectMeta: projectMeta(s.ObjectMeta, false), Spec: corev1.ServiceSpec{Type: s.Spec.Type,
		ExternalName: s.Spec.ExternalName, PublishNotReadyAddresses: s.Spec.PublishNotReadyAddresses}}
	if s.Spec.Type == corev1.ServiceTypeLoadBalancer {
		projectLoadBalancer(s, out)
	}
	if s.Spec.ClusterIP == corev1.ClusterIPNone {
		out.Spec.ClusterIP = corev1.ClusterIPNone
	}
	if len(s.Spec.Selector) > 0 {
		out.Spec.Selector = map[string]string{"": ""} // set; its values are not read
	}
	for _, p := range s.Spec.Ports {
		out.Spec.Ports = append(out.Spec.Ports, corev1.ServicePort{Name: p.Name, Port: p.Port, Protocol: p.Protocol})
	}
	return out, nil
}

// ProjectEndpointSlice keeps what CountEndpoints and the admission backend
// rule read: identity, the service-name label, each port's name, number
// and protocol with nil kept distinct, and per endpoint its ready, serving
// and terminating conditions (nil kept), whether it has an address, and
// its target ref.
func ProjectEndpointSlice(obj any) (any, error) {
	es, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil, errWant("*EndpointSlice", obj)
	}
	out := &discoveryv1.EndpointSlice{
		ObjectMeta:  metav1.ObjectMeta{Name: es.Name, Namespace: es.Namespace, UID: es.UID, Labels: map[string]string{discoveryv1.LabelServiceName: es.Labels[discoveryv1.LabelServiceName]}},
		AddressType: es.AddressType,
	}
	for _, p := range es.Ports {
		out.Ports = append(out.Ports, discoveryv1.EndpointPort{Name: p.Name, Port: p.Port, Protocol: p.Protocol})
	}
	for _, e := range es.Endpoints {
		pe := discoveryv1.Endpoint{Conditions: discoveryv1.EndpointConditions{Ready: e.Conditions.Ready, Serving: e.Conditions.Serving, Terminating: e.Conditions.Terminating}}
		if len(e.Addresses) > 0 {
			pe.Addresses = []string{AddressPresent} // whether it has one, never the address
		}
		if e.TargetRef != nil {
			pe.TargetRef = &corev1.ObjectReference{Kind: e.TargetRef.Kind, Namespace: e.TargetRef.Namespace, Name: e.TargetRef.Name, UID: e.TargetRef.UID}
		}
		out.Endpoints = append(out.Endpoints, pe)
	}
	return out, nil
}

// projectLoadBalancer copies S4's LoadBalancer fields (S4 §2).
func projectLoadBalancer(s, out *corev1.Service) {
	if c := s.Spec.LoadBalancerClass; c != nil {
		v := clipText(*c, 253)
		out.Spec.LoadBalancerClass = &v
	}
	out.Spec.ExternalTrafficPolicy = s.Spec.ExternalTrafficPolicy
	out.Spec.IPFamilies = append([]corev1.IPFamily(nil), s.Spec.IPFamilies...)
	if p := s.Spec.IPFamilyPolicy; p != nil {
		v := *p
		out.Spec.IPFamilyPolicy = &v
	}
	out.Spec.LoadBalancerIP = clipText(s.Spec.LoadBalancerIP, 64)
	for _, k := range lb.AddressAnnotations {
		if v, ok := s.Annotations[k]; ok {
			if out.Annotations == nil {
				out.Annotations = map[string]string{}
			}
			out.Annotations[k] = clipText(v, 256)
		}
	}
	for _, in := range s.Status.LoadBalancer.Ingress {
		e := corev1.LoadBalancerIngress{IP: clipText(in.IP, 64), Hostname: clipText(in.Hostname, 253)}
		if in.IPMode != nil {
			m := *in.IPMode
			e.IPMode = &m
		}
		for _, p := range in.Ports {
			ps := corev1.PortStatus{Port: p.Port, Protocol: p.Protocol}
			if p.Error != nil && *p.Error != "" {
				set := lb.ErrorSet // whether one is set, never its text
				ps.Error = &set
			}
			e.Ports = append(e.Ports, ps)
		}
		out.Status.LoadBalancer.Ingress = append(out.Status.LoadBalancer.Ingress, e)
	}
	for _, c := range s.Status.Conditions {
		out.Status.Conditions = append(out.Status.Conditions, metav1.Condition{Type: clipText(c.Type, 316), Status: c.Status,
			Reason: clipText(c.Reason, 128), Message: clipText(urlscrub.Text(c.Message).Text, 400),
			ObservedGeneration: c.ObservedGeneration, LastTransitionTime: c.LastTransitionTime})
	}
}

// clipText cuts s to n bytes on a rune boundary, marking the cut.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

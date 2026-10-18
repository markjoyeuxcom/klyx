package healthsrc

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/lb"
)

// Coverage S4 §2: a LoadBalancer Service keeps its class, families,
// allowlisted address annotations and whole status; the port error's text
// and every other annotation never cross; a ClusterIP Service gains nothing.
func TestS4ProjectServiceLoadBalancer(t *testing.T) {
	cls := "io.cilium/l2-announcer"
	req := corev1.IPFamilyPolicyRequireDualStack
	errText := "CreatingLoadBalancerFailed"
	in := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web", UID: "u1", Generation: 3,
		Annotations: map[string]string{"metallb.io/address-pool": "gold", "team": "payments", "kubectl.kubernetes.io/last-applied-configuration": "{}"}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, LoadBalancerClass: &cls, IPFamilyPolicy: &req,
			IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol, corev1.IPv6Protocol}, ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "10.0.0.1", Ports: []corev1.PortStatus{{Port: 80, Protocol: corev1.ProtocolTCP, Error: &errText}}}}},
			Conditions: []metav1.Condition{{Type: lb.CondRequestSatisfied, Status: "False", Reason: "out_of_ips",
				Message: "see https://user:secret@example.com/x", ObservedGeneration: 3}},
		}}
	o, err := ProjectService(in)
	if err != nil {
		t.Fatal(err)
	}
	s := o.(*corev1.Service)
	if s.Spec.LoadBalancerClass == nil || *s.Spec.LoadBalancerClass != cls || s.Spec.IPFamilyPolicy == nil || len(s.Spec.IPFamilies) != 2 ||
		s.Spec.ExternalTrafficPolicy != corev1.ServiceExternalTrafficPolicyLocal || s.Generation != 3 {
		t.Fatalf("spec: %+v", s.Spec)
	}
	if len(s.Annotations) != 1 || s.Annotations["metallb.io/address-pool"] != "gold" {
		t.Fatalf("only allowlisted annotations: %v", s.Annotations)
	}
	if p := s.Status.LoadBalancer.Ingress[0].Ports[0]; p.Error == nil || *p.Error != lb.ErrorSet {
		t.Fatalf("whether an error is set, never its text: %+v", p)
	}
	if c := s.Status.Conditions[0]; c.ObservedGeneration != 3 || c.Reason != "out_of_ips" || c.Message == in.Status.Conditions[0].Message {
		t.Fatalf("conditions kept, message scrubbed: %+v", c)
	}
	in.Spec.Type = corev1.ServiceTypeClusterIP
	o, _ = ProjectService(in)
	if s := o.(*corev1.Service); s.Annotations != nil || s.Spec.LoadBalancerClass != nil || len(s.Status.Conditions) != 0 {
		t.Fatalf("a ClusterIP Service gains nothing: %+v", s)
	}
}

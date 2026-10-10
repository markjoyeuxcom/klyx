package netpol

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var nsLabels = NamespaceLabels{
	"team-a": {"kubernetes.io/metadata.name": "team-a", "tier": "app"},
	"team-b": {"kubernetes.io/metadata.name": "team-b", "tier": "app"},
}

func q(srcNS string, srcLabels map[string]string, dstNS string, dstLabels map[string]string, port int) Query {
	return Query{
		Src:  Endpoint{Namespace: srcNS, PodLabels: srcLabels},
		Dst:  Endpoint{Namespace: dstNS, PodLabels: dstLabels},
		Port: port, Protocol: "TCP",
	}
}

func ingressPolicy(ns, name string, podSel map[string]string, rules []networkingv1.NetworkPolicyIngressRule) networkingv1.NetworkPolicy {
	return networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podSel},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress:     rules,
		},
	}
}

func TestNoIsolationAllowsEverything(t *testing.T) {
	v := Evaluate(q("team-a", map[string]string{"app": "web"}, "team-b", map[string]string{"app": "db"}, 5432), nil, nsLabels)
	if !v.Allowed || v.Ingress.Isolated || v.Egress.Isolated {
		t.Fatalf("no policies must be allow-all: %+v", v)
	}
}

func TestIngressIsolationDefaultDeny(t *testing.T) {
	// A deny-all policy on the dst (selects it, no rules).
	pols := []networkingv1.NetworkPolicy{ingressPolicy("team-b", "deny-all", map[string]string{"app": "db"}, nil)}
	v := Evaluate(q("team-a", map[string]string{"app": "web"}, "team-b", map[string]string{"app": "db"}, 5432), pols, nsLabels)
	if v.Allowed || v.Ingress.Allowed || !v.Ingress.Isolated {
		t.Fatalf("selected with no admitting rule must deny: %+v", v)
	}
	if len(v.Ingress.Policies) != 1 || v.Ingress.Policies[0].Name != "deny-all" || v.Ingress.Policies[0].Admits {
		t.Fatalf("policies: %+v", v.Ingress.Policies)
	}
}

func TestCrossNamespaceAdmitViaNamespaceSelector(t *testing.T) {
	port5432 := intstr.FromInt(5432)
	tcp := corev1.ProtocolTCP
	pols := []networkingv1.NetworkPolicy{ingressPolicy("team-b", "allow-app-tier", map[string]string{"app": "db"},
		[]networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "app"}},
				PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			}},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port5432}},
		}},
	)}
	v := Evaluate(q("team-a", map[string]string{"app": "web"}, "team-b", map[string]string{"app": "db"}, 5432), pols, nsLabels)
	if !v.Allowed || !v.Ingress.Policies[0].Admits {
		t.Fatalf("cross-ns admit: %+v", v)
	}
	// Wrong port: same policy selects but does not admit.
	v2 := Evaluate(q("team-a", map[string]string{"app": "web"}, "team-b", map[string]string{"app": "db"}, 9999), pols, nsLabels)
	if v2.Allowed {
		t.Fatalf("wrong port must deny: %+v", v2)
	}
	// Wrong src labels: denied.
	v3 := Evaluate(q("team-a", map[string]string{"app": "batch"}, "team-b", map[string]string{"app": "db"}, 5432), pols, nsLabels)
	if v3.Allowed {
		t.Fatalf("unmatched src must deny: %+v", v3)
	}
}

func TestSameNamespaceScopeWithoutNamespaceSelector(t *testing.T) {
	pols := []networkingv1.NetworkPolicy{ingressPolicy("team-b", "same-ns-only", map[string]string{"app": "db"},
		[]networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}},
		}},
	)}
	// Same-namespace matching pod: allowed.
	same := Evaluate(q("team-b", map[string]string{"app": "web"}, "team-b", map[string]string{"app": "db"}, 5432), pols, nsLabels)
	if !same.Allowed {
		t.Fatalf("same-ns pod must be admitted: %+v", same)
	}
	// Cross-namespace pod with the same labels: podSelector without
	// namespaceSelector scopes to the policy namespace - denied.
	cross := Evaluate(q("team-a", map[string]string{"app": "web"}, "team-b", map[string]string{"app": "db"}, 5432), pols, nsLabels)
	if cross.Allowed {
		t.Fatalf("cross-ns pod must be denied: %+v", cross)
	}
}

func TestEgressIsolation(t *testing.T) {
	pols := []networkingv1.NetworkPolicy{{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "egress-lockdown"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "team-b"}},
				}},
			}},
		},
	}}
	ok := Evaluate(q("team-a", map[string]string{"app": "web"}, "team-b", map[string]string{"app": "db"}, 5432), pols, nsLabels)
	if !ok.Allowed || !ok.Egress.Isolated {
		t.Fatalf("egress to team-b must be admitted: %+v", ok)
	}
	// Destination outside the admitted namespace: egress denied.
	other := Evaluate(Query{
		Src:  Endpoint{Namespace: "team-a", PodLabels: map[string]string{"app": "web"}},
		Dst:  Endpoint{Namespace: "kube-system", PodLabels: map[string]string{"k8s-app": "kube-dns"}},
		Port: 53, Protocol: "UDP",
	}, pols, NamespaceLabels{"kube-system": {"kubernetes.io/metadata.name": "kube-system"}})
	if other.Allowed || other.Egress.Allowed {
		t.Fatalf("egress outside the admit must deny: %+v", other)
	}
}

func TestPureIPBlockNeverMatchesPodQuery(t *testing.T) {
	pols := []networkingv1.NetworkPolicy{ingressPolicy("team-b", "cidr-only", map[string]string{"app": "db"},
		[]networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8"}}},
		}},
	)}
	v := Evaluate(q("team-a", map[string]string{"app": "web"}, "team-b", map[string]string{"app": "db"}, 5432), pols, nsLabels)
	if v.Allowed {
		t.Fatalf("ipBlock-only peers are unknowable for pod-to-pod - must not admit: %+v", v)
	}
}

func TestPortRangeAndAnyPortSemantics(t *testing.T) {
	tcp := corev1.ProtocolTCP
	start := intstr.FromInt(8000)
	end := int32(8999)
	pols := []networkingv1.NetworkPolicy{ingressPolicy("team-b", "range", map[string]string{"app": "db"},
		[]networkingv1.NetworkPolicyIngressRule{{
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &start, EndPort: &end}},
		}},
	)}
	in := Evaluate(q("team-a", nil, "team-b", map[string]string{"app": "db"}, 8443), pols, nsLabels)
	if !in.Allowed {
		t.Fatalf("port in range must admit: %+v", in)
	}
	outr := Evaluate(q("team-a", nil, "team-b", map[string]string{"app": "db"}, 9000), pols, nsLabels)
	if outr.Allowed {
		t.Fatalf("port out of range must deny: %+v", outr)
	}
	anyPort := Evaluate(q("team-a", nil, "team-b", map[string]string{"app": "db"}, 0), pols, nsLabels)
	if !anyPort.Allowed {
		t.Fatalf("any-port query against a port-scoped admit must read allowed: %+v", anyPort)
	}
	if anyPort.Note != "reachable only on TCP 8000-8999" {
		t.Fatalf("an any-port allow must name the reachable ports, got %q", anyPort.Note)
	}
}

func egressPolicy(ns, name string, podSel map[string]string, rules []networkingv1.NetworkPolicyEgressRule) networkingv1.NetworkPolicy {
	return networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podSel},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress:      rules,
		},
	}
}

func tcpPort(p int) networkingv1.NetworkPolicyPort {
	tcp := corev1.ProtocolTCP
	v := intstr.FromInt(p)
	return networkingv1.NetworkPolicyPort{Protocol: &tcp, Port: &v}
}

// Review 2026-10-03 (netpol any-port): the any-port shortcut ran before the
// protocol check, so a UDP-only rule admitted a TCP any-port query.
func TestAnyPortRespectsProtocol(t *testing.T) {
	udp := corev1.ProtocolUDP
	dns := intstr.FromInt(53)
	pols := []networkingv1.NetworkPolicy{ingressPolicy("team-b", "udp-only", map[string]string{"app": "db"},
		[]networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dns}}}},
	)}
	v := Evaluate(q("team-a", nil, "team-b", map[string]string{"app": "db"}, 0), pols, nsLabels)
	if v.Allowed || v.Ingress.Allowed {
		t.Fatalf("a UDP-only rule must not admit an any-port TCP query: %+v", v)
	}
	udpQ := q("team-a", nil, "team-b", map[string]string{"app": "db"}, 0)
	udpQ.Protocol = "UDP"
	if v := Evaluate(udpQ, pols, nsLabels); !v.Allowed || v.Note != "reachable only on UDP 53" {
		t.Fatalf("the same rule admits an any-port UDP query on 53: %+v", v)
	}
}

// Review 2026-10-03 (netpol any-port): ingress and egress were ANDed
// independently, so egress TCP/80 + ingress TCP/443 read allowed although no
// port is admitted both ways.
func TestAnyPortNeedsOnePortAdmittedBothWays(t *testing.T) {
	src, dst := map[string]string{"app": "web"}, map[string]string{"app": "db"}
	pols := []networkingv1.NetworkPolicy{
		egressPolicy("team-a", "egress-80", src, []networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(80)}}}),
		ingressPolicy("team-b", "ingress-443", dst, []networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{tcpPort(443)}}}),
	}
	v := Evaluate(q("team-a", src, "team-b", dst, 0), pols, nsLabels)
	if v.Allowed {
		t.Fatalf("no common port: any-port must be blocked: %+v", v)
	}
	if !v.Ingress.Allowed || !v.Egress.Allowed || v.Note == "" {
		t.Fatalf("each direction admits some port and the note must say why it still blocks: %+v", v)
	}

	// Overlap: egress 80-500 and ingress 443 meet on 443 only.
	end := int32(500)
	wide := tcpPort(80)
	wide.EndPort = &end
	pols[0] = egressPolicy("team-a", "egress-range", src, []networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{wide}}})
	v = Evaluate(q("team-a", src, "team-b", dst, 0), pols, nsLabels)
	if !v.Allowed || v.Note != "reachable only on TCP 443" {
		t.Fatalf("overlap on 443 must allow and name it: %+v", v)
	}

	// Specific-port queries keep their per-port semantics.
	if v := Evaluate(q("team-a", src, "team-b", dst, 443), pols, nsLabels); !v.Allowed || v.Note != "" {
		t.Fatalf("port 443 is admitted both ways: %+v", v)
	}
	if v := Evaluate(q("team-a", src, "team-b", dst, 80), pols, nsLabels); v.Allowed {
		t.Fatalf("port 80 is not admitted on ingress: %+v", v)
	}
}

func TestAnyPortUnisolatedHasNoNote(t *testing.T) {
	v := Evaluate(q("team-a", nil, "team-b", nil, 0), nil, nsLabels)
	if !v.Allowed || v.Note != "" {
		t.Fatalf("no isolation: every port, nothing to qualify: %+v", v)
	}
}

func TestPortSetAlgebra(t *testing.T) {
	a := portSet{{80, 80}}.union(portSet{{81, 90}}).union(portSet{{443, 443}}).union(portSet{{85, 100}})
	if a.String() != "80-100, 443" {
		t.Fatalf("union must merge adjacent and overlapping ranges: %s", a)
	}
	b := a.intersect(portSet{{90, 450}})
	if b.String() != "90-100, 443" {
		t.Fatalf("intersect: %s", b)
	}
	if len(a.intersect(portSet{{101, 442}})) != 0 {
		t.Fatal("disjoint intersect must be empty")
	}
	if !allPorts.union(a).full() || a.full() {
		t.Fatal("full() must recognise only the whole range")
	}
}

func TestMatchExpressions(t *testing.T) {
	sel := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
		{Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"prd", "stg"}},
		{Key: "canary", Operator: metav1.LabelSelectorOpDoesNotExist},
	}}
	if !selectorMatches(sel, map[string]string{"env": "prd"}) {
		t.Fatal("In + DoesNotExist must match")
	}
	if selectorMatches(sel, map[string]string{"env": "dev"}) {
		t.Fatal("env not in set must not match")
	}
	if selectorMatches(sel, map[string]string{"env": "prd", "canary": "true"}) {
		t.Fatal("existing forbidden key must not match")
	}
}

func TestExplainDeniedAgreesInNumber(t *testing.T) {
	one := Explain(DirectionVerdict{Isolated: true, Policies: []PolicyRef{{Name: "a"}}}, "ingress")
	if one != "ingress DENIED - 1 policy selects the pod, none admits this peer/port" {
		t.Fatalf("singular: %q", one)
	}
	two := Explain(DirectionVerdict{Isolated: true, Policies: []PolicyRef{{Name: "a"}, {Name: "b"}}}, "ingress")
	if two != "ingress DENIED - 2 policies select the pod, none admits this peer/port" {
		t.Fatalf("plural: %q", two)
	}
}

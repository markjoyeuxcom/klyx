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

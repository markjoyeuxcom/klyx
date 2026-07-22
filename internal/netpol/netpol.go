// Package netpol statically evaluates Kubernetes NetworkPolicies for one
// pod-to-pod question: "can src reach dst on port P?" - the connectivity twin
// of the RBAC can-i lens. It mirrors the API semantics: a pod is isolated in
// a direction only when at least one policy selects it for that direction;
// isolated traffic is allowed iff any selecting policy has a rule admitting
// the peer AND the port.
//
// Honest scope: this simulates networking.k8s.io/v1 NetworkPolicy ONLY.
// CNI-specific policies (CiliumNetworkPolicy, Calico), L7 rules, and node
// firewalls are invisible here - the fleet layer counts them and the UI says
// so. ipBlock peers never match a pod-to-pod query (pod IPs are not known
// statically); a policy admitting only ipBlocks reads as not admitting.
package netpol

import (
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Endpoint is one side of the question: a pod identified by namespace + labels.
type Endpoint struct {
	Namespace string
	PodLabels map[string]string
}

// Query is the connectivity question.
type Query struct {
	Src      Endpoint
	Dst      Endpoint
	Port     int    // 0 = any port
	Protocol string // "TCP" (default) | "UDP" | "SCTP"
}

// PolicyRef names a policy that participated in the verdict.
type PolicyRef struct {
	Namespace string
	Name      string
	Admits    bool // selecting policy that admits (true) or merely selects (false)
}

// DirectionVerdict is one direction's result.
type DirectionVerdict struct {
	Isolated bool        // any policy selects the pod for this direction
	Allowed  bool        // !Isolated, or an admitting rule exists
	Policies []PolicyRef // the selecting policies, admitters first
}

// Verdict is the full answer. Allowed = egress allowed AND ingress allowed.
type Verdict struct {
	Allowed bool
	Ingress DirectionVerdict // evaluated on the dst side
	Egress  DirectionVerdict // evaluated on the src side
}

// NamespaceLabels maps namespace name -> its labels (for namespaceSelector
// matching). The kubernetes.io/metadata.name label is guaranteed by the API
// server; callers pass real labels and this package adds nothing.
type NamespaceLabels map[string]map[string]string

// Evaluate answers the query given ALL policies from the src and dst
// namespaces (extra namespaces are ignored via the namespace check).
func Evaluate(q Query, policies []networkingv1.NetworkPolicy, nsLabels NamespaceLabels) Verdict {
	proto := q.Protocol
	if proto == "" {
		proto = "TCP"
	}
	v := Verdict{
		Ingress: evalDirection(q.Dst, q.Src, q.Port, proto, policies, nsLabels, networkingv1.PolicyTypeIngress),
		Egress:  evalDirection(q.Src, q.Dst, q.Port, proto, policies, nsLabels, networkingv1.PolicyTypeEgress),
	}
	v.Allowed = v.Ingress.Allowed && v.Egress.Allowed
	return v
}

// evalDirection evaluates one direction: subject is the pod whose policies
// apply (dst for ingress, src for egress); peer is the other endpoint.
func evalDirection(subject, peer Endpoint, port int, proto string,
	policies []networkingv1.NetworkPolicy, nsLabels NamespaceLabels, dir networkingv1.PolicyType) DirectionVerdict {

	out := DirectionVerdict{}
	var admitters, selectors []PolicyRef

	for i := range policies {
		p := &policies[i]
		if p.Namespace != subject.Namespace {
			continue
		}
		if !hasPolicyType(p, dir) {
			continue
		}
		if !selectorMatches(&p.Spec.PodSelector, subject.PodLabels) {
			continue
		}
		out.Isolated = true
		if policyAdmits(p, peer, port, proto, nsLabels, dir) {
			admitters = append(admitters, PolicyRef{Namespace: p.Namespace, Name: p.Name, Admits: true})
		} else {
			selectors = append(selectors, PolicyRef{Namespace: p.Namespace, Name: p.Name})
		}
	}
	out.Policies = append(admitters, selectors...)
	out.Allowed = !out.Isolated || len(admitters) > 0
	return out
}

// hasPolicyType mirrors the API default: an absent policyTypes means Ingress,
// plus Egress when egress rules exist.
func hasPolicyType(p *networkingv1.NetworkPolicy, dir networkingv1.PolicyType) bool {
	if len(p.Spec.PolicyTypes) > 0 {
		for _, t := range p.Spec.PolicyTypes {
			if t == dir {
				return true
			}
		}
		return false
	}
	if dir == networkingv1.PolicyTypeIngress {
		return true
	}
	return len(p.Spec.Egress) > 0
}

func policyAdmits(p *networkingv1.NetworkPolicy, peer Endpoint, port int, proto string, nsLabels NamespaceLabels, dir networkingv1.PolicyType) bool {
	if dir == networkingv1.PolicyTypeIngress {
		for _, rule := range p.Spec.Ingress {
			if peersMatch(rule.From, p.Namespace, peer, nsLabels) && portsMatch(rule.Ports, port, proto) {
				return true
			}
		}
		return false
	}
	for _, rule := range p.Spec.Egress {
		if peersMatch(rule.To, p.Namespace, peer, nsLabels) && portsMatch(rule.Ports, port, proto) {
			return true
		}
	}
	return false
}

// peersMatch: an EMPTY peer list admits everything (all pods, all namespaces).
// A podSelector without namespaceSelector scopes to the POLICY's namespace.
// ipBlock never matches a pod-to-pod query.
func peersMatch(peers []networkingv1.NetworkPolicyPeer, policyNS string, peer Endpoint, nsLabels NamespaceLabels) bool {
	if len(peers) == 0 {
		return true
	}
	for _, pr := range peers {
		if pr.IPBlock != nil && pr.PodSelector == nil && pr.NamespaceSelector == nil {
			continue // pure ipBlock: unknowable for pod-to-pod, never a match
		}
		nsOK := false
		switch {
		case pr.NamespaceSelector != nil:
			nsOK = selectorMatches(pr.NamespaceSelector, nsLabels[peer.Namespace])
		default:
			nsOK = peer.Namespace == policyNS
		}
		if !nsOK {
			continue
		}
		if pr.PodSelector != nil && !selectorMatches(pr.PodSelector, peer.PodLabels) {
			continue
		}
		return true
	}
	return false
}

// portsMatch: an empty ports list admits every port. Named ports cannot be
// resolved statically and never match a numeric query (honest under-admit).
func portsMatch(ports []networkingv1.NetworkPolicyPort, port int, proto string) bool {
	if len(ports) == 0 {
		return true
	}
	if port == 0 {
		// "any port" query against a port-scoped rule: the rule admits SOME
		// port, which satisfies an unqualified question.
		return true
	}
	for _, pp := range ports {
		ruleProto := "TCP"
		if pp.Protocol != nil {
			ruleProto = string(*pp.Protocol)
		}
		if ruleProto != proto {
			continue
		}
		if pp.Port == nil {
			return true // protocol-only rule: all ports of that protocol
		}
		if pp.Port.Type == 1 { // intstr.String: named port, unresolvable statically
			continue
		}
		p := int(pp.Port.IntVal)
		if pp.EndPort != nil {
			if port >= p && port <= int(*pp.EndPort) {
				return true
			}
			continue
		}
		if port == p {
			return true
		}
	}
	return false
}

// selectorMatches evaluates matchLabels + matchExpressions (In, NotIn, Exists,
// DoesNotExist). A nil selector matches nothing here (callers pass the
// policy's own podSelector, which is non-nil; peer selectors are checked for
// nil before calling). An EMPTY selector matches everything.
func selectorMatches(sel *metav1.LabelSelector, labels map[string]string) bool {
	if sel == nil {
		return false
	}
	for k, v := range sel.MatchLabels {
		if labels[k] != v {
			return false
		}
	}
	for _, ex := range sel.MatchExpressions {
		val, exists := labels[ex.Key]
		switch ex.Operator {
		case metav1.LabelSelectorOpExists:
			if !exists {
				return false
			}
		case metav1.LabelSelectorOpDoesNotExist:
			if exists {
				return false
			}
		case metav1.LabelSelectorOpIn:
			if !exists || !contains(ex.Values, val) {
				return false
			}
		case metav1.LabelSelectorOpNotIn:
			if exists && contains(ex.Values, val) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// Explain renders a one-line human verdict for a direction.
func Explain(d DirectionVerdict, direction string) string {
	if !d.Isolated {
		return fmt.Sprintf("%s allowed - no policy selects the pod (not isolated)", direction)
	}
	if d.Allowed {
		return fmt.Sprintf("%s allowed by policy", direction)
	}
	return fmt.Sprintf("%s DENIED - %d polic%s select the pod, none admits this peer/port", direction, len(d.Policies), pluralY(len(d.Policies)))
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

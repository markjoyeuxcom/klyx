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
	"sort"
	"strconv"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
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

// Verdict is the full answer. Allowed = egress allowed AND ingress allowed on
// the SAME port: for an any-port query, some port of the protocol must be
// admitted in both directions.
type Verdict struct {
	Allowed bool
	Ingress DirectionVerdict // evaluated on the dst side
	Egress  DirectionVerdict // evaluated on the src side
	// Note qualifies an any-port verdict: the ports reachable both ways, or
	// why two individually-admitting directions still block.
	Note string
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
	in, inPorts := evalDirection(q.Dst, q.Src, q.Port, proto, policies, nsLabels, networkingv1.PolicyTypeIngress)
	eg, egPorts := evalDirection(q.Src, q.Dst, q.Port, proto, policies, nsLabels, networkingv1.PolicyTypeEgress)
	v := Verdict{Ingress: in, Egress: eg, Allowed: in.Allowed && eg.Allowed}
	if q.Port == 0 && v.Allowed {
		// Each direction admits SOME port; reachability needs one port admitted
		// both ways (review 2026-10-03: egress TCP/80 + ingress TCP/443 read
		// allowed).
		common := inPorts.intersect(egPorts)
		switch {
		case len(common) == 0:
			v.Allowed = false
			v.Note = fmt.Sprintf("ingress and egress each admit some %s port, but no single port is admitted both ways", proto)
		case !common.full():
			v.Note = fmt.Sprintf("reachable only on %s %s", proto, common)
		}
	}
	return v
}

// evalDirection evaluates one direction: subject is the pod whose policies
// apply (dst for ingress, src for egress); peer is the other endpoint. It also
// returns the ports of proto the direction admits for this peer (all ports
// when the subject is not isolated).
func evalDirection(subject, peer Endpoint, port int, proto string,
	policies []networkingv1.NetworkPolicy, nsLabels NamespaceLabels, dir networkingv1.PolicyType) (DirectionVerdict, portSet) {

	out := DirectionVerdict{}
	var admitters, selectors []PolicyRef
	var admitted portSet

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
		ps := policyPorts(p, peer, proto, nsLabels, dir)
		if admitsQuery(ps, port) {
			admitters = append(admitters, PolicyRef{Namespace: p.Namespace, Name: p.Name, Admits: true})
			admitted = admitted.union(ps)
		} else {
			selectors = append(selectors, PolicyRef{Namespace: p.Namespace, Name: p.Name})
		}
	}
	out.Policies = append(admitters, selectors...)
	out.Allowed = !out.Isolated || len(admitters) > 0
	if !out.Isolated {
		admitted = allPorts
	}
	return out, admitted
}

// admitsQuery: a specific port must be in the set; "any port" (0) needs the
// set to hold at least one port of the protocol.
func admitsQuery(ps portSet, port int) bool {
	if port == 0 {
		return len(ps) > 0
	}
	return ps.has(port)
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

// policyPorts is the set of proto ports policy p admits for peer in direction
// dir: the union over its rules whose peers match.
func policyPorts(p *networkingv1.NetworkPolicy, peer Endpoint, proto string, nsLabels NamespaceLabels, dir networkingv1.PolicyType) portSet {
	var out portSet
	if dir == networkingv1.PolicyTypeIngress {
		for _, rule := range p.Spec.Ingress {
			if peersMatch(rule.From, p.Namespace, peer, nsLabels) {
				out = out.union(rulePorts(rule.Ports, proto))
			}
		}
		return out
	}
	for _, rule := range p.Spec.Egress {
		if peersMatch(rule.To, p.Namespace, peer, nsLabels) {
			out = out.union(rulePorts(rule.Ports, proto))
		}
	}
	return out
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

// rulePorts is the set of proto ports a rule's ports list admits. An empty
// list admits every port of every protocol; an entry admits only its own
// protocol (TCP when unset). Named ports cannot be resolved statically and
// admit nothing (honest under-admit).
func rulePorts(ports []networkingv1.NetworkPolicyPort, proto string) portSet {
	if len(ports) == 0 {
		return allPorts
	}
	var out portSet
	for _, pp := range ports {
		ruleProto := "TCP"
		if pp.Protocol != nil {
			ruleProto = string(*pp.Protocol)
		}
		if ruleProto != proto {
			continue
		}
		if pp.Port == nil {
			return allPorts // protocol-only rule: all ports of that protocol
		}
		if pp.Port.Type == intstr.String {
			continue
		}
		lo := int(pp.Port.IntVal)
		hi := lo
		if pp.EndPort != nil && int(*pp.EndPort) > lo {
			hi = int(*pp.EndPort)
		}
		out = out.union(portSet{{lo, hi}})
	}
	return out
}

// portSet is a set of ports as sorted, disjoint, non-adjacent inclusive
// ranges. nil is the empty set.
type portSet []portRange

type portRange struct{ lo, hi int }

var allPorts = portSet{{1, 65535}}

func (s portSet) full() bool { return len(s) == 1 && s[0] == allPorts[0] }

func (s portSet) has(p int) bool {
	for _, r := range s {
		if p >= r.lo && p <= r.hi {
			return true
		}
	}
	return false
}

func (s portSet) union(o portSet) portSet {
	all := append(append(portSet{}, s...), o...)
	sort.Slice(all, func(i, j int) bool { return all[i].lo < all[j].lo })
	var out portSet
	for _, r := range all {
		if n := len(out); n > 0 && r.lo <= out[n-1].hi+1 {
			if r.hi > out[n-1].hi {
				out[n-1].hi = r.hi
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

func (s portSet) intersect(o portSet) portSet {
	var out portSet
	for i, j := 0, 0; i < len(s) && j < len(o); {
		lo, hi := max(s[i].lo, o[j].lo), min(s[i].hi, o[j].hi)
		if lo <= hi {
			out = append(out, portRange{lo, hi})
		}
		if s[i].hi < o[j].hi {
			i++
		} else {
			j++
		}
	}
	return out
}

// String renders the set compactly ("443, 8000-8999"), capped for display.
func (s portSet) String() string {
	const maxShown = 4
	parts := make([]string, 0, maxShown+1)
	for i, r := range s {
		if i == maxShown {
			parts = append(parts, fmt.Sprintf("+%d more", len(s)-maxShown))
			break
		}
		if r.lo == r.hi {
			parts = append(parts, strconv.Itoa(r.lo))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", r.lo, r.hi))
		}
	}
	return strings.Join(parts, ", ")
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

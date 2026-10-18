package crd

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	factBuilders["networking.k8s.io/ingresses"] = ingressFacts
	factBuilders["networking.k8s.io/networkpolicies"] = networkPolicyFacts
	factBuilders["discovery.k8s.io/endpointslices"] = endpointSliceFacts
	factBuilders["/services"] = serviceFacts
}

// networkLBAddrs collects status.loadBalancer.ingress[].ip/.hostname, shared by
// Ingress and Service (both carry the identical LB status shape).
func networkLBAddrs(u *unstructured.Unstructured) []string {
	var addrs []string
	for _, ing := range gslice(u, "status", "loadBalancer", "ingress") {
		m := asMap(ing)
		if ip, _ := m["ip"].(string); ip != "" {
			addrs = append(addrs, ip)
		} else if h, _ := m["hostname"].(string); h != "" {
			addrs = append(addrs, h)
		}
	}
	return addrs
}

// networkLabelStr renders a matchLabels map as sorted "k=v,k=v" prose.
func networkLabelStr(m map[string]interface{}) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+numStr(m[k]))
	}
	return strings.Join(parts, ",")
}

// networkPeerStr turns a single NetworkPolicy peer (from[]/to[] entry) into
// readable prose: an ipBlock CIDR, a namespaceSelector, and/or a podSelector.
func networkPeerStr(m map[string]interface{}) string {
	if ip := asMap(m["ipBlock"]); ip != nil {
		if cidr, _ := ip["cidr"].(string); cidr != "" {
			s := "cidr " + cidr
			if ex := stringSlice(asSlice(ip["except"])); len(ex) > 0 {
				s += " except " + strings.Join(ex, ",")
			}
			return s
		}
	}
	var parts []string
	if nsSel, ok := m["namespaceSelector"]; ok {
		nm := asMap(nsSel)
		ml := asMap(nm["matchLabels"])
		if name, _ := ml["kubernetes.io/metadata.name"].(string); name != "" {
			parts = append(parts, "ns="+name)
		} else if len(ml) == 0 && len(asSlice(nm["matchExpressions"])) == 0 {
			parts = append(parts, "all ns")
		} else {
			parts = append(parts, "ns "+networkLabelStr(ml))
		}
	}
	if podSel, ok := m["podSelector"]; ok {
		pm := asMap(podSel)
		ml := asMap(pm["matchLabels"])
		if len(ml) == 0 {
			parts = append(parts, "all pods")
		} else {
			parts = append(parts, "pods "+networkLabelStr(ml))
		}
	}
	return strings.Join(parts, " ")
}

// networkPortsStr renders a NetworkPolicy/EndpointSlice port list as "TCP/9090".
func networkPortsStr(ports []interface{}) string {
	var out []string
	for _, p := range ports {
		m := asMap(p)
		proto, _ := m["protocol"].(string)
		if proto == "" {
			proto = "TCP"
		}
		s := proto
		if portStr := numStr(m["port"]); portStr != "" {
			s += "/" + portStr
		}
		if ep := numStr(m["endPort"]); ep != "" {
			s += "-" + ep
		}
		out = append(out, s)
	}
	return strings.Join(out, ", ")
}

// networkRuleStr summarizes one ingress/egress rule as "<prefix> <peers> on <ports>".
func networkRuleStr(rule map[string]interface{}, peerKey, prefix string) string {
	var peers []string
	for _, pr := range asSlice(rule[peerKey]) {
		if s := networkPeerStr(asMap(pr)); s != "" {
			peers = append(peers, s)
		}
	}
	var b strings.Builder
	if len(peers) == 0 {
		b.WriteString(prefix + " anywhere")
	} else {
		b.WriteString(prefix + " " + strings.Join(peers, "; "))
	}
	if ports := networkPortsStr(asSlice(rule["ports"])); ports != "" {
		b.WriteString(" on " + ports)
	}
	return b.String()
}

func ingressFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if cls := gstr(u, "spec", "ingressClassName"); cls != "" {
		facts = append(facts, row("", "class", cls, ""))
	}
	rules := gslice(u, "spec", "rules")
	facts = append(facts, well("", "rules", fmt.Sprintf("%d", len(rules)), ""))

	var hosts []string
	var backends []string
	for _, r := range rules {
		rm := asMap(r)
		host, _ := rm["host"].(string)
		if host != "" {
			hosts = append(hosts, host)
		}
		hostPart := host
		if hostPart == "" {
			hostPart = "*"
		}
		for _, p := range asSlice(asMap(rm["http"])["paths"]) {
			pm := asMap(p)
			path, _ := pm["path"].(string)
			svc := asMap(asMap(pm["backend"])["service"])
			name, _ := svc["name"].(string)
			port := asMap(svc["port"])
			portStr := numStr(port["number"])
			if portStr == "" {
				if pn, _ := port["name"].(string); pn != "" {
					portStr = pn
				}
			}
			target := name
			if portStr != "" {
				target += ":" + portStr
			}
			backends = append(backends, strings.Join(strings.Fields(hostPart+" "+path+" -> "+target), " "))
		}
	}
	if len(hosts) > 0 {
		facts = append(facts, row("Routing", "hosts", joinLimited(hosts, 6), ""))
	}
	if len(backends) > 0 {
		facts = append(facts, row("Routing", "backends", joinLimited(backends, 6), ""))
	}

	for _, t := range gslice(u, "spec", "tls") {
		tm := asMap(t)
		secret, _ := tm["secretName"].(string)
		if secret == "" {
			secret = "(default cert)"
		}
		facts = append(facts, row("TLS", secret, joinLimited(stringSlice(asSlice(tm["hosts"])), 6), ""))
	}

	if db := gstr(u, "spec", "defaultBackend", "service", "name"); db != "" {
		facts = append(facts, row("", "default backend", db, ""))
	}

	if addrs := networkLBAddrs(u); len(addrs) > 0 {
		facts = append(facts, row("", "address", joinLimited(addrs, 4), ""))
	} else {
		facts = append(facts, row("", "address", "pending", "warning"))
	}
	return facts
}

func networkPolicyFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	ml := gstrmap(u, "spec", "podSelector", "matchLabels")
	if len(ml) == 0 {
		facts = append(facts, row("", "applies to", "all pods in namespace", ""))
	} else {
		im := make(map[string]interface{}, len(ml))
		for k, v := range ml {
			im[k] = v
		}
		facts = append(facts, row("", "applies to", networkLabelStr(im), ""))
	}
	if types := stringSlice(gslice(u, "spec", "policyTypes")); len(types) > 0 {
		facts = append(facts, row("", "types", strings.Join(types, ", "), ""))
	}
	ingress := gslice(u, "spec", "ingress")
	egress := gslice(u, "spec", "egress")
	facts = append(facts, well("", "ingress rules", fmt.Sprintf("%d", len(ingress)), ""))
	facts = append(facts, well("", "egress rules", fmt.Sprintf("%d", len(egress)), ""))
	for i, r := range ingress {
		facts = append(facts, row("Ingress", fmt.Sprintf("rule %d", i+1), networkRuleStr(asMap(r), "from", "from"), ""))
	}
	for i, r := range egress {
		facts = append(facts, row("Egress", fmt.Sprintf("rule %d", i+1), networkRuleStr(asMap(r), "to", "to"), ""))
	}
	return facts
}

func endpointSliceFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if at := gstr(u, "addressType"); at != "" {
		facts = append(facts, row("", "address type", at, ""))
	}
	endpoints := gslice(u, "endpoints")
	total := len(endpoints)
	ready := 0
	for _, e := range endpoints {
		conds := asMap(asMap(e)["conditions"])
		// A nil ready condition means the endpoint is ready (per the API contract).
		if r, ok := conds["ready"].(bool); ok {
			if r {
				ready++
			}
		} else {
			ready++
		}
	}
	tone := "success"
	if ready < total {
		tone = "warning"
	}
	facts = append(facts, well("", "ready", fmt.Sprintf("%d/%d", ready, total), tone))

	var ports []string
	for _, p := range gslice(u, "ports") {
		pm := asMap(p)
		name, _ := pm["name"].(string)
		proto, _ := pm["protocol"].(string)
		if proto == "" {
			proto = "TCP"
		}
		s := ""
		if name != "" {
			s = name + ":"
		}
		s += numStr(pm["port"]) + "/" + proto
		ports = append(ports, s)
	}
	if len(ports) > 0 {
		facts = append(facts, row("", "ports", joinLimited(ports, 6), ""))
	}

	if svc := u.GetLabels()["kubernetes.io/service-name"]; svc != "" {
		facts = append(facts, row("", "service", svc, ""))
	}
	return facts
}

// serviceFacts is ADDITIVE identity-only detail - the endpoint/backing data
// lives in the typed ServiceBacking section, so this never re-derives it.
func serviceFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	svcType := gstr(u, "spec", "type")
	if svcType != "" {
		facts = append(facts, row("", "type", svcType, ""))
	}
	if cip := gstr(u, "spec", "clusterIP"); cip != "" {
		if cip == "None" {
			facts = append(facts, row("", "cluster IP", "None (headless)", ""))
		} else {
			facts = append(facts, row("", "cluster IP", cip, ""))
		}
	}
	if addrs := networkLBAddrs(u); len(addrs) > 0 {
		facts = append(facts, row("", "LB address", joinLimited(addrs, 4), ""))
	} else if svcType == "LoadBalancer" {
		facts = append(facts, row("", "LB address", "pending", "warning"))
	}
	if cls := gstr(u, "spec", "loadBalancerClass"); cls != "" && svcType == "LoadBalancer" {
		facts = append(facts, row("", "LB class", cls, "")) // coverage S4 §4
	}
	if ext := stringSlice(gslice(u, "spec", "externalIPs")); len(ext) > 0 {
		facts = append(facts, row("", "external IPs", joinLimited(ext, 6), ""))
	}
	if tp := gstr(u, "spec", "externalTrafficPolicy"); tp != "" {
		facts = append(facts, row("", "traffic policy", tp, ""))
	}
	if sa := gstr(u, "spec", "sessionAffinity"); sa != "" && sa != "None" {
		facts = append(facts, row("", "session affinity", sa, ""))
	}
	return facts
}

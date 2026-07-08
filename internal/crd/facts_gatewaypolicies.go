package crd

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	factBuilders["gateway.networking.k8s.io/grpcroutes"] = grpcRouteFacts
	factBuilders["gateway.envoyproxy.io/securitypolicies"] = securityPolicyFacts
	factBuilders["gateway.envoyproxy.io/backendtrafficpolicies"] = backendTrafficPolicyFacts
	factBuilders["gateway.envoyproxy.io/clienttrafficpolicies"] = clientTrafficPolicyFacts
	factBuilders["gateway.networking.k8s.io/backendtlspolicies"] = backendTLSPolicyFacts
}

// policyTargets renders spec.targetRefs[] (kind + name, plus sectionName when
// set) as readable prose, falling back to the singular spec.targetRef. Shared by
// every Gateway/Envoy policy kind, which all attach to a Gateway or Route.
func policyTargets(u *unstructured.Unstructured) []string {
	refs := gslice(u, "spec", "targetRefs")
	if len(refs) == 0 {
		if single, ok, _ := unstructured.NestedMap(u.Object, "spec", "targetRef"); ok {
			refs = []interface{}{single}
		}
	}
	var out []string
	for _, r := range refs {
		m := asMap(r)
		if m == nil {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		kind, _ := m["kind"].(string)
		s := strings.TrimSpace(kind + " " + name)
		if sec, _ := m["sectionName"].(string); sec != "" {
			s += " (" + sec + ")"
		}
		out = append(out, s)
	}
	return out
}

// gwpSpecHas reports whether spec.<key> is present at all - the presence of an
// auth mechanism or a resilience block, not its contents.
func gwpSpecHas(u *unstructured.Unstructured, key string) bool {
	spec := asMap(u.Object["spec"])
	if spec == nil {
		return false
	}
	_, ok := spec[key]
	return ok
}

func grpcRouteFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if hosts := stringSlice(gslice(u, "spec", "hostnames")); len(hosts) > 0 {
		facts = append(facts, row("Routing", "hostnames", joinLimited(hosts, 6), ""))
	} else {
		facts = append(facts, row("Routing", "hostnames", "(all)", ""))
	}
	var parents []string
	for _, p := range gslice(u, "spec", "parentRefs") {
		m := asMap(p)
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		if ns, _ := m["namespace"].(string); ns != "" {
			name = ns + "/" + name
		}
		if sec, _ := m["sectionName"].(string); sec != "" {
			name += " (" + sec + ")"
		}
		parents = append(parents, name)
	}
	if len(parents) > 0 {
		facts = append(facts, row("Routing", "parents", joinLimited(parents, 4), ""))
	}
	rules := gslice(u, "spec", "rules")
	facts = append(facts, well("Routing", "rules", fmt.Sprintf("%d", len(rules)), ""))
	var methods []string
	var backends []string
	for _, r := range rules {
		rm := asMap(r)
		for _, mt := range asSlice(rm["matches"]) {
			method := asMap(asMap(mt)["method"])
			if method == nil {
				continue
			}
			svc, _ := method["service"].(string)
			mth, _ := method["method"].(string)
			if svc != "" || mth != "" {
				methods = append(methods, strings.TrimPrefix(svc+"/"+mth, "/"))
			} else if t, _ := method["type"].(string); t != "" {
				methods = append(methods, "type="+t)
			}
		}
		for _, b := range asSlice(rm["backendRefs"]) {
			bm := asMap(b)
			name, _ := bm["name"].(string)
			if name == "" {
				continue
			}
			s := name
			if p := numStr(bm["port"]); p != "" {
				s += ":" + p
			}
			if w := numStr(bm["weight"]); w != "" {
				s += " (w" + w + ")"
			}
			backends = append(backends, s)
		}
	}
	if len(methods) > 0 {
		facts = append(facts, row("Routing", "method matches", joinLimited(methods, 6), ""))
	}
	if len(backends) > 0 {
		facts = append(facts, row("Routing", "backends", joinLimited(backends, 6), ""))
	}
	return facts
}

func securityPolicyFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if targets := policyTargets(u); len(targets) > 0 {
		facts = append(facts, row("", "target", joinLimited(targets, 4), ""))
	}
	var enforced []string
	for _, k := range []string{"jwt", "oidc", "extAuth", "basicAuth", "apiKeyAuth", "cors", "authorization"} {
		if gwpSpecHas(u, k) {
			enforced = append(enforced, k)
		}
	}
	if len(enforced) > 0 {
		facts = append(facts, row("", "enforces", strings.Join(enforced, " + "), ""))
	}
	if gwpSpecHas(u, "oidc") {
		if issuer := gstr(u, "spec", "oidc", "provider", "issuer"); issuer != "" {
			v := issuer
			if cid := gstr(u, "spec", "oidc", "clientID"); cid != "" {
				v += " (client " + cid + ")"
			}
			facts = append(facts, row("Auth", "oidc", v, ""))
		}
	}
	if gwpSpecHas(u, "jwt") {
		var issuers []string
		for _, p := range gslice(u, "spec", "jwt", "providers") {
			if iss, _ := asMap(p)["issuer"].(string); iss != "" {
				issuers = append(issuers, iss)
			}
		}
		if len(issuers) > 0 {
			facts = append(facts, row("Auth", "jwt issuers", joinLimited(issuers, 4), ""))
		}
	}
	return facts
}

func backendTrafficPolicyFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if targets := policyTargets(u); len(targets) > 0 {
		facts = append(facts, row("", "target", joinLimited(targets, 4), ""))
	}
	if rl := gstr(u, "spec", "rateLimit", "type"); rl != "" {
		facts = append(facts, row("Resilience", "rate limit", rl, ""))
	}
	if gwpSpecHas(u, "circuitBreaker") {
		cb := asMap(asMap(u.Object["spec"])["circuitBreaker"])
		var parts []string
		for _, f := range []string{"maxConnections", "maxPendingRequests", "maxParallelRequests"} {
			if v := numStr(cb[f]); v != "" {
				parts = append(parts, f+"="+v)
			}
		}
		if len(parts) > 0 {
			facts = append(facts, row("Resilience", "circuit breaker", strings.Join(parts, " "), ""))
		} else {
			facts = append(facts, row("Resilience", "circuit breaker", "on", ""))
		}
	}
	if n, ok := gint(u, "spec", "retry", "numRetries"); ok {
		facts = append(facts, well("Resilience", "retries", fmt.Sprintf("%d", n), ""))
	}
	if lb := gstr(u, "spec", "loadBalancer", "type"); lb != "" {
		facts = append(facts, row("Resilience", "lb", lb, ""))
	}
	if gwpSpecHas(u, "timeout") {
		facts = append(facts, row("Resilience", "timeouts", "set", ""))
	}
	return facts
}

func clientTrafficPolicyFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if targets := policyTargets(u); len(targets) > 0 {
		facts = append(facts, row("", "target", joinLimited(targets, 4), ""))
	}
	if gwpSpecHas(u, "tls") {
		var parts []string
		if mv := gstr(u, "spec", "tls", "minVersion"); mv != "" {
			parts = append(parts, "minVersion="+mv)
		}
		tls := asMap(asMap(u.Object["spec"])["tls"])
		if _, ok := tls["clientValidation"]; ok {
			parts = append(parts, "clientValidation")
		}
		if len(parts) > 0 {
			facts = append(facts, row("TLS", "client TLS", strings.Join(parts, " "), ""))
		}
	}
	if gwpSpecHas(u, "timeout") {
		facts = append(facts, row("", "timeouts", "set", ""))
	}
	if gwpSpecHas(u, "tcpKeepalive") {
		facts = append(facts, row("", "keepalive", "on", ""))
	}
	if gwpSpecHas(u, "http2") {
		facts = append(facts, well("", "http2", "on", ""))
	}
	return facts
}

func backendTLSPolicyFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact
	if targets := policyTargets(u); len(targets) > 0 {
		facts = append(facts, row("", "targets", joinLimited(targets, 4), ""))
	}
	if sni := gstr(u, "spec", "validation", "hostname"); sni != "" {
		facts = append(facts, row("TLS", "SNI hostname", sni, ""))
	}
	var caNames []string
	for _, c := range gslice(u, "spec", "validation", "caCertificateRefs") {
		if name, _ := asMap(c)["name"].(string); name != "" {
			caNames = append(caNames, name)
		}
	}
	switch {
	case len(caNames) > 0:
		facts = append(facts, row("TLS", "CA", joinLimited(caNames, 4), ""))
	case gstr(u, "spec", "validation", "wellKnownCACertificates") == "System":
		facts = append(facts, row("TLS", "CA", "System CAs", ""))
	}
	return facts
}

package crd

import (
	"fmt"
	neturl "net/url"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/urlscrub"
)

func init() {
	factBuilders["admissionregistration.k8s.io/validatingwebhookconfigurations"] = validatingWebhookFacts
	factBuilders["admissionregistration.k8s.io/mutatingwebhookconfigurations"] = mutatingWebhookFacts
}

// webhookFacts renders the shared data path of a webhook configuration: how many
// webhooks it fans out to, how many are fail-closed (the landmine that wedges the
// apiserver when the webhook backend is down), whether any dial an external URL,
// and a per-webhook row naming its target endpoint + policy + timeout.
func webhookFacts(u *unstructured.Unstructured) []Fact {
	webhooks := gslice(u, "webhooks")
	var facts []Fact
	facts = append(facts, well("", "webhooks", fmt.Sprintf("%d", len(webhooks)), ""))

	failClosed := 0
	external := false
	var rows []Fact
	for _, w := range webhooks {
		m := asMap(w)
		if m == nil {
			continue
		}
		name, _ := m["name"].(string)
		policy, _ := m["failurePolicy"].(string)
		if policy == "Fail" {
			failClosed++
		}

		cc := asMap(m["clientConfig"])
		target := ""
		if url, _ := cc["url"].(string); url != "" {
			// the origin only, and whether a path is set (coverage S3a §8)
			external = true
			target = urlscrub.OriginOf(url)
			if u, err := neturl.Parse(url); err == nil && u.Path != "" && u.Path != "/" {
				target += " (path set)"
			}
		} else if svc := asMap(cc["service"]); svc != nil {
			ns, _ := svc["namespace"].(string)
			sn, _ := svc["name"].(string)
			target = strings.TrimPrefix(ns+"/"+sn, "/")
			if port := numStr(svc["port"]); port != "" {
				target += ":" + port
			}
			if path, _ := svc["path"].(string); path != "" {
				target += " (path set)"
			}
		}

		var attrs []string
		if policy != "" {
			attrs = append(attrs, policy)
		}
		if to := numStr(m["timeoutSeconds"]); to != "" {
			attrs = append(attrs, to+"s")
		}
		val := target
		if len(attrs) > 0 {
			if val != "" {
				val += " "
			}
			val += "(" + strings.Join(attrs, ", ") + ")"
		}
		if name == "" {
			name = "(unnamed)"
		}
		rows = append(rows, row("Webhooks", name, strings.TrimSpace(val), ""))
	}

	failTone := ""
	if failClosed > 0 {
		failTone = "warning"
	}
	facts = append(facts, well("", "fail-closed", fmt.Sprintf("%d", failClosed), failTone))
	if external {
		facts = append(facts, well("", "external endpoint", "yes", "info"))
	}
	facts = append(facts, rows...)
	return facts
}

func validatingWebhookFacts(u *unstructured.Unstructured) []Fact {
	return webhookFacts(u)
}

func mutatingWebhookFacts(u *unstructured.Unstructured) []Fact {
	facts := webhookFacts(u)
	var policies []string
	seen := map[string]bool{}
	for _, w := range gslice(u, "webhooks") {
		m := asMap(w)
		if m == nil {
			continue
		}
		if rp, _ := m["reinvocationPolicy"].(string); rp != "" && !seen[rp] {
			seen[rp] = true
			policies = append(policies, rp)
		}
	}
	if len(policies) > 0 {
		facts = append(facts, row("", "reinvocation", strings.Join(policies, ", "), ""))
	}
	return facts
}

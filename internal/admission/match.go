package admission

import "strings"

// Candidates (coverage S3b §3): the webhooks and policies whose rules could
// match one API request. The exact pass follows the API server's
// rules.Matcher (scope, operation, group, version, resource with its
// subresource forms), held to it by an oracle. Under matchPolicy
// Equivalent (the default) the API server also matches the request's
// equivalent resources, which Klyx cannot read: a rule naming the request's
// resource (or "*") in any group and version may match through one, and
// is listed so; a rule whose operations and scope match but whose
// resources name something else could match only through an alias, and is
// counted, never listed. Selectors, match conditions, excluded rules and
// bindings only narrow a match: they are not evaluated.

// Request is the API request a write path declares (S3b §3.1).
type Request struct {
	// Operation: CREATE, UPDATE, DELETE or CONNECT.
	Operation                             string
	Group, Version, Resource, Subresource string
	// Namespace: "" for a cluster-scoped object; the namespace object
	// carries its own name, as the API server's attributes do.
	Namespace string
}

// Why a candidate may match, when its exact rules do not prove it.
const (
	MayEquivalent = "may match through an equivalent resource"
	MayInexact    = "may match: a value could not be kept exactly"
	MayUnread     = "may match: not every rule was read"
)

// Candidate kinds.
const (
	KindValidatingWebhook = "ValidatingWebhook"
	KindMutatingWebhook   = "MutatingWebhook"
	KindValidatingPolicy  = "ValidatingAdmissionPolicy"
	KindMutatingPolicy    = "MutatingAdmissionPolicy"
)

// Candidate is one webhook or policy whose rules could match.
type Candidate struct {
	Kind string
	// Config is a webhook's configuration ("" for a policy).
	Config string
	Name   string
	// Why is "" when an exact rule matches, else a May constant.
	Why string
}

// Candidates are one request's candidates.
type Candidates struct {
	List []Candidate
	// Aliased are the distinct webhooks and policies, not listed, whose
	// Equivalent rules match the operation and scope on other resources;
	// Aliases counts them.
	Aliased []Candidate
	Aliases int
	// Unread: a configuration's webhooks were cut, so some are not read.
	Unread bool
	// Exempt: the request is on an admission configuration kind itself,
	// which no webhook or policy sees.
	Exempt bool
}

var exemptResources = map[string]bool{
	"validatingwebhookconfigurations": true, "mutatingwebhookconfigurations": true, "validatingadmissionpolicies": true,
	"validatingadmissionpolicybindings": true, "mutatingadmissionpolicies": true, "mutatingadmissionpolicybindings": true,
}

func exactOrWildcard(items []string, v string) bool {
	for _, it := range items {
		if it == "*" || it == v {
			return true
		}
	}
	return false
}

func splitResource(rs string) (res, sub string) {
	res, sub, _ = strings.Cut(rs, "/")
	return res, sub
}

func (q Request) isNamespaceObject() bool {
	return q.Group == "" && q.Version == "v1" && q.Resource == "namespaces"
}

func scopeMatches(scope string, q Request) bool {
	switch scope {
	case "", "*":
		return true
	case "Namespaced":
		return !q.isNamespaceObject() && q.Namespace != ""
	case "Cluster":
		return q.isNamespaceObject() || q.Namespace == ""
	}
	return false
}

func operationMatches(ops []string, op string) bool {
	for _, o := range ops {
		if o == "*" || o == op {
			return true
		}
	}
	return false
}

// resourceNamed: some entry names the request's resource and subresource
// (or a wildcard for either).
func resourceNamed(resources []string, q Request) bool {
	for _, rs := range resources {
		res, sub := splitResource(rs)
		if (res == "*" || res == q.Resource) && (sub == "*" || sub == q.Subresource) {
			return true
		}
	}
	return false
}

// RuleMatches is rules.Matcher's exact pass.
func RuleMatches(r RuleIDs, q Request) bool {
	return scopeMatches(r.Scope, q) && operationMatches(r.Operations, q.Operation) && exactOrWildcard(r.Groups, q.Group) &&
		exactOrWildcard(r.Versions, q.Version) && resourceNamed(r.Resources, q)
}

// ruleReading is one rule set's reading of a request: an exact match; else
// a may-match reason (a rule naming the request's resource under
// Equivalent, a value not kept exactly, rules not read); else whether an
// Equivalent rule matching the operation and scope on other resources
// could match through an alias.
func ruleReading(rules []Rule, ruleCount int, equivalent bool, q Request) (why string, matched, alias bool) {
	named, lost := false, false
	for _, r := range rules {
		ids := r.IDs()
		if RuleMatches(ids, q) {
			return "", true, false
		}
		if r.Inexact || r.Cut {
			// what was not kept may be what matches: said as such, per rule
			lost = true
			continue
		}
		if equivalent && scopeMatches(ids.Scope, q) && operationMatches(ids.Operations, q.Operation) {
			if resourceNamed(ids.Resources, q) {
				named = true
			} else {
				alias = true
			}
		}
	}
	switch {
	case named:
		return MayEquivalent, true, false
	case lost:
		return MayInexact, true, false
	case ruleCount > len(rules):
		return MayUnread, true, false
	}
	return "", false, alias
}

func equivalentPolicy(p string) bool { return p == "" || p == "Equivalent" }

// Match lists the candidates for one request among the configurations and
// policies read.
func Match(q Request, configs []*Config, policies []*Policy) Candidates {
	var out Candidates
	if exemptResources[q.Resource] && q.Group == Group {
		out.Exempt = true
		return out
	}
	for _, c := range configs {
		if c.Cut {
			out.Unread = true
		}
		kind := KindValidatingWebhook
		if c.Kind == Mutating {
			kind = KindMutatingWebhook
		}
		for _, w := range c.Webhooks {
			why, matched, alias := ruleReading(w.Rules, w.RuleCount, equivalentPolicy(w.MatchPolicy), q)
			switch {
			case matched:
				out.List = append(out.List, Candidate{Kind: kind, Config: c.Name, Name: w.Name, Why: why})
			case alias:
				out.Aliased = append(out.Aliased, Candidate{Kind: kind, Config: c.Name, Name: w.Name})
			}
		}
	}
	for _, p := range policies {
		kind := KindValidatingPolicy
		if p.Kind == Mutating {
			kind = KindMutatingPolicy
		}
		why, matched, alias := ruleReading(p.MatchRules, p.MatchRuleCount, equivalentPolicy(p.MatchPolicy), q)
		switch {
		case matched:
			out.List = append(out.List, Candidate{Kind: kind, Name: p.Name, Why: why})
		case alias:
			out.Aliased = append(out.Aliased, Candidate{Kind: kind, Name: p.Name})
		}
	}
	out.Aliases = len(out.Aliased)
	return out
}

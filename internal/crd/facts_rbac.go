package crd

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func init() {
	factBuilders["rbac.authorization.k8s.io/roles"] = roleFacts
	factBuilders["rbac.authorization.k8s.io/clusterroles"] = clusterRoleFacts
	factBuilders["rbac.authorization.k8s.io/rolebindings"] = roleBindingFacts
	factBuilders["rbac.authorization.k8s.io/clusterrolebindings"] = clusterRoleBindingFacts
}

// roleFacts renders a namespaced Role's policy rules.
func roleFacts(u *unstructured.Unstructured) []Fact {
	return rbacRuleFacts(u)
}

// clusterRoleFacts renders a ClusterRole: its rules plus aggregation and
// non-resource URL grants unique to the cluster scope.
func clusterRoleFacts(u *unstructured.Unstructured) []Fact {
	facts := rbacRuleFacts(u)
	// An aggregationRule means this ClusterRole's rules are assembled from other
	// labelled ClusterRoles by the controller - worth flagging so a reader knows
	// the rules list can change without this object being edited.
	if _, ok := u.Object["aggregationRule"].(map[string]interface{}); ok {
		facts = append(facts, well("", "aggregated", "yes", "info"))
	}
	var urls []string
	for _, r := range gslice(u, "rules") {
		urls = append(urls, stringSlice(asSlice(asMap(r)["nonResourceURLs"]))...)
	}
	if len(urls) > 0 {
		facts = append(facts, row("Rules", "nonResourceURLs", joinLimited(urls, 8), ""))
	}
	return facts
}

// rbacRuleFacts is the shared Role/ClusterRole rule reader: a rules count, a
// wildcard red-flag (the top RBAC smell), and a per-rule breakdown.
func rbacRuleFacts(u *unstructured.Unstructured) []Fact {
	rules := gslice(u, "rules")
	facts := []Fact{well("", "rules", fmt.Sprintf("%d", len(rules)), "")}

	if rbacHasWildcard(rules) {
		facts = append(facts, well("", "wildcard", "yes", "danger"))
	}

	for _, r := range rules {
		m := asMap(r)
		groups := stringSlice(asSlice(m["apiGroups"]))
		resources := stringSlice(asSlice(m["resources"]))
		verbs := stringSlice(asSlice(m["verbs"]))
		// Pure non-resource-URL rules carry no apiGroups/resources; they surface
		// via the ClusterRole nonResourceURLs row, so skip them here.
		if len(resources) == 0 {
			continue
		}
		label := rbacGroupsResources(groups, resources)
		facts = append(facts, row("Rules", label, joinLimited(verbs, 8), ""))
	}
	return facts
}

// rbacHasWildcard reports whether any rule grants "*" across verbs, resources,
// or apiGroups - the pattern that quietly becomes cluster-admin.
func rbacHasWildcard(rules []interface{}) bool {
	for _, r := range rules {
		m := asMap(r)
		for _, key := range []string{"verbs", "resources", "apiGroups"} {
			for _, v := range stringSlice(asSlice(m[key])) {
				if v == "*" {
					return true
				}
			}
		}
	}
	return false
}

// rbacGroupsResources renders the "apiGroups/resources" left side of a rule row,
// mapping the empty core group to "core" so it is never blank.
func rbacGroupsResources(groups, resources []string) string {
	g := make([]string, 0, len(groups))
	for _, x := range groups {
		if x == "" {
			x = "core"
		}
		g = append(g, x)
	}
	left := joinLimited(g, 4)
	right := joinLimited(resources, 6)
	if left == "" {
		return right
	}
	return left + "/" + right
}

// roleBindingFacts renders a namespaced RoleBinding's grant.
func roleBindingFacts(u *unstructured.Unstructured) []Fact {
	return rbacBindingFacts(u)
}

// clusterRoleBindingFacts renders a ClusterRoleBinding's grant.
func clusterRoleBindingFacts(u *unstructured.Unstructured) []Fact {
	return rbacBindingFacts(u)
}

// rbacBindingFacts is the shared RoleBinding/ClusterRoleBinding reader: which
// role is granted, and to whom. No dangling-roleRef resolution (deferred).
func rbacBindingFacts(u *unstructured.Unstructured) []Fact {
	var facts []Fact

	// roleRef.kind disambiguates a ClusterRole granted inside a namespace from a
	// namespace-local Role of the same name.
	kind := gstr(u, "roleRef", "kind")
	name := gstr(u, "roleRef", "name")
	if name != "" {
		facts = append(facts, row("", "role", strings.TrimSpace(kind+" "+name), ""))
	}

	subjects := gslice(u, "subjects")
	facts = append(facts, well("", "subjects", fmt.Sprintf("%d", len(subjects)), ""))

	if by := rbacSubjectsByKind(subjects); by != "" {
		facts = append(facts, row("", "by kind", by, ""))
	}

	for _, s := range subjects {
		m := asMap(s)
		sk, _ := m["kind"].(string)
		sn, _ := m["name"].(string)
		if sn == "" {
			continue
		}
		if ns, _ := m["namespace"].(string); ns != "" {
			sn = ns + "/" + sn
		}
		facts = append(facts, row("Subjects", sk, sn, ""))
	}
	return facts
}

// rbacSubjectsByKind summarises the subject list as "2 ServiceAccount, 1 User".
func rbacSubjectsByKind(subjects []interface{}) string {
	counts := map[string]int{}
	for _, s := range subjects {
		if k, _ := asMap(s)["kind"].(string); k != "" {
			counts[k]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return strings.Join(parts, ", ")
}

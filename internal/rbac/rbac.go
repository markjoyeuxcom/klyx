// Package rbac answers the two daily access questions: "can subject X do verb
// Y on resource Z here?" and "who can do verb Y on resource Z?".
//
// The two answers have DIFFERENT authority and the lens must say so:
//   - can-i is delegated to the API server via SubjectAccessReview - the
//     authoritative verdict, covering every configured authorizer (RBAC,
//     webhooks, ABAC). This package only builds the subject identity; the
//     fleet layer performs the review.
//   - who-can is a static enumeration of the RBAC objects, mirroring the API
//     server's RBAC rule semantics. It is honest-by-framing: webhook/ABAC
//     authorizers and impersonation are invisible to it, so it can under-report
//     but never over-report an RBAC grant.
//
// Strictly read-only: SubjectAccessReview is a virtual POST that persists
// nothing, and enumeration only lists.
package rbac

import (
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
)

// CheckSpec is the action under test, in ResourceAttributes vocabulary.
type CheckSpec struct {
	Verb        string
	Group       string // API group; "" is the core group
	Resource    string // lowercase plural, e.g. "deployments"
	Subresource string // e.g. "scale", "log"
	Name        string // "" means "any object" - rules scoped by resourceNames never satisfy it
	Namespace   string // "" means cluster scope / all namespaces (SubjectAccessReview semantics)
}

// Verdict mirrors SubjectAccessReviewStatus.
type Verdict struct {
	Allowed bool
	// Denied means an authorizer explicitly denied (not just "no opinion").
	// Allowed=false Denied=false is the default-deny fallthrough.
	Denied          bool
	Reason          string
	EvaluationError string
}

// Grant is one RBAC path that grants the checked action: subject <- binding <- role.
type Grant struct {
	SubjectKind      string // ServiceAccount | User | Group
	SubjectName      string
	SubjectNamespace string // ServiceAccount subjects only
	BindingKind      string // RoleBinding | ClusterRoleBinding
	BindingNamespace string // RoleBindings only
	BindingName      string
	RoleKind         string // Role | ClusterRole
	RoleName         string
}

// Enumeration is the who-can answer.
type Enumeration struct {
	Grants []Grant
	// ClusterWideOnly reports that the query had no namespace, so only
	// ClusterRoleBindings were evaluated - namespace-scoped RoleBinding grants
	// exist but were not enumerated.
	ClusterWideOnly bool
}

// ServiceAccountUsername is the username the API server assigns a ServiceAccount.
func ServiceAccountUsername(ns, name string) string {
	return "system:serviceaccount:" + ns + ":" + name
}

// ServiceAccountGroups are the groups a ServiceAccount token authenticates
// with; a SubjectAccessReview must carry them or group-bound RBAC (e.g. a
// binding to system:serviceaccounts:flux-system) evaluates as denied.
func ServiceAccountGroups(ns string) []string {
	return []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"}
}

// RuleMatches reports whether one PolicyRule grants the checked action,
// mirroring the API server's RBAC evaluation helpers (verb/apiGroup/resource
// wildcards, "resource/subresource" and "*/subresource" forms, resourceNames).
func RuleMatches(rule rbacv1.PolicyRule, spec CheckSpec) bool {
	if !matchesWildcard(rule.Verbs, spec.Verb) {
		return false
	}
	if !matchesWildcard(rule.APIGroups, spec.Group) {
		return false
	}
	if !resourceMatches(rule.Resources, spec.Resource, spec.Subresource) {
		return false
	}
	if len(rule.ResourceNames) > 0 {
		// A resourceNames rule grants specific objects only: it can never
		// satisfy an any-object query (Name "").
		if spec.Name == "" {
			return false
		}
		found := false
		for _, n := range rule.ResourceNames {
			if n == spec.Name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func matchesWildcard(list []string, v string) bool {
	for _, x := range list {
		if x == "*" || x == v {
			return true
		}
	}
	return false
}

func resourceMatches(ruleResources []string, resource, subresource string) bool {
	combined := resource
	if subresource != "" {
		combined = resource + "/" + subresource
	}
	for _, r := range ruleResources {
		if r == "*" || r == combined {
			return true
		}
		// "*/status" style: any resource's named subresource.
		if subresource != "" && strings.HasPrefix(r, "*/") && r == "*/"+subresource {
			return true
		}
	}
	return false
}

// Enumerate computes who-can from already-listed RBAC objects. The caller
// scopes roles and roleBindings to spec.Namespace (they are ignored when the
// spec has none - the ClusterWideOnly case). A binding whose roleRef resolves
// to nothing grants nothing here (the dangling-roleRef fact on the binding's
// own inspector calls that out); aggregated ClusterRoles need no special
// handling because the controller materializes their .Rules.
func Enumerate(spec CheckSpec, roles []rbacv1.Role, clusterRoles []rbacv1.ClusterRole,
	roleBindings []rbacv1.RoleBinding, clusterRoleBindings []rbacv1.ClusterRoleBinding) Enumeration {

	crRules := make(map[string][]rbacv1.PolicyRule, len(clusterRoles))
	for i := range clusterRoles {
		crRules[clusterRoles[i].Name] = clusterRoles[i].Rules
	}
	roleRules := make(map[string][]rbacv1.PolicyRule, len(roles))
	for i := range roles {
		roleRules[roles[i].Namespace+"/"+roles[i].Name] = roles[i].Rules
	}

	anyMatch := func(rules []rbacv1.PolicyRule) bool {
		for _, r := range rules {
			if RuleMatches(r, spec) {
				return true
			}
		}
		return false
	}

	var grants []Grant
	seen := map[Grant]bool{}
	add := func(g Grant) {
		if !seen[g] {
			seen[g] = true
			grants = append(grants, g)
		}
	}

	for i := range clusterRoleBindings {
		b := &clusterRoleBindings[i]
		if !anyMatch(crRules[b.RoleRef.Name]) {
			continue
		}
		for _, s := range b.Subjects {
			add(Grant{
				SubjectKind: s.Kind, SubjectName: s.Name, SubjectNamespace: s.Namespace,
				BindingKind: "ClusterRoleBinding", BindingName: b.Name,
				RoleKind: "ClusterRole", RoleName: b.RoleRef.Name,
			})
		}
	}

	if spec.Namespace != "" {
		for i := range roleBindings {
			b := &roleBindings[i]
			if b.Namespace != spec.Namespace {
				continue // defensive: caller scopes the list
			}
			roleKind := b.RoleRef.Kind
			var rules []rbacv1.PolicyRule
			if roleKind == "ClusterRole" {
				rules = crRules[b.RoleRef.Name]
			} else {
				roleKind = "Role"
				rules = roleRules[b.Namespace+"/"+b.RoleRef.Name]
			}
			if !anyMatch(rules) {
				continue
			}
			for _, s := range b.Subjects {
				subjNS := s.Namespace
				// A ServiceAccount subject with no namespace defaults to the
				// binding's own (matches serviceAccountBindingRefs).
				if s.Kind == "ServiceAccount" && subjNS == "" {
					subjNS = b.Namespace
				}
				add(Grant{
					SubjectKind: s.Kind, SubjectName: s.Name, SubjectNamespace: subjNS,
					BindingKind: "RoleBinding", BindingNamespace: b.Namespace, BindingName: b.Name,
					RoleKind: roleKind, RoleName: b.RoleRef.Name,
				})
			}
		}
	}

	sort.Slice(grants, func(i, j int) bool {
		a, b := grants[i], grants[j]
		if a.SubjectKind != b.SubjectKind {
			return a.SubjectKind < b.SubjectKind
		}
		if a.SubjectNamespace != b.SubjectNamespace {
			return a.SubjectNamespace < b.SubjectNamespace
		}
		if a.SubjectName != b.SubjectName {
			return a.SubjectName < b.SubjectName
		}
		return a.BindingKind+a.BindingName < b.BindingKind+b.BindingName
	})

	return Enumeration{Grants: grants, ClusterWideOnly: spec.Namespace == ""}
}

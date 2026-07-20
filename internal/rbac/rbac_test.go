package rbac

import (
	"reflect"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestServiceAccountIdentity(t *testing.T) {
	if got := ServiceAccountUsername("flux-system", "kustomize-controller"); got != "system:serviceaccount:flux-system:kustomize-controller" {
		t.Fatalf("username: %q", got)
	}
	want := []string{"system:serviceaccounts", "system:serviceaccounts:flux-system", "system:authenticated"}
	if got := ServiceAccountGroups("flux-system"); !reflect.DeepEqual(got, want) {
		t.Fatalf("groups: %v", got)
	}
}

func rule(verbs, groups, resources []string, names ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{Verbs: verbs, APIGroups: groups, Resources: resources, ResourceNames: names}
}

func TestRuleMatches(t *testing.T) {
	deployGet := CheckSpec{Verb: "get", Group: "apps", Resource: "deployments"}
	cases := []struct {
		name string
		rule rbacv1.PolicyRule
		spec CheckSpec
		want bool
	}{
		{"exact", rule([]string{"get"}, []string{"apps"}, []string{"deployments"}), deployGet, true},
		{"verb wildcard", rule([]string{"*"}, []string{"apps"}, []string{"deployments"}), deployGet, true},
		{"group wildcard", rule([]string{"get"}, []string{"*"}, []string{"deployments"}), deployGet, true},
		{"resource wildcard", rule([]string{"get"}, []string{"apps"}, []string{"*"}), deployGet, true},
		{"wrong verb", rule([]string{"list"}, []string{"apps"}, []string{"deployments"}), deployGet, false},
		{"wrong group", rule([]string{"get"}, []string{""}, []string{"deployments"}), deployGet, false},
		{"wrong resource", rule([]string{"get"}, []string{"apps"}, []string{"statefulsets"}), deployGet, false},
		{"core group empty string", rule([]string{"get"}, []string{""}, []string{"pods"}),
			CheckSpec{Verb: "get", Group: "", Resource: "pods"}, true},
		{"subresource needs the combined form", rule([]string{"get"}, []string{""}, []string{"pods"}),
			CheckSpec{Verb: "get", Group: "", Resource: "pods", Subresource: "log"}, false},
		{"combined subresource", rule([]string{"get"}, []string{""}, []string{"pods/log"}),
			CheckSpec{Verb: "get", Group: "", Resource: "pods", Subresource: "log"}, true},
		{"star-slash subresource", rule([]string{"get"}, []string{""}, []string{"*/status"}),
			CheckSpec{Verb: "get", Group: "", Resource: "pods", Subresource: "status"}, true},
		{"resource star covers subresources", rule([]string{"get"}, []string{""}, []string{"*"}),
			CheckSpec{Verb: "get", Group: "", Resource: "pods", Subresource: "log"}, true},
		// resourceNames: object-scoped grants never satisfy an any-object query.
		{"resourceNames matches the named object", rule([]string{"get"}, []string{""}, []string{"secrets"}, "sops-age"),
			CheckSpec{Verb: "get", Group: "", Resource: "secrets", Name: "sops-age"}, true},
		{"resourceNames rejects another object", rule([]string{"get"}, []string{""}, []string{"secrets"}, "sops-age"),
			CheckSpec{Verb: "get", Group: "", Resource: "secrets", Name: "other"}, false},
		{"resourceNames rejects any-object query", rule([]string{"get"}, []string{""}, []string{"secrets"}, "sops-age"),
			CheckSpec{Verb: "get", Group: "", Resource: "secrets"}, false},
	}
	for _, c := range cases {
		if got := RuleMatches(c.rule, c.spec); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func clusterRole(name string, rules ...rbacv1.PolicyRule) rbacv1.ClusterRole {
	return rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name}, Rules: rules}
}

func role(ns, name string, rules ...rbacv1.PolicyRule) rbacv1.Role {
	return rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Rules: rules}
}

func crb(name, roleName string, subjects ...rbacv1.Subject) rbacv1.ClusterRoleBinding {
	return rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: roleName},
		Subjects:   subjects,
	}
}

func rb(ns, name, roleKind, roleName string, subjects ...rbacv1.Subject) rbacv1.RoleBinding {
	return rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		RoleRef:    rbacv1.RoleRef{Kind: roleKind, Name: roleName},
		Subjects:   subjects,
	}
}

func TestEnumerate_NamespacedQuery(t *testing.T) {
	spec := CheckSpec{Verb: "get", Group: "", Resource: "secrets", Namespace: "team-a"}
	crs := []rbacv1.ClusterRole{
		clusterRole("secret-reader", rule([]string{"get", "list"}, []string{""}, []string{"secrets"})),
		clusterRole("pod-reader", rule([]string{"get"}, []string{""}, []string{"pods"})),
	}
	roles := []rbacv1.Role{
		role("team-a", "local-secrets", rule([]string{"get"}, []string{""}, []string{"secrets"})),
	}
	crbs := []rbacv1.ClusterRoleBinding{
		crb("global-secret-readers", "secret-reader", rbacv1.Subject{Kind: "Group", Name: "ops"}),
		crb("pod-only", "pod-reader", rbacv1.Subject{Kind: "User", Name: "dev"}),
		crb("dangling", "no-such-role", rbacv1.Subject{Kind: "User", Name: "ghost"}),
	}
	rbs := []rbacv1.RoleBinding{
		// SA subject with no namespace: defaults to the binding's namespace.
		rb("team-a", "eso-secrets", "Role", "local-secrets", rbacv1.Subject{Kind: "ServiceAccount", Name: "eso"}),
		// RoleBinding to a ClusterRole grants inside the namespace only.
		rb("team-a", "via-clusterrole", "ClusterRole", "secret-reader", rbacv1.Subject{Kind: "User", Name: "alice"}),
		// Another namespace's binding must not leak in.
		rb("team-b", "other-ns", "Role", "local-secrets", rbacv1.Subject{Kind: "User", Name: "bob"}),
	}

	e := Enumerate(spec, roles, crs, rbs, crbs)
	if e.ClusterWideOnly {
		t.Fatal("namespaced query must not report ClusterWideOnly")
	}
	want := []Grant{
		{SubjectKind: "Group", SubjectName: "ops", BindingKind: "ClusterRoleBinding", BindingName: "global-secret-readers", RoleKind: "ClusterRole", RoleName: "secret-reader"},
		{SubjectKind: "ServiceAccount", SubjectNamespace: "team-a", SubjectName: "eso", BindingKind: "RoleBinding", BindingNamespace: "team-a", BindingName: "eso-secrets", RoleKind: "Role", RoleName: "local-secrets"},
		{SubjectKind: "User", SubjectName: "alice", BindingKind: "RoleBinding", BindingNamespace: "team-a", BindingName: "via-clusterrole", RoleKind: "ClusterRole", RoleName: "secret-reader"},
	}
	if !reflect.DeepEqual(e.Grants, want) {
		t.Fatalf("grants:\n got %+v\nwant %+v", e.Grants, want)
	}
}

func TestEnumerate_ClusterWideOnlyWhenNoNamespace(t *testing.T) {
	spec := CheckSpec{Verb: "delete", Group: "", Resource: "nodes"}
	crs := []rbacv1.ClusterRole{clusterRole("node-admin", rule([]string{"*"}, []string{""}, []string{"nodes"}))}
	crbs := []rbacv1.ClusterRoleBinding{crb("node-admins", "node-admin", rbacv1.Subject{Kind: "User", Name: "root"})}
	// RoleBindings are present but MUST NOT be evaluated without a namespace.
	rbs := []rbacv1.RoleBinding{rb("kube-system", "stray", "ClusterRole", "node-admin", rbacv1.Subject{Kind: "User", Name: "stray"})}

	e := Enumerate(spec, nil, crs, rbs, crbs)
	if !e.ClusterWideOnly {
		t.Fatal("no-namespace query must report ClusterWideOnly")
	}
	if len(e.Grants) != 1 || e.Grants[0].SubjectName != "root" {
		t.Fatalf("grants: %+v", e.Grants)
	}
}

func TestEnumerate_DedupsIdenticalGrantPaths(t *testing.T) {
	spec := CheckSpec{Verb: "get", Group: "", Resource: "pods", Namespace: "x"}
	crs := []rbacv1.ClusterRole{clusterRole("pr", rule([]string{"get"}, []string{""}, []string{"pods"}))}
	crbs := []rbacv1.ClusterRoleBinding{
		crb("b1", "pr", rbacv1.Subject{Kind: "User", Name: "u"}, rbacv1.Subject{Kind: "User", Name: "u"}),
	}
	e := Enumerate(spec, nil, crs, nil, crbs)
	if len(e.Grants) != 1 {
		t.Fatalf("duplicate subjects in one binding must dedup: %+v", e.Grants)
	}
}

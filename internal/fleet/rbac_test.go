package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/rbac"
)

func TestAccessCanI_SelfUsesSelfSubjectAccessReview(t *testing.T) {
	typed := k8sfake.NewSimpleClientset()
	var got *authorizationv1.SelfSubjectAccessReview
	typed.PrependReactor("create", "selfsubjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		got = action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		out := got.DeepCopy()
		out.Status = authorizationv1.SubjectAccessReviewStatus{Allowed: true, Reason: "RBAC: allowed by ClusterRoleBinding"}
		return true, out, nil
	})
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	v, err := c.AccessCanI(context.Background(),
		rbac.CheckSpec{Verb: "get", Group: "apps", Resource: "deployments", Namespace: "shop"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Allowed || v.Reason != "RBAC: allowed by ClusterRoleBinding" {
		t.Fatalf("verdict: %+v", v)
	}
	attrs := got.Spec.ResourceAttributes
	if attrs.Verb != "get" || attrs.Group != "apps" || attrs.Resource != "deployments" || attrs.Namespace != "shop" {
		t.Fatalf("attrs did not pass through: %+v", attrs)
	}
}

func TestAccessCanI_ServiceAccountCarriesUserAndGroups(t *testing.T) {
	typed := k8sfake.NewSimpleClientset()
	var got *authorizationv1.SubjectAccessReview
	typed.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		got = action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		out := got.DeepCopy()
		out.Status = authorizationv1.SubjectAccessReviewStatus{Denied: true, Reason: "explicitly denied"}
		return true, out, nil
	})
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	v, err := c.AccessCanI(context.Background(),
		rbac.CheckSpec{Verb: "update", Group: "", Resource: "secrets", Namespace: "team-a"},
		"flux-system", "kustomize-controller")
	if err != nil {
		t.Fatal(err)
	}
	if v.Allowed || !v.Denied || v.Reason != "explicitly denied" {
		t.Fatalf("verdict: %+v", v)
	}
	if got.Spec.User != "system:serviceaccount:flux-system:kustomize-controller" {
		t.Fatalf("user: %q", got.Spec.User)
	}
	// Without the SA's groups, group-bound RBAC would mis-evaluate.
	wantGroup := "system:serviceaccounts:flux-system"
	found := false
	for _, g := range got.Spec.Groups {
		if g == wantGroup {
			found = true
		}
	}
	if !found {
		t.Fatalf("groups must include %q: %v", wantGroup, got.Spec.Groups)
	}
}

func TestAccessCanI_APIErrorSurfaces(t *testing.T) {
	typed := k8sfake.NewSimpleClientset()
	typed.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("boom")
	})
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	if _, err := c.AccessCanI(context.Background(), rbac.CheckSpec{Verb: "get", Resource: "pods"}, "", ""); err == nil {
		t.Fatal("API failure must error, never render as a verdict")
	}
}

func TestAccessWhoCan_EnumeratesAcrossBindingKinds(t *testing.T) {
	typed := k8sfake.NewSimpleClientset(
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "secret-reader"},
			Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"secrets"}}},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "global"},
			RoleRef:    rbacv1.RoleRef{Kind: "ClusterRole", Name: "secret-reader"},
			Subjects:   []rbacv1.Subject{{Kind: "Group", Name: "ops"}},
		},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "local"},
			Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"secrets"}}},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "local-binding"},
			RoleRef:    rbacv1.RoleRef{Kind: "Role", Name: "local"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "eso"}},
		},
	)
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})

	e, err := c.AccessWhoCan(context.Background(), rbac.CheckSpec{Verb: "get", Group: "", Resource: "secrets", Namespace: "team-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Grants) != 2 {
		t.Fatalf("want the CRB + RB grants, got %+v", e.Grants)
	}
	if e.ClusterWideOnly {
		t.Fatal("namespaced query must not be ClusterWideOnly")
	}
}

func TestAccessWhoCan_ListFailureErrorsNeverPartial(t *testing.T) {
	typed := k8sfake.NewSimpleClientset()
	typed.PrependReactor("list", "clusterrolebindings", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	c := NewClusterConn("x", typed, nil, nil, nil, clock.Real{}, config.MetricsConfig{})
	_, err := c.AccessWhoCan(context.Background(), rbac.CheckSpec{Verb: "get", Resource: "secrets"})
	if err == nil || !strings.Contains(err.Error(), "clusterrolebindings") {
		t.Fatalf("a failing list must error naming the list, got %v", err)
	}
}

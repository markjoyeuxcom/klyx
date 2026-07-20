package appbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/moomora/klyx/internal/rbac"
)

type fakeRBACConn struct {
	verdict     rbac.Verdict
	verdictErr  error
	enum        rbac.Enumeration
	enumErr     error
	gotSpec     rbac.CheckSpec
	gotSANS     string
	gotSAName   string
	whoCanCalls int
}

func (f *fakeRBACConn) AccessCanI(_ context.Context, spec rbac.CheckSpec, saNS, saName string) (rbac.Verdict, error) {
	f.gotSpec, f.gotSANS, f.gotSAName = spec, saNS, saName
	return f.verdict, f.verdictErr
}

func (f *fakeRBACConn) AccessWhoCan(_ context.Context, spec rbac.CheckSpec) (rbac.Enumeration, error) {
	f.gotSpec = spec
	f.whoCanCalls++
	return f.enum, f.enumErr
}

func rbacSvc(conn *fakeRBACConn) *RBACService {
	return NewRBACService(func(name string) (RBACConn, bool) {
		if name == "prd" {
			return conn, true
		}
		return nil, false
	})
}

func TestCanIMapsVerdictAndNamesSubject(t *testing.T) {
	conn := &fakeRBACConn{verdict: rbac.Verdict{Allowed: true, Reason: "RBAC allowed"}}
	dto := rbacSvc(conn).CanI("prd", AccessQueryDTO{
		Verb: "update", Group: "apps", Resource: "deployments", Namespace: "shop",
		SANamespace: "flux-system", SAName: "kustomize-controller",
	})
	if dto.Error != "" || !dto.Allowed || dto.Reason != "RBAC allowed" {
		t.Fatalf("dto: %+v", dto)
	}
	if dto.Subject != "system:serviceaccount:flux-system:kustomize-controller" {
		t.Fatalf("subject must name the checked identity: %q", dto.Subject)
	}
	if conn.gotSpec.Verb != "update" || conn.gotSANS != "flux-system" || conn.gotSAName != "kustomize-controller" {
		t.Fatalf("passthrough: %+v %q %q", conn.gotSpec, conn.gotSANS, conn.gotSAName)
	}
}

func TestCanISelfSubject(t *testing.T) {
	conn := &fakeRBACConn{verdict: rbac.Verdict{Allowed: false, Denied: true}}
	dto := rbacSvc(conn).CanI("prd", AccessQueryDTO{Verb: "delete", Resource: "nodes"})
	if dto.Subject != "kubeconfig user (self)" || !dto.Denied {
		t.Fatalf("dto: %+v", dto)
	}
}

func TestCanIValidation(t *testing.T) {
	conn := &fakeRBACConn{}
	svc := rbacSvc(conn)
	cases := []struct {
		q    AccessQueryDTO
		want string
	}{
		{AccessQueryDTO{Resource: "pods"}, "verb is required"},
		{AccessQueryDTO{Verb: "get"}, "resource is required"},
		{AccessQueryDTO{Verb: "get", Resource: "pods", SAName: "x"}, "service account namespace is required"},
		{AccessQueryDTO{Verb: "get", Resource: "pods", SANamespace: "x"}, "service account name is required"},
	}
	for _, c := range cases {
		if dto := svc.CanI("prd", c.q); dto.Error != c.want {
			t.Errorf("%+v: got %q want %q", c.q, dto.Error, c.want)
		}
	}
}

func TestCanIClusterMissAndErrorAreHonest(t *testing.T) {
	conn := &fakeRBACConn{verdictErr: errors.New("boom")}
	svc := rbacSvc(conn)
	if dto := svc.CanI("nope", AccessQueryDTO{Verb: "get", Resource: "pods"}); dto.Error == "" {
		t.Fatal("cluster miss must set Error")
	}
	if dto := svc.CanI("prd", AccessQueryDTO{Verb: "get", Resource: "pods"}); dto.Error != "boom" {
		t.Fatalf("conn error must fold into Error: %+v", dto)
	}
}

func TestWhoCanMapsGrantsAndIgnoresSubject(t *testing.T) {
	conn := &fakeRBACConn{enum: rbac.Enumeration{
		Grants: []rbac.Grant{{
			SubjectKind: "ServiceAccount", SubjectNamespace: "team-a", SubjectName: "eso",
			BindingKind: "RoleBinding", BindingNamespace: "team-a", BindingName: "b",
			RoleKind: "Role", RoleName: "r",
		}},
	}}
	// The SA fields must not fail validation for who-can - it asks "who", not
	// "can this subject" - so a half-filled subject is ignored, not an error.
	dto := rbacSvc(conn).WhoCan("prd", AccessQueryDTO{Verb: "get", Resource: "secrets", Namespace: "team-a", SAName: "half-filled"})
	if dto.Error != "" || len(dto.Grants) != 1 {
		t.Fatalf("dto: %+v", dto)
	}
	g := dto.Grants[0]
	if g.SubjectName != "eso" || g.BindingKind != "RoleBinding" || g.RoleName != "r" {
		t.Fatalf("grant mapping: %+v", g)
	}
}

func TestWhoCanEmptyGrantsIsNeverNil(t *testing.T) {
	conn := &fakeRBACConn{enum: rbac.Enumeration{ClusterWideOnly: true}}
	dto := rbacSvc(conn).WhoCan("prd", AccessQueryDTO{Verb: "get", Resource: "nodes"})
	if dto.Grants == nil || !dto.ClusterWideOnly {
		t.Fatalf("dto: %+v", dto)
	}
}

func TestWhoCanErrorsFoldIn(t *testing.T) {
	conn := &fakeRBACConn{enumErr: errors.New("list clusterroles: forbidden")}
	dto := rbacSvc(conn).WhoCan("prd", AccessQueryDTO{Verb: "get", Resource: "pods"})
	if dto.Error != "list clusterroles: forbidden" {
		t.Fatalf("dto: %+v", dto)
	}
}

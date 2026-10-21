package identity

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/keda"
)

func saGet(sas ...*corev1.ServiceAccount) SALookup {
	return func(ns, name string) (*corev1.ServiceAccount, Read) {
		for _, s := range sas {
			if s.Namespace == ns && s.Name == name {
				return s, ReadFound
			}
		}
		return nil, ReadMissing
	}
}

func kedaSA(ns, name string, ann map[string]string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Annotations: ann}}
}

// Coverage S9 §6.3: KEDA authenticates as its operator pod; aws with
// identityOwner workload borrows the workload ServiceAccount's role.
func TestTriggerAuthSubject(t *testing.T) {
	op := &SARef{Namespace: "keda", Name: "keda-operator"}
	wl := &SARef{Namespace: "team", Name: "wl-identity"}
	get := saGet(kedaSA("keda", "keda-operator", nil), kedaSA("team", "wl-identity", map[string]string{AnnRoleARN: "arn:aws:iam::111122223333:role/klyx-lab-keda"}),
		kedaSA("team", "bare", nil))
	join := func(xs []string) string { return strings.Join(xs, " | ") }

	s := TriggerAuthSubject(&keda.PodIdentity{Provider: "aws", IdentityOwner: "workload"}, op, wl, get)
	if s.SA != op || s.RoleFrom != wl || len(s.Refusals) != 0 || !strings.Contains(join(s.Requested), "from ServiceAccount team/wl-identity") {
		t.Fatalf("aws workload: %+v", s)
	}
	s = TriggerAuthSubject(&keda.PodIdentity{Provider: "aws", IdentityOwner: "workload"}, op, &SARef{Namespace: "team", Name: "bare"}, get)
	if !strings.Contains(join(s.Refusals), "KEDA requires eks.amazonaws.com/role-arn on ServiceAccount team/bare") {
		t.Fatalf("no annotation: %+v", s.Refusals)
	}
	s = TriggerAuthSubject(&keda.PodIdentity{Provider: "aws", IdentityOwner: "workload", RoleARN: "arn:x"}, op, wl, get)
	if !strings.Contains(join(s.Refusals), "roleArn with identityOwner workload") {
		t.Fatalf("roleArn plus workload: %+v", s.Refusals)
	}
	s = TriggerAuthSubject(&keda.PodIdentity{Provider: "aws", RoleARN: "arn:aws:iam::111122223333:role/r"}, op, nil, get)
	if s.Mode != "operator" || s.SA != op || len(s.Requested) != 1 {
		t.Fatalf("aws operator with roleArn: %+v", s)
	}
	s = TriggerAuthSubject(&keda.PodIdentity{Provider: "azure-workload", IdentityOwner: "workload", IdentityID: "00000000-0000-0000-0000-000000000001"}, op, wl, get)
	if !strings.Contains(join(s.Notes), "ignored by azure-workload") || !strings.Contains(join(s.Notes), "federate with the KEDA operator") {
		t.Fatalf("azure: %+v", s.Notes)
	}
	s = TriggerAuthSubject(&keda.PodIdentity{Provider: "gcp"}, nil, nil, get)
	if !strings.Contains(join(s.Unresolved), "handling KEDA operator is unverified") {
		t.Fatalf("an unverified operator: %+v", s)
	}
	if s := TriggerAuthSubject(&keda.PodIdentity{Provider: "azure"}, op, nil, get); len(s.Refusals) == 0 {
		t.Fatal("a removed provider")
	}
}

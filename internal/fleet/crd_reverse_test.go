package fleet

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/crd"
)

func hasRef(refs []crd.RelatedRef, kind, name, relation string) bool {
	for _, r := range refs {
		if r.Kind == kind && r.Name == name && r.Relation == relation {
			return true
		}
	}
	return false
}

func TestPVCConsumerRefs(t *testing.T) {
	mounts := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web"},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name:         "d",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}},
		}}},
	}
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "other"}}
	c := &ClusterConn{typed: fake.NewSimpleClientset(mounts, other)}

	refs := c.pvcConsumerRefs(context.Background(), "app", "data")
	if len(refs) != 1 || !hasRef(refs, "Pod", "web", "used by") {
		t.Fatalf("want the mounting pod as 'used by', got %+v", refs)
	}
}

func TestOwnerPodRefs(t *testing.T) {
	owned := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "app", Name: "j-1",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Job", Name: "j", UID: "job-123"}},
	}}
	unrelated := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "stray"}}
	c := &ClusterConn{typed: fake.NewSimpleClientset(owned, unrelated)}

	refs := c.ownerPodRefs(context.Background(), "app", "job-123", "job pod")
	if len(refs) != 1 || !hasRef(refs, "Pod", "j-1", "job pod") {
		t.Fatalf("want the owned pod, got %+v", refs)
	}
	// Empty UID short-circuits (never lists).
	if refs := c.ownerPodRefs(context.Background(), "app", "", "job pod"); refs != nil {
		t.Fatalf("empty UID must yield nil, got %+v", refs)
	}
}

func TestPDBGuardedRefs(t *testing.T) {
	web := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web-1", Labels: map[string]string{"app": "web"}}}
	db := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "db-1", Labels: map[string]string{"app": "db"}}}
	c := &ClusterConn{typed: fake.NewSimpleClientset(web, db)}

	refs := c.pdbGuardedRefs(context.Background(), "app", map[string]string{"app": "web"})
	if len(refs) != 1 || !hasRef(refs, "Pod", "web-1", "guarded pod") {
		t.Fatalf("want only the selector-matching pod, got %+v", refs)
	}
	// An empty selector guards nothing (never all pods).
	if refs := c.pdbGuardedRefs(context.Background(), "app", nil); refs != nil {
		t.Fatalf("empty selector must yield nil, got %+v", refs)
	}
}

func TestServiceAccountBindingRefs(t *testing.T) {
	rbMatch := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ci", Name: "rb-builder"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "builder"}}, // ns implicit -> binding ns
		RoleRef:    rbacv1.RoleRef{Kind: "Role", Name: "r"},
	}
	rbUser := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ci", Name: "rb-user"},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: "builder"}},
	}
	crbMatch := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "crb-builder"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "builder", Namespace: "ci"}},
	}
	crbOtherNS := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "crb-other"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "builder", Namespace: "other"}}, // wrong ns
	}
	c := &ClusterConn{typed: fake.NewSimpleClientset(rbMatch, rbUser, crbMatch, crbOtherNS)}

	refs := c.serviceAccountBindingRefs(context.Background(), "ci", "builder")
	if !hasRef(refs, "RoleBinding", "rb-builder", "granted by") {
		t.Fatalf("want rb-builder as 'granted by', got %+v", refs)
	}
	if !hasRef(refs, "ClusterRoleBinding", "crb-builder", "granted by") {
		t.Fatalf("want crb-builder as 'granted by', got %+v", refs)
	}
	if hasRef(refs, "RoleBinding", "rb-user", "granted by") {
		t.Fatal("a User subject must not match the ServiceAccount")
	}
	if hasRef(refs, "ClusterRoleBinding", "crb-other", "granted by") {
		t.Fatal("a ClusterRoleBinding subject in another namespace must not match")
	}
}

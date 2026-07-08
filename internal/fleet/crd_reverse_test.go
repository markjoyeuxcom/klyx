package fleet

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

func TestRBACDanglingFacts(t *testing.T) {
	existing := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "reader"}}
	c := &ClusterConn{typed: fake.NewSimpleClientset(existing)}

	// roleRef -> an existing Role: no dangling fact.
	rbOK := &unstructured.Unstructured{Object: map[string]interface{}{
		"roleRef": map[string]interface{}{"kind": "Role", "name": "reader"},
	}}
	if f := c.rbacDanglingFacts(context.Background(), "rbac.authorization.k8s.io", "rolebindings", "ns", rbOK); f != nil {
		t.Fatalf("existing roleRef must not flag dangling: %+v", f)
	}

	// roleRef -> a missing Role: a danger fact.
	rbBad := &unstructured.Unstructured{Object: map[string]interface{}{
		"roleRef": map[string]interface{}{"kind": "Role", "name": "ghost"},
	}}
	f := c.rbacDanglingFacts(context.Background(), "rbac.authorization.k8s.io", "rolebindings", "ns", rbBad)
	if len(f) != 1 || f[0].Tone != "danger" || f[0].Value != "missing: Role/ghost" {
		t.Fatalf("missing roleRef must flag danger, got %+v", f)
	}

	// A non-RBAC kind is never touched.
	if f := c.rbacDanglingFacts(context.Background(), "", "configmaps", "ns", rbBad); f != nil {
		t.Fatalf("non-RBAC kind must be ignored: %+v", f)
	}
}

func deploy(ns, name string, ann map[string]string, spec corev1.PodSpec) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Annotations: ann},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: spec}},
	}
}

func TestReloaderConsumerRefs(t *testing.T) {
	// Explicit annotation lists this ConfigMap by name (comma-separated).
	explicit := deploy("app", "api", map[string]string{
		"configmap.reloader.stakater.com/reload": "other-cm,app-config",
	}, corev1.PodSpec{})
	// Auto mode + the ConfigMap is mounted as a volume -> reloads.
	autoMounted := deploy("app", "web", map[string]string{
		"reloader.stakater.com/auto": "true",
	}, corev1.PodSpec{Volumes: []corev1.Volume{{
		Name:         "cfg",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}},
	}}})
	// Auto mode but does NOT mount this ConfigMap -> must not match.
	autoUnrelated := deploy("app", "worker", map[string]string{
		"reloader.stakater.com/auto": "true",
	}, corev1.PodSpec{Volumes: []corev1.Volume{{
		Name:         "cfg",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "different-cm"}}},
	}}})
	// No Reloader annotation, even though it mounts the ConfigMap -> must not match.
	plain := deploy("app", "legacy", nil, corev1.PodSpec{Volumes: []corev1.Volume{{
		Name:         "cfg",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}},
	}}})
	c := &ClusterConn{typed: fake.NewSimpleClientset(explicit, autoMounted, autoUnrelated, plain)}

	refs := c.reloaderConsumerRefs(context.Background(), "app", "app-config", "ConfigMap", nil)
	if !hasRef(refs, "Deployment", "api", "reloads") {
		t.Errorf("explicit reload annotation must match, got %+v", refs)
	}
	if !hasRef(refs, "Deployment", "web", "reloads") {
		t.Errorf("auto + mounted must match, got %+v", refs)
	}
	if hasRef(refs, "Deployment", "worker", "reloads") {
		t.Error("auto without mounting this ConfigMap must not match")
	}
	if hasRef(refs, "Deployment", "legacy", "reloads") {
		t.Error("mounting without a Reloader annotation must not match")
	}
	if len(refs) != 2 {
		t.Fatalf("want exactly api + web, got %+v", refs)
	}
}

func TestReloaderConsumerRefsSecretEnvFrom(t *testing.T) {
	// Auto mode + the Secret is consumed via envFrom -> reloads.
	autoEnv := deploy("app", "api", map[string]string{
		"reloader.stakater.com/auto": "true",
	}, corev1.PodSpec{Containers: []corev1.Container{{
		Name:    "api",
		EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-secret"}}}},
	}}})
	// The ConfigMap explicit key must NOT match a Secret lookup (wrong annotation key).
	wrongKind := deploy("app", "web", map[string]string{
		"configmap.reloader.stakater.com/reload": "app-secret",
	}, corev1.PodSpec{})
	c := &ClusterConn{typed: fake.NewSimpleClientset(autoEnv, wrongKind)}

	refs := c.reloaderConsumerRefs(context.Background(), "app", "app-secret", "Secret", nil)
	if !hasRef(refs, "Deployment", "api", "reloads") {
		t.Errorf("auto + envFrom secret must match, got %+v", refs)
	}
	if hasRef(refs, "Deployment", "web", "reloads") {
		t.Error("the configmap reload key must not match a Secret lookup")
	}
	if len(refs) != 1 {
		t.Fatalf("want exactly api, got %+v", refs)
	}
}

func TestReloaderConsumerRefsSearchMatch(t *testing.T) {
	// search mode: the workload opts in, and reloads on any mounted CM labeled match=true.
	searchMounted := deploy("app", "api", map[string]string{
		"reloader.stakater.com/search": "true",
	}, corev1.PodSpec{Volumes: []corev1.Volume{{
		Name:         "cfg",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}},
	}}})
	// search-annotated but does NOT mount app-config -> must not match.
	searchUnrelated := deploy("app", "worker", map[string]string{
		"reloader.stakater.com/search": "true",
	}, corev1.PodSpec{Volumes: []corev1.Volume{{
		Name:         "cfg",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "different-cm"}}},
	}}})
	c := &ClusterConn{typed: fake.NewSimpleClientset(searchMounted, searchUnrelated)}

	// Object carries the match label: the search-mode workload that mounts it matches.
	labeled := map[string]string{"reloader.stakater.com/match": "true"}
	refs := c.reloaderConsumerRefs(context.Background(), "app", "app-config", "ConfigMap", labeled)
	if !hasRef(refs, "Deployment", "api", "reloads") {
		t.Errorf("search + match label + mounted must match, got %+v", refs)
	}
	if hasRef(refs, "Deployment", "worker", "reloads") {
		t.Error("search mode must still require the object to be mounted")
	}
	if len(refs) != 1 {
		t.Fatalf("want exactly api, got %+v", refs)
	}

	// Honesty guard: without the match label, search mode matches nothing - even
	// though api mounts the ConfigMap.
	if refs := c.reloaderConsumerRefs(context.Background(), "app", "app-config", "ConfigMap", nil); len(refs) != 0 {
		t.Fatalf("search mode must not match an unlabeled object, got %+v", refs)
	}
}

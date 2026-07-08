package crd

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFacts_Role(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "Role",
		"rules": []interface{}{
			map[string]interface{}{
				"apiGroups": []interface{}{"apps"},
				"resources": []interface{}{"deployments"},
				"verbs":     []interface{}{"get", "list", "watch"},
			},
			map[string]interface{}{
				"apiGroups": []interface{}{""},
				"resources": []interface{}{"pods"},
				"verbs":     []interface{}{"get"},
			},
		},
	}}
	facts := BuildFacts("rbac.authorization.k8s.io", "roles", u)
	if f, ok := factByLabel(facts, "rules"); !ok || f.Value != "2" {
		t.Fatalf("rules fact wrong: %+v", f)
	}
	if _, ok := factByLabel(facts, "wildcard"); ok {
		t.Fatalf("no wildcard rule present, wildcard fact must be absent")
	}
	if f, ok := factByLabel(facts, "apps/deployments"); !ok || f.Value != "get, list, watch" {
		t.Fatalf("per-rule fact wrong: %+v", f)
	}
	// The empty core apiGroup renders as "core", never blank.
	if f, ok := factByLabel(facts, "core/pods"); !ok || f.Value != "get" {
		t.Fatalf("core-group rule fact wrong: %+v", f)
	}
}

func TestBuildFacts_Role_Wildcard(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "Role",
		"rules": []interface{}{
			map[string]interface{}{
				"apiGroups": []interface{}{"*"},
				"resources": []interface{}{"*"},
				"verbs":     []interface{}{"*"},
			},
		},
	}}
	facts := BuildFacts("rbac.authorization.k8s.io", "roles", u)
	f, ok := factByLabel(facts, "wildcard")
	if !ok || f.Value != "yes" || f.Tone != "danger" {
		t.Fatalf("wildcard danger fact wrong: %+v", f)
	}
}

func TestBuildFacts_ClusterRole_AggregatedAndNonResource(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ClusterRole",
		"aggregationRule": map[string]interface{}{
			"clusterRoleSelectors": []interface{}{},
		},
		"rules": []interface{}{
			map[string]interface{}{
				"nonResourceURLs": []interface{}{"/healthz", "/metrics"},
				"verbs":           []interface{}{"get"},
			},
		},
	}}
	facts := BuildFacts("rbac.authorization.k8s.io", "clusterroles", u)
	if f, ok := factByLabel(facts, "aggregated"); !ok || f.Value != "yes" || f.Tone != "info" {
		t.Fatalf("aggregated fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "nonResourceURLs"); !ok || f.Value != "/healthz, /metrics" {
		t.Fatalf("nonResourceURLs fact wrong: %+v", f)
	}
	// A pure non-resource rule carries no resources, so it produces no rule row.
	if f, ok := factByLabel(facts, "rules"); !ok || f.Value != "1" {
		t.Fatalf("rules count fact wrong: %+v", f)
	}
}

func TestBuildFacts_RoleBinding(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "RoleBinding",
		"roleRef": map[string]interface{}{
			"kind": "ClusterRole",
			"name": "view",
		},
		"subjects": []interface{}{
			map[string]interface{}{"kind": "ServiceAccount", "name": "prometheus", "namespace": "monitoring"},
			map[string]interface{}{"kind": "ServiceAccount", "name": "grafana", "namespace": "monitoring"},
			map[string]interface{}{"kind": "User", "name": "alice"},
		},
	}}
	facts := BuildFacts("rbac.authorization.k8s.io", "rolebindings", u)
	if f, ok := factByLabel(facts, "role"); !ok || f.Value != "ClusterRole view" {
		t.Fatalf("role fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "subjects"); !ok || f.Value != "3" {
		t.Fatalf("subjects count fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "by kind"); !ok || f.Value != "2 ServiceAccount, 1 User" {
		t.Fatalf("by kind fact wrong: %+v", f)
	}
	// Per-subject row: kind label, namespace/name value.
	if f, ok := factByLabel(facts, "ServiceAccount"); !ok || f.Value != "monitoring/prometheus" {
		t.Fatalf("per-subject fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "User"); !ok || f.Value != "alice" {
		t.Fatalf("cluster-scoped user subject fact wrong: %+v", f)
	}
}

func TestBuildFacts_ClusterRoleBinding(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"kind": "ClusterRoleBinding",
		"roleRef": map[string]interface{}{
			"kind": "ClusterRole",
			"name": "cluster-admin",
		},
		"subjects": []interface{}{
			map[string]interface{}{"kind": "Group", "name": "system:masters"},
		},
	}}
	facts := BuildFacts("rbac.authorization.k8s.io", "clusterrolebindings", u)
	if f, ok := factByLabel(facts, "role"); !ok || f.Value != "ClusterRole cluster-admin" {
		t.Fatalf("role fact wrong: %+v", f)
	}
	if f, ok := factByLabel(facts, "Group"); !ok || f.Value != "system:masters" {
		t.Fatalf("group subject fact wrong: %+v", f)
	}
}

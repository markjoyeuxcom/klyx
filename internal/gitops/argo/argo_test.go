package argo

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func app(spec, status map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": "demo", "namespace": "argocd"},
		"spec":       spec,
		"status":     status,
	}}
}

func TestParseHealthyApp(t *testing.T) {
	u := app(
		map[string]interface{}{
			"project": "default",
			"source": map[string]interface{}{
				"repoURL":        "https://gitlab.com/x/workloads.git",
				"path":           "apps/console/overlays/dev",
				"targetRevision": "main",
			},
			"destination": map[string]interface{}{"namespace": "console-dev", "server": "https://kubernetes.default.svc"},
			"syncPolicy":  map[string]interface{}{"automated": map[string]interface{}{}},
		},
		map[string]interface{}{
			"sync":   map[string]interface{}{"status": "Synced", "revision": "abc1234def"},
			"health": map[string]interface{}{"status": "Healthy"},
			"resources": []interface{}{
				map[string]interface{}{"group": "apps", "kind": "Deployment", "namespace": "console-dev", "name": "console", "status": "Synced", "health": map[string]interface{}{"status": "Healthy"}},
			},
			"operationState": map[string]interface{}{
				"phase": "Succeeded", "message": "successfully synced",
			},
			"reconciledAt": "2026-06-11T11:21:48Z",
		},
	)
	u.SetOwnerReferences([]metav1.OwnerReference{{Kind: "ApplicationSet", Name: "console-set"}})
	a := Parse(u)
	if a.SyncStatus != "Synced" || a.HealthStatus != "Healthy" {
		t.Fatalf("status: %+v", a)
	}
	if a.Project != "default" || a.Path != "apps/console/overlays/dev" || a.TargetRevision != "main" {
		t.Fatalf("source: %+v", a)
	}
	if !a.AutoSync || a.DestNamespace != "console-dev" {
		t.Fatalf("policy/dest: %+v", a)
	}
	if a.ApplicationSet != "console-set" {
		t.Fatalf("applicationset owner: %+v", a.ApplicationSet)
	}
	if a.OpPhase != "Succeeded" || a.ReconciledAt.IsZero() {
		t.Fatalf("op/reconciledAt: %+v", a)
	}
	if len(a.Resources) != 1 || a.Resources[0].Kind != "Deployment" || a.Resources[0].Health != "Healthy" {
		t.Fatalf("resources: %+v", a.Resources)
	}
	if a.Broken() {
		t.Fatal("synced+healthy must not be broken")
	}
}

func TestParseAbsentStatusIsUnknownNeverHealthy(t *testing.T) {
	a := Parse(app(map[string]interface{}{}, map[string]interface{}{}))
	if a.SyncStatus != "Unknown" || a.HealthStatus != "Unknown" {
		t.Fatalf("absent status must read Unknown: %+v", a)
	}
	if !a.Broken() {
		t.Fatal("Unknown must count as needing attention")
	}
}

func TestBrokenSemantics(t *testing.T) {
	cases := []struct {
		sync, health string
		broken       bool
	}{
		{"Synced", "Healthy", false},
		{"Synced", "Progressing", false}, // transitional, not broken
		{"OutOfSync", "Healthy", true},
		{"Synced", "Degraded", true},
		{"Synced", "Missing", true},
		{"Synced", "Suspended", true},
	}
	for _, tc := range cases {
		a := App{SyncStatus: tc.sync, HealthStatus: tc.health}
		if a.Broken() != tc.broken {
			t.Errorf("%s/%s: broken=%v, want %v", tc.sync, tc.health, a.Broken(), tc.broken)
		}
	}
}

func TestParseMultiSource(t *testing.T) {
	u := app(map[string]interface{}{
		"sources": []interface{}{
			map[string]interface{}{"repoURL": "https://r1", "chart": "grafana", "targetRevision": "8.x"},
			map[string]interface{}{"repoURL": "https://r2", "path": "values"},
		},
	}, map[string]interface{}{})
	a := Parse(u)
	if a.RepoURL != "https://r1" || a.Chart != "grafana" || a.ExtraSources != 1 {
		t.Fatalf("multi-source: %+v", a)
	}
}

func TestParseProject(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "platform", "namespace": "argocd"},
		"spec": map[string]interface{}{
			"description": "platform apps",
			"sourceRepos": []interface{}{"https://gitlab.com/org/repo.git"},
			"destinations": []interface{}{
				map[string]interface{}{"name": "in-cluster", "namespace": "apps"},
			},
			"roles":             []interface{}{map[string]interface{}{"name": "reader"}},
			"orphanedResources": map[string]interface{}{"warn": true},
			"clusterResourceWhitelist": []interface{}{
				map[string]interface{}{"group": "*", "kind": "*"},
			},
			"namespaceResourceBlacklist": []interface{}{
				map[string]interface{}{"group": "", "kind": "Secret"},
			},
		},
	}}
	p := ParseProject(u)
	if p.Name != "platform" || len(p.SourceRepos) != 1 || len(p.Destinations) != 1 || p.Roles != 1 || !p.OrphanWarn {
		t.Fatalf("project: %+v", p)
	}
	if len(p.RoleNames) != 1 || p.RoleNames[0] != "reader" {
		t.Fatalf("role names: %+v", p.RoleNames)
	}
	if len(p.ClusterResourceWhitelist) != 1 || p.ClusterResourceWhitelist[0].Group != "*" || p.ClusterResourceWhitelist[0].Kind != "*" {
		t.Fatalf("cluster whitelist: %+v", p.ClusterResourceWhitelist)
	}
	if len(p.NamespaceResourceBlacklist) != 1 || p.NamespaceResourceBlacklist[0].Kind != "Secret" {
		t.Fatalf("namespace blacklist: %+v", p.NamespaceResourceBlacklist)
	}
}

func TestParseApplicationSet(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "platform-set", "namespace": "argocd"},
		"spec": map[string]interface{}{
			"generators": []interface{}{map[string]interface{}{"clusters": map[string]interface{}{}}},
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{"name": "{{name}}-platform"},
				"spec": map[string]interface{}{
					"project":     "platform",
					"source":      map[string]interface{}{"repoURL": "https://git", "path": "apps", "targetRevision": "main"},
					"destination": map[string]interface{}{"namespace": "apps", "server": "https://kubernetes.default.svc"},
				},
			},
		},
		"status": map[string]interface{}{
			"resources":  []interface{}{map[string]interface{}{"name": "dev-platform"}, map[string]interface{}{"name": "prd-platform"}},
			"conditions": []interface{}{map[string]interface{}{"type": "ResourcesUpToDate", "status": "True", "reason": "ok", "message": "generated"}},
		},
	}}
	a := ParseApplicationSet(u)
	if a.Name != "platform-set" || a.Project != "platform" || len(a.Generators) != 1 || a.GeneratedApps != 2 || !a.Healthy {
		t.Fatalf("applicationset: %+v", a)
	}
	if a.DestServer != "https://kubernetes.default.svc" || len(a.GeneratedNames) != 2 || a.GeneratedNames[0] != "dev-platform" {
		t.Fatalf("applicationset details: %+v", a)
	}
}

func TestPatches(t *testing.T) {
	if !strings.Contains(string(RefreshPatch()), "argocd.argoproj.io/refresh") {
		t.Fatal("refresh patch wrong")
	}
	s := string(SyncPatch(SyncOptions{
		Revision:        "abc123",
		Strategy:        SyncStrategyApply,
		Prune:           true,
		DryRun:          true,
		Force:           true,
		Replace:         true,
		ServerSideApply: true,
		Resources:       []Resource{{Group: "apps", Kind: "Deployment", Namespace: "default", Name: "api"}},
	}))
	if !strings.Contains(s, `"revision":"abc123"`) || !strings.Contains(s, `"username":"klyx"`) {
		t.Fatalf("sync patch wrong: %s", s)
	}
	for _, want := range []string{`"prune":true`, `"dryRun":true`, `"apply"`, `"Force=true"`, `"Replace=true"`, `"ServerSideApply=true"`, `"resources"`, `"Deployment"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("sync patch missing %s: %s", want, s)
		}
	}
	if !strings.Contains(string(SyncPatch(SyncOptions{})), `"revision":"HEAD"`) {
		t.Fatal("empty revision must default to HEAD")
	}
}

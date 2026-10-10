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
		// Suspended is a deliberate operator pause (e.g. a paused Rollout), not
		// a failure. Argo's UI shows it as a distinct paused state and the Flux
		// side keeps Suspended out of broken too - previously this pinned
		// Suspended as broken, which reddened triage permanently.
		{"Synced", "Suspended", false},
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

func TestParseDeployedRevision(t *testing.T) {
	multiSourceSpec := map[string]interface{}{
		"sources": []interface{}{
			map[string]interface{}{"repoURL": "https://r1", "chart": "grafana", "targetRevision": "8.x"},
			map[string]interface{}{"repoURL": "https://r2", "path": "values"},
		},
	}
	cases := []struct {
		name string
		spec map[string]interface{}
		sync map[string]interface{}
		want string
	}{
		{"single-source revision", map[string]interface{}{}, map[string]interface{}{"revision": "abc1234"}, "abc1234"},
		// Multi-source apps leave status.sync.revision empty and report
		// status.sync.revisions[] parallel to spec.sources.
		{"multi-source revisions[0]", multiSourceSpec, map[string]interface{}{"revisions": []interface{}{"8.5.1", "def5678"}}, "8.5.1"},
		{"revision wins when both set", multiSourceSpec, map[string]interface{}{"revision": "abc1234", "revisions": []interface{}{"8.5.1"}}, "abc1234"},
		{"no revision at all stays empty", map[string]interface{}{}, map[string]interface{}{}, ""},
	}
	for _, tc := range cases {
		a := Parse(app(tc.spec, map[string]interface{}{"sync": tc.sync}))
		if a.Revision != tc.want {
			t.Errorf("%s: Revision=%q, want %q", tc.name, a.Revision, tc.want)
		}
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
			"resources": []interface{}{map[string]interface{}{"name": "dev-platform"}, map[string]interface{}{"name": "prd-platform"}},
			// The full condition set a healthy ApplicationSet controller emits:
			// ErrorOccurred is False on success - inverse polarity to the rest.
			"conditions": []interface{}{
				map[string]interface{}{"type": "ErrorOccurred", "status": "False", "reason": "ApplicationSetUpToDate", "message": "Successfully generated parameters for all Applications"},
				map[string]interface{}{"type": "ParametersGenerated", "status": "True", "reason": "ParametersGenerated", "message": "Successfully generated parameters for all Applications"},
				map[string]interface{}{"type": "ResourcesUpToDate", "status": "True", "reason": "ApplicationSetUpToDate", "message": "ApplicationSet up to date"},
			},
		},
	}}
	a := ParseApplicationSet(u)
	if a.Name != "platform-set" || a.Project != "platform" || len(a.Generators) != 1 || a.GeneratedApps != 2 || !a.Healthy {
		t.Fatalf("applicationset: %+v", a)
	}
	if a.DestServer != "https://kubernetes.default.svc" || len(a.GeneratedNames) != 2 || a.GeneratedNames[0] != "dev-platform" {
		t.Fatalf("applicationset details: %+v", a)
	}
	if len(a.Conditions) != 3 {
		t.Fatalf("conditions: %+v", a.Conditions)
	}
}

func TestParseApplicationSetGeneratorNames(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{"name": "s", "namespace": "argocd"},
		"spec": map[string]interface{}{
			"generators": []interface{}{
				// `selector` is a sibling filter key inside a generator entry,
				// not a generator - it must not be listed or counted.
				map[string]interface{}{
					"git":      map[string]interface{}{"repoURL": "https://git"},
					"selector": map[string]interface{}{"matchLabels": map[string]interface{}{"env": "dev"}},
				},
				map[string]interface{}{"clusters": map[string]interface{}{}},
			},
		},
	}}
	a := ParseApplicationSet(u)
	if len(a.Generators) != 2 || a.Generators[0] != "clusters" || a.Generators[1] != "git" {
		t.Fatalf("want sorted generator types [clusters git], got %+v", a.Generators)
	}
}

func TestParseApplicationSetConditionPolarity(t *testing.T) {
	appSet := func(conds ...map[string]interface{}) *unstructured.Unstructured {
		items := make([]interface{}, 0, len(conds))
		for _, c := range conds {
			items = append(items, map[string]interface{}(c))
		}
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"metadata": map[string]interface{}{"name": "s", "namespace": "argocd"},
			"status":   map[string]interface{}{"conditions": items},
		}}
	}
	cases := []struct {
		name    string
		u       *unstructured.Unstructured
		healthy bool
	}{
		{"error occurred true is broken", appSet(
			map[string]interface{}{"type": "ErrorOccurred", "status": "True", "reason": "ApplicationGenerationError"},
		), false},
		{"error occurred false is healthy", appSet(
			map[string]interface{}{"type": "ErrorOccurred", "status": "False", "reason": "ApplicationSetUpToDate"},
		), true},
		{"resources not up to date is broken", appSet(
			map[string]interface{}{"type": "ErrorOccurred", "status": "False", "reason": "ApplicationSetUpToDate"},
			map[string]interface{}{"type": "ResourcesUpToDate", "status": "False", "reason": "ErrorOccurred"},
		), false},
		{"parameters not generated is broken", appSet(
			map[string]interface{}{"type": "ParametersGenerated", "status": "False", "reason": "ErrorOccurred"},
		), false},
		{"rollout progressing is neutral", appSet(
			map[string]interface{}{"type": "ErrorOccurred", "status": "False", "reason": "ApplicationSetUpToDate"},
			map[string]interface{}{"type": "ResourcesUpToDate", "status": "True", "reason": "ApplicationSetUpToDate"},
			map[string]interface{}{"type": "RolloutProgressing", "status": "True", "reason": "ApplicationSetRolloutProgressing"},
		), true},
		{"unknown condition type is neutral", appSet(
			map[string]interface{}{"type": "SomeFutureCondition", "status": "False", "reason": "x"},
		), true},
	}
	for _, tc := range cases {
		if got := ParseApplicationSet(tc.u).Healthy; got != tc.healthy {
			t.Errorf("%s: Healthy=%v, want %v", tc.name, got, tc.healthy)
		}
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

func TestParseDestinationServerAndName(t *testing.T) {
	byServer := Parse(app(map[string]interface{}{"destination": map[string]interface{}{"namespace": "a", "server": "https://kubernetes.default.svc"}}, map[string]interface{}{}))
	if byServer.DestServer != "https://kubernetes.default.svc" || byServer.DestName != "" {
		t.Fatalf("server destination: %+v", byServer)
	}
	byName := Parse(app(map[string]interface{}{"destination": map[string]interface{}{"namespace": "a", "name": "prd-weu"}}, map[string]interface{}{}))
	if byName.DestName != "prd-weu" || byName.DestServer != "" {
		t.Fatalf("name destination: %+v", byName)
	}
}

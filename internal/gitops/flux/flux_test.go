package flux

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/health"
)

func ks(status string, reason string, suspend bool, rev, msg string) *unstructured.Unstructured {
	conds := []interface{}{
		map[string]interface{}{"type": "Ready", "status": status, "reason": reason, "message": msg},
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"name": "flux-system", "namespace": "flux-system"},
		"spec":       map[string]interface{}{"suspend": suspend},
		"status": map[string]interface{}{
			"conditions":          conds,
			"lastAppliedRevision": rev,
		},
	}}
}

func ksWithSource(status string, reason string, suspend bool, rev, msg string, sourceKind, sourceName string) *unstructured.Unstructured {
	conds := []interface{}{
		map[string]interface{}{"type": "Ready", "status": status, "reason": reason, "message": msg},
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"name": "flux-system", "namespace": "flux-system"},
		"spec": map[string]interface{}{
			"suspend": suspend,
			"sourceRef": map[string]interface{}{
				"kind": sourceKind,
				"name": sourceName,
			},
		},
		"status": map[string]interface{}{
			"conditions":          conds,
			"lastAppliedRevision": rev,
		},
	}}
}

func TestParseKustomizationReady(t *testing.T) {
	r := ParseKustomization(ks("True", "ReconciliationSucceeded", false, "main@sha1:abc1234", ""))
	if r.Kind != KustomizationKind {
		t.Fatalf("kind: %q", r.Kind)
	}
	if r.Name != "flux-system" || r.Namespace != "flux-system" {
		t.Fatalf("name/ns: %+v", r)
	}
	if r.Ready != Ready {
		t.Fatalf("ready: %q", r.Ready)
	}
	if r.Revision != "main@sha1:abc1234" {
		t.Fatalf("revision: %q", r.Revision)
	}
	if r.Suspended {
		t.Fatalf("should not be suspended")
	}
}

func TestParseKustomizationFailedAndSuspended(t *testing.T) {
	r := ParseKustomization(ks("False", "BuildFailed", true, "", "kustomize build failed"))
	if r.Ready != Failed {
		t.Fatalf("want Failed, got %q", r.Ready)
	}
	if r.Message != "kustomize build failed" {
		t.Fatalf("message: %q", r.Message)
	}
	if !r.Suspended {
		t.Fatalf("want suspended")
	}
}

func TestParseKustomizationReconciling(t *testing.T) {
	u := ks("Unknown", "Progressing", false, "", "reconciliation in progress")
	conds := u.Object["status"].(map[string]interface{})["conditions"].([]interface{})
	conds = append(conds, map[string]interface{}{"type": "Reconciling", "status": "True"})
	u.Object["status"].(map[string]interface{})["conditions"] = conds
	r := ParseKustomization(u)
	if r.Ready != Reconciling {
		t.Fatalf("want Reconciling, got %q", r.Ready)
	}
}

func TestParseHelmReleaseReadyRevisionFromHistory(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]interface{}{"name": "cilium", "namespace": "kube-system"},
		"spec":       map[string]interface{}{},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True", "message": "Helm install succeeded"},
			},
			"history": []interface{}{
				map[string]interface{}{"chartVersion": "1.16.5"},
			},
		},
	}}
	r := ParseHelmRelease(u)
	if r.Kind != HelmReleaseKind || r.Name != "cilium" {
		t.Fatalf("identity: %+v", r)
	}
	if r.Ready != Ready {
		t.Fatalf("ready: %q", r.Ready)
	}
	if r.Revision != "1.16.5" {
		t.Fatalf("revision: %q", r.Revision)
	}
}

func TestParseHelmReleaseCurrentChartVersionFromHistory(t *testing.T) {
	// Flux status.history is newest-first; the current release has the highest
	// `version`. The old code read history[last] (oldest) -> showed a stale chart.
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]interface{}{"name": "argo-cd", "namespace": "argocd"},
		"spec":       map[string]interface{}{},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True", "message": "Helm upgrade succeeded"},
			},
			"history": []interface{}{
				map[string]interface{}{"chartVersion": "9.5.21", "version": int64(4), "status": "deployed"},
				map[string]interface{}{"chartVersion": "9.5.15", "version": int64(3), "status": "superseded"},
			},
		},
	}}
	if r := ParseHelmRelease(u); r.Revision != "9.5.21" {
		t.Fatalf("want current chart 9.5.21, got %q", r.Revision)
	}
}

func TestParseHelmReleaseCurrentChartVersionOldestFirst(t *testing.T) {
	// Robust to ordering: even if history is oldest-first, the max version wins.
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]interface{}{"name": "argo-cd", "namespace": "argocd"},
		"spec":       map[string]interface{}{},
		"status": map[string]interface{}{
			"history": []interface{}{
				map[string]interface{}{"chartVersion": "9.5.15", "version": float64(3)},
				map[string]interface{}{"chartVersion": "9.5.21", "version": float64(4)},
			},
		},
	}}
	if r := ParseHelmRelease(u); r.Revision != "9.5.21" {
		t.Fatalf("want current chart 9.5.21, got %q", r.Revision)
	}
}

func TestParseKustomizationSourceRef(t *testing.T) {
	r := ParseKustomization(ksWithSource("True", "ReconciliationSucceeded", false, "main@sha1:abc1234", "", "GitRepository", "flux-system"))
	if r.SourceName != "flux-system" {
		t.Fatalf("sourceRef name: %q", r.SourceName)
	}
	if r.SourceKind != "GitRepository" {
		t.Fatalf("sourceRef kind: %q", r.SourceKind)
	}
}

func TestParseHelmReleaseChartSourceRef(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]interface{}{"name": "cilium", "namespace": "kube-system"},
		"spec": map[string]interface{}{
			"chart": map[string]interface{}{
				"spec": map[string]interface{}{
					"sourceRef": map[string]interface{}{
						"kind": "HelmRepository",
						"name": "cilium",
					},
				},
			},
		},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True", "message": "Helm install succeeded"},
			},
			"history": []interface{}{
				map[string]interface{}{"chartVersion": "1.16.5"},
			},
		},
	}}
	r := ParseHelmRelease(u)
	if r.SourceName != "cilium" {
		t.Fatalf("chart sourceRef name: %q", r.SourceName)
	}
	if r.SourceKind != "HelmRepository" {
		t.Fatalf("chart sourceRef kind: %q", r.SourceKind)
	}
}

func TestStalenessFromObservedGeneration(t *testing.T) {
	ks := func(gen, observed int64, withObserved bool) *unstructured.Unstructured {
		status := map[string]interface{}{
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
		}
		if withObserved {
			status["observedGeneration"] = observed
		}
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata":   map[string]interface{}{"name": "app", "namespace": "flux-system"},
			"status":     status,
		}}
		u.SetGeneration(gen)
		return u
	}

	// Spec edited (gen 5) but controller last observed gen 4: the row is stale
	// and Ready must not be presented as confirmed.
	r := ParseKustomization(ks(5, 4, true))
	if !r.Stale || r.Generation != 5 || r.ObservedGeneration != 4 {
		t.Fatalf("stale: %+v", r)
	}
	// Observed caught up: not stale.
	if r := ParseKustomization(ks(5, 5, true)); r.Stale {
		t.Fatalf("caught-up resource must not be stale: %+v", r)
	}
	// No observedGeneration in status: not-stale (never guess).
	if r := ParseKustomization(ks(5, 0, false)); r.Stale {
		t.Fatalf("missing observedGeneration must read not-stale: %+v", r)
	}
}

// S1b §4.1 (Stage 0): the controllers advance status.observedGeneration
// only on success, so a failing spec change keeps it behind while Ready=False
// is written for the current generation. That is a fault, never "Syncing…".
func TestPendingFollowsReadyGeneration(t *testing.T) {
	ks := func(gen, statusGen, readyGen int64, ready, reason string) *unstructured.Unstructured {
		cond := map[string]interface{}{"type": "Ready", "status": ready, "reason": reason, "message": "kustomize build failed"}
		if readyGen > 0 {
			cond["observedGeneration"] = readyGen
		}
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata":   map[string]interface{}{"name": "app", "namespace": "flux-system"},
			"status":     map[string]interface{}{"observedGeneration": statusGen, "conditions": []interface{}{cond}},
		}}
		u.SetGeneration(gen)
		return u
	}
	cases := []struct {
		name      string
		u         *unstructured.Unstructured
		wantStale bool
		wantRole  health.Kind
		wantLabel string
	}{
		{"failing the current generation, status behind", ks(5, 4, 5, "False", "BuildFailed"), false, health.Fault, "Failed"},
		{"a pending generation with an old Ready", ks(5, 4, 4, "True", "ReconciliationSucceeded"), true, health.Progressing, "Syncing…"},
		{"an old failure, a newer generation pending", ks(5, 4, 4, "False", "BuildFailed"), true, health.Progressing, "Syncing…"},
		{"Ready True for the current generation, status behind", ks(5, 4, 5, "True", "ReconciliationSucceeded"), true, health.Progressing, "Syncing…"},
		{"an old Ready, status caught up (a suspension)", ks(5, 5, 4, "True", "ReconciliationSucceeded"), true, health.Progressing, "Syncing…"},
		{"Ready without its own generation: the status rule", ks(5, 4, 0, "False", "BuildFailed"), true, health.Progressing, "Syncing…"},
		{"Ready without its own generation, caught up", ks(5, 5, 0, "False", "BuildFailed"), false, health.Fault, "Failed"},
		{"current and healthy", ks(5, 5, 5, "True", "ReconciliationSucceeded"), false, health.OK, "Ready"},
	}
	for _, c := range cases {
		r := ParseKustomization(c.u)
		e := ReconciliationRule(r)
		if r.Stale != c.wantStale || e.Role != c.wantRole || e.Label != c.wantLabel {
			t.Errorf("%s: stale=%v role=%v label=%q, want %v %v %q", c.name, r.Stale, e.Role, e.Label, c.wantStale, c.wantRole, c.wantLabel)
		}
	}
	// The current failure carries its reason (the fault is read from Ready).
	if e := ReconciliationRule(ParseKustomization(ks(5, 4, 5, "False", "BuildFailed"))); e.Reason == "" {
		t.Fatalf("a current failure reads its reason from Ready: %+v", e)
	}
	// A HelmRelease follows the same rule.
	hr := ks(3, 2, 3, "False", "InstallFailed")
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	if r := ParseHelmRelease(hr); r.Stale || ReconciliationRule(r).Role != health.Fault {
		t.Fatalf("a HelmRelease failing its current generation is a fault: %+v", r)
	}
}

func TestSourceNamespaceResolution(t *testing.T) {
	// Kustomization without sourceRef.namespace: Flux defaults to its own.
	r := ParseKustomization(ksWithSource("True", "", false, "", "", "GitRepository", "repo"))
	if r.SourceNamespace != "flux-system" {
		t.Fatalf("default ks source ns: %q", r.SourceNamespace)
	}
	// Explicit cross-namespace sourceRef wins.
	u := ksWithSource("True", "", false, "", "", "GitRepository", "repo")
	_ = unstructured.SetNestedField(u.Object, "team-a", "spec", "sourceRef", "namespace")
	if r := ParseKustomization(u); r.SourceNamespace != "team-a" {
		t.Fatalf("explicit ks source ns: %q", r.SourceNamespace)
	}
	hr := func(spec map[string]interface{}) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
			"metadata": map[string]interface{}{"name": "app", "namespace": "apps"},
			"spec":     spec,
		}}
	}
	// chart.spec.sourceRef: default and explicit.
	if r := ParseHelmRelease(hr(map[string]interface{}{"chart": map[string]interface{}{"spec": map[string]interface{}{
		"sourceRef": map[string]interface{}{"kind": "HelmRepository", "name": "charts"}}}})); r.SourceNamespace != "apps" {
		t.Fatalf("default hr chart source ns: %q", r.SourceNamespace)
	}
	if r := ParseHelmRelease(hr(map[string]interface{}{"chart": map[string]interface{}{"spec": map[string]interface{}{
		"sourceRef": map[string]interface{}{"kind": "HelmRepository", "name": "charts", "namespace": "flux-system"}}}})); r.SourceNamespace != "flux-system" {
		t.Fatalf("explicit hr chart source ns: %q", r.SourceNamespace)
	}
	// chartRef: explicit.
	if r := ParseHelmRelease(hr(map[string]interface{}{"chartRef": map[string]interface{}{"kind": "OCIRepository", "name": "podinfo", "namespace": "sources"}})); r.SourceNamespace != "sources" || r.SourceKind != "OCIRepository" {
		t.Fatalf("hr chartRef source: %+v", r)
	}
}

// Coverage S1c §6: a Kustomization's messages pass Origins on the row and
// in the detail, whatever it references now.
func TestKustomizationMessagesPassOrigins(t *testing.T) {
	const needle = "SIGNEDneedle3R"
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]interface{}{"name": "apps", "namespace": "flux-system"},
		"spec":     map[string]interface{}{"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "fleet"}},
		"status": map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "False", "reason": "ArtifactFailed",
			"message": "failed to download archive from https://store.example/a.tgz?sig=" + needle + " (status: 403 Forbidden)"}}},
	}}
	r := ParseKustomization(u)
	d := ParseDetail(u)
	if strings.Contains(r.Message, needle) || strings.Contains(d.Conditions[0].Message, needle) || !strings.Contains(r.Message, "https://store.example") {
		t.Fatalf("row %q, detail %q", r.Message, d.Conditions[0].Message)
	}
	if e := ReconciliationRule(r); strings.Contains(e.Reason, needle) {
		t.Fatalf("health reason: %q", e.Reason)
	}
}

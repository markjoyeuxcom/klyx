package capability

import "testing"

func TestControllerRefs(t *testing.T) {
	fluxOnly := Set{GitOps: GitOpsCapability{Flux: FluxInfo{Present: true}}}
	refs := ControllerRefs(fluxOnly)
	if len(refs) != 1 || refs[0].Tool != "flux" || refs[0].Kind != "Deployment" ||
		refs[0].Namespace != "flux-system" || refs[0].Name != "kustomize-controller" {
		t.Fatalf("flux-only refs wrong: %+v", refs)
	}

	argoOnly := Set{GitOps: GitOpsCapability{Argo: ArgoInfo{Present: true}}}
	refs = ControllerRefs(argoOnly)
	if len(refs) != 1 || refs[0].Tool != "argo" || refs[0].Kind != "StatefulSet" ||
		refs[0].Namespace != "argocd" || refs[0].Name != "argocd-application-controller" {
		t.Fatalf("argo-only refs wrong: %+v", refs)
	}

	both := Set{GitOps: GitOpsCapability{Flux: FluxInfo{Present: true}, Argo: ArgoInfo{Present: true}}}
	if len(ControllerRefs(both)) != 2 {
		t.Fatalf("both: want 2 refs")
	}

	if len(ControllerRefs(Set{})) != 0 {
		t.Fatalf("neither: want 0 refs")
	}
}

func TestWithGitOpsHealth(t *testing.T) {
	base := Set{GitOps: GitOpsCapability{
		Base: Base{Tier: Healthy},
		Flux: FluxInfo{Present: true, Version: "v2.4.0", Healthy: true},
	}}

	g := WithGitOpsHealth(base, ControllerRead{Health: ControllerReady}, ControllerRead{Health: ControllerNotReady})
	if g.Tier != Healthy || g.Reason != "" {
		t.Fatalf("want Healthy/empty, got %v/%q", g.Tier, g.Reason)
	}
	if g.Flux.Version != "v2.4.0" {
		t.Fatalf("version not preserved: %q", g.Flux.Version)
	}
	if len(g.Flux.Controllers) != 1 {
		t.Fatalf("want Controllers populated when healthy")
	}

	g = WithGitOpsHealth(base, ControllerRead{Health: ControllerNotReady}, ControllerRead{Health: ControllerNotReady})
	if g.Tier != Degraded || g.Reason == "" {
		t.Fatalf("want Degraded/reason, got %v/%q", g.Tier, g.Reason)
	}
	if len(g.Flux.Controllers) != 0 {
		t.Fatalf("want Controllers cleared when unhealthy")
	}

	co := Set{GitOps: GitOpsCapability{
		Base:        Base{Tier: Healthy},
		Flux:        FluxInfo{Present: true, Healthy: true},
		Argo:        ArgoInfo{Present: true, Healthy: true},
		Coexistence: true,
	}}
	g = WithGitOpsHealth(co, ControllerRead{Health: ControllerReady}, ControllerRead{Health: ControllerNotReady})
	if g.Tier != Degraded {
		t.Fatalf("want Degraded when argo unhealthy, got %v", g.Tier)
	}
	if !g.Coexistence {
		t.Fatal("coexistence must be preserved")
	}
}

// Codex 2c review P1: a workload that is not there, or cannot be read, is
// no evidence: the tool is unverified and the reason says so - never "not
// ready".
func TestWithGitOpsHealthUnverifiedClaimsNothing(t *testing.T) {
	base := Set{GitOps: GitOpsCapability{Flux: FluxInfo{Present: true, Healthy: true, Verified: true}}}
	g := WithGitOpsHealth(base, ReadFromWorkload("Deployment", "flux-system", "kustomize-controller", nil), ControllerRead{})
	if g.Flux.Verified || g.Flux.Healthy || g.Flux.Why != "no Deployment flux-system/kustomize-controller" {
		t.Fatalf("a missing workload is unverified: %+v", g.Flux)
	}
	if got := g.FluxIssue(); got != "Flux installed but kustomize-controller health is unverified (no Deployment flux-system/kustomize-controller)" {
		t.Fatalf("issue: %q", got)
	}
	r := false
	g = WithGitOpsHealth(base, ReadFromWorkload("Deployment", "flux-system", "kustomize-controller", &r), ControllerRead{})
	if !g.Flux.Verified || g.Flux.Healthy || g.FluxIssue() != "Flux installed but kustomize-controller is not ready" {
		t.Fatalf("a workload read adverse is a verified not-ready: %+v %q", g.Flux, g.FluxIssue())
	}
}

package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/gitops/flux"
)

func TestToFluxDTO(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 30, 0, time.UTC)
	r := flux.Resource{
		Kind: flux.KustomizationKind, Namespace: "flux-system", Name: "flux-system",
		Ready: flux.Ready, Message: "", Revision: "main@sha1:abc",
		ReadySince: now.Add(-30 * time.Second), Suspended: false,
		SourceKind: "GitRepository", SourceName: "flux-system",
	}
	d := ToFluxDTO(r, now)
	if d.Kind != "Kustomization" || d.Name != "flux-system" || d.Namespace != "flux-system" {
		t.Fatalf("identity: %+v", d)
	}
	if d.Ready != "Ready" || d.Revision != "main@sha1:abc" {
		t.Fatalf("fields: %+v", d)
	}
	if d.ReadySinceSeconds != 30 {
		t.Fatalf("age: %d", d.ReadySinceSeconds)
	}
	if d.SourceKind != "GitRepository" || d.SourceName != "flux-system" {
		t.Fatalf("source: kind=%q name=%q", d.SourceKind, d.SourceName)
	}
}

func TestToFluxDTOCarriesReason(t *testing.T) {
	now := time.Now()
	r := flux.Resource{Kind: flux.HelmReleaseKind, Namespace: "kube-system", Name: "cilium", Ready: flux.Failed, Reason: "UpgradeFailed"}
	if d := ToFluxDTO(r, now); d.Reason != "UpgradeFailed" {
		t.Fatalf("want reason carried, got %q", d.Reason)
	}
}

func TestToFluxDTOZeroTimeAge(t *testing.T) {
	now := time.Now()
	d := ToFluxDTO(flux.Resource{Kind: flux.HelmReleaseKind, Name: "x", Ready: flux.Failed}, now)
	if d.ReadySinceSeconds != 0 {
		t.Fatalf("want 0 age for zero time, got %d", d.ReadySinceSeconds)
	}
}

func TestToDetailDTOCarriesSuspended(t *testing.T) {
	d := flux.Detail{Kind: flux.KustomizationKind, Name: "x", Namespace: "flux-system", Suspended: true}
	if got := toDetailDTO(d); !got.Suspended {
		t.Fatal("want Suspended=true in DTO")
	}
}

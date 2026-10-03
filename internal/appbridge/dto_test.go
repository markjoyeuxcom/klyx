package appbridge

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
)

func TestToDTO(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 30, 0, time.UTC)
	snap := fleet.Snapshot{
		Name:       "plt-sea-prd-we-aks-01",
		State:      fleet.Synced,
		NodesReady: 12, NodesTotal: 12, Pods: 487,
		Version:  "v1.30.4",
		LastSync: now.Add(-15 * time.Second),
		Capabilities: capability.Set{
			GitOps:  capability.GitOpsCapability{Base: capability.Base{Tier: capability.Healthy}},
			Network: capability.NetworkCapability{Base: capability.Base{Tier: capability.Degraded, Reason: "no EnvoyProxy"}},
		},
	}
	cc := config.ClusterConfig{
		Name:  "plt-sea-prd-we-aks-01",
		Group: "prd-we",
		Tags:  map[string]string{"env": "prd", "region": "we", "provider": "aks"},
	}

	d := ToDTO(snap, cc, now)
	if d.Name != "plt-sea-prd-we-aks-01" {
		t.Fatalf("name: %q", d.Name)
	}
	if d.State != "Synced" {
		t.Fatalf("state: %q", d.State)
	}
	if d.NodesReady != 12 || d.NodesTotal != 12 || d.Pods != 487 {
		t.Fatalf("counts: %+v", d)
	}
	if d.Version != "v1.30.4" {
		t.Fatalf("version: %q", d.Version)
	}
	if d.Env != "prd" || d.Region != "we" || d.Provider != "aks" || d.Group != "prd-we" {
		t.Fatalf("tags: %+v", d)
	}
	if d.GitopsTier != "Healthy" {
		t.Fatalf("gitops tier: %q", d.GitopsTier)
	}
	if d.NetworkTier != "Degraded" || d.NetworkReason != "no EnvoyProxy" {
		t.Fatalf("network: %q/%q", d.NetworkTier, d.NetworkReason)
	}
	if d.AgeSeconds != 15 {
		t.Fatalf("age: %d", d.AgeSeconds)
	}
}

func TestToDTOMapsEnvironmentAndProtected(t *testing.T) {
	s := fleet.Snapshot{Name: "prd-we", State: fleet.Synced}
	cc := config.ClusterConfig{Name: "prd-we", Environment: "prd", Protected: true}
	dto := ToDTO(s, cc, time.Now())
	if dto.Env != "prd" {
		t.Fatalf("want env=prd, got %q", dto.Env)
	}
	if !dto.Protected {
		t.Fatal("want protected=true")
	}

	cc2 := config.ClusterConfig{Name: "x", Tags: map[string]string{"env": "stg"}}
	if got := ToDTO(fleet.Snapshot{Name: "x"}, cc2, time.Now()).Env; got != "stg" {
		t.Fatalf("want env fallback=stg, got %q", got)
	}
}

func TestToDTODerivesEKSProviderMetadata(t *testing.T) {
	ctx := "arn:aws:eks:us-east-1:934692410245:cluster/eks-tooling"
	dto := ToDTO(
		fleet.Snapshot{Name: ctx},
		config.ClusterConfig{Name: ctx, Context: ctx},
		time.Now(),
	)
	if dto.Provider != "eks" || dto.Region != "us-east-1" {
		t.Fatalf("want derived EKS metadata, got %+v", dto)
	}
}

func TestToDTOZeroLastSyncAgeIsZero(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	d := ToDTO(fleet.Snapshot{Name: "x", State: fleet.Connecting}, config.ClusterConfig{Name: "x"}, now)
	if d.AgeSeconds != 0 {
		t.Fatalf("want 0 age when never synced, got %d", d.AgeSeconds)
	}
}

// Review 2026-10-03 #7: the fleet DTO carries EFFECTIVE protection, so the
// ConfirmDialog arms for an inferred-prod cluster instead of offering a plain
// confirm the backend then refuses.
func TestToDTO_EffectiveProtection(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := []struct {
		cc                  config.ClusterConfig
		protected, inferred bool
	}{
		{config.ClusterConfig{Name: "aks-prd-we"}, true, true},
		{config.ClusterConfig{Name: "core", Protected: true}, true, false},
		{config.ClusterConfig{Name: "lab", Context: "kind-prod"}, true, true},
		{config.ClusterConfig{Name: "dev-we"}, false, false},
	}
	for _, tc := range cases {
		d := ToDTO(fleet.Snapshot{Name: tc.cc.Name}, tc.cc, now)
		if d.Protected != tc.protected || d.ProtectedInferred != tc.inferred {
			t.Errorf("%s: (protected, inferred) = (%v, %v), want (%v, %v)", tc.cc.Name, d.Protected, d.ProtectedInferred, tc.protected, tc.inferred)
		}
	}
}

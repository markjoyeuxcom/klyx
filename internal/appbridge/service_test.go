package appbridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
)

// snapshotter is the minimal registry surface the service needs.
type fakeSnapshotter struct{ snaps []fleet.Snapshot }

func (f *fakeSnapshotter) Snapshots() []fleet.Snapshot { return f.snaps }

func TestGetFleetJoinsConfigByName(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	reg := &fakeSnapshotter{snaps: []fleet.Snapshot{
		{Name: "a", State: fleet.Synced},
		{Name: "b", State: fleet.Failed},
	}}
	cfg := &config.Config{Clusters: []config.ClusterConfig{
		{Name: "a", Tags: map[string]string{"env": "prd"}},
		{Name: "b", Tags: map[string]string{"env": "dev"}},
	}}

	svc := NewFleetService(reg, cfg, func() time.Time { return now })
	dtos := svc.GetFleet()

	if len(dtos) != 2 {
		t.Fatalf("want 2 dtos, got %d", len(dtos))
	}
	byName := map[string]ClusterDTO{}
	for _, d := range dtos {
		byName[d.Name] = d
	}
	if byName["a"].Env != "prd" || byName["a"].State != "Synced" {
		t.Fatalf("a wrong: %+v", byName["a"])
	}
	if byName["b"].Env != "dev" || byName["b"].State != "Failed" {
		t.Fatalf("b wrong: %+v", byName["b"])
	}
}

func TestGetFleetUnknownConfigStillProjects(t *testing.T) {
	now := time.Now()
	reg := &fakeSnapshotter{snaps: []fleet.Snapshot{{Name: "ghost", State: fleet.Synced}}}
	cfg := &config.Config{}
	svc := NewFleetService(reg, cfg, func() time.Time { return now })
	dtos := svc.GetFleet()
	if len(dtos) != 1 || dtos[0].Name != "ghost" {
		t.Fatalf("want ghost projected with empty tags, got %+v", dtos)
	}
}

func TestToDTOProjectsCapabilityDetails(t *testing.T) {
	now := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	dto := ToDTO(fleet.Snapshot{
		Name:  "nelli",
		State: fleet.Synced,
		Capabilities: capability.Set{
			GitOps: capability.GitOpsCapability{
				Base: capability.Base{Tier: capability.Healthy},
				Flux: capability.FluxInfo{Present: true, Healthy: true},
			},
			Network: capability.NetworkCapability{
				Base:              capability.Base{Tier: capability.Healthy},
				GatewayAPIVersion: "v1",
				CiliumPresent:     true,
				ClusterMesh:       true,
			},
		},
	}, config.ClusterConfig{}, now)

	if !dto.FluxPresent || !dto.FluxHealthy {
		t.Fatalf("want flux capability projected, got %+v", dto)
	}
	if dto.GatewayAPIVersion != "v1" || !dto.CiliumPresent || !dto.ClusterMesh {
		t.Fatalf("want network capability details projected, got %+v", dto)
	}
}

type fakeEmitter struct {
	mu     sync.Mutex
	events int
	last   any
}

func (e *fakeEmitter) Emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events++
	e.last = data
}

func TestRunEmitsOnTick(t *testing.T) {
	now := time.Now()
	reg := &fakeSnapshotter{snaps: []fleet.Snapshot{{Name: "a", State: fleet.Synced}}}
	svc := NewFleetService(reg, &config.Config{}, func() time.Time { return now })
	em := &fakeEmitter{}

	ctx, cancel := context.WithCancel(context.Background())
	go RunFleetPush(ctx, svc, em, 10*time.Millisecond)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		em.mu.Lock()
		n := em.events
		em.mu.Unlock()
		if n >= 1 {
			cancel()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	t.Fatal("expected at least one emit within 1s")
}

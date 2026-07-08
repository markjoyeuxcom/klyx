package appbridge

import (
	"context"
	"time"

	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
)

// Snapshotter is the registry surface the service depends on (so tests can fake it).
type Snapshotter interface {
	Snapshots() []fleet.Snapshot
}

// Emitter pushes a named event with a payload to the frontend. The Wails app
// provides the real implementation; tests provide a fake.
type Emitter interface {
	Emit(name string, data any)
}

const FleetUpdatedEvent = "fleet:updated"

// FleetService is bound to JS. GetFleet seeds the UI; Run pushes live updates.
type FleetService struct {
	reg    Snapshotter
	byName map[string]config.ClusterConfig
	now    func() time.Time
}

func NewFleetService(reg Snapshotter, cfg *config.Config, now func() time.Time) *FleetService {
	byName := make(map[string]config.ClusterConfig, len(cfg.Clusters))
	for _, c := range cfg.Clusters {
		byName[c.Name] = c
	}
	return &FleetService{reg: reg, byName: byName, now: now}
}

// GetFleet returns the current fleet as DTOs (bound, callable from JS).
func (s *FleetService) GetFleet() []ClusterDTO {
	snaps := s.reg.Snapshots()
	now := s.now()
	out := make([]ClusterDTO, 0, len(snaps))
	for _, snap := range snaps {
		out = append(out, ToDTO(snap, s.byName[snap.Name], now))
	}
	return out
}

// RunFleetPush emits FleetUpdatedEvent on a coalescing ticker until ctx is
// cancelled. One emit per tick carries the full current fleet (the frontend
// replaces state). A free function rather than a FleetService method on
// purpose: every exported method on a bound service lands in the JS binding
// surface, and this is a Go lifecycle hook, not an RPC.
func RunFleetPush(ctx context.Context, s *FleetService, em Emitter, interval time.Duration) {
	// Panic net: GetFleet aggregates parsed state across every cluster; a panic
	// here (started detached by main) would crash the whole app. Contain it.
	defer recoverGuard("fleet-push", nil)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			em.Emit(FleetUpdatedEvent, s.GetFleet())
		}
	}
}

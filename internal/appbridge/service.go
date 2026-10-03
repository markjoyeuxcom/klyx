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
	lookup func(string) (config.ClusterConfig, bool)
	now    func() time.Time
}

// NewFleetService builds the fleet DTO source. lookup resolves a cluster's
// config from the LIVE fleet (main passes LiveClusterConfig), so clusters
// hot-added through Settings carry their env/group/protection - a map built
// once at startup gave them zero values, i.e. protected=false for a prd
// context imported mid-session.
func NewFleetService(reg Snapshotter, lookup func(string) (config.ClusterConfig, bool), now func() time.Time) *FleetService {
	return &FleetService{reg: reg, lookup: lookup, now: now}
}

// StaticClusterConfig is a lookup over a fixed config (tests, tools that never
// hot-add). Free function: not part of any bound service.
func StaticClusterConfig(cfg *config.Config) func(string) (config.ClusterConfig, bool) {
	return func(name string) (config.ClusterConfig, bool) {
		for _, c := range cfg.Clusters {
			if c.Name == name {
				return c, true
			}
		}
		return config.ClusterConfig{}, false
	}
}

// GetFleet returns the current fleet as DTOs (bound, callable from JS).
func (s *FleetService) GetFleet() []ClusterDTO {
	snaps := s.reg.Snapshots()
	now := s.now()
	out := make([]ClusterDTO, 0, len(snaps))
	for _, snap := range snaps {
		cc, _ := s.lookup(snap.Name)
		out = append(out, ToDTO(snap, cc, now))
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

package fleet

import (
	"testing"

	"github.com/moomora/klyx/internal/config"
)

// Settings slice F §1: a new incarnation each time the connection becomes
// usable from not usable, told before the new state is visible; a blip
// through Stale, or a capability flip, is not one.
func TestAnIncarnationIsEachReturnToUsable(t *testing.T) {
	c := &ClusterConn{name: "a", state: Connecting}
	var ups []ConnState
	c.SetOnUsable(func(ev UsabilityEvent) {
		if ev.Cluster != "a" {
			t.Errorf("told %q", ev.Cluster)
		}
		ups = append(ups, c.state) // the state before the move: not yet visible
	})
	step := func(ev Event, want int) {
		t.Helper()
		c.setState(ev, "")
		if len(ups) != want {
			t.Fatalf("after %v (now %v): %d incarnations, want %d", ev, c.state, len(ups), want)
		}
	}
	step(EvSynced, 1)       // Connecting -> Synced: the first incarnation
	step(EvCapUnhealthy, 1) // Synced -> Degraded: still usable
	step(EvCapHealthy, 1)   // Degraded -> Synced
	step(EvWatchDrop, 1)    // Synced -> Stale: a blip, still usable
	step(EvSynced, 1)       // Stale -> Synced: the connection never stopped being usable
	step(EvConnError, 1)    // -> Failed
	step(EvSynced, 2)       // Failed -> Synced: the retry path's recovery
	for _, s := range ups {
		if s.Usable() {
			t.Fatalf("told after the state was usable: %v", ups)
		}
	}
}

func TestTheRegistryWiresTheHook(t *testing.T) {
	var told []string
	cc := &ClusterConn{name: "b"}
	r := NewRegistry(&config.Config{Clusters: []config.ClusterConfig{{Name: "b"}}}, func(config.ClusterConfig) (Conn, error) { return cc, nil })
	r.SetOnUsable(func(ev UsabilityEvent) { told = append(told, ev.Cluster) })
	r.wireHealth(cc)
	cc.mu.Lock()
	cc.moveLocked(Synced)
	cc.mu.Unlock()
	if len(told) != 1 || told[0] != "b" {
		t.Fatalf("%v", told)
	}
}

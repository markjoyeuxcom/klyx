package fleet

import (
	"time"

	"github.com/moomora/klyx/internal/capability"
)

// Snapshot is the per-cluster state the view layer consumes. It is a value copy;
// the registry never hands out live pointers into informer caches.
type Snapshot struct {
	Name       string
	State      ConnState
	Reason     string
	LastSync   time.Time
	NodesReady int
	NodesTotal int
	Pods       int
	// PodsKnown: a complete pod read has happened, so Pods is evidence (an
	// observed zero included); false means unknown, never zero. PodsStale:
	// Pods is the last good count, not current.
	PodsKnown    bool
	PodsStale    bool
	Version      string
	Capabilities capability.Set
}

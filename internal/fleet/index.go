package fleet

import (
	"context"

	"github.com/moomora/klyx/internal/fleetindex"
	"github.com/moomora/klyx/internal/healthsrc"
)

// The fleet metadata index (The Line slice 9 note §2.7): each conn hands
// the index its Observer once built and its connection word in the same
// serialized handoff that gives the word to the Observer.

// SetIndex gives the conn the fleet index; it must be called before Start.
func (c *ClusterConn) SetIndex(ix *fleetindex.Index) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.index = ix
}

// indexAcquire is the conn's read path for the kinds the Observer does not
// watch: discovery through the conn's probe, and plain REST requests over
// the conn's authenticated transport (never the metadata client, §2.5).
func (c *ClusterConn) indexAcquire() (fleetindex.Acquire, bool) {
	if c.restConfig == nil {
		return fleetindex.Acquire{}, false
	}
	rd, err := fleetindex.NewReader(c.restConfig)
	if err != nil {
		return fleetindex.Acquire{}, false
	}
	return fleetindex.Acquire{
		Served: func(ctx context.Context, group, resource, prefer string) (string, bool, error) {
			if group == "" {
				return "v1", true, nil // the core group: always v1
			}
			p, err := c.crdServed(ctx, group, resource, prefer)
			if err != nil {
				return "", false, err
			}
			return p.Version, p.Served, nil
		},
		Reader: rd,
	}, true
}

// attachIndex hands the index the conn's Observer.
func (c *ClusterConn) attachIndex() {
	c.mu.RLock()
	ix, h := c.index, c.health
	c.mu.RUnlock()
	if ix == nil || h == nil {
		return
	}
	acq, ok := c.indexAcquire()
	if !ok {
		return
	}
	ix.Attach(c.name, h, acq)
}

// IndexGaps names the Observer projections that keep no labels or no
// creation time (§2.1): ReplicaSets and Services keep identity and creation
// time only; namespaces keep name, UID and, since coverage S7a, labels.
func IndexGaps() map[string]fleetindex.Gap {
	return map[string]fleetindex.Gap{
		healthsrc.KindReplicaSets:     {NoLabels: true},
		healthsrc.KindGatewayServices: {NoLabels: true},
		healthsrc.KindNamespaces:      {NoCreated: true},
	}
}

package fleet

import (
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/clock"
)

// discTTL bounds how long discovery results are reused. Long enough to
// collapse the per-render bursts (a topology render probes 7 policy kinds, a
// cert-chain render resolves versions 3+ times, every lazy-watch open resolves
// preferred versions), short enough that a CRD installed mid-session shows up
// within a minute.
const discTTL = 60 * time.Second

// discoveryLike is the slice of discovery.DiscoveryInterface Klyx's version
// resolution actually uses - small enough to memoize precisely.
type discoveryLike interface {
	ServerGroups() (*metav1.APIGroupList, error)
	ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
}

// cachedDiscovery memoizes ServerGroups and ServerResourcesForGroupVersion
// with a TTL. client-go's memcache client caches until an explicit Invalidate;
// a TTL fits Klyx better (frequent identical probes per render, occasional CRD
// installs) without invalidation plumbing. Errors are never cached - a failed
// probe retries on the next call.
type cachedDiscovery struct {
	disc discoveryLike
	clk  clock.Clock

	mu        sync.Mutex
	groups    *metav1.APIGroupList
	groupsAt  time.Time
	resources map[string]discResourcesEntry
}

type discResourcesEntry struct {
	list *metav1.APIResourceList
	at   time.Time
}

func newCachedDiscovery(disc discoveryLike, clk clock.Clock) *cachedDiscovery {
	if clk == nil {
		clk = clock.Real{}
	}
	return &cachedDiscovery{disc: disc, clk: clk, resources: map[string]discResourcesEntry{}}
}

func (c *cachedDiscovery) ServerGroups() (*metav1.APIGroupList, error) {
	now := c.clk.Now()
	c.mu.Lock()
	if c.groups != nil && now.Sub(c.groupsAt) < discTTL {
		g := c.groups
		c.mu.Unlock()
		return g, nil
	}
	c.mu.Unlock()

	g, err := c.disc.ServerGroups()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.groups = g
	c.groupsAt = now
	c.mu.Unlock()
	return g, nil
}

func (c *cachedDiscovery) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	now := c.clk.Now()
	c.mu.Lock()
	if e, ok := c.resources[groupVersion]; ok && now.Sub(e.at) < discTTL {
		c.mu.Unlock()
		return e.list, nil
	}
	c.mu.Unlock()

	rl, err := c.disc.ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.resources[groupVersion] = discResourcesEntry{list: rl, at: now}
	c.mu.Unlock()
	return rl, nil
}

// discovery returns the conn's TTL-cached discovery view, creating it on first
// use. All per-render version resolution goes through this; capability
// detection keeps its own uncached path (it runs once per connect).
func (c *ClusterConn) discovery() discoveryLike {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disc == nil {
		c.disc = newCachedDiscovery(c.typed.Discovery(), c.clk)
	}
	return c.disc
}

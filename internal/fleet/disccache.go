package fleet

import (
	"context"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

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
	// ctxd is the context-aware network read behind the *Ctx methods: the
	// discovery REST client in production, so a deadline or cancellation
	// reaches the HTTP request itself.
	ctxd ctxDiscovery

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
	return &cachedDiscovery{disc: disc, clk: clk, ctxd: syncDiscovery{disc}, resources: map[string]discResourcesEntry{}}
}

// ctxDiscovery is discovery read with a context.
type ctxDiscovery interface {
	groups(ctx context.Context) (*metav1.APIGroupList, error)
	resources(ctx context.Context, groupVersion string) (*metav1.APIResourceList, error)
}

// restDiscovery reads discovery through the REST client: the request
// carries the context, so a deadline or a cancellation ends it at the
// transport - no work survives its caller. It answers exactly as client-go's
// ServerGroups and ServerResourcesForGroupVersion do - the legacy core group
// (/api) first, then /apis; core versions at /api/<v>, named groups at
// /apis/<group>/<v> - because both paths share one cache (Codex review
// 2026-10-13: a cache warmed from /apis alone had no core group).
type restDiscovery struct{ rc rest.Interface }

func (d restDiscovery) groups(ctx context.Context) (*metav1.APIGroupList, error) {
	var core metav1.APIVersions
	if err := d.rc.Get().AbsPath("/api").Do(ctx).Into(&core); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return nil, err
	}
	var named metav1.APIGroupList
	if err := d.rc.Get().AbsPath("/apis").Do(ctx).Into(&named); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return nil, err
	}
	out := &metav1.APIGroupList{}
	if len(core.Versions) > 0 {
		legacy := metav1.APIGroup{Name: ""}
		for _, v := range core.Versions {
			legacy.Versions = append(legacy.Versions, metav1.GroupVersionForDiscovery{GroupVersion: v, Version: v})
		}
		legacy.PreferredVersion = legacy.Versions[0]
		out.Groups = append(out.Groups, legacy)
	}
	out.Groups = append(out.Groups, named.Groups...)
	return out, nil
}

func (d restDiscovery) resources(ctx context.Context, groupVersion string) (*metav1.APIResourceList, error) {
	path := "/apis/" + groupVersion
	if !strings.Contains(groupVersion, "/") {
		path = "/api/" + groupVersion // the legacy core group ("v1")
	}
	var l metav1.APIResourceList
	if err := d.rc.Get().AbsPath(path).Do(ctx).Into(&l); err != nil {
		return nil, err
	}
	return &l, nil
}

// syncDiscovery adapts a context-free discovery (test fakes, which have no
// REST client): it honours a context already ended, and cannot cancel a
// call in progress.
type syncDiscovery struct{ d discoveryLike }

func (s syncDiscovery) groups(ctx context.Context) (*metav1.APIGroupList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.d.ServerGroups()
}

func (s syncDiscovery) resources(ctx context.Context, groupVersion string) (*metav1.APIResourceList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.d.ServerResourcesForGroupVersion(groupVersion)
}

// ServerGroupsCtx is ServerGroups bounded by ctx. A result whose caller has
// gone is never cached.
func (c *cachedDiscovery) ServerGroupsCtx(ctx context.Context) (*metav1.APIGroupList, error) {
	now := c.clk.Now()
	c.mu.Lock()
	if c.groups != nil && now.Sub(c.groupsAt) < discTTL {
		g := c.groups
		c.mu.Unlock()
		return g, nil
	}
	c.mu.Unlock()
	g, err := c.ctxd.groups(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.groups, c.groupsAt = g, now
	c.mu.Unlock()
	return g, nil
}

// ServerResourcesForGroupVersionCtx is ServerResourcesForGroupVersion
// bounded by ctx, with the same no-late-write rule.
func (c *cachedDiscovery) ServerResourcesForGroupVersionCtx(ctx context.Context, groupVersion string) (*metav1.APIResourceList, error) {
	now := c.clk.Now()
	c.mu.Lock()
	if e, ok := c.resources[groupVersion]; ok && now.Sub(e.at) < discTTL {
		c.mu.Unlock()
		return e.list, nil
	}
	c.mu.Unlock()
	rl, err := c.ctxd.resources(ctx, groupVersion)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.resources[groupVersion] = discResourcesEntry{list: rl, at: now}
	c.mu.Unlock()
	return rl, nil
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
		cd := newCachedDiscovery(c.typed.Discovery(), c.clk)
		if rc := c.typed.Discovery().RESTClient(); rc != nil {
			cd.ctxd = restDiscovery{rc}
		}
		c.disc = cd
	}
	return c.disc
}

// probeDiscovery is the context-aware view the health probes use.
func (c *ClusterConn) probeDiscovery() *cachedDiscovery {
	d := c.discovery()
	if cd, ok := d.(*cachedDiscovery); ok {
		return cd
	}
	return newCachedDiscovery(d, c.clk) // an injected test double
}

// invalidate forgets a group's cached discovery, so the next read asks the
// server (a NotFound at a cached version re-probes once).
func (c *cachedDiscovery) invalidate(group string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.groups = nil
	for gv := range c.resources {
		if strings.HasPrefix(gv, group+"/") {
			delete(c.resources, gv)
		}
	}
}

package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/moomora/klyx/internal/observe"
)

// crdServed is a health Observer kind probe (1c addendum §3.3): the version
// at which a CRD resource is served, or absence, or an error that confirms
// nothing. It differs from servedVersion on one point: a group missing from
// a SUCCESSFUL discovery is absence (the CRD is not installed), never the
// fallback. A discovery error is a gap. As in servedVersion, versions whose
// resource lists cannot be read are uncertainty: when no readable version
// serves the resource, the documented fallback (or the first unreadable
// version) is tried, and its watch shows whether it works.
//
// One deadline, crdProbeTimeout, spans every discovery request of the probe,
// and it and the caller's context reach the HTTP request itself (the
// context-aware discovery read): when the probe returns, no request of it
// is still running, and a result nobody waits for is never cached. A
// deadline reached mid-probe is an error that confirms nothing - never a
// guess at a version.
func (c *ClusterConn) crdServed(ctx context.Context, group, resource, fallback string) (observe.Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, crdProbeTimeout)
	defer cancel()
	disc := c.probeDiscovery()
	groups, err := disc.ServerGroupsCtx(ctx)
	if err != nil {
		return observe.Probe{}, probeErr(group, resource, err)
	}
	var candidates []string
	if groups != nil {
		for _, g := range groups.Groups {
			if g.Name != group {
				continue
			}
			if g.PreferredVersion.Version != "" {
				candidates = append(candidates, g.PreferredVersion.Version)
			}
			for _, v := range g.Versions {
				if v.Version != g.PreferredVersion.Version {
					candidates = append(candidates, v.Version)
				}
			}
		}
	}
	if len(candidates) == 0 {
		return observe.Probe{Served: false}, nil
	}
	var unreadable []string
	for _, v := range candidates {
		rl, err := disc.ServerResourcesForGroupVersionCtx(ctx, group+"/"+v)
		if ctx.Err() != nil {
			return observe.Probe{}, probeErr(group, resource, ctx.Err())
		}
		if err != nil || rl == nil {
			unreadable = append(unreadable, v)
			continue
		}
		for _, r := range rl.APIResources {
			if r.Name == resource {
				return observe.Probe{Served: true, Version: v}, nil
			}
		}
	}
	if len(unreadable) == 0 {
		return observe.Probe{Served: false}, nil
	}
	for _, v := range unreadable {
		if v == fallback {
			return observe.Probe{Served: true, Version: v}, nil
		}
	}
	return observe.Probe{Served: true, Version: unreadable[0]}, nil
}

// crdProbeTimeout bounds one probe across all of a group's versions. A test
// seam.
var crdProbeTimeout = 15 * time.Second

func probeErr(group, resource string, err error) error {
	return fmt.Errorf("discovery of %s/%s: %w", group, resource, err)
}

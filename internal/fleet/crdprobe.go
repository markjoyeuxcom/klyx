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
// Discovery calls take no context, so the probe runs aside: the Observer
// gets its answer within crdProbeTimeout, or a timeout that confirms
// nothing, and at once when its context ends. An abandoned call stays
// bounded by the discovery client's own request timeout (client-go's 32s).
func (c *ClusterConn) crdServed(ctx context.Context, group, resource, fallback string) (observe.Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, crdProbeTimeout)
	defer cancel()
	type result struct {
		p   observe.Probe
		err error
	}
	done := make(chan result, 1)
	go func() {
		p, err := c.crdServedNow(group, resource, fallback)
		done <- result{p, err}
	}()
	select {
	case r := <-done:
		return r.p, r.err
	case <-ctx.Done():
		return observe.Probe{}, fmt.Errorf("discovery of %s/%s did not answer: %w", group, resource, ctx.Err())
	}
}

// crdProbeTimeout bounds one probe across all of a group's versions. A test
// seam.
var crdProbeTimeout = 15 * time.Second

func (c *ClusterConn) crdServedNow(group, resource, fallback string) (observe.Probe, error) {
	disc := c.discovery()
	groups, err := disc.ServerGroups()
	if err != nil {
		return observe.Probe{}, err
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
		rl, err := disc.ServerResourcesForGroupVersion(group + "/" + v)
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

package fleet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

// crdServedAmong is crdServed for a resource read at one of a list of
// candidate versions, in order (coverage S7 §2): the first candidate served
// is read, never the group's preferred version, which says nothing about
// one resource. One deadline spans every discovery request, as in
// crdServed.
func (c *ClusterConn) crdServedAmong(ctx context.Context, group, resource string, candidates []string) (observe.Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, crdProbeTimeout)
	defer cancel()
	disc := c.probeDiscovery()
	groups, err := disc.ServerGroupsCtx(ctx)
	if err != nil {
		return observe.Probe{}, probeErr(group, resource, err)
	}
	return servedAmong(groups, func(gv string) (*metav1.APIResourceList, error) {
		return disc.ServerResourcesForGroupVersionCtx(ctx, gv)
	}, ctx.Err, group, resource, candidates)
}

// servedAmong resolves, from one discovery answer, the version at which
// group serves resource among candidates (S7 §2):
//   - the first candidate whose resource list names the resource;
//   - else, when a candidate's list cannot be read, that candidate - the
//     uncertainty crdServed takes too: its watch shows whether it works;
//   - absence only when discovery answers completely: the group is not
//     advertised, or every advertised version was read and none serves it;
//   - a resource served only at a version outside the candidates is a
//     named gap, never a list read at a version Klyx does not parse, and so
//     is a version that cannot be read when no candidate serves it.
//
// ctxErr ends the probe when its deadline passes mid-way: an error that
// confirms nothing.
func servedAmong(groups *metav1.APIGroupList, resources func(string) (*metav1.APIResourceList, error), ctxErr func() error,
	group, resource string, candidates []string) (observe.Probe, error) {
	var advertised []string
	if groups != nil {
		for _, g := range groups.Groups {
			if g.Name != group {
				continue
			}
			for _, v := range g.Versions {
				advertised = append(advertised, v.Version)
			}
			if pv := g.PreferredVersion.Version; pv != "" && !slices.Contains(advertised, pv) {
				advertised = append(advertised, pv)
			}
		}
	}
	if len(advertised) == 0 {
		return observe.Probe{Served: false}, nil
	}
	serves := func(v string) (bool, error) {
		rl, err := resources(group + "/" + v)
		if e := ctxErr(); e != nil {
			return false, e
		}
		if err != nil || rl == nil {
			return false, errUnreadable
		}
		for _, r := range rl.APIResources {
			if r.Name == resource {
				return true, nil
			}
		}
		return false, nil
	}
	var unreadable []string
	for _, v := range candidates {
		if !slices.Contains(advertised, v) {
			continue
		}
		ok, err := serves(v)
		switch {
		case errors.Is(err, errUnreadable):
			unreadable = append(unreadable, v)
		case err != nil:
			return observe.Probe{}, probeErr(group, resource, err)
		case ok:
			return observe.Probe{Served: true, Version: v}, nil
		}
	}
	if len(unreadable) > 0 {
		return observe.Probe{Served: true, Version: unreadable[0]}, nil
	}
	var others, otherUnreadable []string
	for _, v := range advertised {
		if slices.Contains(candidates, v) {
			continue
		}
		ok, err := serves(v)
		switch {
		case errors.Is(err, errUnreadable):
			otherUnreadable = append(otherUnreadable, v)
		case err != nil:
			return observe.Probe{}, probeErr(group, resource, err)
		case ok:
			others = append(others, v)
		}
	}
	switch {
	case len(others) > 0:
		return observe.Probe{}, fmt.Errorf("%s/%s is served at %s; Klyx reads %s", group, resource, strings.Join(others, ", "), strings.Join(candidates, " or "))
	case len(otherUnreadable) > 0:
		return observe.Probe{}, fmt.Errorf("discovery of %s/%s: %s could not be read", group, resource, strings.Join(otherUnreadable, ", "))
	}
	return observe.Probe{Served: false}, nil
}

var errUnreadable = errors.New("resource list unreadable")

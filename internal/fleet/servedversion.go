package fleet

import (
	"fmt"
	"slices"

	"github.com/moomora/klyx/internal/readfence"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// servedVersion resolves the version at which a group serves one RESOURCE: the
// group's preferred version when it serves the resource, else the first other
// advertised version that does. A group's preferred version does not establish
// that every resource in the group is served there - the Flux source group can
// prefer v1 while OCIRepository is served only at v1beta2 (review 2026-10-03
// #11) - so per-resource discovery decides, as servedResourceGVR does for the
// Gateway policy kinds.
//
// served=false only when discovery COMPLETELY answers: every advertised
// version was read and none serves the resource (that CRD is not installed),
// so callers skip it instead of requesting a GVR the API server will 404.
// Anything less is uncertainty, never absence (batch-3 review R2: one
// unreadable version must not hide an installed OCIRepository). Then the
// request goes to a version that could not be ruled out - the documented
// fallback when it is one of them - so it either works or fails observably in
// the watch's health. When discovery cannot answer at all (it errors, or the
// group is not advertised - as with the fake discovery in tests) the
// documented fallback is returned as served.
//
// A fence that no longer holds is the one error: a context-bound discovery
// (discoveryFor) refused the read, and nothing falls back past it (Settings
// F1 review, finding F2).
func servedVersion(disc discoveryLike, group, resource, fallback string) (version string, served bool, err error) {
	groups, err := disc.ServerGroups()
	if readfence.Failed(err) {
		return "", false, err
	}
	if err != nil || groups == nil {
		return fallback, true, nil
	}
	var candidates []string
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
	if len(candidates) == 0 {
		return fallback, true, nil
	}
	var unreadable []string
	for _, v := range candidates {
		rl, err := disc.ServerResourcesForGroupVersion(group + "/" + v)
		if readfence.Failed(err) {
			return "", false, err
		}
		if err != nil || rl == nil {
			unreadable = append(unreadable, v)
			continue
		}
		for _, r := range rl.APIResources {
			if r.Name == resource {
				return v, true, nil
			}
		}
	}
	if len(unreadable) == 0 {
		return "", false, nil
	}
	for _, v := range unreadable {
		if v == fallback {
			return v, true, nil
		}
	}
	return unreadable[0], true, nil
}

// servedResourceAmong is servedVersion for a resource read at one of a list
// of candidate versions, in order (coverage S7 §2, servedAmong): the first
// candidate served, a candidate discovery could not rule out, or absence
// when discovery answers completely. A resource served only at a version
// outside the candidates is an error. As in servedVersion, discovery that
// cannot answer at all (it errors, or does not advertise the group - the
// fake discovery in tests) yields the first candidate, served: the read
// then fails observably or works.
func servedResourceAmong(disc discoveryLike, group, resource string, candidates []string) (version string, served bool, err error) {
	if len(candidates) == 0 {
		return "", false, fmt.Errorf("%s/%s: no candidate versions", group, resource)
	}
	groups, gerr := disc.ServerGroups()
	if readfence.Failed(gerr) {
		return "", false, gerr // never a fallback past a fence (F1 review, F2)
	}
	if gerr != nil || groups == nil || !slices.ContainsFunc(groups.Groups, func(g metav1.APIGroup) bool { return g.Name == group }) {
		return candidates[0], true, nil
	}
	var fenced error
	resources := func(gv string) (*metav1.APIResourceList, error) {
		rl, err := disc.ServerResourcesForGroupVersion(gv)
		if readfence.Failed(err) {
			fenced = err
		}
		return rl, err
	}
	p, err := servedAmong(groups, resources, func() error { return fenced }, group, resource, candidates)
	if err != nil {
		return "", false, err
	}
	return p.Version, p.Served, nil
}

// preferredVersion returns the served preferred version for a CRD group, or
// fallback when the group is not advertised (the fake discovery in tests has no
// groups, so the fallback is used there).
func preferredVersion(disc discoveryLike, group, fallback string) string {
	groups, err := disc.ServerGroups()
	if err != nil || groups == nil {
		return fallback
	}
	for _, g := range groups.Groups {
		if g.Name == group {
			if g.PreferredVersion.Version != "" {
				return g.PreferredVersion.Version
			}
			if len(g.Versions) > 0 {
				return g.Versions[0].Version
			}
		}
	}
	return fallback
}

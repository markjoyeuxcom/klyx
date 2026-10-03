package fleet

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
func servedVersion(disc discoveryLike, group, resource, fallback string) (version string, served bool) {
	groups, err := disc.ServerGroups()
	if err != nil || groups == nil {
		return fallback, true
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
		return fallback, true
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
				return v, true
			}
		}
	}
	if len(unreadable) == 0 {
		return "", false
	}
	for _, v := range unreadable {
		if v == fallback {
			return v, true
		}
	}
	return unreadable[0], true
}

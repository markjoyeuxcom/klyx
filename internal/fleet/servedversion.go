package fleet

// servedVersion resolves the version at which a group serves one RESOURCE: the
// group's preferred version when it serves the resource, else the first other
// advertised version that does. A group's preferred version does not establish
// that every resource in the group is served there - the Flux source group can
// prefer v1 while OCIRepository is served only at v1beta2 (review 2026-10-03
// #11) - so per-resource discovery decides, as servedResourceGVR does for the
// Gateway policy kinds.
//
// served=false means discovery advertises the group but no version serves the
// resource (that CRD is not installed); callers skip it instead of
// requesting a GVR the API server will 404. When discovery cannot answer (it
// errors, or the group is not advertised - as with the fake discovery in
// tests) the documented fallback is returned as served, preserving the
// pre-discovery behaviour; likewise the preferred version when every
// per-version lookup errors.
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
	answered := false
	for _, v := range candidates {
		rl, err := disc.ServerResourcesForGroupVersion(group + "/" + v)
		if err != nil || rl == nil {
			continue
		}
		answered = true
		for _, r := range rl.APIResources {
			if r.Name == resource {
				return v, true
			}
		}
	}
	if !answered {
		return candidates[0], true
	}
	return "", false
}

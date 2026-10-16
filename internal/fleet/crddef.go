package fleet

import (
	"context"
	"errors"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/crd"
)

// GetCRDDefinition reads one CustomResourceDefinition in full (The Line
// slice 15c), with the schema of version ("" for the storage version).
func (c *ClusterConn) GetCRDDefinition(ctx context.Context, name, version string) (crd.Definition, error) {
	if c.dyn == nil {
		return crd.Definition{}, errors.New("dynamic client unavailable")
	}
	u, err := c.dyn.Resource(crd.GVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return crd.Definition{}, err
	}
	return crd.ParseDefinition(u, version), nil
}

// ListCRDsFromDiscovery is the API browser's fallback when the CRD list
// itself cannot be read (slice 15c): the served groups the kube-apiserver
// does not serve itself, in their preferred version, from the cached
// discovery. Kinds carry no operator - discovery does not say.
func (c *ClusterConn) ListCRDsFromDiscovery(ctx context.Context) ([]crd.Info, error) {
	if c.typed == nil {
		return nil, errors.New("discovery unavailable")
	}
	disc := c.discovery()
	groups, err := disc.ServerGroups()
	if err != nil {
		return nil, err
	}
	var out []crd.Info
	for _, g := range groups.Groups {
		if crd.BuiltinGroups[g.Name] || g.PreferredVersion.GroupVersion == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		list, err := disc.ServerResourcesForGroupVersion(g.PreferredVersion.GroupVersion)
		if err != nil {
			continue // one unreadable group never hides the others
		}
		gv, _ := schema.ParseGroupVersion(g.PreferredVersion.GroupVersion)
		for _, r := range list.APIResources {
			if strings.Contains(r.Name, "/") || !hasVerb(r.Verbs, "list") {
				continue
			}
			scope := "Cluster"
			if r.Namespaced {
				scope = "Namespaced"
			}
			out = append(out, crd.Info{Group: g.Name, Kind: r.Kind, Plural: r.Name, ShortNames: r.ShortNames, Scope: scope, Version: gv.Version})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

func hasVerb(verbs []string, v string) bool {
	for _, x := range verbs {
		if x == v {
			return true
		}
	}
	return false
}

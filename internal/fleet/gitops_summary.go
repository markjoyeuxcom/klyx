package fleet

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gitops/flux"
)

// GitOpsSummary is a cheap on-demand snapshot of Flux reconciliation health,
// read with its own list, used by the Overview attention strip (until slice 4
// moves the strip onto the health Observer's summary).
type GitOpsSummary struct {
	FluxPresent bool
	Total       int
	NotReady    int // excludes suspended — paused on purpose is not attention
	Suspended   int
}

// GitOpsSummaryFlux satisfies the appbridge.GitOpsConn interface, forwarding to
// GitOpsSummary and flattening the struct to scalar return values so that the
// appbridge package does not need to import fleet types.
func (c *ClusterConn) GitOpsSummaryFlux(ctx context.Context) (fluxPresent bool, total, notReady, suspended int, err error) {
	s, err := c.GitOpsSummary(ctx)
	return s.FluxPresent, s.Total, s.NotReady, s.Suspended, err
}

// GitOpsSummary performs a cluster-wide dynamic LIST of Kustomizations and
// HelmReleases, parses each with the existing flux parse functions, and returns
// counts. Flux absent → {FluxPresent: false}, nil. List error → error.
func (c *ClusterConn) GitOpsSummary(ctx context.Context) (GitOpsSummary, error) {
	c.mu.RLock()
	fluxPresent := c.caps.GitOps.Flux.Present
	c.mu.RUnlock()

	if !fluxPresent {
		return GitOpsSummary{FluxPresent: false}, nil
	}

	// Per-resource versions (review 2026-10-03 #11); a kind whose CRD is not
	// installed contributes nothing rather than failing the summary on a 404.
	var resources []flux.Resource
	for _, k := range []struct {
		group, resource, fallback string
		parse                     func(*unstructured.Unstructured) flux.Resource
	}{
		{"kustomize.toolkit.fluxcd.io", "kustomizations", "v1", flux.ParseKustomization},
		{"helm.toolkit.fluxcd.io", "helmreleases", "v2", flux.ParseHelmRelease},
	} {
		ver, served := servedVersion(c.discovery(), k.group, k.resource, k.fallback)
		if !served {
			continue
		}
		list, err := c.dyn.Resource(schema.GroupVersionResource{Group: k.group, Version: ver, Resource: k.resource}).List(ctx, metav1.ListOptions{})
		if err != nil {
			return GitOpsSummary{}, err
		}
		for i := range list.Items {
			resources = append(resources, k.parse(&unstructured.Unstructured{Object: list.Items[i].Object}))
		}
	}

	out := GitOpsSummary{FluxPresent: true, Total: len(resources)}
	for _, r := range resources {
		if r.Suspended {
			out.Suspended++
		} else if r.Ready != flux.Ready {
			out.NotReady++
		}
	}
	return out, nil
}

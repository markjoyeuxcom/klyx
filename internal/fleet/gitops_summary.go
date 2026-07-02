package fleet

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gitops/flux"
)

// GitOpsSummary is a cheap on-demand snapshot of Flux reconciliation health.
// It is independent of the lazy watch (OpenGitOps) and is used by the Overview
// attention strip.
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

	ksVer := preferredVersion(c.discovery(), "kustomize.toolkit.fluxcd.io", "v1")
	hrVer := preferredVersion(c.discovery(), "helm.toolkit.fluxcd.io", "v2")
	ksGVR := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: ksVer, Resource: "kustomizations"}
	hrGVR := schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: hrVer, Resource: "helmreleases"}

	var resources []flux.Resource

	ksList, err := c.dyn.Resource(ksGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return GitOpsSummary{}, err
	}
	for i := range ksList.Items {
		u := &unstructured.Unstructured{Object: ksList.Items[i].Object}
		resources = append(resources, flux.ParseKustomization(u))
	}

	hrList, err := c.dyn.Resource(hrGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return GitOpsSummary{}, err
	}
	for i := range hrList.Items {
		u := &unstructured.Unstructured{Object: hrList.Items[i].Object}
		resources = append(resources, flux.ParseHelmRelease(u))
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

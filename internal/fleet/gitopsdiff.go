package fleet

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/cachediff"
	"github.com/moomora/klyx/internal/fluxcli"
	"github.com/moomora/klyx/internal/gitops/flux"
)

// newFluxRunner builds the flux Runner for one call, carrying the cluster's
// subprocess environment (its KUBECONFIG selection); swappable in tests.
var newFluxRunner = func(env []string) fluxcli.Runner { return fluxcli.ExecRunner{Env: env} }

// FluxAvailable reports whether the flux CLI is resolvable (so the UI can hide
// the "compute diff" affordance when it isn't installed).
func (c *ClusterConn) FluxAvailable() bool { return fluxcli.Detect() }

// FluxDiffKustomization runs an on-demand `flux diff` for a Kustomization. It is
// GATED: only suspended or apply-failing (Ready=False) Kustomizations - a diff on
// a healthy auto-reconciling one is empty/misleading because Flux heals drift
// each interval. The flux CLI builds locally from path and dry-runs against the
// cluster, inheriting the shell's per-cloud auth for SOPS/KMS. When path is
// empty the resource's spec.path is used.
func (c *ClusterConn) FluxDiffKustomization(ctx context.Context, ns, name, path string) (fluxcli.DiffResult, error) {
	gvr, err := c.gvrForKind(ctx, flux.KustomizationKind)
	if err != nil {
		return fluxcli.DiffResult{}, err
	}
	u, err := c.dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fluxcli.DiffResult{}, fmt.Errorf("kustomization %s/%s: %w", ns, name, err)
	}
	r := flux.ParseKustomization(u)
	if !r.Suspended && r.Ready != flux.Failed {
		return fluxcli.DiffResult{}, fmt.Errorf("diff is only available for suspended or failing Kustomizations")
	}
	if path == "" {
		path, _, _ = unstructured.NestedString(u.Object, "spec", "path")
	}
	return fluxcli.DiffKustomization(ctx, newFluxRunner(c.cliEnv()), c.kubeContext, ns, name, path), nil
}

// ServerSideDiff dry-runs objects built from the Git cache on this
// station's own pinned connection, with Flux's field manager (The Line
// slice 6 note §7.1 point 6).
func (c *ClusterConn) ServerSideDiff(ctx context.Context, objs []*unstructured.Unstructured, inventory []string, prune bool) ([]cachediff.Change, error) {
	if c.restConfig == nil {
		return nil, fmt.Errorf("no connection for a server-side dry-run")
	}
	return cachediff.ServerSideDiff(ctx, c.restConfig, objs, inventory, prune)
}

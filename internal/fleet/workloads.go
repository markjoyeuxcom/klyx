package fleet

import (
	"context"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/workloads"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ListWorkloads lists Deploy/StatefulSet/DaemonSet + Pods scoped to namespace
// ("" = all; a set namespace scopes the typed list at source) and assembles
// their health. Returns whether Flux is present (so the UI can distinguish
// "no Flux" from "not Flux-managed") and whether any of the four lists was
// truncated at liveListCap. On-demand; no watch.
func (c *ClusterConn) ListWorkloads(ctx context.Context, namespace string) ([]workloads.Workload, bool, bool, error) {
	c.mu.RLock()
	fluxPresent := c.caps.GitOps.Flux.Present
	c.mu.RUnlock()

	opts := metav1.ListOptions{Limit: liveListCap}
	deps, err := c.typed.AppsV1().Deployments(namespace).List(ctx, opts)
	if err != nil {
		return nil, fluxPresent, false, err
	}
	stss, err := c.typed.AppsV1().StatefulSets(namespace).List(ctx, opts)
	if err != nil {
		return nil, fluxPresent, false, err
	}
	dss, err := c.typed.AppsV1().DaemonSets(namespace).List(ctx, opts)
	if err != nil {
		return nil, fluxPresent, false, err
	}
	pods, err := c.typed.CoreV1().Pods(namespace).List(ctx, opts)
	if err != nil {
		return nil, fluxPresent, false, err
	}
	// ReplicaSets carry the Deployment ownership chain for health
	// attribution. Unreadable (e.g. RBAC) is not fatal: Deployment health
	// then reports unverified instead of guessing.
	ev := workloads.Evidence{PodsComplete: pods.GetContinue() == ""}
	var rsItems []appsv1.ReplicaSet
	if rss, rerr := c.typed.AppsV1().ReplicaSets(namespace).List(ctx, opts); rerr == nil {
		rsItems = rss.Items
		ev.ReplicaSetsComplete = rss.GetContinue() == ""
	}
	truncated := deps.GetContinue() != "" || stss.GetContinue() != "" || dss.GetContinue() != "" || pods.GetContinue() != ""

	clk := c.clk
	if clk == nil {
		clk = clock.Real{}
	}
	return workloads.AssembleObserved(deps.Items, rsItems, stss.Items, dss.Items, pods.Items, ev, fluxPresent, clk.Now()), fluxPresent, truncated, nil
}

package fleet

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gitops/flux"
)

// PromotionUnit is one GitOps unit's applied state on ONE cluster - the row
// unit of the fleet promotion tracker ("did my change land everywhere?").
// Kind is Kustomization/HelmRelease (Flux) or Application (Argo).
type PromotionUnit struct {
	Kind       string
	Namespace  string
	Name       string
	SourceKind string
	SourceName string
	Revision   string // applied/deployed revision - "" when never reconciled
	Ready      string // "ready" | "not-ready" | "unknown"
	Suspended  bool
	Stale      bool // spec observed lag - render as transitioning, never green
	Message    string
}

// PromotionSource is one source's fetched head on ONE cluster. Each cluster
// fetches independently, so differing heads across the fleet are themselves a
// propagation signal (a cluster whose source-controller is stuck).
type PromotionSource struct {
	Kind      string
	Namespace string
	Name      string
	Revision  string
	Ready     string
	Suspended bool
}

// PromotionSnapshot is one cluster's full promotion state.
type PromotionSnapshot struct {
	FluxPresent bool
	ArgoPresent bool
	Units       []PromotionUnit
	Sources     []PromotionSource
}

func readyString(r flux.ReadyState) string {
	switch r {
	case flux.Ready:
		return "ready"
	case flux.Failed, flux.Reconciling:
		return "not-ready"
	default:
		return "unknown"
	}
}

// GitOpsPromotion returns this cluster's applied GitOps state for the fleet
// promotion tracker: Flux Kustomizations/HelmReleases with their applied
// revisions, source heads (GitRepository/OCIRepository artifact revisions),
// and Argo Applications with their deployed sync revision. On-demand dynamic
// lists - independent of the lazy GitOps watch, exactly like GitOpsSummary.
// Neither tool present → an honest empty snapshot, nil error.
func (c *ClusterConn) GitOpsPromotion(ctx context.Context) (PromotionSnapshot, error) {
	c.mu.RLock()
	fluxPresent := c.caps.GitOps.Flux.Present
	argoPresent := c.caps.GitOps.Argo.Present
	c.mu.RUnlock()

	out := PromotionSnapshot{FluxPresent: fluxPresent, ArgoPresent: argoPresent}

	if fluxPresent {
		disc := c.discovery()
		lists := []struct {
			gvr   schema.GroupVersionResource
			parse func(*unstructured.Unstructured) flux.Resource
		}{
			{schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: preferredVersion(disc, "kustomize.toolkit.fluxcd.io", "v1"), Resource: "kustomizations"}, flux.ParseKustomization},
			{schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: preferredVersion(disc, "helm.toolkit.fluxcd.io", "v2"), Resource: "helmreleases"}, flux.ParseHelmRelease},
		}
		for _, l := range lists {
			ul, err := c.dyn.Resource(l.gvr).List(ctx, metav1.ListOptions{})
			if err != nil {
				return PromotionSnapshot{}, err
			}
			for i := range ul.Items {
				r := l.parse(&unstructured.Unstructured{Object: ul.Items[i].Object})
				out.Units = append(out.Units, PromotionUnit{
					Kind: string(r.Kind), Namespace: r.Namespace, Name: r.Name,
					SourceKind: r.SourceKind, SourceName: r.SourceName,
					Revision: r.Revision, Ready: readyString(r.Ready),
					Suspended: r.Suspended, Stale: r.Stale, Message: r.Message,
				})
			}
		}

		srcGVRs := []schema.GroupVersionResource{
			{Group: "source.toolkit.fluxcd.io", Version: preferredVersion(disc, "source.toolkit.fluxcd.io", "v1"), Resource: "gitrepositories"},
			{Group: "source.toolkit.fluxcd.io", Version: preferredVersion(disc, "source.toolkit.fluxcd.io", "v1"), Resource: "ocirepositories"},
		}
		for _, gvr := range srcGVRs {
			ul, err := c.dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
			if err != nil {
				return PromotionSnapshot{}, err
			}
			for i := range ul.Items {
				s := flux.ParseSource(&unstructured.Unstructured{Object: ul.Items[i].Object})
				out.Sources = append(out.Sources, PromotionSource{
					Kind: string(s.Kind), Namespace: s.Namespace, Name: s.Name,
					Revision: s.Revision, Ready: readyString(s.Ready), Suspended: s.Suspended,
				})
			}
		}
	}

	if argoPresent {
		apps, err := c.ListArgoApps(ctx)
		if err != nil {
			return PromotionSnapshot{}, err
		}
		for _, a := range apps {
			// Argo vocabulary maps onto the row: Synced+Healthy reads as ready;
			// Suspended health is the intended-pause state (ratified 2026-07-02).
			ready := "not-ready"
			if a.SyncStatus == "Synced" && a.HealthStatus == "Healthy" {
				ready = "ready"
			} else if a.SyncStatus == "Unknown" || a.SyncStatus == "" {
				ready = "unknown"
			}
			out.Units = append(out.Units, PromotionUnit{
				Kind: "Application", Namespace: a.Namespace, Name: a.Name,
				SourceKind: "Repo", SourceName: a.RepoURL,
				Revision: a.Revision, Ready: ready,
				Suspended: a.HealthStatus == "Suspended",
				Message:   a.SyncStatus + "/" + a.HealthStatus,
			})
		}
	}

	return out, nil
}

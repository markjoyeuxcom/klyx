package fleet

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/gitops/argo"
	"github.com/moomora/klyx/internal/gitops/flux"
)

// GitOpsOwnership is the authoritative pre-write verdict for one target object:
// which GitOps unit OWNS it (an edit reverts on the next reconcile) and which
// unit REACHES its namespace. Derived from Flux Kustomization status.inventory
// and Argo Application status.resources - not from object labels, which miss
// unlabeled-but-managed objects entirely.
//
// Namespace reach is CONTEXT, not a pruning verdict (review 2026-10-03 #13):
// Flux garbage-collects only objects in its own inventory and Argo prunes only
// resources it tracks, so an out-of-band object in a reached namespace is left
// alone. The real conditional risk is NamespaceManaged: when the reaching unit
// manages the Namespace object itself and prunes, removing that namespace from
// Git deletes it - and everything in it.
//
// Honesty contract: object-level ownership (Basis "flux-inventory" /
// "argo-resources") is authoritative. Namespace-level reach is a HEURISTIC in
// the safe direction - a unit whose manifests set namespaces inline and which
// currently owns nothing in the namespace is invisible here - so an empty
// NamespaceOwnerKind means "no managing unit detected", never "safe".
type GitOpsOwnership struct {
	// Object-level: the unit whose inventory/resources contains the target.
	OwnerKind      string // "Kustomization" | "Application" | ""
	OwnerNamespace string
	OwnerName      string
	OwnerPrunes    bool
	OwnerSuspended bool
	Basis          string // "flux-inventory" | "argo-resources" | ""

	// Namespace-level: a unit deploys into the target namespace. When several
	// do, the one managing the Namespace object itself is reported.
	NamespaceOwnerKind      string
	NamespaceOwnerNamespace string
	NamespaceOwnerName      string
	NamespacePrunes         bool
	NamespaceManaged        bool   // the unit's inventory/resources hold the Namespace object
	NamespaceBasis          string // "flux" | "argo" | ""

	// ArgoApps lists EVERY Argo Application reaching (or managing) the target
	// namespace - whether one of them would treat a new object as its own is a
	// separate question from which unit manages the namespace (batch-4 review).
	ArgoApps []ArgoAppRef

	// Warnings carries degraded-check honesty (a failing list never reads as
	// "unmanaged" silently).
	Warnings []string
}

// ArgoAppRef names one Argo Application and whether it prunes automatically.
type ArgoAppRef struct {
	Namespace string
	Name      string
	Prunes    bool
}

// noteReach records a unit reaching the target namespace, preferring one that
// manages the Namespace object itself (the unit whose pruning can delete it).
func (o *GitOpsOwnership) noteReach(kind, ns, name string, prunes, managed bool, basis string) {
	if o.NamespaceOwnerKind != "" && (o.NamespaceManaged || !managed) {
		return
	}
	o.NamespaceOwnerKind = kind
	o.NamespaceOwnerNamespace = ns
	o.NamespaceOwnerName = name
	o.NamespacePrunes = prunes
	o.NamespaceManaged = managed
	o.NamespaceBasis = basis
}

// ownershipListCap bounds the fallback live lists; Kustomizations/Applications
// number in the dozens on real fleets.
const ownershipListCap = 500

// GitOpsOwnership computes the pre-write verdict for (group, kind, ns, name).
// Reads the Flux informer stores when the GitOps watch is open (no API calls),
// falling back to bounded live lists; Argo Applications are always live-listed
// (they are not watched). Failures degrade to Warnings, never to a silent
// "unmanaged".
func (c *ClusterConn) GitOpsOwnership(ctx context.Context, group, kind, ns, name string) GitOpsOwnership {
	out := GitOpsOwnership{}

	c.mu.RLock()
	fluxPresent := c.caps.GitOps.Flux.Present
	argoPresent := c.caps.GitOps.Argo.Present
	c.mu.RUnlock()

	if fluxPresent {
		kss, warn := c.kustomizationsForOwnership(ctx)
		if warn != "" {
			out.Warnings = append(out.Warnings, warn)
		}
		for _, u := range kss {
			s := flux.ParseOwnershipSignals(u)
			if out.OwnerKind == "" && s.Owns(group, kind, ns, name) {
				out.OwnerKind = "Kustomization"
				out.OwnerNamespace = s.Namespace
				out.OwnerName = s.Name
				out.OwnerPrunes = s.Prune
				out.OwnerSuspended = s.Suspended
				out.Basis = "flux-inventory"
			}
			// Managing the Namespace object itself is the strongest reach.
			if managed := ns != "" && s.Owns("", "Namespace", "", ns); managed || s.ReachesNamespace(ns) {
				out.noteReach("Kustomization", s.Namespace, s.Name, s.Prune, managed, "flux")
			}
		}
	}

	if argoPresent {
		apps, err := c.ListArgoApps(ctx)
		if err != nil {
			out.Warnings = append(out.Warnings, "Argo ownership check failed: "+err.Error())
		}
		for _, a := range apps {
			if out.OwnerKind == "" && argoOwns(a, group, kind, ns, name) {
				out.OwnerKind = "Application"
				out.OwnerNamespace = a.Namespace
				out.OwnerName = a.Name
				out.OwnerPrunes = a.AutoPrune
				out.Basis = "argo-resources"
			}
			if managed := ns != "" && argoOwns(a, "", "Namespace", "", ns); managed || argoReaches(a, ns) {
				out.noteReach("Application", a.Namespace, a.Name, a.AutoPrune, managed, "argo")
				out.ArgoApps = append(out.ArgoApps, ArgoAppRef{Namespace: a.Namespace, Name: a.Name, Prunes: a.AutoPrune})
			}
		}
	}

	return out
}

// kustomizationsForOwnership returns the Kustomization unstructureds - from the
// open GitOps informer store when available (free), else one bounded live list.
// The warning distinguishes a degraded check from a clean "none found".
func (c *ClusterConn) kustomizationsForOwnership(ctx context.Context) ([]*unstructured.Unstructured, string) {
	c.mu.RLock()
	g := c.gitops
	c.mu.RUnlock()
	if g != nil && g.ksInf != nil && g.ksInf.HasSynced() {
		items := g.ksInf.GetStore().List()
		out := make([]*unstructured.Unstructured, 0, len(items))
		for _, o := range items {
			if u, ok := o.(*unstructured.Unstructured); ok {
				out = append(out, u)
			}
		}
		return out, ""
	}
	if c.dyn == nil {
		return nil, "Flux ownership check unavailable: no cluster connection"
	}
	// discovery() needs the typed client; without it fall back to the modern
	// Kustomization version rather than panic (tests, degraded conns).
	ver := "v1"
	if c.typed != nil {
		v, served := servedVersion(c.discovery(), "kustomize.toolkit.fluxcd.io", "kustomizations", "v1")
		if !served {
			return nil, "" // no Kustomization CRD: nothing can own the object
		}
		ver = v
	}
	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: ver, Resource: "kustomizations"}
	list, err := c.dyn.Resource(gvr).List(ctx, metav1.ListOptions{Limit: ownershipListCap})
	if err != nil {
		return nil, "Flux ownership check failed: " + err.Error()
	}
	out := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, ""
}

func argoOwns(a argo.App, group, kind, ns, name string) bool {
	for _, r := range a.Resources {
		if r.Group == group && r.Kind == kind && r.Namespace == ns && r.Name == name {
			return true
		}
	}
	return false
}

// argoReaches mirrors flux.OwnershipSignals.ReachesNamespace: destination
// namespace or an already-managed resource in ns. Same safe-direction heuristic.
func argoReaches(a argo.App, ns string) bool {
	if ns == "" {
		return false
	}
	if a.DestNamespace == ns {
		return true
	}
	for _, r := range a.Resources {
		if r.Namespace == ns {
			return true
		}
	}
	return false
}

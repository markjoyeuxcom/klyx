package fleet

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/capability"
)

// capHealth watches the GitOps controller workloads for the present tools and
// recomputes the GitOps tier from their informer stores on every change.
type capHealth struct {
	set     capability.Set
	fluxInf cache.SharedIndexInformer // nil if Flux absent
	argoInf cache.SharedIndexInformer // nil if Argo absent
	apply   func(capability.GitOpsCapability)
}

// startCapHealth begins watching controller health for the present GitOps tools.
// No-op when no GitOps tool is present. Informers run on ctx (parent), so they
// retry/relist alongside the eager set.
func (c *ClusterConn) startCapHealth(ctx context.Context, set capability.Set) {
	refs := capability.ControllerRefs(set)
	if len(refs) == 0 {
		return
	}

	h := &capHealth{set: set, apply: c.applyGitOpsHealth}

	// Two passes: assign every informer field and register handlers first, start
	// factories second. A started informer's handler runs recompute() on an
	// informer goroutine and reads BOTH h.fluxInf and h.argoInf - starting the
	// first factory while the second field is still being assigned is a data
	// race (and a tier computed without the other tool) on Flux+Argo clusters.
	var factories []informers.SharedInformerFactory
	for _, ref := range refs {
		factory := informers.NewSharedInformerFactoryWithOptions(c.typed, defaultResync,
			informers.WithNamespace(ref.Namespace),
			informers.WithTweakListOptions(func(o *metav1.ListOptions) {
				o.FieldSelector = "metadata.name=" + ref.Name
			}),
		)
		var inf cache.SharedIndexInformer
		switch ref.Kind {
		case "Deployment":
			inf = factory.Apps().V1().Deployments().Informer()
		case "StatefulSet":
			inf = factory.Apps().V1().StatefulSets().Informer()
		default:
			continue // unknown workload kind; nothing to watch
		}
		switch ref.Tool {
		case "flux":
			h.fluxInf = inf
		case "argo":
			h.argoInf = inf
		}
		_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    func(interface{}) { h.recompute() },
			UpdateFunc: func(interface{}, interface{}) { h.recompute() },
			DeleteFunc: func(interface{}) { h.recompute() },
		})
		factories = append(factories, factory)
	}
	for _, factory := range factories {
		c.addFactory(factory)
		factory.Start(ctx.Done())
	}

	// After starting the controller informers, do one reconciliation once they
	// have synced, so the monitor's own view (not just the initial Detect)
	// establishes the tier and no boot-window health change is missed.
	var syncs []cache.InformerSynced
	if h.fluxInf != nil {
		syncs = append(syncs, h.fluxInf.HasSynced)
	}
	if h.argoInf != nil {
		syncs = append(syncs, h.argoInf.HasSynced)
	}
	c.goTracked(func() {
		if cache.WaitForCacheSync(ctx.Done(), syncs...) {
			h.recompute()
		}
	})
}

// recompute reads controller readiness from the informer stores and applies the
// resulting GitOps capability. It waits until all present controller informers
// have synced, to avoid a spurious Degraded before the first list completes.
func (h *capHealth) recompute() {
	if h.fluxInf != nil && !h.fluxInf.HasSynced() {
		return
	}
	if h.argoInf != nil && !h.argoInf.HasSynced() {
		return
	}
	var flux, argo capability.ControllerRead
	if h.fluxInf != nil {
		ns, name := capability.FluxControllerRef()
		flux = capability.ReadFromWorkload("Deployment", ns, name, deploymentReadyFromStore(h.fluxInf))
	}
	if h.argoInf != nil {
		ns, name := capability.ArgoControllerRef()
		argo = capability.ReadFromWorkload("StatefulSet", ns, name, statefulSetReadyFromStore(h.argoInf))
	}
	h.apply(capability.WithGitOpsHealth(h.set, flux, argo))
}

// deploymentReadyFromStore: nil when the synced store holds no workload -
// none under the conventional name, which is unverified, never an outage.
func deploymentReadyFromStore(inf cache.SharedIndexInformer) *bool {
	for _, obj := range inf.GetStore().List() {
		if d, ok := obj.(*appsv1.Deployment); ok {
			r := capability.DeploymentReady(d)
			return &r
		}
	}
	return nil
}

func statefulSetReadyFromStore(inf cache.SharedIndexInformer) *bool {
	for _, obj := range inf.GetStore().List() {
		if s, ok := obj.(*appsv1.StatefulSet); ok {
			r := capability.StatefulSetReady(s)
			return &r
		}
	}
	return nil
}

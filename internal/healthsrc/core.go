// Package healthsrc builds the health Observer's sources and tasks
// (docs/superpowers/specs/2026-10-11-klyx-health-1c-cluster-summary.md §1):
// the watched kinds with their projections, and each task's parts with
// their evaluators. Verdict tasks reuse the exact builders the lenses use
// today (internal/workloads, internal/health), so a projected object yields
// the same rows and verdicts as the raw one (the §1.2 parity gate).
package healthsrc

import (
	"context"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"

	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// Source and task names. Task ids are nav registry destination ids.
const (
	KindPods         = "pods"
	KindDeployments  = "deployments"
	KindStatefulSets = "statefulsets"
	KindDaemonSets   = "daemonsets"
	KindReplicaSets  = "replicasets"
	KindNodes        = "nodes"
	KindNamespaces   = "namespaces"

	// FactFluxPresent: whether Flux is installed. It gates GitOps owner
	// extraction on workload rows, so it is an Observer fact - captured
	// with the objects and re-evaluating Workloads when it changes.
	FactFluxPresent = "fluxPresent"

	TaskWorkloads = "workloads"
	TaskPods      = "pods"
	TaskNodes     = "nodes"
)

// CoreDeps are the clients the core tasks need. Capability facts reach the
// Observer through SetFact (FactFluxPresent).
type CoreDeps struct {
	Cluster string
	Typed   kubernetes.Interface
	Meta    metadata.Interface
}

// Core returns the core kinds (no discovery: always served) and the
// Workloads, Pods and Nodes verdict tasks.
func Core(d CoreDeps) ([]observe.KindSpec, []observe.DomainSpec) {
	typed := d.Typed
	kinds := []observe.KindSpec{
		{Name: KindPods, GVR: "v1/pods", Scope: "all namespaces", ExpectedType: &corev1.Pod{}, Project: ProjectPod,
			NewListWatch: lw(typed, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return typed.CoreV1().Pods("").List(ctx, o)
			}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return typed.CoreV1().Pods("").Watch(ctx, o)
			})},
		{Name: KindDeployments, GVR: "apps/v1/deployments", Scope: "all namespaces", ExpectedType: &appsv1.Deployment{}, Project: ProjectDeployment,
			NewListWatch: lw(typed, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return typed.AppsV1().Deployments("").List(ctx, o)
			}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return typed.AppsV1().Deployments("").Watch(ctx, o)
			})},
		{Name: KindStatefulSets, GVR: "apps/v1/statefulsets", Scope: "all namespaces", ExpectedType: &appsv1.StatefulSet{}, Project: ProjectStatefulSet,
			NewListWatch: lw(typed, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return typed.AppsV1().StatefulSets("").List(ctx, o)
			}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return typed.AppsV1().StatefulSets("").Watch(ctx, o)
			})},
		{Name: KindDaemonSets, GVR: "apps/v1/daemonsets", Scope: "all namespaces", ExpectedType: &appsv1.DaemonSet{}, Project: ProjectDaemonSet,
			NewListWatch: lw(typed, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return typed.AppsV1().DaemonSets("").List(ctx, o)
			}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return typed.AppsV1().DaemonSets("").Watch(ctx, o)
			})},
		metaKind(d.Meta, KindReplicaSets, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, "all namespaces", ProjectOwnerMeta),
		{Name: KindNodes, GVR: "v1/nodes", Scope: "cluster-scoped", ExpectedType: &corev1.Node{}, Project: ProjectNode, Tally: NodeReadyTally,
			NewListWatch: lw(typed, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return typed.CoreV1().Nodes().List(ctx, o)
			}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return typed.CoreV1().Nodes().Watch(ctx, o)
			})},
		namespacesKind(d.Meta),
	}
	domains := []observe.DomainSpec{
		{ID: TaskWorkloads, Parts: []observe.PartSpec{{
			ID: "workloads", Entity: "workloads (Deployments, StatefulSets, DaemonSets)", Scope: "all namespaces", Classifier: "verdict",
			Counted: []string{KindDeployments, KindStatefulSets, KindDaemonSets}, Deps: []string{KindPods, KindReplicaSets},
			Facts:    []string{FactFluxPresent},
			Evaluate: evalWorkloads(d.Cluster), Ledger: true,
		}}},
		{ID: TaskPods, Parts: []observe.PartSpec{{
			ID: "pods", Entity: "pods", Scope: "all namespaces", Classifier: "verdict",
			Counted: []string{KindPods}, Deps: []string{KindReplicaSets},
			Evaluate: evalPods(d.Cluster), Ledger: true,
		}}},
		{ID: TaskNodes, Parts: []observe.PartSpec{{
			ID: "nodes", Entity: "nodes", Scope: "cluster-scoped", Classifier: "verdict",
			Counted:  []string{KindNodes},
			Evaluate: evalNodes(d.Cluster), Ledger: true,
		}}},
	}
	return kinds, domains
}

// lw builds a kind's source. The client's watch-list support is forwarded
// (cache.ToListWatcherWithWatchListSemantics): real clients stream the
// initial list; fake clientsets, which cannot, take the plain list.
func lw(client any, list func(context.Context, metav1.ListOptions) (runtime.Object, error),
	watchFn func(context.Context, metav1.ListOptions) (watch.Interface, error)) func(string) cache.ListerWatcherWithContext {
	return func(string) cache.ListerWatcherWithContext {
		return cache.ToListWatcherWithWatchListSemantics(
			&cache.ListWatch{ListWithContextFunc: list, WatchFuncWithContext: watchFn}, client).(cache.ListerWatcherWithContext)
	}
}

// The namespaces budget (coverage S7 §8): labels are kept for the Gateway
// API's namespace selectors, within 256 KiB encoded per cluster, a namespace
// at most 4 KiB. Past it the kind reads truncated and a selector over it is
// not established; the Summary's namespace count stays exact (the Observer
// counts what it left out).
const (
	budgetNamespaces    = 256 << 10
	maxNamespaceProject = 4 << 10
)

func namespacesKind(m metadata.Interface) observe.KindSpec {
	ks := metaKind(m, KindNamespaces, schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "cluster-scoped", ProjectNamespaceMeta)
	ks.Budget, ks.MaxObject, ks.Size = budgetNamespaces, maxNamespaceProject, namespaceSize
	return ks
}

func metaKind(m metadata.Interface, name string, gvr schema.GroupVersionResource, scope string, project func(any) (any, error)) observe.KindSpec {
	return observe.KindSpec{Name: name, GVR: gvrString(gvr), Scope: scope, ExpectedType: &metav1.PartialObjectMetadata{}, Project: project,
		NewListWatch: lw(m, func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return m.Resource(gvr).List(ctx, o)
		}, func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return m.Resource(gvr).Watch(ctx, o)
		})}
}

func gvrString(g schema.GroupVersionResource) string {
	if g.Group == "" {
		return g.Version + "/" + g.Resource
	}
	return g.Group + "/" + g.Version + "/" + g.Resource
}

// ---- evaluators -------------------------------------------------------------

func evalWorkloads(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		deploys, stss, dss, reload := []appsv1.Deployment{}, []appsv1.StatefulSet{}, []appsv1.DaemonSet{}, map[types.UID]*workloads.ReloadTargets{}
		for _, o := range c.Objects[KindDeployments] {
			p := o.(*ProjectedDeployment)
			deploys = append(deploys, p.Deployment)
			reload[p.UID] = p.Reload
		}
		for _, o := range c.Objects[KindStatefulSets] {
			p := o.(*ProjectedStatefulSet)
			stss = append(stss, p.StatefulSet)
			reload[p.UID] = p.Reload
		}
		for _, o := range c.Objects[KindDaemonSets] {
			p := o.(*ProjectedDaemonSet)
			dss = append(dss, p.DaemonSet)
			reload[p.UID] = p.Reload
		}
		podObjs, podsRead := c.Objects[KindPods]
		rsObjs, rsRead := c.Objects[KindReplicaSets]
		pods := podsOf(podObjs)
		rss := make([]appsv1.ReplicaSet, 0, len(rsObjs))
		for _, o := range rsObjs {
			rss = append(rss, appsv1.ReplicaSet{ObjectMeta: o.(*metav1.PartialObjectMetadata).ObjectMeta})
		}
		// Evidence: a dependency that is not read (checking, unreadable)
		// makes attribution unverified, never assumed complete.
		ev := workloads.Evidence{PodsComplete: podsRead, ReplicaSetsComplete: rsRead}
		flux, _ := c.Facts[FactFluxPresent].(bool)
		ws := workloads.AssembleProjected(deploys, rss, stss, dss, pods, ev, flux, c.Now, reload)

		verdicts := make([]health.Verdict, len(ws))
		refs := make([]refInput, len(ws))
		for i := range ws {
			verdicts[i] = ws[i].Health
			refs[i] = refInput{gvr: workloadGVR(ws[i].Kind), ns: ws[i].Namespace, name: ws[i].Name, uid: string(ws[i].UID), generation: ws[i].Generation,
				source: workloadSource(ws[i].Kind)}
		}
		r := verdictResult(c.RoleCap, cluster, TaskWorkloads, verdicts, refs)
		if c.Rows {
			r.Rows = RowSet[workloads.Workload]{Rows: verdictRows(ws, refs), Facts: map[string]any{FactFluxPresent: flux}}
		}
		return r
	}
}

func evalPods(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		pods := podsOf(c.Objects[KindPods])
		sums := workloads.SummarizePods(pods, c.Now)
		controllers := map[types.UID]metav1.OwnerReference{}
		for _, o := range c.Objects[KindReplicaSets] {
			m := o.(*metav1.PartialObjectMetadata)
			if ref := metav1.GetControllerOf(m); ref != nil {
				controllers[m.UID] = *ref
			}
		}
		workloads.ResolveReplicaSetOwners(sums, controllers)
		verdicts := make([]health.Verdict, len(sums))
		refs := make([]refInput, len(sums))
		for i := range sums {
			verdicts[i] = sums[i].Health
			refs[i] = refInput{gvr: "v1/pods", ns: sums[i].Namespace, name: sums[i].Name, uid: string(sums[i].UID), generation: sums[i].Generation, source: KindPods}
		}
		r := verdictResult(c.RoleCap, cluster, TaskPods, verdicts, refs)
		if c.Rows {
			r.Rows = rowSet(verdictRows(sums, refs))
		}
		return r
	}
}

func evalNodes(cluster string) func(observe.Capture) observe.PartResult {
	return func(c observe.Capture) observe.PartResult {
		nodes := make([]corev1.Node, 0, len(c.Objects[KindNodes]))
		for _, o := range c.Objects[KindNodes] {
			nodes = append(nodes, *o.(*corev1.Node))
		}
		sums := workloads.SummarizeNodes(nodes, c.Now)
		verdicts := make([]health.Verdict, len(sums))
		refs := make([]refInput, len(sums))
		for i := range sums {
			verdicts[i] = sums[i].Health
			refs[i] = refInput{gvr: "v1/nodes", name: sums[i].Name, uid: string(sums[i].UID), generation: sums[i].Generation, source: KindNodes}
		}
		r := verdictResult(c.RoleCap, cluster, TaskNodes, verdicts, refs)
		if c.Rows {
			r.Rows = rowSet(verdictRows(sums, refs))
		}
		return r
	}
}

func podsOf(objs []any) []corev1.Pod {
	pods := make([]corev1.Pod, 0, len(objs))
	for _, o := range objs {
		pods = append(pods, *o.(*corev1.Pod))
	}
	return pods
}

func workloadSource(kind string) string {
	switch kind {
	case "StatefulSet":
		return KindStatefulSets
	case "DaemonSet":
		return KindDaemonSets
	default:
		return KindDeployments
	}
}

func workloadGVR(kind string) string {
	switch kind {
	case "StatefulSet":
		return "apps/v1/statefulsets"
	case "DaemonSet":
		return "apps/v1/daemonsets"
	default:
		return "apps/v1/deployments"
	}
}

type refInput struct {
	gvr, ns, name, uid string
	generation         int64
	source             string // the counted kind or snapshot (the ledger partition)
}

// key is the ledger key: the UID, or namespace/name for an object without
// one (a Helm release).
func (r refInput) key() string {
	if r.uid != "" {
		return r.uid
	}
	return r.ns + "/" + r.name
}

// newRoles preallocates a ledger part's role slice (nil without a ledger).
func newRoles(cap, n int) []observe.RoleEntry {
	if cap <= 0 {
		return nil
	}
	return make([]observe.RoleEntry, 0, min(cap, n))
}

// appendRole adds one ledger role entry, bounded by cap (The Line slice 2b
// §1.1): past it, capped is set and nothing more is appended. cap 0 means the
// part keeps no ledger.
func appendRole(roles []observe.RoleEntry, capped *bool, cap int, e observe.RoleEntry) []observe.RoleEntry {
	if cap <= 0 {
		return roles
	}
	if len(roles) >= cap {
		*capped = true
		return roles
	}
	return append(roles, e)
}

// maxTop bounds Top refs per part (spec §5).
const maxTop = 5

// verdictResult partitions verdicts into Counts, picks the worst Top refs
// (verdict order, then rank, then namespace/name) and the earliest time
// boundary any verdict depends on.
func verdictResult(roleCap int, cluster, route string, verdicts []health.Verdict, refs []refInput) observe.PartResult {
	var counts observe.Counts
	var next *time.Time
	roles := newRoles(roleCap, len(verdicts))
	capped := false
	idx := make([]int, 0, len(verdicts))
	for i, v := range verdicts {
		e := observe.RoleEntry{Source: refs[i].source, GVR: refs[i].gvr, Key: refs[i].key(), NS: refs[i].ns, Name: refs[i].name,
			Role: v.Kind.String(), Label: v.Label}
		if v.Kind == health.Fault || v.Kind == health.Degraded {
			e.Reason = health.CapReason(v.Reason, v.Message)
		}
		roles = appendRole(roles, &capped, roleCap, e)
		counts.Total++
		switch v.Kind {
		case health.Fault:
			counts.Fault++
		case health.Degraded:
			counts.Degraded++
		case health.Progressing:
			counts.Progressing++
		case health.Paused:
			counts.Paused++
		case health.OK:
			counts.OK++
		default:
			counts.Unknown++
		}
		if v.ReclassifyAt != nil && (next == nil || v.ReclassifyAt.Before(*next)) {
			t := *v.ReclassifyAt
			next = &t
		}
		if v.Kind.Attention() {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		va, vb := verdicts[idx[a]], verdicts[idx[b]]
		if va.Kind != vb.Kind {
			return va.Kind < vb.Kind
		}
		if va.Rank != vb.Rank {
			return va.Rank > vb.Rank
		}
		ra, rb := refs[idx[a]], refs[idx[b]]
		if ra.ns != rb.ns {
			return ra.ns < rb.ns
		}
		return ra.name < rb.name
	})
	var top []observe.Ref
	for _, i := range idx {
		if len(top) == maxTop {
			break
		}
		r := refs[i]
		top = append(top, observe.Ref{Cluster: cluster, GVR: r.gvr, Namespace: r.ns, Name: r.name, UID: r.uid,
			Role: verdicts[i].Kind.String(), Label: verdicts[i].Label, Generation: r.generation, Route: route})
	}
	return observe.PartResult{Counts: &counts, Top: top, Next: next, Roles: roles, RolesCapped: capped}
}

// NodeReadyTally is the nodes kind's native Ready tally (1c slice 3 §6): the
// connection's node counts, from the same capture as the kind state. "ready"
// counts nodes whose Ready condition is True - NodeReadiness's rule, not the
// verdict's OK (a Ready node under pressure or cordoned is Ready here).
func NodeReadyTally(objs []any) map[string]int {
	ready := 0
	for _, o := range objs {
		n, ok := o.(*corev1.Node)
		if !ok {
			continue
		}
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				ready++
				break
			}
		}
	}
	return map[string]int{TallyNodesReady: ready, TallyNodesTotal: len(objs)}
}

// The nodes kind's tally keys.
const (
	TallyNodesReady = "ready"
	TallyNodesTotal = "total"
)

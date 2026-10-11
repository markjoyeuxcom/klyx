package workloads

import (
	"fmt"
	"sort"
	"time"

	"github.com/moomora/klyx/internal/health"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

// Evidence states how complete the observations behind a health verdict are.
// A workload is never confirmed healthy on incomplete evidence.
type Evidence struct {
	PodsComplete bool // the pod list was read in full
	// ReplicaSetsComplete: the ReplicaSet list was read in full. Without it a
	// Deployment's pods cannot be attributed by ownership.
	ReplicaSetsComplete bool
}

// Assemble joins workloads with their pods for the legacy fields. It has no
// ReplicaSets, so Deployment health is reported unverified; production
// callers use AssembleObserved.
func Assemble(deploys []appsv1.Deployment, stss []appsv1.StatefulSet, dss []appsv1.DaemonSet,
	pods []corev1.Pod, fluxPresent bool, now time.Time) []Workload {
	return AssembleObserved(deploys, nil, stss, dss, pods, Evidence{PodsComplete: true}, fluxPresent, now)
}

// AssembleObserved joins workloads with the pods they own and derives health
// (internal/health). Pods are attributed by controller UID chain only:
// StatefulSet/DaemonSet directly, Deployment via pod -> ReplicaSet ->
// Deployment. fluxPresent gates owner extraction. AgeSeconds and time-based
// verdicts are computed from now. Rows sort by verdict, then severity,
// namespace and name.
func AssembleObserved(deploys []appsv1.Deployment, rss []appsv1.ReplicaSet, stss []appsv1.StatefulSet, dss []appsv1.DaemonSet,
	pods []corev1.Pod, ev Evidence, fluxPresent bool, now time.Time) []Workload {
	return assemble(deploys, rss, stss, dss, pods, ev, fluxPresent, now,
		func(_ types.UID, annotations map[string]string, spec corev1.PodSpec) *ReloadTargets {
			return reloadTargets(annotations, spec)
		})
}

// AssembleProjected is AssembleObserved over projected objects (the health
// Observer's pruned caches, 1c addendum §1.2): their annotations and pod
// template specs are dropped, so each workload's Reloader targets come from
// reload, precomputed from the raw object by ReloadTargetsOf.
func AssembleProjected(deploys []appsv1.Deployment, rss []appsv1.ReplicaSet, stss []appsv1.StatefulSet, dss []appsv1.DaemonSet,
	pods []corev1.Pod, ev Evidence, fluxPresent bool, now time.Time, reload map[types.UID]*ReloadTargets) []Workload {
	return assemble(deploys, rss, stss, dss, pods, ev, fluxPresent, now,
		func(uid types.UID, _ map[string]string, _ corev1.PodSpec) *ReloadTargets { return reload[uid] })
}

// ReloadTargetsOf is the Reloader forward view of one workload's raw object.
func ReloadTargetsOf(annotations map[string]string, spec corev1.PodSpec) *ReloadTargets {
	return reloadTargets(annotations, spec)
}

func assemble(deploys []appsv1.Deployment, rss []appsv1.ReplicaSet, stss []appsv1.StatefulSet, dss []appsv1.DaemonSet,
	pods []corev1.Pod, ev Evidence, fluxPresent bool, now time.Time,
	reloadOf func(types.UID, map[string]string, corev1.PodSpec) *ReloadTargets) []Workload {

	podVerdicts := make([]health.Verdict, len(pods))
	for i := range pods {
		podVerdicts[i] = health.Pod(&pods[i], now)
	}
	classify := func(in health.WorkloadInput, owns func(*metav1.OwnerReference) bool, ns string, complete bool) health.Verdict {
		in.Pods = ownedPods(ns, pods, podVerdicts, owns)
		in.PodsComplete = complete
		return health.Workload(in, now)
	}
	// rsOwner maps a ReplicaSet UID to the UID of the Deployment controlling it.
	rsOwner := make(map[types.UID]types.UID, len(rss))
	for i := range rss {
		if ref := metav1.GetControllerOf(&rss[i]); ref != nil && ref.Kind == "Deployment" {
			rsOwner[rss[i].UID] = ref.UID
		}
	}

	out := make([]Workload, 0, len(deploys)+len(stss)+len(dss))
	for i := range deploys {
		d := &deploys[i]
		w := build("Deployment", d.Namespace, d.Name, d.Labels, reloadOf(d.UID, d.Annotations, d.Spec.Template.Spec), d.Spec.Selector, classifyDeployment(d), pods, fluxPresent, now, d.Generation, d.Status.ObservedGeneration)
		w.Paused = d.Spec.Paused
		w.Health = classify(deploymentInput(d), ownedByDeployment(d.UID, rsOwner), d.Namespace, ev.PodsComplete && ev.ReplicaSetsComplete)
		out = append(out, w)
	}
	for i := range stss {
		s := &stss[i]
		w := build("StatefulSet", s.Namespace, s.Name, s.Labels, reloadOf(s.UID, s.Annotations, s.Spec.Template.Spec), s.Spec.Selector, classifyStatefulSet(s), pods, fluxPresent, now, s.Generation, s.Status.ObservedGeneration)
		w.Health = classify(statefulSetInput(s), ownedByUID("StatefulSet", s.UID), s.Namespace, ev.PodsComplete)
		out = append(out, w)
	}
	for i := range dss {
		ds := &dss[i]
		w := build("DaemonSet", ds.Namespace, ds.Name, ds.Labels, reloadOf(ds.UID, ds.Annotations, ds.Spec.Template.Spec), ds.Spec.Selector, classifyDaemonSet(ds), pods, fluxPresent, now, ds.Generation, ds.Status.ObservedGeneration)
		w.Health = classify(daemonSetInput(ds), ownedByUID("DaemonSet", ds.UID), ds.Namespace, ev.PodsComplete)
		out = append(out, w)
	}

	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Health.Kind != out[b].Health.Kind {
			return out[a].Health.Kind < out[b].Health.Kind
		}
		if out[a].Health.Rank != out[b].Health.Rank {
			return out[a].Health.Rank > out[b].Health.Rank
		}
		if out[a].Namespace != out[b].Namespace {
			return out[a].Namespace < out[b].Namespace
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// ownedPods attributes pods to one workload by controller owner reference,
// never by label selector (selectors can overlap; ownership cannot).
func ownedPods(ns string, pods []corev1.Pod, verdicts []health.Verdict, owns func(*metav1.OwnerReference) bool) []health.OwnedPod {
	var out []health.OwnedPod
	for i := range pods {
		p := &pods[i]
		if p.Namespace != ns {
			continue
		}
		ref := metav1.GetControllerOf(p)
		if ref == nil || !owns(ref) {
			continue
		}
		out = append(out, health.OwnedPod{Namespace: p.Namespace, Name: p.Name, UID: string(p.UID), Verdict: verdicts[i]})
	}
	return out
}

// ownedByUID matches pods a StatefulSet/DaemonSet controls directly.
func ownedByUID(kind string, uid types.UID) func(*metav1.OwnerReference) bool {
	return func(ref *metav1.OwnerReference) bool {
		return ref.Kind == kind && uid != "" && ref.UID == uid
	}
}

// ownedByDeployment matches pods whose controlling ReplicaSet is itself
// controlled by this Deployment (UID chain, never names): an orphaned or
// same-named old ReplicaSet does not attach, an adopted one does.
func ownedByDeployment(uid types.UID, rsOwner map[types.UID]types.UID) func(*metav1.OwnerReference) bool {
	return func(ref *metav1.OwnerReference) bool {
		return ref.Kind == "ReplicaSet" && uid != "" && rsOwner[ref.UID] == uid
	}
}

func deploymentInput(d *appsv1.Deployment) health.WorkloadInput {
	in := health.WorkloadInput{
		Kind: "Deployment", Generation: d.Generation, ObservedGeneration: d.Status.ObservedGeneration,
		Desired: replicas(d.Spec.Replicas), Ready: int(d.Status.ReadyReplicas), Available: int(d.Status.AvailableReplicas),
		Updated: int(d.Status.UpdatedReplicas), Strategy: health.StrategyRolling, Paused: d.Spec.Paused, ProgressDeadline: 600 * time.Second,
	}
	if d.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		in.Strategy = health.StrategyRecreate
	}
	if d.Spec.ProgressDeadlineSeconds != nil {
		in.ProgressDeadline = time.Duration(*d.Spec.ProgressDeadlineSeconds) * time.Second
	}
	for i := range d.Status.Conditions {
		c := &d.Status.Conditions[i]
		hc := &health.Condition{Status: string(c.Status), Reason: c.Reason, Message: c.Message, LastUpdateTime: c.LastUpdateTime.Time}
		switch c.Type {
		case appsv1.DeploymentReplicaFailure:
			in.ReplicaFailure = hc
		case appsv1.DeploymentProgressing:
			in.Progressing = hc
		}
	}
	return in
}

func statefulSetInput(s *appsv1.StatefulSet) health.WorkloadInput {
	in := health.WorkloadInput{
		Kind: "StatefulSet", Generation: s.Generation, ObservedGeneration: s.Status.ObservedGeneration,
		Desired: replicas(s.Spec.Replicas), Ready: int(s.Status.ReadyReplicas), Updated: int(s.Status.UpdatedReplicas),
		Strategy:      health.StrategyRolling,
		RevisionDrift: s.Status.CurrentRevision != "" && s.Status.UpdateRevision != "" && s.Status.CurrentRevision != s.Status.UpdateRevision,
	}
	if s.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
		in.Strategy = health.StrategyOnDelete
	}
	if ru := s.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil {
		in.Partition = int(*ru.Partition)
	}
	if s.Spec.Ordinals != nil {
		in.OrdinalStart = int(s.Spec.Ordinals.Start)
	}
	return in
}

func daemonSetInput(ds *appsv1.DaemonSet) health.WorkloadInput {
	in := health.WorkloadInput{
		Kind: "DaemonSet", Generation: ds.Generation, ObservedGeneration: ds.Status.ObservedGeneration,
		Desired: int(ds.Status.DesiredNumberScheduled), Ready: int(ds.Status.NumberReady), Updated: int(ds.Status.UpdatedNumberScheduled),
		Strategy:      health.StrategyRolling,
		RevisionDrift: ds.Status.UpdatedNumberScheduled < ds.Status.DesiredNumberScheduled,
	}
	if ds.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
		in.Strategy = health.StrategyOnDelete
	}
	return in
}

func build(kind, ns, name string, objLabels map[string]string, reload *ReloadTargets, selector *metav1.LabelSelector,
	c classified, allPods []corev1.Pod, fluxPresent bool, now time.Time,
	generation, observedGeneration int64) Workload {

	w := Workload{Kind: kind, Namespace: ns, Name: name,
		Desired: c.desired, Ready: c.ready, Available: c.available, Updated: c.updated,
		Generation: generation, ObservedGeneration: observedGeneration,
		Reloader: reload}

	matched := matchPods(ns, selector, allPods)
	w.Pods = make([]Pod, 0, len(matched))
	for _, p := range matched {
		pr, _ := worstPodReason([]corev1.Pod{*p})
		w.Restarts += podRestarts(p)
		w.Pods = append(w.Pods, Pod{
			Name: p.Name, Ready: podReady(p), Restarts: podRestarts(p), Reason: pr,
			Node: p.Spec.NodeName, AgeSeconds: ageSeconds(p.CreationTimestamp.Time, now),
		})
	}

	// Rank is pod-backed by design: it derives from the worst pod severity, NOT
	// from c.condReason. A lagging controller condition (e.g. Available=False while
	// ready==desired) is shown in the status text but does not redden the rank dot.
	// This is intentional - any real pod hard-failure still forces Unhealthy via sev,
	// so a broken workload can never render healthy. Do not wire condReason into rankOf.
	reason, sev := worstPodReason(deref(matched))
	recent := recentlyTerminated(matched, now)
	// Suppress a stale historical reason (an old OOMKill/Error left in lastState)
	// from the row's status text, so the dot and the text agree. Hard (current
	// failure) and benign (current transient) reasons are always shown.
	shownReason := reason
	if sev == sevHistorical && !recent {
		shownReason = ""
	}
	w.Rank = rankOf(c.desired, c.ready, recent, sev)
	w.Reason = displayReason(kind, c, shownReason)

	if fluxPresent {
		w.GitOps = extractOwner(objLabels)
	}
	w.Resources = aggregateResources(matched)
	return w
}

// matchPods returns pods in ns matching selector. A nil or EMPTY selector matches
// ZERO pods (never the whole namespace).
func matchPods(ns string, selector *metav1.LabelSelector, allPods []corev1.Pod) []*corev1.Pod {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return nil
	}
	s, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil
	}
	// Pods are matched by label selector, same namespace. NOTE: if two workloads
	// have overlapping selectors a pod can match both — this mirrors Kubernetes
	// label semantics (no owner-reference arbitration in this slice).
	var out []*corev1.Pod
	for i := range allPods {
		p := &allPods[i]
		if p.Namespace == ns && s.Matches(labels.Set(p.Labels)) {
			out = append(out, p)
		}
	}
	return out
}

func deref(ps []*corev1.Pod) []corev1.Pod {
	out := make([]corev1.Pod, len(ps))
	for i, p := range ps {
		out[i] = *p
	}
	return out
}

// podReady reports the pod's PodReady condition for the per-pod expand. The
// workload's ready/desired count comes from the workload status (Classify), which
// is authoritative; a pod that hasn't yet reported PodReady shows not-ready here
// but does not affect the row's rank.
func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func podRestarts(p *corev1.Pod) int {
	n := 0
	for _, cs := range p.Status.InitContainerStatuses {
		n += int(cs.RestartCount)
	}
	for _, cs := range p.Status.ContainerStatuses {
		n += int(cs.RestartCount)
	}
	return n
}

func ageSeconds(created, now time.Time) int {
	if created.IsZero() {
		return 0
	}
	d := int(now.Sub(created).Seconds())
	if d < 0 {
		return 0
	}
	return d
}

// recentTerminationWindow bounds how long a container termination keeps a
// workload in the info "Restarts" tier. Beyond it, an old restart is treated as
// settled (the restart COUNT stays visible regardless - only the rank dot quiets).
const recentTerminationWindow = time.Hour

// recentlyTerminated reports whether any container (init or main) in the matched
// pods terminated within recentTerminationWindow of now. It reads both the current
// terminated state and the last-termination state (the marker a restart leaves
// behind), across init and main containers.
func recentlyTerminated(pods []*corev1.Pod, now time.Time) bool {
	within := func(t metav1.Time) bool {
		if t.IsZero() {
			return false
		}
		return now.Sub(t.Time) <= recentTerminationWindow
	}
	scan := func(css []corev1.ContainerStatus) bool {
		for _, cs := range css {
			if cs.LastTerminationState.Terminated != nil && within(cs.LastTerminationState.Terminated.FinishedAt) {
				return true
			}
			// The CURRENT terminated state counts only when it was a FAILURE. A
			// normally-completed init container (Reason "Completed", ExitCode 0)
			// stays in State.Terminated for the whole life of a Running pod, so
			// counting it would mis-rank every freshly-rolled healthy pod as
			// "Restarts" for an hour. A genuine restart is captured above.
			if t := cs.State.Terminated; t != nil && t.ExitCode != 0 && within(t.FinishedAt) {
				return true
			}
		}
		return false
	}
	for _, p := range pods {
		if scan(p.Status.InitContainerStatuses) || scan(p.Status.ContainerStatuses) {
			return true
		}
	}
	return false
}

// rankOf honors a hard pod failure regardless of ready count. The "Restarts"
// (info) tier is recency-gated: only a *recent* container termination elevates an
// otherwise-healthy workload, so a pod that restarted weeks ago does not stay lit
// forever. The restart COUNT remains visible in its own column regardless.
func rankOf(desired, ready int, recent bool, sev severity) HealthRank {
	if desired == 0 {
		return Healthy
	}
	if sev == sevHard || ready == 0 {
		return Unhealthy
	}
	if ready < desired {
		return Degraded
	}
	// ready == desired
	if recent {
		return Restarts
	}
	return Healthy
}

func displayReason(kind string, c classified, podReason string) string {
	if c.desired == 0 {
		return "Scaled to 0"
	}
	if podReason != "" {
		return podReason
	}
	if c.condReason != "" {
		return c.condReason
	}
	if c.ready < c.desired {
		// Readable fallback when conditions are sparse.
		return fmt.Sprintf("Progressing · %d unavailable", c.desired-c.ready)
	}
	// Speak each kind's vocabulary: Deployments have an "Available" condition;
	// StatefulSets/DaemonSets are "Ready".
	if kind == "Deployment" {
		return "Available"
	}
	return "Ready"
}

func extractOwner(l map[string]string) *Owner {
	// Kustomization is checked first deliberately: it is the GitOps root (a
	// HelmRelease may itself be owned by a Kustomization). GitOps is primary.
	if n := l["kustomize.toolkit.fluxcd.io/name"]; n != "" {
		return &Owner{Kind: "Kustomization", Namespace: l["kustomize.toolkit.fluxcd.io/namespace"], Name: n}
	}
	if n := l["helm.toolkit.fluxcd.io/name"]; n != "" {
		return &Owner{Kind: "HelmRelease", Namespace: l["helm.toolkit.fluxcd.io/namespace"], Name: n}
	}
	return nil
}

// GitOpsOwnerFromLabels is the exported owner extraction for surfaces beyond the
// workloads list (the fleet compare). Flux owners first (same precedence as
// extractOwner), then the Argo instance label - an Argo Owner carries no
// namespace (the label does not include the Application's namespace).
func GitOpsOwnerFromLabels(l map[string]string) *Owner {
	if o := extractOwner(l); o != nil {
		return o
	}
	if n := l["argocd.argoproj.io/instance"]; n != "" {
		return &Owner{Kind: "Application", Name: n}
	}
	return nil
}

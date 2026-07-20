package workloads

import (
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// Assemble joins workloads with their pods and derives health, sorted
// triage-first (Rank, then namespace/name). fluxPresent gates owner extraction.
// AgeSeconds is computed from now.
func Assemble(deploys []appsv1.Deployment, stss []appsv1.StatefulSet, dss []appsv1.DaemonSet,
	pods []corev1.Pod, fluxPresent bool, now time.Time) []Workload {

	out := make([]Workload, 0, len(deploys)+len(stss)+len(dss))
	for i := range deploys {
		d := &deploys[i]
		out = append(out, build("Deployment", d.Namespace, d.Name, d.Labels, d.Annotations, d.Spec.Selector, d.Spec.Template.Spec, classifyDeployment(d), pods, fluxPresent, now, d.Generation, d.Status.ObservedGeneration))
	}
	for i := range stss {
		s := &stss[i]
		out = append(out, build("StatefulSet", s.Namespace, s.Name, s.Labels, s.Annotations, s.Spec.Selector, s.Spec.Template.Spec, classifyStatefulSet(s), pods, fluxPresent, now, s.Generation, s.Status.ObservedGeneration))
	}
	for i := range dss {
		ds := &dss[i]
		out = append(out, build("DaemonSet", ds.Namespace, ds.Name, ds.Labels, ds.Annotations, ds.Spec.Selector, ds.Spec.Template.Spec, classifyDaemonSet(ds), pods, fluxPresent, now, ds.Generation, ds.Status.ObservedGeneration))
	}

	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Rank != out[b].Rank {
			return out[a].Rank < out[b].Rank
		}
		if out[a].Namespace != out[b].Namespace {
			return out[a].Namespace < out[b].Namespace
		}
		return out[a].Name < out[b].Name
	})
	return out
}

func build(kind, ns, name string, objLabels, annotations map[string]string, selector *metav1.LabelSelector,
	podSpec corev1.PodSpec, c classified, allPods []corev1.Pod, fluxPresent bool, now time.Time,
	generation, observedGeneration int64) Workload {

	w := Workload{Kind: kind, Namespace: ns, Name: name,
		Desired: c.desired, Ready: c.ready, Available: c.available, Updated: c.updated,
		Generation: generation, ObservedGeneration: observedGeneration,
		Reloader: reloadTargets(annotations, podSpec)}

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

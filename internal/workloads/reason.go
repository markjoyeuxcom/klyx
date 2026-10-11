package workloads

import corev1 "k8s.io/api/core/v1"

type severity int

const (
	sevNone       severity = iota
	sevBenign              // ContainerCreating, PodInitializing
	sevHistorical          // OOMKilled in lastState while currently running (explains restarts)
	sevHard                // currently-broken: CrashLoop, ImagePull, CreateContainer, Unschedulable, Failed
)

// hardRank orders hard reasons for display when several are present (higher wins).
var hardRank = map[string]int{
	"CrashLoopBackOff":           70,
	"OOMKilled":                  60, // active terminated OOM (not lastState-while-running)
	"ImagePullBackOff":           50,
	"ErrImagePull":               50,
	"CreateContainerConfigError": 40,
	"CreateContainerError":       40,
	"InvalidImageName":           40,
	"Unschedulable":              30,
	"Failed":                     20,
}

// worstPodReason returns the single worst reason across a workload's pods and its
// severity. Reads container waiting/terminated state AND pod phase/conditions.
func worstPodReason(pods []corev1.Pod) (string, severity) {
	ps := make([]*corev1.Pod, len(pods))
	for i := range pods {
		ps[i] = &pods[i]
	}
	return worstPodReasonOf(ps)
}

// worstPodReasonOf is worstPodReason over pointers: the assembly never copies
// a pod to classify it.
func worstPodReasonOf(pods []*corev1.Pod) (string, severity) {
	bestReason := ""
	bestSev := sevNone
	bestHard := -1
	benign := ""
	historical := ""

	consider := func(reason string, sev severity) {
		switch sev {
		case sevHard:
			// Unknown waiting reasons are still treated as hard (a health lens must
			// not silently hide an unrecognized failure) but rank below named ones.
			r := hardRank[reason]
			if r == 0 {
				r = 10
			}
			if r > bestHard {
				bestHard, bestReason, bestSev = r, reason, sevHard
			}
		case sevHistorical:
			if bestSev < sevHistorical && historical == "" {
				historical = reason
			}
		case sevBenign:
			if bestSev < sevBenign && benign == "" {
				benign = reason
			}
		}
	}

	for _, p := range pods {
		// Pod-level: unschedulable / failed.
		if p.Status.Phase == corev1.PodFailed {
			consider("Failed", sevHard)
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Reason == "Unschedulable" {
				consider("Unschedulable", sevHard)
			}
		}
		// Container-level (init AND main containers — a failing init container is a
		// real failure that must not hide behind a benign "PodInitializing").
		scanContainer := func(cs corev1.ContainerStatus) {
			if w := cs.State.Waiting; w != nil && w.Reason != "" {
				switch w.Reason {
				case "ContainerCreating", "PodInitializing":
					consider(w.Reason, sevBenign)
				default:
					consider(w.Reason, sevHard) // CrashLoopBackOff, ImagePullBackOff, etc.
				}
			}
			if t := cs.State.Terminated; t != nil && t.Reason == "OOMKilled" {
				consider("OOMKilled", sevHard) // currently terminated by OOM
			}
			// OOMKilled in lastState while currently running -> historical signal.
			if cs.State.Running != nil && cs.LastTerminationState.Terminated != nil &&
				cs.LastTerminationState.Terminated.Reason == "OOMKilled" {
				consider("OOMKilled", sevHistorical)
			}
		}
		for j := range p.Status.InitContainerStatuses {
			scanContainer(p.Status.InitContainerStatuses[j])
		}
		for j := range p.Status.ContainerStatuses {
			scanContainer(p.Status.ContainerStatuses[j])
		}
	}

	if bestSev == sevHard {
		return bestReason, sevHard
	}
	if historical != "" {
		return historical, sevHistorical
	}
	if benign != "" {
		return benign, sevBenign
	}
	return "", sevNone
}

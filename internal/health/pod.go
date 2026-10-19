package health

import (
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// failureRank orders current-failure evidence when a pod has several (higher
// wins). Every waiting failure outranks an active termination except OOM:
// a waiting reason is what the kubelet is doing NOW, while a terminated exit
// is the moment before it; OOMKilled is kept above image problems because it
// names the cause rather than the symptom.
var failureRank = map[string]int{
	"CrashLoopBackOff":           70,
	"OOMKilled":                  60,
	"ImagePullBackOff":           50,
	"ErrImagePull":               50,
	"CreateContainerConfigError": 40,
	"CreateContainerError":       40,
	"InvalidImageName":           40,
	"Exited":                     35, // current non-zero exit without a specific reason
	"Unschedulable":              30,
	"Failed":                     20,
}

// unnamedWaitingRank ranks a waiting reason the table does not know. Still a
// failure (a health lens must not hide an unrecognised one), ranked low.
const unnamedWaitingRank = 10

// benignWaiting are waiting reasons that are normal start-up, not failure.
var benignWaiting = map[string]bool{"ContainerCreating": true, "PodInitializing": true}

type evidence struct {
	rank      int
	label     string
	reason    string
	message   string
	container string
	init      bool
}

// Pod classifies one pod (spec rows P1-P13, first match wins).
func Pod(p *corev1.Pod, now time.Time) Verdict {
	v := podVerdict(p, now)
	if v.Native == "" {
		v.Native = podNative(p)
	}
	if v.Kind != Fault {
		v.Note = podNote(p, now)
	}
	return resizeVerdict(p, v)
}

func podVerdict(p *corev1.Pod, now time.Time) Verdict {
	phase := p.Status.Phase

	// P1/P2: terminating. deletionTimestamp is the deletion deadline and
	// already includes the grace period - never add it twice.
	if dt := p.DeletionTimestamp; dt != nil {
		in, deadline := within(dt.Time, StuckTerminating, now)
		if in {
			return Verdict{Kind: Progressing, Label: "Terminating", Reason: "Terminating", Rule: "pod.terminating", ReclassifyAt: at(deadline)}
		}
		return Verdict{Kind: Degraded, Label: "Stuck terminating", Reason: "StuckTerminating", Rule: "pod.stuck-terminating",
			Message: fmt.Sprintf("deletion deadline passed %s ago; a finalizer or an unreachable node may be holding it", short(now.Sub(dt.Time)))}
	}

	switch phase {
	case corev1.PodSucceeded: // P3
		return Verdict{Kind: OK, Label: "Completed", Reason: "Completed", Rule: "pod.completed"}
	case corev1.PodFailed: // P4
		label := p.Status.Reason
		msg := p.Status.Message
		if label == "" {
			if e, ok := worstEvidence(p, true); ok {
				label, msg = e.label, e.message
			}
		}
		if label == "" {
			label = "Failed"
		}
		return Verdict{Kind: Fault, Label: label, Reason: "Failed", Message: msg, Rule: "pod.failed", Rank: failureRank["Failed"]}
	}

	// P5-P7 folded into one ranked current-failure rule.
	if e, ok := worstEvidence(p, false); ok {
		return Verdict{Kind: Fault, Label: e.label, Reason: e.reason, Message: e.message, Rule: "pod.current-failure", Rank: e.rank}
	}

	switch phase {
	case corev1.PodPending: // P9/P10
		return pendingVerdict(p, now)
	case corev1.PodRunning:
		return runningVerdict(p, now)
	}
	// P8 and any unsupported phase.
	return Verdict{Kind: Unknown, Label: "Unknown", Reason: "PhaseUnknown", Rule: "pod.phase-unknown"}
}

// worstEvidence collects every current failure and returns the worst, with
// stable tie-breaks (rank, init before main, container name). terminalOnly
// restricts to terminated evidence (used to label an already-Failed pod).
func worstEvidence(p *corev1.Pod, terminalOnly bool) (evidence, bool) {
	var all []evidence
	if !terminalOnly {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == "Unschedulable" {
				all = append(all, evidence{rank: failureRank["Unschedulable"], label: "Unschedulable", reason: "Unschedulable", message: c.Message})
			}
		}
	}
	scan := func(statuses []corev1.ContainerStatus, specs []corev1.Container, init bool) {
		for _, cs := range statuses {
			if w := cs.State.Waiting; w != nil && w.Reason != "" && !benignWaiting[w.Reason] && !terminalOnly {
				r, ok := failureRank[w.Reason]
				if !ok {
					r = unnamedWaitingRank
				}
				all = append(all, evidence{rank: r, label: w.Reason, reason: w.Reason, message: prefix(cs.Name, w.Message), container: cs.Name, init: init})
			}
			t := cs.State.Terminated
			if t == nil || t.ExitCode == 0 {
				continue
			}
			if !terminalOnly && !willRestart(p, specs, cs.Name, init) {
				continue
			}
			label, reason := t.Reason, t.Reason
			if label == "" || label == "Error" {
				label, reason = fmt.Sprintf("Exited %d", t.ExitCode), "Exited"
			}
			r, ok := failureRank[reason]
			if !ok {
				r = failureRank["Exited"]
			}
			all = append(all, evidence{rank: r, label: label, reason: reason, message: exitMessage(cs, t), container: cs.Name, init: init})
		}
	}
	scan(p.Status.InitContainerStatuses, p.Spec.InitContainers, true)
	scan(p.Status.ContainerStatuses, p.Spec.Containers, false)
	if len(all) == 0 {
		return evidence{}, false
	}
	sort.SliceStable(all, func(a, b int) bool {
		if all[a].rank != all[b].rank {
			return all[a].rank > all[b].rank
		}
		if all[a].init != all[b].init {
			return all[a].init
		}
		return all[a].container < all[b].container
	})
	return all[0], true
}

// willRestart reports whether a container that exited non-zero will be run
// again: a restartable init sidecar always, otherwise per the pod's policy.
func willRestart(p *corev1.Pod, specs []corev1.Container, name string, init bool) bool {
	if init {
		for _, c := range specs {
			if c.Name == name && c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
				return true
			}
		}
	}
	return p.Spec.RestartPolicy != corev1.RestartPolicyNever
}

func pendingVerdict(p *corev1.Pod, now time.Time) Verdict {
	label := "Pending"
	if r := benignReason(p); r != "" {
		label = r
	}
	created := p.CreationTimestamp.Time
	if created.IsZero() {
		return Verdict{Kind: Unknown, Label: label, Reason: "NoTimestamp", Rule: "pod.pending-untimed"}
	}
	in, deadline := within(created, PodStartGrace, now)
	if in {
		return Verdict{Kind: Progressing, Label: label, Reason: "Starting", Rule: "pod.pending", ReclassifyAt: at(deadline)}
	}
	return Verdict{Kind: Degraded, Label: label, Reason: "PendingTooLong", Rule: "pod.pending-overdue",
		Message: fmt.Sprintf("pending for %s (start allowance %s)", short(now.Sub(created)), short(PodStartGrace))}
}

func runningVerdict(p *corev1.Pod, now time.Time) Verdict {
	var ready *corev1.PodCondition
	for i := range p.Status.Conditions {
		if p.Status.Conditions[i].Type == corev1.PodReady {
			ready = &p.Status.Conditions[i]
		}
	}
	switch {
	case ready == nil: // readiness not reported yet
		anchor := startAnchor(p)
		if anchor.IsZero() {
			return Verdict{Kind: Unknown, Label: "Readiness not reported", Reason: "ReadyMissing", Rule: "pod.ready-missing"}
		}
		in, deadline := within(anchor, PodStartGrace, now)
		if in {
			return Verdict{Kind: Progressing, Label: "Starting", Reason: "ReadyMissing", Rule: "pod.ready-missing-starting", ReclassifyAt: at(deadline)}
		}
		return Verdict{Kind: Unknown, Label: "Readiness not reported", Reason: "ReadyMissing", Rule: "pod.ready-missing"}
	case ready.Status == corev1.ConditionTrue: // P13
		return Verdict{Kind: OK, Label: "Running", Reason: "Ready", Rule: "pod.ready"}
	case ready.Status == corev1.ConditionFalse: // P11/P12
		anchor := ready.LastTransitionTime.Time
		if anchor.IsZero() {
			anchor = startAnchor(p)
		}
		if anchor.IsZero() {
			return Verdict{Kind: Degraded, Label: "Not ready", Reason: "NotReady", Message: ready.Message, Rule: "pod.not-ready-untimed"}
		}
		in, deadline := within(anchor, ReadyGrace, now)
		if in {
			return Verdict{Kind: Progressing, Label: "Not ready", Reason: "NotReady", Message: ready.Message, Rule: "pod.not-ready", ReclassifyAt: at(deadline)}
		}
		return Verdict{Kind: Degraded, Label: "Not ready", Reason: "NotReady", Rule: "pod.not-ready-overdue",
			Message: joinNonEmpty(fmt.Sprintf("not ready for %s", short(now.Sub(anchor))), ready.Message)}
	default: // Ready=Unknown
		return Verdict{Kind: Unknown, Label: "Readiness unknown", Reason: "ReadyUnknown", Message: ready.Message, Rule: "pod.ready-unknown"}
	}
}

// startAnchor is status.startTime, falling back to creation.
func startAnchor(p *corev1.Pod) time.Time {
	if p.Status.StartTime != nil && !p.Status.StartTime.IsZero() {
		return p.Status.StartTime.Time
	}
	return p.CreationTimestamp.Time
}

func benignReason(p *corev1.Pod) string {
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range list {
			if w := cs.State.Waiting; w != nil && benignWaiting[w.Reason] {
				return w.Reason
			}
		}
	}
	return ""
}

// podNote is history: the most recent container termination within
// RecentTermination, only when its timestamp is known.
func podNote(p *corev1.Pod, now time.Time) string {
	var latest time.Time
	reason := ""
	for _, list := range [][]corev1.ContainerStatus{p.Status.InitContainerStatuses, p.Status.ContainerStatuses} {
		for _, cs := range list {
			if t := cs.LastTerminationState.Terminated; t != nil && !t.FinishedAt.IsZero() && t.FinishedAt.After(latest) {
				latest, reason = t.FinishedAt.Time, t.Reason
			}
		}
	}
	if latest.IsZero() || now.Sub(latest) >= RecentTermination {
		return ""
	}
	if reason == "OOMKilled" {
		return fmt.Sprintf("last OOMKilled %s ago", short(now.Sub(latest)))
	}
	return fmt.Sprintf("restarted %s ago", short(now.Sub(latest)))
}

func podNative(p *corev1.Pod) string {
	parts := []string{"phase " + orDash(string(p.Status.Phase))}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			parts = append(parts, "Ready="+string(c.Status))
		}
	}
	return strings.Join(parts, " · ")
}

// exitMessage says what a non-zero exit was, with the restart count as the
// evidence of a loop (the label stays the kubelet's own word).
func exitMessage(cs corev1.ContainerStatus, t *corev1.ContainerStateTerminated) string {
	m := fmt.Sprintf("%s exited %d", cs.Name, t.ExitCode)
	switch {
	case cs.RestartCount == 1:
		m += " (restarted once)"
	case cs.RestartCount > 1:
		m += fmt.Sprintf(" (restarted %d times)", cs.RestartCount)
	}
	return joinNonEmpty(m, t.Message)
}

func prefix(container, msg string) string {
	if msg == "" {
		return container
	}
	return container + ": " + msg
}

func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " - " + b
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// short renders a duration the way the UI does ("45s", "4m", "3h", "2d").
func short(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

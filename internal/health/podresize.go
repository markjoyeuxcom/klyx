package health

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// Coverage S6 §3: in-place resize verdicts. They apply only to a running
// pod (phase Running, not deleting) whose verdict is otherwise OK: a
// CrashLoop stays a CrashLoop, Pending and Not ready keep their rule, and
// a terminal pod keeps its own. A resize condition is active only with
// status True. The two conditions' generations mean different things
// (the resize task page; kubelet status_manager.go): PodResizePending's is
// the allocation last attempted, rewritten on every attempt;
// PodResizeInProgress's is where the ongoing actuation started, kept until
// it clears. No clock: both rules are status-driven.

// activeResize returns the first active condition of a type.
func activeResize(p *corev1.Pod, t corev1.PodConditionType) *corev1.PodCondition {
	for i := range p.Status.Conditions {
		c := &p.Status.Conditions[i]
		if c.Type == t && c.Status == corev1.ConditionTrue {
			return c
		}
	}
	return nil
}

func generationText(g int64) string {
	if g == 0 {
		return "generation not reported"
	}
	return fmt.Sprintf("generation %d", g)
}

// resizeVerdict applies the resize rules over v.
func resizeVerdict(p *corev1.Pod, v Verdict) Verdict {
	if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil {
		return v
	}
	pending := activeResize(p, corev1.PodResizePending)
	inProgress := activeResize(p, corev1.PodResizeInProgress)
	if pending == nil && inProgress == nil {
		return v
	}
	// infeasibleNow: the Infeasible evidence is about the current request:
	// its attempt was this generation, or neither generation is reported (a
	// kubelet that does not track them, said so).
	infeasibleNow, unreported := false, false
	if pending != nil && pending.Reason == corev1.PodReasonInfeasible {
		switch {
		case p.Generation > 0 && pending.ObservedGeneration == p.Generation:
			infeasibleNow = true
		case pending.ObservedGeneration == 0 && p.Status.ObservedGeneration == 0:
			infeasibleNow, unreported = true, true
		}
	}
	var notes []string
	add := func(s string) { notes = append(notes, s) }
	if v.Kind == OK {
		switch {
		case inProgress != nil && inProgress.Reason == corev1.PodReasonError:
			v = Verdict{Kind: Degraded, Label: "Resize error", Reason: "ResizeError", Rule: "pod.resize-error", Native: v.Native, Note: v.Note,
				Message: joinNonEmpty("resize started at "+generationText(inProgress.ObservedGeneration)+" failed", inProgress.Message)}
			inProgress = nil // said
		case infeasibleNow:
			msg := pending.Message
			if unreported {
				msg = joinNonEmpty(msg, "for the latest request the kubelet reported; it does not report which generation")
			}
			v = Verdict{Kind: Degraded, Label: "Resize infeasible", Reason: "ResizeInfeasible", Rule: "pod.resize-infeasible", Native: v.Native, Note: v.Note, Message: msg}
			pending = nil // said
		}
	}
	if v.Kind == Fault {
		return v
	}
	if inProgress != nil {
		if inProgress.Reason == corev1.PodReasonError {
			add("resize error (started at " + generationText(inProgress.ObservedGeneration) + "): " + inProgress.Message)
		} else {
			add("resizing (started at " + generationText(inProgress.ObservedGeneration) + ")")
		}
	}
	if pending != nil {
		switch {
		case pending.Reason == corev1.PodReasonInfeasible && !infeasibleNow:
			add(fmt.Sprintf("an earlier resize (%s) was infeasible; generation %d is not yet evaluated", generationText(pending.ObservedGeneration), p.Generation))
		case pending.Reason == corev1.PodReasonInfeasible:
			add("resize infeasible: " + pending.Message)
		default:
			add("resize deferred: " + pending.Message)
		}
	}
	for _, n := range notes {
		if v.Note == "" {
			v.Note = n
		} else {
			v.Note += "; " + n
		}
	}
	return v
}

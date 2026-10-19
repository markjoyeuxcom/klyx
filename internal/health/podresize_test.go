package health

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func gens(meta, observed int64) podOpt {
	return func(p *corev1.Pod) { p.Generation, p.Status.ObservedGeneration = meta, observed }
}

func resizeCond(t corev1.PodConditionType, status corev1.ConditionStatus, reason string, gen int64) podOpt {
	return func(p *corev1.Pod) {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: t, Status: status, Reason: reason, Message: "msg-" + reason, ObservedGeneration: gen})
	}
}

// Coverage S6 §3: the resize rules, by generation and activity.
func TestPodResizeVerdicts(t *testing.T) {
	ready := readyCond(corev1.ConditionTrue, t0)
	now := t0.Add(time.Hour)
	cases := []struct {
		name      string
		opts      []podOpt
		kind      Kind
		rule      string
		noteHas   string
		messageIn string
	}{
		{"infeasible for the current generation", []podOpt{ready, gens(3, 3), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonInfeasible, 3)},
			Degraded, "pod.resize-infeasible", "", "msg-Infeasible"},
		{"an older infeasible attempt beside a newer desired generation is a Note", []podOpt{ready, gens(8, 7), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonInfeasible, 7)},
			OK, "pod.ready", "generation 8 is not yet evaluated", ""},
		{"both generations 0: infeasible, qualified", []podOpt{ready, gens(0, 0), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonInfeasible, 0)},
			Degraded, "pod.resize-infeasible", "", "does not report which generation"},
		{"condition generation 0 beside a reported pod generation: not proven current", []podOpt{ready, gens(4, 4), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonInfeasible, 0)},
			OK, "pod.ready", "not yet evaluated", ""},
		{"an in-progress error is current whatever its generation", []podOpt{ready, gens(9, 9), resizeCond(corev1.PodResizeInProgress, corev1.ConditionTrue, corev1.PodReasonError, 5)},
			Degraded, "pod.resize-error", "", "started at generation 5"},
		{"an older active error beside a newer pending: degraded for the error, the pending a Note", []podOpt{ready, gens(9, 9),
			resizeCond(corev1.PodResizeInProgress, corev1.ConditionTrue, corev1.PodReasonError, 5), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonDeferred, 9)},
			Degraded, "pod.resize-error", "resize deferred", ""},
		{"deferred is a Note", []podOpt{ready, gens(2, 2), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonDeferred, 2)},
			OK, "pod.ready", "resize deferred: msg-Deferred", ""},
		{"in progress without error is a Note", []podOpt{ready, gens(2, 2), resizeCond(corev1.PodResizeInProgress, corev1.ConditionTrue, "", 2)},
			OK, "pod.ready", "resizing (started at generation 2)", ""},
		{"a non-True condition is not active", []podOpt{ready, gens(3, 3), resizeCond(corev1.PodResizePending, corev1.ConditionFalse, corev1.PodReasonInfeasible, 3)},
			OK, "pod.ready", "", ""},
		{"a fault stays a fault", []podOpt{ready, gens(3, 3), container(waiting("app", "CrashLoopBackOff")), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonInfeasible, 3)},
			Fault, "pod.current-failure", "", ""},
		{"not ready keeps its rule, the resize a Note", []podOpt{readyCond(corev1.ConditionFalse, t0), gens(3, 3), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonInfeasible, 3)},
			Degraded, "pod.not-ready-overdue", "resize infeasible", ""},
		{"a succeeded pod keeps Completed", []podOpt{phase(corev1.PodSucceeded), gens(3, 3), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonInfeasible, 3)},
			OK, "pod.completed", "", ""},
		{"a failed pod keeps its fault", []podOpt{phase(corev1.PodFailed), gens(3, 3), resizeCond(corev1.PodResizeInProgress, corev1.ConditionTrue, corev1.PodReasonError, 3)},
			Fault, "pod.failed", "", ""},
		{"a terminating pod keeps its rule", []podOpt{ready, gens(3, 3), resizeCond(corev1.PodResizePending, corev1.ConditionTrue, corev1.PodReasonInfeasible, 3),
			func(p *corev1.Pod) { dt := metav1.NewTime(now); p.DeletionTimestamp = &dt }},
			Progressing, "pod.terminating", "", ""},
	}
	for _, c := range cases {
		v := Pod(newPod(c.opts...), now)
		if v.Kind != c.kind || v.Rule != c.rule || !strings.Contains(v.Note, c.noteHas) || !strings.Contains(v.Message, c.messageIn) {
			t.Errorf("%s: %+v", c.name, v)
		}
	}
}

// A debug container's failure is evidence, never the application's.
func TestPodEphemeralFailureIsNotTheApps(t *testing.T) {
	p := newPod(readyCond(corev1.ConditionTrue, t0))
	p.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{terminated("debugger-x", "Error", 3)}
	if v := Pod(p, t0.Add(time.Hour)); v.Kind != OK {
		t.Fatalf("an exited debug container changed the verdict: %+v", v)
	}
}

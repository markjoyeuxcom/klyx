package health

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

type podOpt func(*corev1.Pod)

func newPod(opts ...podOpt) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "ns", CreationTimestamp: metav1.NewTime(t0)},
		Spec:       corev1.PodSpec{RestartPolicy: corev1.RestartPolicyAlways, Containers: []corev1.Container{{Name: "app"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: t0}},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func phase(ph corev1.PodPhase) podOpt { return func(p *corev1.Pod) { p.Status.Phase = ph } }

func readyCond(s corev1.ConditionStatus, since time.Time) podOpt {
	return func(p *corev1.Pod) {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: corev1.PodReady, Status: s, LastTransitionTime: metav1.NewTime(since)})
	}
}

func container(cs corev1.ContainerStatus) podOpt {
	return func(p *corev1.Pod) { p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, cs) }
}

func waiting(name, reason string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}
}

func terminated(name, reason string, code int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code}}}
}

func TestPodTable(t *testing.T) {
	sidecar := corev1.ContainerRestartPolicyAlways
	cases := []struct {
		name      string
		pod       *corev1.Pod
		now       time.Time
		kind      Kind
		label     string
		rule      string
		reclassAt *time.Time
	}{
		{"active deletion within allowance", newPod(func(p *corev1.Pod) { p.DeletionTimestamp = &metav1.Time{Time: t0.Add(30 * time.Second)} }),
			t0.Add(time.Minute), Progressing, "Terminating", "pod.terminating", at(t0.Add(30*time.Second + StuckTerminating))},
		{"grace already in deletion deadline: not added twice", newPod(func(p *corev1.Pod) {
			p.DeletionTimestamp = &metav1.Time{Time: t0.Add(30 * time.Second)}
			g := int64(600)
			p.Spec.TerminationGracePeriodSeconds = &g
		}), t0.Add(30*time.Second + StuckTerminating), Degraded, "Stuck terminating", "pod.stuck-terminating", nil},
		{"terminating beats a crash", newPod(func(p *corev1.Pod) { p.DeletionTimestamp = &metav1.Time{Time: t0} }, container(waiting("app", "CrashLoopBackOff"))),
			t0, Progressing, "Terminating", "pod.terminating", at(t0.Add(StuckTerminating))},
		{"completed Job pod", newPod(phase(corev1.PodSucceeded)), t0, OK, "Completed", "pod.completed", nil},
		{"Never-policy failed pod", newPod(phase(corev1.PodFailed), func(p *corev1.Pod) { p.Spec.RestartPolicy = corev1.RestartPolicyNever }, container(terminated("app", "Error", 2))),
			t0, Fault, "Exited 2", "pod.failed", nil},
		{"failed pod uses status.reason", newPod(phase(corev1.PodFailed), func(p *corev1.Pod) { p.Status.Reason = "Evicted" }), t0, Fault, "Evicted", "pod.failed", nil},
		{"image pull", newPod(phase(corev1.PodPending), container(waiting("app", "ErrImagePull"))), t0, Fault, "ErrImagePull", "pod.current-failure", nil},
		{"unschedulable", newPod(phase(corev1.PodPending), func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/1 nodes"}}
		}), t0, Fault, "Unschedulable", "pod.current-failure", nil},
		{"current Error exit (crash loop window) is never Running", newPod(readyCond(corev1.ConditionFalse, t0), container(terminated("app", "Error", 1))),
			t0, Fault, "Exited 1", "pod.current-failure", nil},
		{"CrashLoopBackOff named only when waiting says so", newPod(readyCond(corev1.ConditionFalse, t0), container(waiting("app", "CrashLoopBackOff"))),
			t0, Fault, "CrashLoopBackOff", "pod.current-failure", nil},
		{"current OOMKilled", newPod(container(terminated("app", "OOMKilled", 137))), t0, Fault, "OOMKilled", "pod.current-failure", nil},
		{"Ready=True with a current termination is still a fault", newPod(readyCond(corev1.ConditionTrue, t0), container(terminated("app", "Error", 1))),
			t0, Fault, "Exited 1", "pod.current-failure", nil},
		{"init failure", newPod(phase(corev1.PodPending), func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "migrate"}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{terminated("migrate", "Error", 3)}
		}), t0, Fault, "Exited 3", "pod.current-failure", nil},
		{"restartable sidecar failure under Never policy", newPod(func(p *corev1.Pod) {
			p.Spec.RestartPolicy = corev1.RestartPolicyNever
			p.Spec.InitContainers = []corev1.Container{{Name: "proxy", RestartPolicy: &sidecar}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{terminated("proxy", "Error", 1)}
		}, readyCond(corev1.ConditionTrue, t0)), t0, Fault, "Exited 1", "pod.current-failure", nil},
		{"completed init container is not a failure", newPod(readyCond(corev1.ConditionTrue, t0), func(p *corev1.Pod) {
			p.Spec.InitContainers = []corev1.Container{{Name: "migrate"}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{terminated("migrate", "Completed", 0)}
		}), t0, OK, "Running", "pod.ready", nil},
		{"phase Unknown", newPod(phase(corev1.PodUnknown)), t0, Unknown, "Unknown", "pod.phase-unknown", nil},
		{"empty phase", newPod(phase("")), t0, Unknown, "Unknown", "pod.phase-unknown", nil},
		{"pending within start grace", newPod(phase(corev1.PodPending), container(waiting("app", "ContainerCreating"))),
			t0.Add(time.Minute), Progressing, "ContainerCreating", "pod.pending", at(t0.Add(PodStartGrace))},
		{"long startup turns degraded, not fault", newPod(phase(corev1.PodPending), container(waiting("app", "ContainerCreating"))),
			t0.Add(PodStartGrace), Degraded, "ContainerCreating", "pod.pending-overdue", nil},
		{"pending without creation time", newPod(phase(corev1.PodPending), func(p *corev1.Pod) { p.CreationTimestamp = metav1.Time{} }),
			t0, Unknown, "Pending", "pod.pending-untimed", nil},
		{"creation in the future (skew) is elapsed zero", newPod(phase(corev1.PodPending), func(p *corev1.Pod) { p.CreationTimestamp = metav1.NewTime(t0.Add(time.Hour)) }),
			t0, Progressing, "Pending", "pod.pending", at(t0.Add(time.Hour + PodStartGrace))},
		{"Ready transition on an old pod: timed from transition", newPod(func(p *corev1.Pod) { p.CreationTimestamp = metav1.NewTime(t0.Add(-30 * 24 * time.Hour)) },
			readyCond(corev1.ConditionFalse, t0)), t0.Add(30 * time.Second), Progressing, "Not ready", "pod.not-ready", at(t0.Add(ReadyGrace))},
		{"not ready past grace", newPod(readyCond(corev1.ConditionFalse, t0)), t0.Add(ReadyGrace), Degraded, "Not ready", "pod.not-ready-overdue", nil},
		{"Ready=Unknown is unknown, never ok", newPod(readyCond(corev1.ConditionUnknown, t0)), t0, Unknown, "Readiness unknown", "pod.ready-unknown", nil},
		{"Ready missing within start grace", newPod(), t0.Add(time.Minute), Progressing, "Starting", "pod.ready-missing-starting", at(t0.Add(PodStartGrace))},
		{"Ready missing past grace is unknown", newPod(), t0.Add(PodStartGrace), Unknown, "Readiness not reported", "pod.ready-missing", nil},
		{"ready", newPod(readyCond(corev1.ConditionTrue, t0)), t0, OK, "Running", "pod.ready", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Pod(c.pod, c.now)
			if v.Kind != c.kind || v.Label != c.label || v.Rule != c.rule {
				t.Fatalf("got %s %q (%s), want %s %q (%s)", v.Kind, v.Label, v.Rule, c.kind, c.label, c.rule)
			}
			switch {
			case c.reclassAt == nil && v.ReclassifyAt != nil:
				t.Fatalf("unexpected ReclassifyAt %v", *v.ReclassifyAt)
			case c.reclassAt != nil && (v.ReclassifyAt == nil || !v.ReclassifyAt.Equal(*c.reclassAt)):
				t.Fatalf("ReclassifyAt = %v, want %v", v.ReclassifyAt, *c.reclassAt)
			}
			if v.ReclassifyAt != nil && !v.ReclassifyAt.After(c.now) {
				t.Fatalf("ReclassifyAt %v is not in the future of %v", *v.ReclassifyAt, c.now)
			}
		})
	}
}

// Advancing a fake clock with no object change crosses each boundary exactly
// at ReclassifyAt.
func TestPodReclassifiesAtBoundary(t *testing.T) {
	pods := []*corev1.Pod{
		newPod(phase(corev1.PodPending)),
		newPod(readyCond(corev1.ConditionFalse, t0)),
		newPod(),
		newPod(func(p *corev1.Pod) { p.DeletionTimestamp = &metav1.Time{Time: t0} }),
	}
	for _, p := range pods {
		before := Pod(p, t0)
		if before.ReclassifyAt == nil {
			t.Fatalf("%s: expected a pending boundary", before.Rule)
		}
		justBefore := Pod(p, before.ReclassifyAt.Add(-time.Nanosecond))
		if justBefore.Kind != before.Kind {
			t.Fatalf("%s: flipped early to %s", before.Rule, justBefore.Kind)
		}
		after := Pod(p, *before.ReclassifyAt)
		if after.Kind == before.Kind {
			t.Fatalf("%s: did not flip at its boundary", before.Rule)
		}
		if after.ReclassifyAt != nil {
			t.Fatalf("%s: terminal verdict still schedules %v", after.Rule, *after.ReclassifyAt)
		}
	}
}

func TestPodWorstEvidenceIsDeterministic(t *testing.T) {
	p := newPod(phase(corev1.PodPending),
		container(waiting("b", "ErrImagePull")),
		container(waiting("a", "ErrImagePull")),
		container(terminated("c", "Error", 1)),
		func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable"}}
		})
	v := Pod(p, t0)
	if v.Label != "ErrImagePull" || v.Message != "a" {
		t.Fatalf("got %q from %q, want ErrImagePull from container a", v.Label, v.Message)
	}
	crash := newPod(container(terminated("x", "OOMKilled", 137)), container(waiting("y", "CrashLoopBackOff")))
	if v := Pod(crash, t0); v.Label != "CrashLoopBackOff" {
		t.Fatalf("CrashLoopBackOff must outrank an active OOM, got %q", v.Label)
	}
}

func TestPodNotesAreHistoryOnly(t *testing.T) {
	recovered := newPod(readyCond(corev1.ConditionTrue, t0), container(corev1.ContainerStatus{
		Name:  "app",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(t0.Add(-4 * time.Minute))}},
	}))
	v := Pod(recovered, t0)
	if v.Kind != OK || v.Label != "Running" || v.Note != "last OOMKilled 4m ago" {
		t.Fatalf("recovered restart: got %s %q note %q", v.Kind, v.Label, v.Note)
	}
	if v := Pod(recovered, t0.Add(RecentTermination)); v.Note != "" {
		t.Fatalf("note outlived RecentTermination: %q", v.Note)
	}
	untimed := newPod(readyCond(corev1.ConditionTrue, t0), container(corev1.ContainerStatus{
		Name:                 "app",
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
	}))
	if v := Pod(untimed, t0); v.Note != "" {
		t.Fatalf("untimed termination produced a note: %q", v.Note)
	}
}

func TestPodExitMessageCarriesLoopEvidence(t *testing.T) {
	cs := terminated("worker", "Error", 1)
	cs.RestartCount = 6
	v := Pod(newPod(readyCond(corev1.ConditionFalse, t0), container(cs)), t0)
	if v.Label != "Exited 1" || v.Message != "worker exited 1 (restarted 6 times)" {
		t.Fatalf("got %q / %q", v.Label, v.Message)
	}
}

// Coverage S11b §3.4: device health from allocatedResourcesStatus.
func TestPodDeviceHealth(t *testing.T) {
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	started := metav1.NewTime(at.Add(-time.Hour))
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}}
	health := func(h corev1.ResourceHealthStatus, ids ...string) []corev1.ResourceStatus {
		rs := corev1.ResourceStatus{Name: "claim:gpu/req"}
		for _, id := range ids {
			rs.Resources = append(rs.Resources, corev1.ResourceHealth{ResourceID: corev1.ResourceID(id), Health: h})
		}
		return []corev1.ResourceStatus{rs}
	}
	always := corev1.ContainerRestartPolicyAlways
	mk := func() *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", CreationTimestamp: started},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}},
				InitContainers: []corev1.Container{{Name: "side", RestartPolicy: &always}, {Name: "setup"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &started,
				Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: running}}}}
	}
	p := mk()
	p.Status.ContainerStatuses[0].AllocatedResourcesStatus = health(corev1.ResourceHealthStatusUnhealthy, "cdi/a", "cdi/b")
	if v := Pod(p, at); v.Kind != Degraded || v.Rule != "pod.device-unhealthy" || !strings.Contains(v.Message, "gpu/req: 2 unhealthy") {
		t.Fatalf("a running container's unhealthy devices: %+v", v)
	}
	p = mk()
	p.Status.ContainerStatuses[0].AllocatedResourcesStatus = health(corev1.ResourceHealthStatusUnknown, "cdi/a")
	if v := Pod(p, at); v.Kind != OK {
		t.Fatalf("Unknown is not a reading: %+v", v)
	}
	p = mk()
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "side", State: running, AllocatedResourcesStatus: health(corev1.ResourceHealthStatusUnhealthy, "cdi/s")}}
	if v := Pod(p, at); v.Kind != Degraded {
		t.Fatalf("a running restartable init container counts: %+v", v)
	}
	p = mk()
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		AllocatedResourcesStatus: health(corev1.ResourceHealthStatusUnhealthy, "cdi/i")}}
	if v := Pod(p, at); v.Kind != OK {
		t.Fatalf("a completed regular init container is history: %+v", v)
	}
	// a regular init container re-running in a Running pod (its sandbox
	// recreated) is not one of the containers the rule reads
	p = mk()
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "setup", State: running, AllocatedResourcesStatus: health(corev1.ResourceHealthStatusUnhealthy, "cdi/i")}}
	if v := Pod(p, at); v.Rule == "pod.device-unhealthy" {
		t.Fatalf("a running regular init container does not count: %+v", v)
	}
	p = mk()
	p.Status.ContainerStatuses[0].AllocatedResourcesStatus = health(corev1.ResourceHealthStatusUnhealthy, "cdi/a")
	p.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	if v := Pod(p, at); v.Kind != Fault {
		t.Fatalf("a current failure still leads: %+v", v)
	}
	p = mk()
	p.Status.ContainerStatuses[0].AllocatedResourcesStatus = health(corev1.ResourceHealthStatusUnhealthy, "cdi/a")
	p.Status.Conditions[0].Status = corev1.ConditionFalse
	if v := Pod(p, at); v.Rule != "pod.device-unhealthy" {
		t.Fatalf("an unhealthy device outranks Not ready: %+v", v)
	}
}

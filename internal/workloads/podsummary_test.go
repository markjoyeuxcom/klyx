package workloads

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// now is a fixed reference time for all pod-summary tests.
var psNow = time.Unix(200_000, 0)

// makePod is a builder that covers the common cases; tests override specific
// fields directly on the returned struct when they need finer control.
func makePod(ns, name string, phase corev1.PodPhase, ready bool) corev1.Pod {
	readyCond := corev1.ConditionTrue
	if !ready {
		readyCond = corev1.ConditionFalse
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         ns,
			Name:              name,
			CreationTimestamp: metav1.NewTime(psNow.Add(-10 * time.Minute)),
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx:1.25"},
			},
		},
		Status: corev1.PodStatus{
			Phase: phase,
			PodIP: "10.0.0.1",
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: readyCond},
			},
		},
	}
}

func TestRankPod(t *testing.T) {
	tests := []struct {
		name   string
		phase  corev1.PodPhase
		sev    severity
		recent bool
		ready  bool
		want   HealthRank
	}{
		// Succeeded is always Healthy regardless of severity (completed job pod is not broken).
		{"succeeded clean", corev1.PodSucceeded, sevNone, false, false, Healthy},
		{"succeeded with historical sev", corev1.PodSucceeded, sevHistorical, false, false, Healthy},
		// sevHard forces Unhealthy.
		{"hard failure", corev1.PodRunning, sevHard, false, true, Unhealthy},
		// Failed phase forces Unhealthy.
		{"failed phase", corev1.PodFailed, sevNone, false, false, Unhealthy},
		// Not ready (and not Succeeded) → Degraded.
		{"not ready pending", corev1.PodPending, sevNone, false, false, Degraded},
		{"not ready running no hard", corev1.PodRunning, sevNone, false, false, Degraded},
		{"not ready benign", corev1.PodRunning, sevBenign, false, false, Degraded},
		// Ready with recent termination → Restarts.
		{"ready recent termination", corev1.PodRunning, sevNone, true, true, Restarts},
		// Ready, no issues → Healthy.
		{"ready healthy", corev1.PodRunning, sevNone, false, true, Healthy},
		// Ready with stale historical → Healthy (same as rankOf's "stale historical settles").
		{"ready stale historical", corev1.PodRunning, sevHistorical, false, true, Healthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := makePod("x", "p", tt.phase, tt.ready)
			got := rankPod(&p, tt.sev, tt.recent)
			if got != tt.want {
				t.Fatalf("rankPod(phase=%v sev=%v recent=%v ready=%v) = %v, want %v",
					tt.phase, tt.sev, tt.recent, tt.ready, got, tt.want)
			}
		})
	}
}

func TestSummarizePods_CrashLoop(t *testing.T) {
	p := makePod("default", "api-abc", corev1.PodRunning, false)
	p.Spec.Containers = []corev1.Container{{Name: "app", Image: "myapp:1"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:         "app",
			Ready:        false,
			RestartCount: 3,
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
			},
		},
	}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	if len(out) != 1 {
		t.Fatalf("want 1 result, got %d", len(out))
	}
	s := out[0]
	if s.Rank != Unhealthy {
		t.Errorf("Rank: got %v, want Unhealthy", s.Rank)
	}
	if s.Reason != "CrashLoopBackOff" {
		t.Errorf("Reason: got %q, want CrashLoopBackOff", s.Reason)
	}
	if s.Restarts != 3 {
		t.Errorf("Restarts: got %d, want 3", s.Restarts)
	}
	if len(s.Containers) != 1 || s.Containers[0].State != "waiting:CrashLoopBackOff" {
		t.Errorf("container state: %+v", s.Containers)
	}
}

func TestSummarizePods_HealthyRunning(t *testing.T) {
	p := makePod("prod", "web-xyz", corev1.PodRunning, true)
	p.Spec.Containers = []corev1.Container{{Name: "web", Image: "nginx:1.25"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "web", Ready: true, RestartCount: 0, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
	}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	if len(out) != 1 {
		t.Fatalf("want 1 result, got %d", len(out))
	}
	s := out[0]
	if s.Rank != Healthy {
		t.Errorf("Rank: got %v, want Healthy", s.Rank)
	}
	if s.Reason != "" {
		t.Errorf("Reason: got %q, want empty", s.Reason)
	}
	if s.Restarts != 0 {
		t.Errorf("Restarts: got %d, want 0", s.Restarts)
	}
}

func TestSummarizePods_PendingUnschedulable(t *testing.T) {
	// Unschedulable maps to sevHard in reason.go → rank Unhealthy.
	p := makePod("default", "sched-fail", corev1.PodPending, false)
	p.Status.Conditions = []corev1.PodCondition{
		{
			Type:   corev1.PodScheduled,
			Status: corev1.ConditionFalse,
			Reason: "Unschedulable",
		},
	}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	if len(out) != 1 {
		t.Fatalf("want 1 result, got %d", len(out))
	}
	s := out[0]
	// worstPodReason returns ("Unschedulable", sevHard) → rankPod returns Unhealthy.
	if s.Rank != Unhealthy {
		t.Errorf("Rank: got %v, want Unhealthy (Unschedulable is sevHard)", s.Rank)
	}
	if s.Reason != "Unschedulable" {
		t.Errorf("Reason: got %q, want Unschedulable", s.Reason)
	}
}

func TestSummarizePods_Succeeded(t *testing.T) {
	// A completed job pod — Succeeded phase → Healthy; Reason gets "Completed" if empty.
	p := makePod("jobs", "batch-1", corev1.PodSucceeded, false)
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "job", Ready: false, RestartCount: 0,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}},
	}
	p.Spec.Containers = []corev1.Container{{Name: "job", Image: "batch:latest"}}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	s := out[0]
	if s.Rank != Healthy {
		t.Errorf("Rank: got %v, want Healthy", s.Rank)
	}
	if s.Reason != "Completed" {
		t.Errorf("Reason: got %q, want Completed", s.Reason)
	}
}

func TestSummarizePods_NotReadyDegraded(t *testing.T) {
	// Running but not-ready with no hard reason → Degraded.
	p := makePod("staging", "api-pending", corev1.PodRunning, false)
	p.Spec.Containers = []corev1.Container{{Name: "api", Image: "api:2"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "api", Ready: false, RestartCount: 0,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
	}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	s := out[0]
	if s.Rank != Degraded {
		t.Errorf("Rank: got %v, want Degraded", s.Rank)
	}
	if s.Reason != "" {
		t.Errorf("Reason: got %q, want empty", s.Reason)
	}
}

func TestSummarizePods_RecentTermination(t *testing.T) {
	// Ready pod with a recent container termination → Restarts rank.
	p := makePod("prod", "api-1", corev1.PodRunning, true)
	p.Spec.Containers = []corev1.Container{{Name: "api", Image: "api:3"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:         "api",
			Ready:        true,
			RestartCount: 2,
			State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					Reason:     "Error",
					FinishedAt: metav1.NewTime(psNow.Add(-5 * time.Minute)),
				},
			},
		},
	}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	s := out[0]
	if s.Rank != Restarts {
		t.Errorf("Rank: got %v, want Restarts (recent termination 5m ago)", s.Rank)
	}
}

func TestSummarizePods_StaleTermination(t *testing.T) {
	// Ready pod with a termination 30h ago → Healthy (recency window expired).
	p := makePod("prod", "api-2", corev1.PodRunning, true)
	p.Spec.Containers = []corev1.Container{{Name: "api", Image: "api:3"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{
			Name:         "api",
			Ready:        true,
			RestartCount: 1,
			State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					Reason:     "Error",
					FinishedAt: metav1.NewTime(psNow.Add(-30 * time.Hour)),
				},
			},
		},
	}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	s := out[0]
	if s.Rank != Healthy {
		t.Errorf("Rank: got %v, want Healthy (termination 30h ago is stale)", s.Rank)
	}
}

func TestSummarizePods_InitContainerCrash(t *testing.T) {
	// Init container in CrashLoopBackOff → Unhealthy; init entry appears first in Containers.
	p := makePod("default", "init-fail", corev1.PodPending, false)
	p.Spec.InitContainers = []corev1.Container{{Name: "init", Image: "init:1"}}
	p.Spec.Containers = []corev1.Container{{Name: "main", Image: "app:1"}}
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{
			Name:         "init",
			Ready:        false,
			RestartCount: 4,
			State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		},
	}
	// main container has no status yet (still waiting for init).

	out := SummarizePods([]corev1.Pod{p}, psNow)
	s := out[0]
	if s.Rank != Unhealthy {
		t.Errorf("Rank: got %v, want Unhealthy", s.Rank)
	}
	if s.Reason != "CrashLoopBackOff" {
		t.Errorf("Reason: got %q, want CrashLoopBackOff", s.Reason)
	}
	if len(s.Containers) < 1 {
		t.Fatalf("want at least init container in list, got %d", len(s.Containers))
	}
	if !s.Containers[0].Init {
		t.Errorf("first container should be Init=true (init containers listed first)")
	}
	if s.Containers[0].Name != "init" {
		t.Errorf("first container name: got %q, want init", s.Containers[0].Name)
	}
	if s.Containers[0].State != "waiting:CrashLoopBackOff" {
		t.Errorf("init container state: got %q", s.Containers[0].State)
	}
	if s.Containers[0].Restarts != 4 {
		t.Errorf("init container restarts: got %d, want 4", s.Containers[0].Restarts)
	}
}

func TestSummarizePods_OwnerExtraction(t *testing.T) {
	t.Run("ReplicaSet controller ownerRef", func(t *testing.T) {
		p := makePod("default", "app-rs-abc", corev1.PodRunning, true)
		ctrl := true
		p.OwnerReferences = []metav1.OwnerReference{
			{Kind: "ReplicaSet", Name: "app-rs", Controller: &ctrl},
		}
		p.Spec.Containers = []corev1.Container{{Name: "app", Image: "app:1"}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{
			{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}

		out := SummarizePods([]corev1.Pod{p}, psNow)
		s := out[0]
		if s.OwnerKind != "ReplicaSet" {
			t.Errorf("OwnerKind: got %q, want ReplicaSet", s.OwnerKind)
		}
		if s.OwnerName != "app-rs" {
			t.Errorf("OwnerName: got %q, want app-rs", s.OwnerName)
		}
	})

	t.Run("no ownerRef", func(t *testing.T) {
		p := makePod("default", "standalone", corev1.PodRunning, true)
		p.OwnerReferences = nil
		p.Spec.Containers = []corev1.Container{{Name: "app", Image: "app:1"}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{
			{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}

		out := SummarizePods([]corev1.Pod{p}, psNow)
		s := out[0]
		if s.OwnerKind != "" {
			t.Errorf("OwnerKind: got %q, want empty for pod with no ownerRef", s.OwnerKind)
		}
	})

	t.Run("non-controller ownerRef is ignored", func(t *testing.T) {
		p := makePod("default", "owned-noctl", corev1.PodRunning, true)
		notCtrl := false
		p.OwnerReferences = []metav1.OwnerReference{
			{Kind: "ConfigMap", Name: "cfg", Controller: &notCtrl},
		}
		p.Spec.Containers = []corev1.Container{{Name: "app", Image: "app:1"}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{
			{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}

		out := SummarizePods([]corev1.Pod{p}, psNow)
		s := out[0]
		if s.OwnerKind != "" {
			t.Errorf("OwnerKind: got %q, want empty (non-controller ownerRef)", s.OwnerKind)
		}
	})
}

func TestSummarizePods_Sort(t *testing.T) {
	// Unhealthy pod should sort before Healthy pod regardless of name order.
	crashPod := makePod("default", "aaa-crash", corev1.PodRunning, false)
	crashPod.Spec.Containers = []corev1.Container{{Name: "app", Image: "app:1"}}
	crashPod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "app", Ready: false, RestartCount: 1,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
	}

	healthyPod := makePod("default", "zzz-ok", corev1.PodRunning, true)
	healthyPod.Spec.Containers = []corev1.Container{{Name: "app", Image: "app:1"}}
	healthyPod.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
	}

	// Pass in alphabetical order (healthy "aaa" first if were sorting by name); unhealthy must float up.
	out := SummarizePods([]corev1.Pod{healthyPod, crashPod}, psNow)
	if len(out) != 2 {
		t.Fatalf("want 2 pods, got %d", len(out))
	}
	if out[0].Rank != Unhealthy {
		t.Errorf("first row should be Unhealthy, got %v", out[0].Rank)
	}
	if out[1].Rank != Healthy {
		t.Errorf("second row should be Healthy, got %v", out[1].Rank)
	}
}

func TestSummarizePods_ContainerStateStrings(t *testing.T) {
	// Verify state string encoding for all three container states + missing status.
	p := makePod("default", "multi", corev1.PodRunning, true)
	p.Spec.Containers = []corev1.Container{
		{Name: "running-c", Image: "r:1"},
		{Name: "waiting-c", Image: "w:1"},
		{Name: "terminated-c", Image: "t:1"},
		{Name: "nostatus-c", Image: "n:1"}, // no ContainerStatus entry yet
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "running-c", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: "waiting-c", Ready: false, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
		{Name: "terminated-c", Ready: false, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}}},
		// "nostatus-c" intentionally absent from ContainerStatuses.
	}
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	s := out[0]
	byName := map[string]ContainerSummary{}
	for _, c := range s.Containers {
		byName[c.Name] = c
	}

	if byName["running-c"].State != "running" {
		t.Errorf("running-c state: got %q, want running", byName["running-c"].State)
	}
	if byName["waiting-c"].State != "waiting:ContainerCreating" {
		t.Errorf("waiting-c state: got %q", byName["waiting-c"].State)
	}
	if byName["terminated-c"].State != "terminated:Completed" {
		t.Errorf("terminated-c state: got %q", byName["terminated-c"].State)
	}
	if byName["nostatus-c"].State != "" {
		t.Errorf("nostatus-c state: got %q, want empty string", byName["nostatus-c"].State)
	}
	if _, ok := byName["nostatus-c"]; !ok {
		t.Errorf("nostatus-c must still appear in Containers list")
	}
}

func TestSummarizePods_LastTerminationState(t *testing.T) {
	finished := metav1.NewTime(psNow.Add(-5 * time.Minute))
	p := makePod("default", "oomy", corev1.PodRunning, true)
	p.Spec.InitContainers = []corev1.Container{{Name: "setup", Image: "busybox:1.36"}}
	p.Spec.Containers = []corev1.Container{
		{Name: "app", Image: "app:1"},
		{Name: "clean", Image: "clean:1"},
	}
	p.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{Name: "setup", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}},
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "app", Ready: true, RestartCount: 3,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Reason: "OOMKilled", ExitCode: 137, FinishedAt: finished,
			}}},
		// No LastTerminationState: the fields must stay zero, never fabricated.
		{Name: "clean", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
	}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	byName := map[string]ContainerSummary{}
	for _, c := range out[0].Containers {
		byName[c.Name] = c
	}

	app := byName["app"]
	if app.LastReason != "OOMKilled" {
		t.Errorf("app LastReason: got %q, want OOMKilled", app.LastReason)
	}
	if app.LastExitCode != 137 {
		t.Errorf("app LastExitCode: got %d, want 137", app.LastExitCode)
	}
	if app.LastFinishedUnix != finished.Unix() {
		t.Errorf("app LastFinishedUnix: got %d, want %d", app.LastFinishedUnix, finished.Unix())
	}

	// Init containers map last termination too.
	setup := byName["setup"]
	if setup.LastReason != "Error" || setup.LastExitCode != 1 {
		t.Errorf("setup last termination: got %q/%d, want Error/1", setup.LastReason, setup.LastExitCode)
	}
	if setup.LastFinishedUnix != 0 {
		t.Errorf("setup LastFinishedUnix: got %d, want 0 (FinishedAt unset)", setup.LastFinishedUnix)
	}

	clean := byName["clean"]
	if clean.LastReason != "" || clean.LastExitCode != 0 || clean.LastFinishedUnix != 0 {
		t.Errorf("clean must carry zero last-termination values, got %q/%d/%d", clean.LastReason, clean.LastExitCode, clean.LastFinishedUnix)
	}
}

func TestSummarizePods_Fields(t *testing.T) {
	// Validate that Node, IP, Namespace, Name, Phase, AgeSeconds are all populated.
	p := makePod("mynamespace", "mypod", corev1.PodRunning, true)
	p.Spec.NodeName = "worker-1"
	p.Status.PodIP = "10.1.2.3"
	p.Spec.Containers = []corev1.Container{{Name: "app", Image: "app:1"}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
	}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	s := out[0]
	if s.Namespace != "mynamespace" {
		t.Errorf("Namespace: %q", s.Namespace)
	}
	if s.Name != "mypod" {
		t.Errorf("Name: %q", s.Name)
	}
	if s.Phase != "Running" {
		t.Errorf("Phase: %q", s.Phase)
	}
	if s.Node != "worker-1" {
		t.Errorf("Node: %q", s.Node)
	}
	if s.IP != "10.1.2.3" {
		t.Errorf("IP: %q", s.IP)
	}
	if s.AgeSeconds != 600 {
		t.Errorf("AgeSeconds: got %d, want 600 (10min)", s.AgeSeconds)
	}
}

func TestSummarizePods_ContainerPorts(t *testing.T) {
	p := makePod("default", "web", corev1.PodRunning, true)
	p.Spec.InitContainers = []corev1.Container{{
		Name: "setup", Image: "busybox:1.36",
		Ports: []corev1.ContainerPort{{ContainerPort: 9999}}, // declared but init ports are never offered
	}}
	p.Spec.Containers = []corev1.Container{
		{
			Name: "app", Image: "app:1",
			Ports: []corev1.ContainerPort{
				{Name: "http", ContainerPort: 3000, Protocol: corev1.ProtocolTCP},
				{ContainerPort: 9090}, // unnamed, protocol unset -> defaults TCP
			},
		},
		{Name: "sidecar", Image: "sc:1"}, // no declared ports
	}

	out := SummarizePods([]corev1.Pod{p}, psNow)
	s := out[0]
	if len(s.Containers) != 3 {
		t.Fatalf("want 3 containers (init+2), got %d", len(s.Containers))
	}
	if s.Containers[0].Ports != nil {
		t.Errorf("init container ports must not be collected, got %+v", s.Containers[0].Ports)
	}
	app := s.Containers[1]
	if len(app.Ports) != 2 {
		t.Fatalf("app ports: want 2, got %+v", app.Ports)
	}
	if app.Ports[0] != (ContainerPort{Name: "http", Port: 3000, Protocol: "TCP"}) {
		t.Errorf("port 0 wrong: %+v", app.Ports[0])
	}
	if app.Ports[1] != (ContainerPort{Name: "", Port: 9090, Protocol: "TCP"}) {
		t.Errorf("port 1 must default protocol to TCP: %+v", app.Ports[1])
	}
	if s.Containers[2].Ports != nil {
		t.Errorf("sidecar with no spec ports should have nil ports, got %+v", s.Containers[2].Ports)
	}
}

// A summary carries its creation time beside the age at its now, so a live
// view ticks the age instead of freezing it between pushes (1c slice 3 §3).
func TestSummarizePods_CarriesTheCreationAnchor(t *testing.T) {
	p := makePod("prod", "web-xyz", corev1.PodRunning, true)
	s := SummarizePods([]corev1.Pod{p}, psNow)[0]
	if !s.Created.Equal(psNow.Add(-10*time.Minute)) || s.AgeSeconds != 600 {
		t.Fatalf("anchor: created=%v age=%d", s.Created, s.AgeSeconds)
	}
}

// The scheduler's rule without resize state, on every model from 1.34
// (pod-level requests counted): the app sum plus sidecars, at least the
// init peak (each init beside the sidecars started before it), pod-level
// requests replacing the container sum, plus overhead.
func TestPodRequestSchedulerEffective(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	cpu := func(v string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(v)}}
	}
	cases := []struct {
		name string
		spec corev1.PodSpec
		want float64
	}{
		{"app containers sum", corev1.PodSpec{Containers: []corev1.Container{{Resources: cpu("100m")}, {Resources: cpu("200m")}}}, 0.3},
		{"a heavy init container sets the floor", corev1.PodSpec{
			InitContainers: []corev1.Container{{Resources: cpu("4")}},
			Containers:     []corev1.Container{{Resources: cpu("100m")}},
		}, 4},
		{"a sidecar adds to the app sum", corev1.PodSpec{
			InitContainers: []corev1.Container{{Resources: cpu("250m"), RestartPolicy: &always}},
			Containers:     []corev1.Container{{Resources: cpu("500m")}},
		}, 0.75},
		{"an init after a sidecar runs beside it", corev1.PodSpec{
			InitContainers: []corev1.Container{{Resources: cpu("1"), RestartPolicy: &always}, {Resources: cpu("2")}},
			Containers:     []corev1.Container{{Resources: cpu("100m")}},
		}, 3},
		{"pod-level requests replace the container sum", corev1.PodSpec{
			Resources:  &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1500m")}},
			Containers: []corev1.Container{{Resources: cpu("100m")}},
		}, 1.5},
		{"overhead is added", corev1.PodSpec{
			Overhead:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
			Containers: []corev1.Container{{Resources: cpu("500m")}},
		}, 0.75},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &corev1.Pod{Spec: c.spec}
			for minor := 34; minor <= 37; minor++ {
				if got, _ := cpuMem(SchedModelFor(minor).Requests(p)); got < c.want-1e-9 || got > c.want+1e-9 {
					t.Fatalf("1.%d: want %v cores, got %v", minor, c.want, got)
				}
			}
		})
	}
}

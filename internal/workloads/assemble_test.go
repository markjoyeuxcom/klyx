package workloads

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func sel(m map[string]string) *metav1.LabelSelector { return &metav1.LabelSelector{MatchLabels: m} }

func pod(ns, name string, labels map[string]string, ready bool, restarts int32, waiting string) corev1.Pod {
	cs := corev1.ContainerStatus{RestartCount: restarts}
	if waiting != "" {
		cs.State.Waiting = &corev1.ContainerStateWaiting{Reason: waiting}
	} else {
		cs.State.Running = &corev1.ContainerStateRunning{}
	}
	readyCond := corev1.ConditionTrue
	if !ready {
		readyCond = corev1.ConditionFalse
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels, CreationTimestamp: metav1.NewTime(time.Unix(0, 0))},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: readyCond}},
			ContainerStatuses: []corev1.ContainerStatus{cs},
		},
	}
}

func TestAssembleRankAndJoinAndOwner(t *testing.T) {
	now := time.Unix(600, 0)
	broken := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ollama-prod", Name: "ollama",
			Labels: map[string]string{"kustomize.toolkit.fluxcd.io/name": "ollama", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}},
		Spec:   appsv1.DeploymentSpec{Replicas: i32(1), Selector: sel(map[string]string{"app": "ollama"})},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	healthy := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "grafana"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(1), Selector: sel(map[string]string{"app": "grafana"})},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1},
	}
	pods := []corev1.Pod{
		pod("ollama-prod", "ollama-x", map[string]string{"app": "ollama"}, false, 7, "CrashLoopBackOff"),
		pod("monitoring", "grafana-y", map[string]string{"app": "grafana"}, true, 0, ""),
	}
	pods[0].UID = "uid-ollama-x"

	out := Assemble([]appsv1.Deployment{healthy, broken}, nil, nil, pods, true, now)
	if len(out) != 2 {
		t.Fatalf("want 2, got %d", len(out))
	}
	// Triage: broken first.
	if out[0].Name != "ollama" || out[0].Rank != Unhealthy || out[0].Reason != "CrashLoopBackOff" || out[0].Restarts != 7 {
		t.Fatalf("row0: %+v", out[0])
	}
	if out[0].GitOps == nil || out[0].GitOps.Kind != "Kustomization" || out[0].GitOps.Name != "ollama" {
		t.Fatalf("owner: %+v", out[0].GitOps)
	}
	if len(out[0].Pods) != 1 || out[0].Pods[0].AgeSeconds != 600 || !out[0].Pods[0].Created.Equal(time.Unix(0, 0)) || out[0].Pods[0].UID != "uid-ollama-x" {
		t.Fatalf("pods/age, identity and the creation anchor a live view ticks from: %+v", out[0].Pods)
	}
	if out[1].Name != "grafana" || out[1].Rank != Healthy || out[1].GitOps != nil || out[1].Reason != "Available" {
		t.Fatalf("row1: %+v", out[1])
	}
}

func TestAssembleScaledToZeroAndEmptySelector(t *testing.T) {
	now := time.Unix(0, 0)
	scaled := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "scaled"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(0), Selector: sel(map[string]string{"app": "scaled"})},
		Status:     appsv1.DeploymentStatus{},
	}
	// Empty selector must match ZERO pods (not the whole namespace).
	noSel := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "nosel"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(1), Selector: &metav1.LabelSelector{}},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1},
	}
	pods := []corev1.Pod{pod("x", "stray", map[string]string{"app": "whatever"}, true, 3, "")}

	out := Assemble([]appsv1.Deployment{scaled, noSel}, nil, nil, pods, false, now)
	byName := map[string]Workload{}
	for _, w := range out {
		byName[w.Name] = w
	}
	if byName["scaled"].Rank != Healthy || byName["scaled"].Reason != "Scaled to 0" {
		t.Fatalf("scaled: %+v", byName["scaled"])
	}
	if len(byName["nosel"].Pods) != 0 || byName["nosel"].Restarts != 0 {
		t.Fatalf("empty selector matched pods: %+v", byName["nosel"])
	}
}

func TestAssembleInitRestartsCounted(t *testing.T) {
	now := time.Unix(0, 0)
	d := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "initcrash"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(1), Selector: sel(map[string]string{"app": "ic"})},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0},
	}
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "ic-1", Labels: map[string]string{"app": "ic"}},
		Status: corev1.PodStatus{
			Phase:                 corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{RestartCount: 9, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}},
		},
	}
	out := Assemble([]appsv1.Deployment{d}, nil, nil, []corev1.Pod{p}, false, now)
	// The reason and the restart count must agree: init crashloop with its restarts.
	if out[0].Reason != "CrashLoopBackOff" || out[0].Restarts != 9 || out[0].Rank != Unhealthy {
		t.Fatalf("init restarts must surface with the reason: %+v", out[0])
	}
}

func TestAssembleDisplayReasonArms(t *testing.T) {
	now := time.Unix(0, 0)
	// Deployment ready<desired, no pod reason, no condition -> Progressing fallback.
	prog := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "prog"},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(3), Selector: sel(map[string]string{"app": "prog"})},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	// Healthy StatefulSet -> "Ready" (kind vocabulary).
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "db"},
		Spec:       appsv1.StatefulSetSpec{Replicas: i32(1), Selector: sel(map[string]string{"app": "db"})},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1, AvailableReplicas: 1},
	}
	out := Assemble([]appsv1.Deployment{prog}, []appsv1.StatefulSet{sts}, nil, nil, false, now)
	byName := map[string]Workload{}
	for _, w := range out {
		byName[w.Name] = w
	}
	if byName["prog"].Reason != "Progressing · 2 unavailable" {
		t.Fatalf("progressing fallback: %q", byName["prog"].Reason)
	}
	if byName["db"].Reason != "Ready" {
		t.Fatalf("sts vocabulary: %q", byName["db"].Reason)
	}
}

// runningPodLastTerminated builds a 1/1-ready pod whose single container is
// currently Running but carries a lastState terminated marker (the trace a
// restart leaves behind). reason/finishedAt drive recency classification.
func runningPodLastTerminated(ns, name string, labels map[string]string, restarts int32, reason string, finishedAt time.Time) corev1.Pod {
	cs := corev1.ContainerStatus{
		RestartCount: restarts,
		State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: reason, FinishedAt: metav1.NewTime(finishedAt),
		}},
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels, CreationTimestamp: metav1.NewTime(time.Unix(0, 0))},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{cs},
		},
	}
}

func readyDeploy(ns, name string, sel *metav1.LabelSelector) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(1), Selector: sel},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1},
	}
}

func TestRankOf(t *testing.T) {
	tests := []struct {
		name           string
		desired, ready int
		recent         bool
		sev            severity
		want           HealthRank
	}{
		{"scaled to zero", 0, 0, false, sevNone, Healthy},
		{"hard failure forces unhealthy even when ready", 1, 1, false, sevHard, Unhealthy},
		{"zero ready is unhealthy", 1, 0, false, sevNone, Unhealthy},
		{"partial ready is degraded", 3, 1, false, sevNone, Degraded},
		{"recent termination lights info", 1, 1, true, sevNone, Restarts},
		{"recent termination with historical sev lights info", 1, 1, true, sevHistorical, Restarts},
		// The whole point: an old (non-recent) restart settles to grey, even when
		// the reason scanner still tags it historical.
		{"stale historical settles to healthy", 1, 1, false, sevHistorical, Healthy},
		{"old restart settles to healthy", 1, 1, false, sevNone, Healthy},
		{"benign current transient does not light info on its own", 1, 1, false, sevBenign, Healthy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rankOf(tt.desired, tt.ready, tt.recent, tt.sev); got != tt.want {
				t.Fatalf("rankOf(%d,%d,%v,%v)=%v want %v", tt.desired, tt.ready, tt.recent, tt.sev, got, tt.want)
			}
		})
	}
}

func TestRecentlyTerminated(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	withInit := func(finished time.Time) []*corev1.Pod {
		p := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "p"},
			Status: corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{{
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Reason: "Error", FinishedAt: metav1.NewTime(finished),
					}},
				}},
			},
		}
		return []*corev1.Pod{&p}
	}
	if !recentlyTerminated(withInit(now.Add(-5*time.Minute)), now) {
		t.Fatalf("init container terminated 5m ago must be recent")
	}
	if recentlyTerminated(withInit(now.Add(-5*time.Hour)), now) {
		t.Fatalf("init container terminated 5h ago must NOT be recent")
	}
	// No termination at all.
	none := &corev1.Pod{Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{}}}}
	if recentlyTerminated([]*corev1.Pod{none}, now) {
		t.Fatalf("pod with no termination must NOT be recent")
	}
}

func TestAssembleOldRestartSettlesToHealthy(t *testing.T) {
	now := time.Unix(200_000, 0)
	d := readyDeploy("x", "settled", sel(map[string]string{"app": "s"}))
	p := runningPodLastTerminated("x", "s-1", map[string]string{"app": "s"}, 3, "Error", now.Add(-30*time.Hour))
	out := Assemble([]appsv1.Deployment{d}, nil, nil, []corev1.Pod{p}, false, now)
	w := out[0]
	if w.Rank != Healthy {
		t.Fatalf("old restart must settle to Healthy: %+v", w)
	}
	if w.Restarts != 3 {
		t.Fatalf("restart COUNT must stay visible: %+v", w)
	}
	if w.Reason == "Error" {
		t.Fatalf("stale historical reason must be suppressed from text, got %q", w.Reason)
	}
	if w.Reason != "Available" {
		t.Fatalf("healthy Deployment must read Available, got %q", w.Reason)
	}
}

func TestAssembleRecentRestartLightsInfo(t *testing.T) {
	now := time.Unix(200_000, 0)
	d := readyDeploy("x", "recent", sel(map[string]string{"app": "r"}))
	p := runningPodLastTerminated("x", "r-1", map[string]string{"app": "r"}, 3, "Error", now.Add(-2*time.Minute))
	out := Assemble([]appsv1.Deployment{d}, nil, nil, []corev1.Pod{p}, false, now)
	if out[0].Rank != Restarts {
		t.Fatalf("recent restart must light info tier: %+v", out[0])
	}
}

func TestAssembleStaleOOMKillSuppressedFromText(t *testing.T) {
	now := time.Unix(200_000, 0)
	d := readyDeploy("x", "stale-oom", sel(map[string]string{"app": "o"}))
	p := runningPodLastTerminated("x", "o-1", map[string]string{"app": "o"}, 1, "OOMKilled", now.Add(-10*time.Hour))
	out := Assemble([]appsv1.Deployment{d}, nil, nil, []corev1.Pod{p}, false, now)
	w := out[0]
	if w.Rank != Healthy {
		t.Fatalf("stale OOMKill must settle to Healthy: %+v", w)
	}
	if w.Reason != "Available" {
		t.Fatalf("stale OOMKill must be suppressed from text (want Available), got %q", w.Reason)
	}
}

func TestAssembleRecentOOMKillShown(t *testing.T) {
	now := time.Unix(200_000, 0)
	d := readyDeploy("x", "recent-oom", sel(map[string]string{"app": "ro"}))
	p := runningPodLastTerminated("x", "ro-1", map[string]string{"app": "ro"}, 1, "OOMKilled", now.Add(-3*time.Minute))
	out := Assemble([]appsv1.Deployment{d}, nil, nil, []corev1.Pod{p}, false, now)
	w := out[0]
	if w.Rank != Restarts {
		t.Fatalf("recent OOMKill must light info tier: %+v", w)
	}
	if w.Reason != "OOMKilled" {
		t.Fatalf("recent OOMKill must be shown in text, got %q", w.Reason)
	}
}

func TestAssembleHelmOwner(t *testing.T) {
	now := time.Unix(0, 0)
	d := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "chart",
			Labels: map[string]string{"helm.toolkit.fluxcd.io/name": "grafana", "helm.toolkit.fluxcd.io/namespace": "monitoring"}},
		Spec:   appsv1.DeploymentSpec{Replicas: i32(1), Selector: sel(map[string]string{"app": "c"})},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1},
	}
	out := Assemble([]appsv1.Deployment{d}, nil, nil, nil, true, now)
	if out[0].GitOps == nil || out[0].GitOps.Kind != "HelmRelease" || out[0].GitOps.Name != "grafana" || out[0].GitOps.Namespace != "monitoring" {
		t.Fatalf("helm owner: %+v", out[0].GitOps)
	}
}

func TestAssembleOverlappingSelectorsDoubleCount(t *testing.T) {
	// Regression-LOCK (known behavior, not desired): overlapping selectors match a
	// shared pod into BOTH workloads. Documents that a future owner-reference join
	// would be a deliberate change. Mirrors Kubernetes label semantics.
	now := time.Unix(0, 0)
	a := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "a"},
		Spec:       appsv1.StatefulSetSpec{Replicas: i32(1), Selector: sel(map[string]string{"app": "web"})},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 0},
	}
	b := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "b"},
		Spec:       appsv1.StatefulSetSpec{Replicas: i32(1), Selector: sel(map[string]string{"app": "web", "tier": "api"})},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 0},
	}
	shared := pod("x", "shared", map[string]string{"app": "web", "tier": "api"}, false, 2, "CrashLoopBackOff")
	out := Assemble(nil, []appsv1.StatefulSet{a, b}, nil, []corev1.Pod{shared}, false, now)
	byName := map[string]Workload{}
	for _, w := range out {
		byName[w.Name] = w
	}
	// Both workloads see the shared pod (known double-count).
	if len(byName["a"].Pods) != 1 || len(byName["b"].Pods) != 1 {
		t.Fatalf("expected the shared pod under both (known behavior): a=%d b=%d", len(byName["a"].Pods), len(byName["b"].Pods))
	}
}

func TestAssembleCarriesGenerations(t *testing.T) {
	now := time.Unix(600, 0)
	// Spec edited (generation 3) but the controller has not observed it yet
	// (observedGeneration 2): status reflects the previous spec, so the UI
	// must render the row as transitioning, never as confidently Ready.
	edited := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "web", Name: "api", Generation: 3},
		Spec:       appsv1.DeploymentSpec{Replicas: i32(1), Selector: sel(map[string]string{"app": "api"})},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1, AvailableReplicas: 1, ObservedGeneration: 2},
	}
	ds := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "cilium", Generation: 5},
		Spec:       appsv1.DaemonSetSpec{Selector: sel(map[string]string{"app": "cilium"})},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 1, NumberReady: 1, ObservedGeneration: 5},
	}
	out := Assemble([]appsv1.Deployment{edited}, nil, []appsv1.DaemonSet{ds}, nil, false, now)
	byName := map[string]Workload{}
	for _, w := range out {
		byName[w.Name] = w
	}
	if byName["api"].Generation != 3 || byName["api"].ObservedGeneration != 2 {
		t.Fatalf("deployment generations not carried: %+v", byName["api"])
	}
	if byName["cilium"].Generation != 5 || byName["cilium"].ObservedGeneration != 5 {
		t.Fatalf("daemonset generations not carried: %+v", byName["cilium"])
	}
}

// TestRecentlyTerminated_CompletedInitContainerNotCounted is the regression: a
// normally-completed init container (State.Terminated{ExitCode:0}) stays in the
// current terminated state for the whole life of a Running pod and must NOT be
// mis-ranked as a recent restart; only a FAILED current-state termination counts.
func TestRecentlyTerminated_CompletedInitContainerNotCounted(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	initState := func(exitCode int32, reason string) []*corev1.Pod {
		p := corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "x", Name: "p"},
			Status: corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{{
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Reason: reason, ExitCode: exitCode, FinishedAt: metav1.NewTime(now.Add(-5 * time.Minute)),
					}},
				}},
			},
		}
		return []*corev1.Pod{&p}
	}
	if recentlyTerminated(initState(0, "Completed"), now) {
		t.Fatal("completed init container (ExitCode 0) must not count as a recent restart")
	}
	if !recentlyTerminated(initState(1, "Error"), now) {
		t.Fatal("failed current-state termination (ExitCode 1) must still count")
	}
}

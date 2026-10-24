package evalrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/evalrun/record"
)

var (
	ksGVR  = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	depGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

func kustomization(attempted, ready, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"name": "a1-web", "namespace": "klyx-a1-flux", "uid": "uid-ks"},
		"status": map[string]any{"lastAttemptedRevision": attempted,
			"conditions": []any{map[string]any{"type": "Ready", "status": ready, "reason": reason}}},
	}}
}

func depObj() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "klyx-a1-app", "uid": "uid-dep"}}}
}

// crashLab is the E1 plant: the Deployment's revision 2 ReplicaSet crash
// loops; revision 1's pod runs.
func crashLab(waiting string) *kubefake.Clientset {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "klyx-a1-app", UID: "uid-dep"}}
	ctl := true
	rs := func(name, uid, rev string) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "klyx-a1-app", UID: types.UID(uid),
			Annotations:     map[string]string{"deployment.kubernetes.io/revision": rev},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", UID: "uid-dep", Controller: &ctl}}}}
	}
	pod := func(name, uid, owner string, st corev1.ContainerStatus) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "klyx-a1-app", UID: types.UID(uid),
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: owner, UID: types.UID("uid-" + owner), Controller: &ctl}}},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{st}}}
	}
	crash := corev1.ContainerStatus{Name: "web", RestartCount: 7, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: waiting}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error",
			FinishedAt: metav1.NewTime(time.Date(2026, 10, 24, 11, 58, 0, 0, time.UTC))}}}
	run := corev1.ContainerStatus{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	return kubefake.NewSimpleClientset(dep, rs("web-old", "uid-web-old", "1"), rs("web-new", "uid-web-new", "2"),
		pod("web-new-a", "uid-pod-new", "web-new", crash), pod("web-old-a", "uid-pod-old", "web-old", run))
}

func checker(kube *kubefake.Clientset, objs ...runtime.Object) Checker {
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		ksGVR: "KustomizationList", depGVR: "DeploymentList"}, objs...)
	return Checker{Dyn: dyn, Kube: kube, Now: func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }}
}

func e1Plant() *Scenario {
	ks := Ref{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations", Kind: "Kustomization", Namespace: "klyx-a1-flux", Name: "a1-web"}
	dep := Ref{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespace: "klyx-a1-app", Name: "web"}
	return &Scenario{Name: "e1", Anchor: ks,
		Invariants: []Invariant{
			{Object: ks, Path: "status.lastAttemptedRevision", Matches: `@sha1:a9b3c81`},
			{Object: ks, Condition: &ConditionSpec{Type: "Ready", Status: "False", Reason: "HealthCheckFailed"}},
			{Object: dep, Pods: &PodsSpec{Waiting: "CrashLoopBackOff", MinRestarts: 1}},
			{Object: Ref{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespace: "klyx-a1-app", Name: "no-such"}, Absent: true},
		},
		Witnesses: []Witness{{Object: ks, Path: "status.lastAttemptedRevision"}, {Object: dep, Path: "metadata.uid"}},
		Predicates: []PredicateSpec{
			{Predicate: record.Predicate{ID: "e1-log", Kind: record.KindLog}, PodsOf: &dep},
			{Predicate: record.Predicate{ID: "e1-diff", Kind: record.KindDiff, Pattern: `env\[MODE\]`}, SubjectRef: &dep},
		},
	}
}

func TestThePlantHolds(t *testing.T) {
	c := checker(crashLab("CrashLoopBackOff"), kustomization("main@sha1:a9b3c81", "False", "HealthCheckFailed"), depObj())
	p := c.Check(context.Background(), e1Plant())
	if !p.OK {
		t.Fatal(p.Failed)
	}
	if p.AnchorUID != "uid-ks" || p.Witnesses["Deployment klyx-a1-app/web metadata.uid"] != "uid-dep" {
		t.Fatalf("%+v", p)
	}
	if len(p.Predicates) != 2 || len(p.Predicates[0].Pods) != 1 || p.Predicates[0].Pods[0] != "uid-pod-new" || p.Predicates[1].Subject.UID != "uid-dep" {
		t.Fatalf("the newest ReplicaSet's pods and the subject's UID are filled in: %+v", p.Predicates)
	}
}

func TestABrokenPlantSaysWhichInvariant(t *testing.T) {
	cases := []struct {
		name string
		c    Checker
		want string
	}{
		{"another revision attempted", checker(crashLab("CrashLoopBackOff"), kustomization("main@sha1:ffff", "False", "HealthCheckFailed"), depObj()), "invariant 1"},
		{"ready", checker(crashLab("CrashLoopBackOff"), kustomization("main@sha1:a9b3c81", "True", "ReconciliationSucceeded"), depObj()), "invariant 2"},
		{"the pods are not crash looping", checker(crashLab("ImagePullBackOff"), kustomization("main@sha1:a9b3c81", "False", "HealthCheckFailed"), depObj()), "invariant 3"},
		{"no anchor", checker(crashLab("CrashLoopBackOff"), depObj()), "the anchor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.c.Check(context.Background(), e1Plant())
			if p.OK || !strings.Contains(p.Failed, c.want) {
				t.Fatalf("want %q, got %+v", c.want, p)
			}
		})
	}
}

func TestACrashLoopHoldsWhateverItsStateNow(t *testing.T) {
	s := e1Plant()
	s.Invariants[2].Pods = &PodsSpec{Crashing: true, MinRestarts: 1}
	// between back-offs the container runs again: still a crash loop
	if p := checker(crashLab("ContainerCreating"), kustomization("main@sha1:a9b3c81", "False", "HealthCheckFailed"), depObj()).Check(context.Background(), s); !p.OK {
		t.Fatal(p.Failed)
	}
}

func TestWitnessesTellAChange(t *testing.T) {
	a := Plant{OK: true, AnchorUID: "u1", Witnesses: map[string]string{"x": "1", "y": "2"}}
	b := Plant{OK: true, AnchorUID: "u2", Witnesses: map[string]string{"x": "1", "y": "3"}}
	if ch := a.Changed(b); strings.Join(ch, ",") != "the anchor's UID,y" {
		t.Fatalf("%v", ch)
	}
	if ch := a.Changed(a); len(ch) != 0 {
		t.Fatalf("%v", ch)
	}
}

func TestEventInvariant(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	ev := func(reason, msg string, at time.Time) *corev1.Event {
		return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: reason + at.String(), Namespace: "lab-s4"}, Reason: reason, Message: msg,
			InvolvedObject: corev1.ObjectReference{UID: "uid-svc"}, LastTimestamp: metav1.NewTime(at)}
	}
	svc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "one-b", "namespace": "lab-s4", "uid": "uid-svc"}}}
	inv := EventSpec{Reason: "AllocationFailed", Message: `no available IPs`, Within: "1h"}
	recent := Checker{Kube: kubefake.NewSimpleClientset(ev("AllocationFailed", "Failed to allocate IP for \"lab-s4/one-b\": no available IPs", now.Add(-10*time.Minute)))}
	if err := recent.event(context.Background(), svc, inv, now); err != nil {
		t.Fatal(err)
	}
	old := Checker{Kube: kubefake.NewSimpleClientset(ev("AllocationFailed", "no available IPs", now.Add(-2*time.Hour)))}
	if err := old.event(context.Background(), svc, inv, now); err == nil {
		t.Fatal("an event older than the window does not hold the plant")
	}
}

func TestLookup(t *testing.T) {
	obj := map[string]any{"status": map[string]any{
		"conditions": []any{map[string]any{"type": "Ready", "status": "False"}},
		"history":    []any{map[string]any{"lastReconciledStatus": "HealthCheckFailed"}},
	}}
	if v, ok := lookup(obj, "status.conditions[type=Ready].status"); !ok || v != "False" {
		t.Fatal(v)
	}
	if v, ok := lookup(obj, "status.history[0].lastReconciledStatus"); !ok || v != "HealthCheckFailed" {
		t.Fatal(v)
	}
	for _, p := range []string{"status.history[3].x", "status.conditions[type=Healthy]", "spec.x", "status.history[a]"} {
		if _, ok := lookup(obj, p); ok {
			t.Fatalf("%s resolved", p)
		}
	}
}

func TestScenarioManifests(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "scenario.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("e2-healthy-control", `name: e2-healthy-control
control: true
anchor: {group: apps, version: v1, resource: deployments, kind: Deployment, namespace: klyx-eval, name: steady}
invariants:
- {object: {group: apps, version: v1, resource: deployments, kind: Deployment, namespace: klyx-eval, name: steady}, pods: {ready: true}}
`)
	ss, err := LoadScenarios(dir, nil)
	if err != nil || len(ss) != 1 || !ss[0].Control {
		t.Fatalf("%v %+v", err, ss)
	}
	for name, body := range map[string]string{
		"wrong-name":    "name: other\nanchor: {version: v1, resource: pods, kind: Pod, name: x}\ninvariants: [{object: {version: v1, resource: pods, kind: Pod, name: x}, absent: true}]\n",
		"unknown-field": "name: unknown-field\nanchor: {version: v1, resource: pods, kind: Pod, name: x}\nbogus: 1\ninvariants: [{object: {version: v1, resource: pods, kind: Pod, name: x}, absent: true}]\n",
		"two-forms":     "name: two-forms\ncontrol: true\nanchor: {version: v1, resource: pods, kind: Pod, name: x}\ninvariants: [{object: {version: v1, resource: pods, kind: Pod, name: x}, absent: true, path: a, matches: b}]\n",
		"no-evidence":   "name: no-evidence\nanchor: {version: v1, resource: pods, kind: Pod, name: x}\ninvariants: [{object: {version: v1, resource: pods, kind: Pod, name: x}, absent: true}]\n",
	} {
		d := t.TempDir()
		if err := os.MkdirAll(filepath.Join(d, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name, "scenario.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadScenarios(d, nil); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := LoadScenarios(dir, []string{"e9-missing"}); err == nil {
		t.Fatal("an unknown scenario name is refused")
	}
}

func TestTheLabsScenariosLoad(t *testing.T) {
	ss, err := LoadScenarios("../../scripts/lab/eval", nil)
	if err != nil || len(ss) != 4 || ss[0].Name != "e1-flux-crashloop" || !ss[1].Control || ss[2].Name != "e3-keda-no-target" || ss[3].Name != "e4-lb-pool-exhausted" {
		t.Fatalf("%v %+v", err, ss)
	}
	if len(ss[2].Lookalikes) != 3 || len(ss[2].Predicates) != 2 || len(ss[3].Lookalikes) != 2 || len(ss[3].Predicates) != 1 {
		t.Fatalf("%+v %+v", ss[2], ss[3])
	}
	if len(ss[0].Predicates) != 2 || ss[0].Repos[0] != "https://github.com/markjoyeuxcom/klyx-lab-a1" {
		t.Fatalf("%+v", ss[0])
	}
	st, err := LoadScenarios("../../scripts/lab/eval/selftest", nil)
	if err != nil || len(st) != 3 {
		t.Fatalf("%v %+v", err, st)
	}
	// a filter keeps only the named, whatever their order
	one, err := LoadScenarios("../../scripts/lab/eval", []string{"e1-flux-crashloop"})
	if err != nil || len(one) != 1 || one[0].Name != "e1-flux-crashloop" {
		t.Fatalf("%v %+v", err, one)
	}
	two, err := LoadScenarios("../../scripts/lab/eval/selftest", []string{"s3-drift", "s1-broken-plant"})
	if err != nil || len(two) != 2 || two[0].Name != "s1-broken-plant" || two[1].Name != "s3-drift" {
		t.Fatalf("%v %+v", err, two)
	}
}

// Codex's implementation review, finding 3: a replaced ReplicaSet or pod
// between the two checks is drift.
func TestAReplacedReplicaSetIsDrift(t *testing.T) {
	s := e1Plant()
	before := checker(crashLab("CrashLoopBackOff"), kustomization("main@sha1:a9b3c81", "False", "HealthCheckFailed"), depObj()).Check(context.Background(), s)
	kube := crashLab("CrashLoopBackOff")
	_ = kube.AppsV1().ReplicaSets("klyx-a1-app").Delete(context.Background(), "web-new", metav1.DeleteOptions{})
	_ = kube.CoreV1().Pods("klyx-a1-app").Delete(context.Background(), "web-new-a", metav1.DeleteOptions{})
	ctl := true
	_, _ = kube.AppsV1().ReplicaSets("klyx-a1-app").Create(context.Background(), &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-newer", Namespace: "klyx-a1-app", UID: "uid-web-newer",
		Annotations:     map[string]string{"deployment.kubernetes.io/revision": "2"},
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", UID: "uid-dep", Controller: &ctl}}}}, metav1.CreateOptions{})
	_, _ = kube.CoreV1().Pods("klyx-a1-app").Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-newer-a", Namespace: "klyx-a1-app", UID: "uid-pod-newer",
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-newer", UID: "uid-web-newer", Controller: &ctl}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "web", RestartCount: 3, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, FinishedAt: metav1.NewTime(time.Date(2026, 10, 24, 11, 59, 0, 0, time.UTC))}}}}}}, metav1.CreateOptions{})
	after := checker(kube, kustomization("main@sha1:a9b3c81", "False", "HealthCheckFailed"), depObj()).Check(context.Background(), s)
	if !before.OK || !after.OK {
		t.Fatalf("both plants hold: %s / %s", before.Failed, after.Failed)
	}
	ch := before.Changed(after)
	if len(ch) == 0 {
		t.Fatal("a replaced ReplicaSet and pod are drift")
	}
	if got, _ := Classify(RunFacts{Before: before, Ready: true, AnchorRead: true, Outcome: "answered", Report: record.ReportAccepted, After: after}); got != StatusDrift {
		t.Fatalf("%s %v", got, ch)
	}
}

// A pod the ReplicaSet replaced (same ReplicaSet, new pod) is drift too.
func TestAReplacedPodIsDrift(t *testing.T) {
	s := e1Plant()
	before := checker(crashLab("CrashLoopBackOff"), kustomization("main@sha1:a9b3c81", "False", "HealthCheckFailed"), depObj()).Check(context.Background(), s)
	kube := crashLab("CrashLoopBackOff")
	p, _ := kube.CoreV1().Pods("klyx-a1-app").Get(context.Background(), "web-new-a", metav1.GetOptions{})
	_ = kube.CoreV1().Pods("klyx-a1-app").Delete(context.Background(), "web-new-a", metav1.DeleteOptions{})
	p.Name, p.UID, p.ResourceVersion = "web-new-b", "uid-pod-new-b", ""
	_, _ = kube.CoreV1().Pods("klyx-a1-app").Create(context.Background(), p, metav1.CreateOptions{})
	after := checker(kube, kustomization("main@sha1:a9b3c81", "False", "HealthCheckFailed"), depObj()).Check(context.Background(), s)
	if !after.OK || len(before.Changed(after)) == 0 {
		t.Fatalf("%v %v", after.Failed, before.Changed(after))
	}
}

// Codex's implementation review, finding 4: a recovered pod is not a crash
// loop, whatever its history.
func TestARecoveredPodDoesNotHoldTheCrashPlant(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	pod := func(ready bool, state corev1.ContainerState, lastFailed time.Time) corev1.Pod {
		st := corev1.ConditionFalse
		if ready {
			st = corev1.ConditionTrue
		}
		return corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st}},
			ContainerStatuses: []corev1.ContainerStatus{{RestartCount: 9, State: state,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, FinishedAt: metav1.NewTime(lastFailed)}}}}}}
	}
	running := func(since time.Time) corev1.ContainerState {
		return corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(since)}}
	}
	waiting := corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}
	cases := []struct {
		name string
		pod  corev1.Pod
		want bool
	}{
		{"running and Ready for two hours, its last failure a day old", pod(true, running(now.Add(-2*time.Hour)), now.Add(-24*time.Hour)), false},
		{"not Ready, but its last failure a day old", pod(false, waiting, now.Add(-24*time.Hour)), false},
		{"waiting after a failure a minute ago", pod(false, waiting, now.Add(-time.Minute)), true},
		{"the brief run between back-offs", pod(false, running(now.Add(-10*time.Second)), now.Add(-time.Minute)), true},
		{"running for half an hour since its last failure", pod(false, running(now.Add(-30*time.Minute)), now.Add(-31*time.Minute)), false},
		{"Ready again after a failure a minute ago", pod(true, running(now.Add(-10*time.Second)), now.Add(-time.Minute)), false},
	}
	for _, c := range cases {
		if got := crashing(c.pod, 1, now); got != c.want {
			t.Errorf("%s: want %v", c.name, c.want)
		}
	}
}

package workloads

import (
	"testing"
	"time"

	"github.com/moomora/klyx/internal/health"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var hnow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func hDeploy(name string, labels map[string]string) appsv1.Deployment {
	one := int32(1)
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: labels}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: 1, UpdatedReplicas: 1, Conditions: []appsv1.DeploymentCondition{
			{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable", LastUpdateTime: metav1.NewTime(hnow)},
		}},
	}
}

func hPod(name, rsOwner, hash string, labels map[string]string, cs corev1.ContainerStatus, ready corev1.ConditionStatus) corev1.Pod {
	l := map[string]string{appsv1.DefaultDeploymentUniqueLabelKey: hash}
	for k, v := range labels {
		l[k] = v
	}
	yes := true
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID(name), Labels: l, CreationTimestamp: metav1.NewTime(hnow.Add(-time.Hour)),
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rsOwner, Controller: &yes}}},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyAlways, Containers: []corev1.Container{{Name: cs.Name}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{cs},
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: ready, LastTransitionTime: metav1.NewTime(hnow.Add(-time.Hour))}}},
	}
}

func running(name string) corev1.ContainerStatus {
	return corev1.ContainerStatus{Name: name, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
}

// The seeded-cluster crash loop: the container sits in Terminated:Error
// between restarts. Both the pod row and its workload must call it a fault,
// from the same evaluator.
func TestHealthCrashLoopWindowIsAFaultOnPodAndWorkload(t *testing.T) {
	sel := map[string]string{"app": "worker"}
	d := hDeploy("worker", sel)
	d.Status.ReadyReplicas = 0
	crashed := corev1.ContainerStatus{Name: "worker", RestartCount: 6,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}}}
	pods := []corev1.Pod{hPod("worker-abc-1", "worker-abc", "abc", sel, crashed, corev1.ConditionFalse)}

	ps := SummarizePods(pods, hnow)
	if ps[0].Health.Kind != health.Fault || ps[0].Health.Label != "Exited 1" {
		t.Fatalf("pod: got %s %q, want fault \"Exited 1\"", ps[0].Health.Kind, ps[0].Health.Label)
	}
	ws := Assemble([]appsv1.Deployment{d}, nil, nil, pods, false, hnow)
	if ws[0].Health.Kind != health.Fault || ws[0].Health.Label != "Exited 1" {
		t.Fatalf("workload: got %s %q, want fault \"Exited 1\"", ws[0].Health.Kind, ws[0].Health.Label)
	}
}

// Overlapping selectors: a pod is attributed by owner reference only, so a
// broken pod never reddens a neighbour whose selector happens to match it.
func TestHealthAttributesPodsByOwnerNotSelector(t *testing.T) {
	sel := map[string]string{"tier": "web"}
	api, web := hDeploy("api", sel), hDeploy("web", sel)
	broken := hPod("api-111-x", "api-111", "111", sel, corev1.ContainerStatus{Name: "api",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}, corev1.ConditionFalse)
	healthy := hPod("web-222-y", "web-222", "222", sel, running("web"), corev1.ConditionTrue)
	api.Status.ReadyReplicas = 0

	ws := Assemble([]appsv1.Deployment{api, web}, nil, nil, []corev1.Pod{broken, healthy}, false, hnow)
	got := map[string]health.Kind{}
	for _, w := range ws {
		got[w.Name] = w.Health.Kind
	}
	if got["api"] != health.Fault || got["web"] != health.OK {
		t.Fatalf("attribution leaked across overlapping selectors: %v", got)
	}
	if ws[0].Name != "api" {
		t.Fatalf("faults must sort first, got %s first", ws[0].Name)
	}
}

func TestHealthTruncatedPodsNeverReadHealthy(t *testing.T) {
	sel := map[string]string{"app": "web"}
	d := hDeploy("web", sel)
	pods := []corev1.Pod{hPod("web-222-y", "web-222", "222", sel, running("web"), corev1.ConditionTrue)}
	if ws := AssembleObserved([]appsv1.Deployment{d}, nil, nil, pods, true, false, hnow); ws[0].Health.Kind != health.OK {
		t.Fatalf("complete evidence: got %s, want ok", ws[0].Health.Kind)
	}
	if ws := AssembleObserved([]appsv1.Deployment{d}, nil, nil, pods, false, false, hnow); ws[0].Health.Kind != health.Unknown {
		t.Fatalf("truncated evidence: got %s, want unknown", ws[0].Health.Kind)
	}
}

func TestHealthStatefulSetOwnershipIsByUID(t *testing.T) {
	two := int32(2)
	s := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "sts-uid", Generation: 1},
		Spec:       appsv1.StatefulSetSpec{Replicas: &two, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}},
		Status:     appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 1, UpdatedReplicas: 2},
	}
	yes := true
	stale := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "db-0", Namespace: "shop", Labels: map[string]string{"app": "db"},
		OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "db", UID: "old-uid", Controller: &yes}}},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}}
	ws := Assemble(nil, []appsv1.StatefulSet{s}, nil, []corev1.Pod{stale}, false, hnow)
	// The Failed pod belongs to a previous same-named StatefulSet: it must not
	// headline this one, which is simply one replica short.
	if ws[0].Health.Kind != health.Degraded || ws[0].Health.Label != "1/2 ready" {
		t.Fatalf("got %s %q, want degraded \"1/2 ready\"", ws[0].Health.Kind, ws[0].Health.Label)
	}
}

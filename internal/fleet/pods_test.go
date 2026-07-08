package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/workloads"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
)

func TestListPods(t *testing.T) {
	now := time.Unix(200_000, 0)

	crashPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "team",
			Name:              "api-crash",
			CreationTimestamp: metav1.NewTime(now.Add(-5 * time.Minute)),
		},
		Spec: corev1.PodSpec{
			NodeName:   "node-1",
			Containers: []corev1.Container{{Name: "api", Image: "api:1"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.1",
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "api",
					Ready:        false,
					RestartCount: 5,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				},
			},
		},
	}

	healthyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:         "team",
			Name:              "web-ok",
			CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Hour)),
		},
		Spec: corev1.PodSpec{
			NodeName:   "node-2",
			Containers: []corev1.Container{{Name: "web", Image: "nginx:1.25"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.2",
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "web",
					Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			},
		},
	}

	cs := fake.NewSimpleClientset(crashPod, healthyPod)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(now)}

	out, _, err := c.ListPods(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 pods, got %d", len(out))
	}
	// Triage sort: unhealthy first.
	if out[0].Rank != workloads.Unhealthy {
		t.Errorf("out[0].Rank: got %v, want Unhealthy", out[0].Rank)
	}
	if out[0].Name != "api-crash" {
		t.Errorf("out[0].Name: got %q, want api-crash", out[0].Name)
	}
	if out[0].Reason != "CrashLoopBackOff" {
		t.Errorf("out[0].Reason: got %q, want CrashLoopBackOff", out[0].Reason)
	}
	if out[0].Restarts != 5 {
		t.Errorf("out[0].Restarts: got %d, want 5", out[0].Restarts)
	}

	if out[1].Rank != workloads.Healthy {
		t.Errorf("out[1].Rank: got %v, want Healthy", out[1].Rank)
	}
	if out[1].Name != "web-ok" {
		t.Errorf("out[1].Name: got %q, want web-ok", out[1].Name)
	}
}

func TestListPods_NamespaceScope(t *testing.T) {
	now := time.Unix(200_000, 0)

	podA := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns-a", Name: "pod-a", CreationTimestamp: metav1.NewTime(now)},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img:1"}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "c", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	podB := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns-b", Name: "pod-b", CreationTimestamp: metav1.NewTime(now)},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "img:1"}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "c", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}

	cs := fake.NewSimpleClientset(podA, podB)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(now)}

	// Scoped to ns-a only.
	out, _, err := c.ListPods(context.Background(), "ns-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 pod in ns-a, got %d", len(out))
	}
	if out[0].Namespace != "ns-a" || out[0].Name != "pod-a" {
		t.Errorf("namespace scope failed: got %+v", out[0])
	}
}

func TestListPodsResolvesReplicaSetOwner(t *testing.T) {
	now := time.Unix(200_000, 0)
	truth := true

	rsPod := func(name, rsName string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "team", Name: name,
				CreationTimestamp: metav1.NewTime(now.Add(-time.Hour)),
				OwnerReferences: []metav1.OwnerReference{
					{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rsName, Controller: &truth},
				},
			},
			Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "i"}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	cs := fake.NewSimpleClientset(rsPod("web-7d4b9c6f9-abc", "web-7d4b9c6f9"), rsPod("bare-5c9d8-xyz", "bare-5c9d8"))

	mscheme := metadatafake.NewTestScheme()
	_ = metav1.AddMetaToScheme(mscheme)
	// "web-7d4b9c6f9" is Deployment-owned; "bare-5c9d8" has no controller.
	deployOwned := &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team", Name: "web-7d4b9c6f9",
			OwnerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Controller: &truth},
			},
		},
	}
	bare := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "bare-5c9d8"},
	}
	mclient := metadatafake.NewSimpleMetadataClient(mscheme, deployOwned, bare)

	c := &ClusterConn{typed: cs, meta: mclient, clk: clock.NewFake(now)}
	sums, _, err := c.ListPods(context.Background(), "team")
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	byName := map[string]workloads.PodSummary{}
	for _, s := range sums {
		byName[s.Name] = s
	}
	// Deployment-owned RS resolves to the Deployment - the UI restart verb must
	// never have to guess the name by stripping the pod-template-hash.
	if got := byName["web-7d4b9c6f9-abc"]; got.OwnerKind != "Deployment" || got.OwnerName != "web" {
		t.Fatalf("resolved owner: %s/%s", got.OwnerKind, got.OwnerName)
	}
	// A bare ReplicaSet keeps its own identity (nothing to restart).
	if got := byName["bare-5c9d8-xyz"]; got.OwnerKind != "ReplicaSet" || got.OwnerName != "bare-5c9d8" {
		t.Fatalf("bare RS owner: %s/%s", got.OwnerKind, got.OwnerName)
	}
}

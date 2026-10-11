package healthsrc

import (
	"context"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

type sink struct {
	mu   sync.Mutex
	last observe.Summary
	rows map[string]observe.Rows
}

func (s *sink) Summary(x observe.Summary) { s.mu.Lock(); s.last = x; s.mu.Unlock() }
func (s *sink) Rows(x observe.Rows) {
	s.mu.Lock()
	if s.rows == nil {
		s.rows = map[string]observe.Rows{}
	}
	s.rows[x.Task+"/"+x.Part.ID] = x
	s.mu.Unlock()
}

func (s *sink) part(task string) (observe.Part, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.last.Tasks {
		if t.ID == task && len(t.Parts) > 0 {
			return t.Parts[0], true
		}
	}
	return observe.Part{}, false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

// The health spec §3 acceptance fixture: namespace shop, each failure owned
// by a different Deployment - api ErrImagePull, batch Unschedulable on its
// new ReplicaSet, worker crash-looping; web healthy x3.
func shopFixture() ([]runtime.Object, []runtime.Object) {
	created := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	dep := func(name string, replicas, ready int32) *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("dep-" + name), Generation: 1, CreationTimestamp: created},
			Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: "shop/" + name}}}}},
			Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: replicas, ReadyReplicas: ready, AvailableReplicas: ready, UpdatedReplicas: replicas,
				Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable"}}},
		}
	}
	pod := func(name, app string, status corev1.PodStatus) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("pod-" + name), Labels: map[string]string{"app": app}, CreationTimestamp: created,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: app + "-rs", UID: types.UID("rs-" + app), Controller: ptr(true)}}},
			Spec:   corev1.PodSpec{NodeName: "node-1", RestartPolicy: corev1.RestartPolicyAlways, Containers: []corev1.Container{{Name: app, Image: "shop/" + app}}},
			Status: status,
		}
	}
	running := corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &created,
		Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: created}},
		ContainerStatuses: []corev1.ContainerStatus{{Name: "web", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
	waiting := func(app, reason string) corev1.PodStatus {
		return corev1.PodStatus{Phase: corev1.PodPending, StartTime: &created,
			ContainerStatuses: []corev1.ContainerStatus{{Name: app, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}}}}
	}
	crash := waiting("worker", "CrashLoopBackOff")
	crash.Phase = corev1.PodRunning
	typed := []runtime.Object{
		dep("web", 3, 3), dep("api", 1, 0), dep("batch", 1, 0), dep("worker", 1, 0),
		pod("web-1", "web", running), pod("web-2", "web", running), pod("web-3", "web", running),
		pod("api-1", "api", waiting("api", "ErrImagePull")),
		pod("batch-1", "batch", corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable"}}}),
		pod("worker-1", "worker", crash),
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: "node-1"}, Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
	}
	meta := []runtime.Object{}
	for _, app := range []string{"web", "api", "batch", "worker"} {
		meta = append(meta, &metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"},
			ObjectMeta: metav1.ObjectMeta{Name: app + "-rs", Namespace: "shop", UID: types.UID("rs-" + app),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: app, UID: types.UID("dep-" + app), Controller: ptr(true)}}}})
	}
	for _, ns := range []string{"shop", "kube-system"} {
		meta = append(meta, &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: ns, UID: types.UID(ns)}})
	}
	return typed, meta
}

func TestCoreTasksOnTheShopFixture(t *testing.T) {
	typedObjs, metaObjs := shopFixture()
	typed := fake.NewClientset(typedObjs...)
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	meta := metafake.NewSimpleMetadataClient(scheme, metaObjs...)

	kinds, domains := Core(CoreDeps{Cluster: "kind-klyx", Typed: typed, Meta: meta})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "kind-klyx", Sink: s, Kinds: kinds, Domains: domains, NamespacesKind: KindNamespaces, Throttle: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-o.Done() }()
	o.Start(ctx)
	o.SubscribeRows(TaskWorkloads, "workloads", observe.Owner{Kind: "lens", ID: "workloads"})

	waitFor(t, "pods: 3 fault, the worker pod never reads Running", func() bool {
		p, ok := s.part(TaskPods)
		return ok && p.Coverage == observe.Live && p.Counts.Total == 6 && p.Counts.Fault == 3 && p.Counts.OK == 3
	})
	waitFor(t, "workloads: api, worker, batch fault - not double counted with pods", func() bool {
		p, ok := s.part(TaskWorkloads)
		return ok && p.Coverage == observe.Live && p.Counts.Total == 4 && p.Counts.Fault == 3 && p.Counts.OK == 1
	})
	waitFor(t, "nodes: one ok node, nothing lit", func() bool {
		p, ok := s.part(TaskNodes)
		return ok && p.Counts.Total == 1 && p.Counts.OK == 1
	})
	waitFor(t, "namespaces counted from the metadata cache", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.last.Namespaces != nil && *s.last.Namespaces == 2
	})
	p, _ := s.part(TaskWorkloads)
	if len(p.Top) != 3 || p.Top[0].Route != TaskWorkloads || p.Top[0].Role != "fault" {
		t.Fatalf("top refs name the three faulting workloads: %+v", p.Top)
	}
	waitFor(t, "workload rows travel with their counts", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		r, ok := s.rows[TaskWorkloads+"/workloads"]
		return ok && len(r.Rows.([]workloads.Workload)) == r.Part.Counts.Total
	})

	// A live change flows through the watch: the crash-looping pod recovers.
	recovered, _ := typed.CoreV1().Pods("shop").Get(ctx, "worker-1", metav1.GetOptions{})
	recovered.Status = corev1.PodStatus{Phase: corev1.PodRunning, StartTime: recovered.Status.StartTime,
		Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}},
		ContainerStatuses: []corev1.ContainerStatus{{Name: "worker", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
	if _, err := typed.CoreV1().Pods("shop").UpdateStatus(ctx, recovered, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the recovered pod leaves the fault count", func() bool {
		p, ok := s.part(TaskPods)
		return ok && p.Counts.Fault == 2
	})
}

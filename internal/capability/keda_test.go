package capability

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

var kedaServed = map[string]bool{kedaGroup: true}

func yes() *bool { b := true; return &b }

// an operator Deployment, its ReplicaSet, and ready pods running images
func kedaOperator(ns string, ready bool, images ...string) []runtime.Object {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "keda-operator", UID: types.UID(ns + "-dep"), Labels: map[string]string{"app": "keda-operator"}},
		Spec: appsv1.DeploymentSpec{Replicas: ptrInt32(int32(len(images)))}}
	if ready {
		dep.Status.AvailableReplicas = int32(len(images))
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "keda-operator-1", UID: types.UID(ns + "-rs"),
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "keda-operator", UID: dep.UID, Controller: yes()}}}}
	objs := []runtime.Object{dep, rs}
	for i, img := range images {
		objs = append(objs, runningPod(ns, "keda-operator-"+string(rune('a'+i)), types.UID(ns+"-p"+string(rune('a'+i))), rs.UID, img))
	}
	return objs
}

func runningPod(ns, name string, uid, owner types.UID, image string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: uid,
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs", UID: owner, Controller: yes()}}},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: image}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
}

func ptrInt32(n int32) *int32 { return &n }

const apiSvc = `{"metadata":{"labels":{}},"spec":{"service":{"namespace":"keda","name":"keda-metrics-apiserver"}},"status":{"conditions":[{"type":"Available","status":"True","reason":"Passed"}]}}`

func detectK(raw func(context.Context, string) ([]byte, error), objs ...runtime.Object) KEDACapability {
	d := &Detector{cs: fake.NewSimpleClientset(objs...), rawGet: raw}
	return d.detectKEDA(context.Background(), kedaServed)
}

func serve(body string) func(context.Context, string) ([]byte, error) {
	return func(context.Context, string) ([]byte, error) { return []byte(body), nil }
}

func backing(image string) []runtime.Object {
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "keda", Name: "s", Labels: map[string]string{"kubernetes.io/service-name": "keda-metrics-apiserver"}},
		Endpoints: []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: yes()}, TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "metrics", UID: "m-1"}}}}
	return []runtime.Object{slice, runningPod("keda", "metrics", "m-1", "x", image)}
}

// Coverage S9 §2: the operators and their running version, the APIService
// owner proven from the backing pods' images, never from a namespace.
func TestDetectKEDA(t *testing.T) {
	if k := (&Detector{cs: fake.NewSimpleClientset()}).detectKEDA(context.Background(), map[string]bool{}); k.Present || k.Tier != Absent {
		t.Fatalf("absent: %+v", k)
	}
	objs := append(kedaOperator("keda", true, "ghcr.io/kedacore/keda:2.21.0"), backing("ghcr.io/kedacore/keda-metrics-apiserver:2.21.0")...)
	k := detectK(serve(apiSvc), objs...)
	if !k.Healthy || !k.Verified || k.Version().String() != "2.21.0" || k.APIService.Owner != "keda" || k.APIService.Available != "True" {
		t.Fatalf("healthy: %+v", k)
	}
	// a rollout: the running pods disagree, so the version is unknown
	if k := detectK(serve(apiSvc), append(kedaOperator("keda", true, "ghcr.io/kedacore/keda:2.21.0", "ghcr.io/kedacore/keda:2.20.2"), backing("ghcr.io/kedacore/keda-metrics-apiserver:2.21.0")...)...); k.Version().Known {
		t.Fatalf("disagreeing pods: %+v", k.Operators)
	}
	// two operators on different versions: unknown
	two := append(kedaOperator("keda-a", true, "ghcr.io/kedacore/keda:2.21.0"), kedaOperator("keda-b", true, "ghcr.io/kedacore/keda:2.18.0")...)
	if k := detectK(serve(apiSvc), two...); k.Version().Known || len(k.Operators) != 2 {
		t.Fatalf("two operators: %+v", k.Operators)
	}
	// an unrelated adapter in the operator's namespace: foreign, a fault
	k = detectK(serve(apiSvc), append(kedaOperator("keda", true, "ghcr.io/kedacore/keda:2.21.0"), backing("registry.example/prometheus-adapter:0.12")...)...)
	if k.APIService.Owner != "foreign" || k.Healthy || !k.Verified {
		t.Fatalf("foreign adapter: %+v", k.APIService)
	}
	// an unreadable backing chain: unverified, a stated limit, not a fault
	k = detectK(serve(apiSvc), kedaOperator("keda", true, "ghcr.io/kedacore/keda:2.21.0")...)
	if k.APIService.Owner != "unverified" || !k.Healthy {
		t.Fatalf("unverified: %+v", k.APIService)
	}
	// absent: a fault, qualified to external triggers
	gone := func(context.Context, string) ([]byte, error) {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: "apiregistration.k8s.io", Resource: "apiservices"}, kedaAPIService)
	}
	if k := detectK(gone, kedaOperator("keda", true, "ghcr.io/kedacore/keda:2.21.0")...); k.Healthy || !k.APIService.Read || k.APIService.Present {
		t.Fatalf("absent APIService: %+v", k)
	}
	// unreadable: unknown, never absent
	broken := func(context.Context, string) ([]byte, error) { return nil, errors.New("forbidden") }
	if k := detectK(broken, kedaOperator("keda", true, "ghcr.io/kedacore/keda:2.21.0")...); !k.Healthy || k.APIService.Read {
		t.Fatalf("unreadable APIService: %+v", k.APIService)
	}
	// unavailable
	unavail := `{"spec":{"service":{"namespace":"keda","name":"keda-metrics-apiserver"}},"status":{"conditions":[{"type":"Available","status":"False","reason":"MissingEndpoints"}]}}`
	if k := detectK(serve(unavail), kedaOperator("keda", true, "ghcr.io/kedacore/keda:2.21.0")...); k.Healthy {
		t.Fatalf("unavailable: %+v", k)
	}
	// no operator: unknown, never healthy
	if k := detectK(serve(apiSvc)); k.Healthy || k.Verified || k.Reason != kedaNoOperator {
		t.Fatalf("no operator: %+v", k)
	}
	// an operator not ready
	if k := detectK(serve(apiSvc), append(kedaOperator("keda", false, "ghcr.io/kedacore/keda:2.21.0"), backing("ghcr.io/kedacore/keda-metrics-apiserver:2.21.0")...)...); k.Healthy || !k.Verified {
		t.Fatalf("not ready: %+v", k)
	}
}

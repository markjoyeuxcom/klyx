package fleet

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/identity"
	"github.com/moomora/klyx/internal/observe"
)

func saObj(uid string, ann map[string]string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web", UID: "sa-" + metav1Type(uid), Annotations: ann}}
}

func metav1Type(s string) types.UID { return types.UID(s) }

// Coverage S5 §6: the ServiceAccount read is UID-fenced; its pods are
// listed by field selector and capped; a continuation is "more, count
// unknown"; a forbidden list is unreadable, never "no pods".
func TestS5ServiceAccountIdentityReads(t *testing.T) {
	cs := fake.NewSimpleClientset(saObj("1", map[string]string{"eks.amazonaws.com/role-arn": "arn:aws:iam::123456789012:role/a"}))
	var opts metav1.ListOptions
	cs.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		opts = a.(k8stesting.ListActionImpl).ListOptions
		return true, &corev1.PodList{ListMeta: metav1.ListMeta{Continue: "next"}, Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "p1", UID: "p1"},
			Spec: corev1.PodSpec{ServiceAccountName: "web", Containers: []corev1.Container{{Name: "app"}}}}}}, nil
	})
	c := &ClusterConn{typed: cs}
	r, err := c.ServiceAccountIdentity(context.Background(), "app", "web", "sa-1")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != identity.StateRead || opts.FieldSelector != "spec.serviceAccountName=web" || opts.Limit != maxIdentityPods {
		t.Fatalf("the read: %+v %+v", r.State, opts)
	}
	if r.PodsRead != identity.ReadMore || len(r.Pods) != 1 || r.Pods[0].Readings[0].State != identity.StateNotConfig {
		t.Fatalf("more, count unknown; the pod compared: %+v", r)
	}
	if r2, _ := c.ServiceAccountIdentity(context.Background(), "app", "web", "sa-other"); r2.State != identity.StateReplaced {
		t.Fatalf("UID fence: %+v", r2.State)
	}
	cs2 := fake.NewSimpleClientset(saObj("1", nil))
	cs2.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", nil)
	})
	r3, _ := (&ClusterConn{typed: cs2}).ServiceAccountIdentity(context.Background(), "app", "web", "")
	if r3.PodsRead != identity.StateUnreadable {
		t.Fatalf("forbidden pods: %+v", r3.PodsRead)
	}
	cs3 := fake.NewSimpleClientset()
	cs3.PrependReactor("get", "serviceaccounts", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts"}, "web", nil)
	})
	if r4, _ := (&ClusterConn{typed: cs3}).ServiceAccountIdentity(context.Background(), "app", "web", ""); r4.State != identity.StateUnreadable || r4.Declared != nil {
		t.Fatalf("a forbidden ServiceAccount is unreadable, never 'no identity': %+v", r4)
	}
}

func TestS5PodIdentityRead(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "p1", UID: "p1"}, Spec: corev1.PodSpec{ServiceAccountName: "gone", Containers: []corev1.Container{{Name: "app"}}}}
	c := &ClusterConn{typed: fake.NewSimpleClientset(pod)}
	r, _ := c.PodIdentity(context.Background(), "app", "p1", "p1")
	if r.State != identity.StateRead || r.SAState != identity.StateNotFound || r.Report == nil || r.Report.Readings != nil {
		t.Fatalf("a missing ServiceAccount: no comparison: %+v", r)
	}
	if r2, _ := c.PodIdentity(context.Background(), "app", "p1", "other"); r2.State != identity.StateReplaced {
		t.Fatalf("UID fence: %+v", r2.State)
	}
}

type fakeIdentityObserver struct {
	models map[string][]any
	live   map[string]bool
}

func (f fakeIdentityObserver) VisitKind(kind string, visit func(any) bool) (observe.KindState, bool, bool) {
	for _, o := range f.models[kind] {
		visit(o)
	}
	cov := observe.Checking
	if f.live[kind] {
		cov = observe.Live
	}
	return observe.KindState{Coverage: cov}, true, true
}

// Coverage S5 §4.2: candidates by the chart's label and the conventional
// name; pods fenced to their candidate through the ReplicaSet owner's UID;
// each pod's own class; a continued pod list certifies nothing.
func TestS5ESOCandidates(t *testing.T) {
	ctl := true
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "external-secrets", Name: "external-secrets", UID: "dep",
		Labels: map[string]string{"app.kubernetes.io/name": "external-secrets"}},
		Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "external-secrets"}}}}
	pod := func(name, rs, sa, class string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "external-secrets", Name: name, UID: "u-" + metav1Type(name),
			Labels:          map[string]string{"app.kubernetes.io/name": "external-secrets"},
			OwnerReferences: []metav1.OwnerReference{{UID: "rs-" + typesUID(rs), Controller: &ctl}}},
			Spec: corev1.PodSpec{ServiceAccountName: sa, Containers: []corev1.Container{{Name: "external-secrets", Args: []string{"--controller-class=" + class}}}}}
	}
	cs := fake.NewSimpleClientset(dep, pod("old", "a", "sa-a", "a"), pod("new", "b", "sa-b", "b"), pod("stray", "x", "sa-x", "b"))
	rs := func(uid, owner string) *metav1.PartialObjectMetadata {
		return &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{UID: "rs-" + typesUID(uid), OwnerReferences: []metav1.OwnerReference{{UID: "dep", Controller: &ctl}}}}
	}
	obs := fakeIdentityObserver{models: map[string][]any{healthsrc.KindReplicaSets: {rs("a", "dep"), rs("b", "dep")}}, live: map[string]bool{healthsrc.KindReplicaSets: true}}
	c := &ClusterConn{typed: cs}
	cands, complete := c.esoCandidates(context.Background(), obs)
	if !complete || len(cands) != 1 || !cands[0].PodsComplete || len(cands[0].Pods) != 3 {
		t.Fatalf("candidates: %v %+v", complete, cands)
	}
	h := identity.HandlersOf("b", cands, complete)
	if len(h.Groups) != 1 || h.Groups[0].ServiceAccount != "sa-b" || len(h.Groups[0].Pods) != 1 {
		t.Fatalf("the class-B pod only, the stray pod not owned: %+v", h)
	}
	obs.live[healthsrc.KindReplicaSets] = false
	cands, complete = c.esoCandidates(context.Background(), obs)
	if h := identity.HandlersOf("b", cands, complete); h.Unresolved == "" {
		t.Fatalf("an unread ReplicaSets model fences nothing, so names no handler: %+v", h)
	}
}

func typesUID(s string) types.UID { return types.UID(s) }

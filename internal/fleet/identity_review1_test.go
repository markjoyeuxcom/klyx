package fleet

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/identity"
	"github.com/moomora/klyx/internal/observe"
)

// Implementation review 1 (S5): the fleet side's regressions.

type staleIdentityObserver struct{ fakeIdentityObserver }

func (h staleIdentityObserver) VisitKind(k string, f func(any) bool) (observe.KindState, bool, bool) {
	st, complete, ok := h.fakeIdentityObserver.VisitKind(k, f)
	st.Coverage = observe.Stale
	return st, complete, ok
}

func noAuthStore(name, class string) *unstructured.Unstructured {
	spec := map[string]any{"provider": map[string]any{"aws": map[string]any{"service": "SecretsManager"}}}
	if class != "" {
		spec["controller"] = class
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "external-secrets.io/v1", "kind": "SecretStore",
		"metadata": map[string]any{"namespace": "app", "name": name, "uid": "u-" + name}, "spec": spec}}
}

// #3: a stale store read yields no store, so no old auth configuration
// becomes a current subject; the inverse join says a controller join it
// could not resolve, naming the store, even when the inventory is complete.
func TestReview1ESOCoverageCarried(t *testing.T) {
	s := noAuthStore("s", "")
	stale := staleIdentityObserver{fakeIdentityObserver{models: map[string][]any{healthsrc.KindSecretStores: {s}}, live: map[string]bool{}}}
	if stores, read := esoStores(stale); read || len(stores) != 0 {
		t.Fatalf("a stale read yields no store: %v %d", read, len(stores))
	}
	c := &ClusterConn{typed: fake.NewSimpleClientset()}
	if links, state, gap := c.storesFor(context.Background(), stale, "app", "web"); state != identity.ReadNone || len(links) != 0 || gap == "" {
		t.Fatalf("stale stores: unread, said: %s %q", state, gap)
	}
	live := fakeIdentityObserver{models: map[string][]any{healthsrc.KindSecretStores: {s}},
		live: map[string]bool{healthsrc.KindSecretStores: true, healthsrc.KindClusterSecretStores: true}}
	_, state, gap := c.storesFor(context.Background(), live, "app", "web")
	if state != identity.ReadPartial || !strings.Contains(gap, "SecretStore app/s") {
		t.Fatalf("an unresolved controller join is partial and names the store: %s %q", state, gap)
	}
}

// #7: a departed controller pod (Succeeded) names no handler; its phase is
// carried from the pod read.
func TestReview1DepartedControllerIsNotHandler(t *testing.T) {
	ctl := true
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "external-secrets", Name: "external-secrets", UID: "dep", Labels: map[string]string{"app.kubernetes.io/name": "external-secrets"}},
		Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "external-secrets"}}}}
	p := func(name, sa string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "external-secrets", Name: name, UID: typesUID(name), Labels: map[string]string{"app.kubernetes.io/name": "external-secrets"},
			OwnerReferences: []metav1.OwnerReference{{UID: "rs", Controller: &ctl}}}, Spec: corev1.PodSpec{ServiceAccountName: sa, Containers: []corev1.Container{{Name: "external-secrets"}}},
			Status: corev1.PodStatus{Phase: phase}}
	}
	rs := &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{UID: "rs", OwnerReferences: []metav1.OwnerReference{{UID: "dep", Controller: &ctl}}}}
	h := fakeIdentityObserver{models: map[string][]any{healthsrc.KindReplicaSets: {rs}}, live: map[string]bool{healthsrc.KindReplicaSets: true}}
	c := &ClusterConn{typed: fake.NewSimpleClientset(dep, p("old", "old-sa", corev1.PodSucceeded), p("cur", "cur-sa", corev1.PodRunning))}
	cands, complete := c.esoCandidates(context.Background(), h)
	hs := identity.HandlersOf("", cands, complete)
	if len(hs.Groups) != 1 || hs.Groups[0].ServiceAccount != "cur-sa" {
		t.Fatalf("only the running pod handles: %+v", hs)
	}
}

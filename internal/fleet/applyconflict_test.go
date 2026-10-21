package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// v0.15.1: an apply conflict names each field's manager, as the server's
// causes give it, and its advice follows the kind of manager.

func applyConflictStatus(causes ...metav1.StatusCause) error {
	return apierrors.NewApplyConflict(causes, "Apply failed with conflicts")
}

func cause(manager, field string) metav1.StatusCause {
	return metav1.StatusCause{Type: metav1.CauseTypeFieldManagerConflict, Message: `conflict with "` + manager + `" using v1`, Field: field}
}

func owned(kind, apiVersion, name string) []interface{} {
	return []interface{}{map[string]interface{}{"apiVersion": apiVersion, "kind": kind, "name": name, "uid": "u-" + name, "controller": true}}
}

func conflictConn(t *testing.T, live *unstructured.Unstructured, extra []runtime.Object, refuse error) *ClusterConn {
	t.Helper()
	kinds := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}: "PodList", {Version: "v1", Resource: "configmaps"}: "ConfigMapList",
		{Group: "apps", Version: "v1", Resource: "replicasets"}: "ReplicaSetList", {Group: "batch", Version: "v1", Resource: "jobs"}: "JobList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), kinds, append([]runtime.Object{live}, extra...)...)
	dyn.PrependReactor("patch", "*", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, refuse })
	return NewClusterConn("x", nil, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
}

func TestAPodFieldKubeControllerManagerSetsNamesItsOwners(t *testing.T) {
	pod := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]interface{}{"namespace": "monitoring", "name": "grafana-98d-x", "uid": "u1", "ownerReferences": owned("ReplicaSet", "apps/v1", "grafana-98d")}}}
	rs := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]interface{}{"namespace": "monitoring", "name": "grafana-98d", "uid": "u-grafana-98d", "ownerReferences": owned("Deployment", "apps/v1", "grafana")}}}
	c := conflictConn(t, pod, []runtime.Object{rs}, applyConflictStatus(cause("kube-controller-manager", `.spec.containers[name="grafana"].readinessProbe.failureThreshold`)))
	err := c.ApplyResourceYAML(context.Background(), "", "v1", "pods", "Pod", "Namespaced", "monitoring", "grafana-98d-x", "u1",
		"apiVersion: v1\nkind: Pod\nmetadata:\n  namespace: monitoring\n  name: grafana-98d-x\nspec: {}\n", false)
	var ce *ApplyConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("not an apply conflict: %v", err)
	}
	want := `apply conflict: kube-controller-manager sets .spec.containers[name="grafana"].readinessProbe.failureThreshold, as the Kubernetes controller that maintains this Pod; its controller is ReplicaSet monitoring/grafana-98d, owned by Deployment monitoring/grafana: change the Deployment instead. A Pod's spec cannot change after it is created, except its containers' images, activeDeadlineSeconds and added tolerations. Klyx never forces another manager's fields.`
	if err.Error() != want {
		t.Fatalf("got  %s\nwant %s", err, want)
	}
	if strings.Contains(err.Error(), "Flux") || strings.Contains(err.Error(), "reconcile") {
		t.Fatal("a Kubernetes controller's field is never Flux's")
	}
	if top := ce.Owner.Top(); top.Kind != "Deployment" || top.Group != "apps" || top.Name != "grafana" {
		t.Fatalf("the owner to change: %+v", top)
	}
	// the request stays in the chain for the bridge
	_ = requestOf(t, err)
}

func TestAConflictsAdviceFollowsItsManager(t *testing.T) {
	cm := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"namespace": "default", "name": "app", "uid": "u1"}}}
	c := conflictConn(t, cm, nil, applyConflictStatus(cause("helm-controller", ".data.a"), cause("helm-controller", ".data.b"), cause("kubectl-edit", ".data.c")))
	err := c.ApplyResourceYAML(context.Background(), "", "v1", "configmaps", "ConfigMap", "Namespaced", "default", "app", "u1",
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  namespace: default\n  name: app\ndata:\n  a: x\n", false)
	want := `apply conflict: Flux's helm-controller owns .data.a and 1 more: change them in Git - a forced apply would be reverted on the next reconcile; .data.c was last set by "kubectl-edit": change it with the tool that set it. Klyx never forces another manager's fields.`
	if err == nil || err.Error() != want {
		t.Fatalf("got  %v\nwant %s", err, want)
	}
	// a Kubernetes controller's field on an object without a controller
	c = conflictConn(t, cm, nil, applyConflictStatus(cause("kube-controller-manager", ".data.a")))
	err = c.ApplyResourceYAML(context.Background(), "", "v1", "configmaps", "ConfigMap", "Namespaced", "default", "app", "u1",
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  namespace: default\n  name: app\ndata:\n  a: x\n", false)
	if err == nil || !strings.Contains(err.Error(), "change what that controller follows instead") || strings.Contains(err.Error(), "Pod's spec") {
		t.Fatalf("%v", err)
	}
	// causes that cannot be read keep the server's own text, its manager named
	c = conflictConn(t, cm, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "app", errors.New(`Apply failed with 1 conflict: conflict with "argocd-controller": .data.a`)))
	err = c.ApplyResourceYAML(context.Background(), "", "v1", "configmaps", "ConfigMap", "Namespaced", "default", "app", "u1",
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  namespace: default\n  name: app\ndata:\n  a: x\n", false)
	var ce *ApplyConflictError
	if err == nil || errors.As(err, &ce) || !strings.Contains(err.Error(), "argocd-controller") || strings.Contains(err.Error(), "e.g. Flux/Argo") {
		t.Fatalf("%v", err)
	}
}

func TestConflictFieldsReadEveryManagerForm(t *testing.T) {
	err := applyConflictStatus(
		metav1.StatusCause{Type: metav1.CauseTypeFieldManagerConflict, Message: `conflict with "kubelet" with subresource "status" using v1 at 2026-10-21T10:00:00Z`, Field: ".status.x"},
		metav1.StatusCause{Type: metav1.CauseTypeFieldManagerConflict, Message: `conflict with "a \"quoted\" manager"`, Field: ".spec.y"},
		metav1.StatusCause{Type: metav1.CauseTypeFieldValueInvalid, Message: `conflict with "not-a-conflict"`, Field: ".spec.z"},
		metav1.StatusCause{Type: metav1.CauseTypeFieldManagerConflict, Message: `something else`, Field: ".spec.w"},
	)
	got := conflictFields(err)
	if len(got) != 2 || got[0] != (ConflictField{Manager: "kubelet", Class: ManagerKubernetes, Path: ".status.x"}) || got[1].Manager != `a "quoted" manager` || got[1].Class != ManagerOther {
		t.Fatalf("%+v", got)
	}
}

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
	"k8s.io/apimachinery/pkg/types"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

// adhocConn builds a conn whose discovery serves core/v1 Pod+ConfigMap+Secret
// and rbac ClusterRole, with a fake dynamic client to receive the applies.
func adhocConn(t *testing.T) (*ClusterConn, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	typed := k8sfake.NewSimpleClientset()
	crud := []string{"create", "get", "list", "watch", "update", "patch", "delete"}
	typed.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: crud},
			{Name: "pods/log", Kind: "Pod", Namespaced: true, Verbs: []string{"get"}}, // subresource: must be skipped
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: crud},
			{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: crud},
			{Name: "bindings", Kind: "Binding", Namespaced: true, Verbs: []string{"create"}}, // no list/delete: Scratch could not clean it
		}},
		{GroupVersion: "rbac.authorization.k8s.io/v1", APIResources: []metav1.APIResource{
			{Name: "clusterroles", Kind: "ClusterRole", Namespaced: false, Verbs: crud},
		}},
	}
	// Every GVR the scratch sweep lists must be registered with the fake.
	listKinds := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "pods"}:                                  "PodList",
		{Group: "", Version: "v1", Resource: "configmaps"}:                            "ConfigMapList",
		{Group: "", Version: "v1", Resource: "secrets"}:                               "SecretList",
		{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}: "ClusterRoleList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds)
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	c.caps = capability.Set{} // no GitOps tools: reach checks are clean no-ops
	return c, dyn
}

const adhocPodYAML = `apiVersion: v1
kind: Pod
metadata:
  name: dbg
  namespace: scratch
spec:
  containers:
  - name: sh
    image: busybox
`

func TestCreateResourceYAML_StampsAndCreates(t *testing.T) {
	c, dyn := adhocConn(t)

	res := c.CreateResourceYAML(context.Background(), adhocPodYAML, false)
	if len(res) != 1 || !res[0].OK || res[0].Error != "" {
		t.Fatalf("want one OK result, got %+v", res)
	}
	if res[0].Kind != "Pod" || res[0].Namespace != "scratch" || res[0].Name != "dbg" {
		t.Fatalf("identity wrong: %+v", res[0])
	}
	// The created object carries the ad-hoc stamp.
	u, err := dyn.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}).Namespace("scratch").Get(context.Background(), "dbg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("created pod missing: %v", err)
	}
	if u.GetLabels()[AdhocLabel] != "true" {
		t.Fatalf("adhoc label missing: %v", u.GetLabels())
	}
	if u.GetAnnotations()["klyx.dev/created-by"] != "klyx" || u.GetAnnotations()["klyx.dev/created-at"] == "" {
		t.Fatalf("provenance annotations missing: %v", u.GetAnnotations())
	}

	// A second create of the same name errors honestly - this surface never
	// touches an existing object.
	res2 := c.CreateResourceYAML(context.Background(), adhocPodYAML, false)
	if len(res2) != 1 || res2[0].OK || !strings.Contains(res2[0].Error, "already exists") {
		t.Fatalf("name collision must refuse with 'already exists': %+v", res2)
	}
}

func TestCreateResourceYAML_MultiDocPartialFailure(t *testing.T) {
	c, _ := adhocConn(t)
	multi := adhocPodYAML + "---\n" + `apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
` // missing namespace -> per-doc error; the pod must still succeed
	res := c.CreateResourceYAML(context.Background(), multi, false)
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %+v", res)
	}
	if !res[0].OK {
		t.Fatalf("pod doc should succeed: %+v", res[0])
	}
	if res[1].OK || !strings.Contains(res[1].Error, "metadata.namespace explicitly") {
		t.Fatalf("namespace-less doc must fail honestly: %+v", res[1])
	}
}

func TestCreateResourceYAML_RefusesSecrets(t *testing.T) {
	c, _ := adhocConn(t)
	res := c.CreateResourceYAML(context.Background(), `apiVersion: v1
kind: Secret
metadata:
  name: creds
  namespace: scratch
stringData:
  token: hunter2
`, true)
	if len(res) != 1 || res[0].OK || !strings.Contains(res[0].Error, "ESO/SOPS") {
		t.Fatalf("ad-hoc Secrets must be refused with the ESO pointer: %+v", res)
	}
}

func TestCreateResourceYAML_ClusterScopedAndUnknownKind(t *testing.T) {
	c, _ := adhocConn(t)
	// Cluster-scoped works without a namespace...
	res := c.CreateResourceYAML(context.Background(), `apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: dbg-viewer
`, false)
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("cluster-scoped create failed: %+v", res)
	}
	// ...an unserved kind names the likely causes.
	res = c.CreateResourceYAML(context.Background(), `apiVersion: made.up/v1
kind: Widget
metadata:
  name: w
  namespace: scratch
`, false)
	if len(res) != 1 || res[0].OK || !strings.Contains(res[0].Error, "not served") {
		t.Fatalf("unknown apiVersion must fail honestly: %+v", res)
	}
}

func TestLaunchDiagnosticPod_CreatesNamespaceAndTTLBoundedPod(t *testing.T) {
	c, _ := adhocConn(t)

	r := c.LaunchDiagnosticPod(context.Background(), "dbg", "nicolaka/netshoot", 1800)
	if !r.OK || r.Error != "" {
		t.Fatalf("launch failed: %+v", r)
	}
	// Namespace created + stamped.
	ns, err := c.typed.CoreV1().Namespaces().Get(context.Background(), ScratchNamespace, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("scratch namespace missing: %v", err)
	}
	if ns.Labels[AdhocLabel] != "true" {
		t.Fatalf("namespace must carry the adhoc stamp: %v", ns.Labels)
	}
	// Pod TTL-bounded by the CLUSTER (activeDeadlineSeconds), never restarting.
	pod, err := c.typed.CoreV1().Pods(ScratchNamespace).Get(context.Background(), "dbg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod missing: %v", err)
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 1800 {
		t.Fatalf("activeDeadlineSeconds must bound the pod: %+v", pod.Spec.ActiveDeadlineSeconds)
	}
	if pod.Spec.RestartPolicy != "Never" || pod.Labels[AdhocLabel] != "true" {
		t.Fatalf("pod must be Never-restart + adhoc-stamped: %+v", pod.Spec.RestartPolicy)
	}

	// Relaunching the same name refuses honestly (namespace already existing is fine).
	r2 := c.LaunchDiagnosticPod(context.Background(), "dbg", "busybox", 60)
	if r2.OK || !strings.Contains(r2.Error, "already exists") {
		t.Fatalf("duplicate launch must refuse: %+v", r2)
	}
}

func TestListAndDeleteAdhocObjects(t *testing.T) {
	c, dyn := adhocConn(t)
	// One adhoc pod (via the launcher path uses typed; seed dynamic for the list)
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	adhocPod := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]interface{}{
			"name": "dbg", "namespace": ScratchNamespace, "uid": "uid-dbg",
			"labels": map[string]interface{}{AdhocLabel: "true"},
		},
	}}
	plainPod := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]interface{}{"name": "app", "namespace": "prod", "uid": "uid-app"},
	}}
	for _, p := range []*unstructured.Unstructured{adhocPod, plainPod} {
		if _, err := dyn.Resource(podGVR).Namespace(p.GetNamespace()).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	inv, err := c.ListAdhocObjects(context.Background())
	if err != nil || len(inv.Objects) != 1 || inv.Objects[0].Name != "dbg" || inv.Objects[0].Kind != "Pod" || inv.Objects[0].UID != "uid-dbg" || len(inv.Unread) != 0 {
		t.Fatalf("want exactly the adhoc pod, got %+v %v", inv, err)
	}

	// The label guard refuses to delete a non-adhoc object...
	if err := c.DeleteAdhocObject(context.Background(), "", "v1", "pods", "prod", "app", "uid-app"); err == nil || !strings.Contains(err.Error(), "not a Klyx ad-hoc object") {
		t.Fatalf("non-adhoc delete must refuse: %v", err)
	}
	// ...refuses without the listed UID, or when the object was replaced...
	if err := c.DeleteAdhocObject(context.Background(), "", "v1", "pods", ScratchNamespace, "dbg", ""); err == nil {
		t.Fatal("a delete without the listed UID must refuse")
	}
	if err := c.DeleteAdhocObject(context.Background(), "", "v1", "pods", ScratchNamespace, "dbg", "uid-older"); err == nil || !strings.Contains(err.Error(), "was replaced") {
		t.Fatalf("a replaced object must not be deleted: %v", err)
	}
	// ...and deletes the stamped one it listed.
	if err := c.DeleteAdhocObject(context.Background(), "", "v1", "pods", ScratchNamespace, "dbg", "uid-dbg"); err != nil {
		t.Fatalf("adhoc delete failed: %v", err)
	}
	if _, err := dyn.Resource(podGVR).Namespace(ScratchNamespace).Get(context.Background(), "dbg", metav1.GetOptions{}); err == nil {
		t.Fatal("adhoc pod should be gone")
	}
	if _, err := dyn.Resource(podGVR).Namespace("prod").Get(context.Background(), "app", metav1.GetOptions{}); err != nil {
		t.Fatal("plain pod must be untouched")
	}
}

func TestCreateResourceYAML_DryRunDoesNotPersistAndWarnsOnPruneReach(t *testing.T) {
	c, dyn := adhocConn(t)
	// A pruning Kustomization reaches the scratch namespace via targetNamespace.
	c.caps.GitOps.Flux.Present = true
	ks := kustomization("flux-system", "web", true, "scratch")
	ksGVR := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	// Seed the Kustomization into a dynamic client that also serves its list kind.
	listKinds := map[schema.GroupVersionResource]string{
		ksGVR: "KustomizationList",
		{Group: "", Version: "v1", Resource: "pods"}: "PodList",
	}
	dyn2 := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds)
	if _, err := dyn2.Resource(ksGVR).Namespace("flux-system").Create(context.Background(), ks, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c.dyn = dyn2
	_ = dyn

	// The fake tracker ignores DryRun options and would persist; swallow the
	// create so "not persisted" reflects a server honoring DryRunAll.
	dyn2.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		return true, a.(k8stesting.CreateAction).GetObject(), nil
	})

	res := c.CreateResourceYAML(context.Background(), adhocPodYAML, true)
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("dry-run should pass: %+v", res)
	}
	if res[0].NamespaceOwnerKind != "Kustomization" || res[0].NamespaceOwnerName != "web" || !res[0].NamespacePrunes {
		t.Fatalf("dry-run must carry the prune-reach verdict: %+v", res[0])
	}
	// Dry-run must not persist.
	if _, err := dyn2.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}).Namespace("scratch").Get(context.Background(), "dbg", metav1.GetOptions{}); err == nil {
		t.Fatal("dry-run persisted the object")
	}
}

// Codex review 2026-10-13 #2: the delete carries a server-side UID
// precondition, so a replacement created between the check and the delete
// is never removed.
func TestDeleteAdhocObjectPinsTheCheckedUID(t *testing.T) {
	c, dyn := adhocConn(t)
	dyn.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		u := &unstructured.Unstructured{}
		u.SetName("dbg")
		u.SetNamespace("scratch")
		u.SetUID("old")
		u.SetLabels(map[string]string{AdhocLabel: "true"})
		return true, u, nil
	})
	var opts metav1.DeleteOptions
	dyn.PrependReactor("delete", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		opts = a.(k8stesting.DeleteAction).GetDeleteOptions()
		return true, nil, nil
	})
	if err := c.DeleteAdhocObject(context.Background(), "", "v1", "pods", "scratch", "dbg", "old"); err != nil {
		t.Fatal(err)
	}
	if opts.Preconditions == nil || opts.Preconditions.UID == nil || string(*opts.Preconditions.UID) != "old" {
		t.Fatalf("delete can target a replacement: %+v", opts)
	}

	// A precondition conflict says nothing was deleted.
	dyn.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "dbg", errors.New("StorageError: invalid object, Code: 4, Key: /registry/pods/x/dbg, ResourceVersion: 0, AdditionalErrorMsg: Precondition failed: UID in precondition: old, UID in object meta: new"))
	})
	if err := c.DeleteAdhocObject(context.Background(), "", "v1", "pods", "scratch", "dbg", "old"); err == nil || !strings.Contains(err.Error(), "nothing was deleted") {
		t.Fatalf("a precondition conflict must say nothing was deleted: %v", err)
	}
}

// Codex review 2026-10-13 #5: the inventory sweeps every kind the composer can
// create (a ClusterRole too), never a curated subset.
func TestAdhocInventoryCoversEveryCreatableKind(t *testing.T) {
	c, _ := adhocConn(t)
	res := c.CreateResourceYAML(context.Background(), "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: scratch-role\nrules: []\n", false)
	if len(res) != 1 || !res[0].OK {
		t.Fatalf("create failed: %+v", res)
	}
	inv, err := c.ListAdhocObjects(context.Background())
	if err != nil || len(inv.Objects) != 1 || inv.Objects[0].Kind != "ClusterRole" || inv.Objects[0].Scope != "Cluster" {
		t.Fatalf("the composer-created ClusterRole must be in the inventory: %+v %v", inv, err)
	}
}

// The composer refuses a kind Scratch could not list and delete.
func TestCreateRefusesKindsScratchCannotClean(t *testing.T) {
	c, _ := adhocConn(t)
	res := c.CreateResourceYAML(context.Background(), "apiVersion: v1\nkind: Binding\nmetadata:\n  name: b\n  namespace: scratch\n", false)
	if len(res) != 1 || res[0].OK || !strings.Contains(res[0].Error, "could not clean it up") {
		t.Fatalf("a kind without list/delete must be refused: %+v", res)
	}
}

// A kind whose list fails is named, never silently dropped; the readable
// kinds still come back.
func TestAdhocInventoryNamesUnreadKinds(t *testing.T) {
	c, dyn := adhocConn(t)
	podGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	pod := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]interface{}{"name": "dbg", "namespace": ScratchNamespace, "uid": "u1", "labels": map[string]interface{}{AdhocLabel: "true"}},
	}}
	if _, err := dyn.Resource(podGVR).Namespace(ScratchNamespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	dyn.PrependReactor("list", "clusterroles", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: "clusterroles"}, "", errors.New("no list"))
	})
	inv, err := c.ListAdhocObjects(context.Background())
	if err != nil || len(inv.Objects) != 1 || inv.Objects[0].Name != "dbg" {
		t.Fatalf("readable kinds must still be listed: %+v %v", inv, err)
	}
	if len(inv.Unread) != 1 || !strings.HasPrefix(inv.Unread[0], "clusterroles.rbac.authorization.k8s.io: ") {
		t.Fatalf("the unreadable kind must be named: %+v", inv.Unread)
	}
}

// The sweep follows continue tokens: objects past the first page are listed.
func TestAdhocInventoryFollowsContinueTokens(t *testing.T) {
	c, dyn := adhocConn(t)
	page := func(names []string, cont string) *unstructured.UnstructuredList {
		l := &unstructured.UnstructuredList{}
		l.SetContinue(cont)
		for _, n := range names {
			u := unstructured.Unstructured{}
			u.SetAPIVersion("v1")
			u.SetKind("ConfigMap")
			u.SetName(n)
			u.SetNamespace(ScratchNamespace)
			u.SetUID(types.UID("uid-" + n))
			u.SetLabels(map[string]string{AdhocLabel: "true"}) // the fake applies the selector itself
			l.Items = append(l.Items, u)
		}
		return l
	}
	// The fake drops ListOptions before reactors see them (known client-go
	// issue), so pages are served by call order.
	calls := 0
	dyn.PrependReactor("list", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, page([]string{"a", "b"}, "next"), nil
		}
		return true, page([]string{"c"}, ""), nil
	})
	inv, err := c.ListAdhocObjects(context.Background())
	if err != nil || len(inv.Objects) != 3 || calls != 2 {
		t.Fatalf("want all three pages' objects in two calls, got %d objects in %d calls: %+v %v", len(inv.Objects), calls, inv, err)
	}
}

// An object served under two resources (core and events.k8s.io Events, for
// example) is listed once, by UID.
func TestAdhocInventoryDedupesByUID(t *testing.T) {
	c, dyn := adhocConn(t)
	same := func() *unstructured.UnstructuredList {
		l := &unstructured.UnstructuredList{}
		u := unstructured.Unstructured{}
		u.SetName("dup")
		u.SetNamespace(ScratchNamespace)
		u.SetUID("shared-uid")
		u.SetLabels(map[string]string{AdhocLabel: "true"})
		l.Items = append(l.Items, u)
		return l
	}
	for _, r := range []string{"pods", "configmaps"} {
		dyn.PrependReactor("list", r, func(k8stesting.Action) (bool, runtime.Object, error) { return true, same(), nil })
	}
	inv, err := c.ListAdhocObjects(context.Background())
	if err != nil || len(inv.Objects) != 1 {
		t.Fatalf("one UID must be listed once: %+v %v", inv, err)
	}
}

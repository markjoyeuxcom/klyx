package fleet

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	typed.Discovery().(*discoveryfake.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true},
			{Name: "pods/log", Kind: "Pod", Namespaced: true}, // subresource: must be skipped
			{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
			{Name: "secrets", Kind: "Secret", Namespaced: true},
		}},
		{GroupVersion: "rbac.authorization.k8s.io/v1", APIResources: []metav1.APIResource{
			{Name: "clusterroles", Kind: "ClusterRole", Namespaced: false},
		}},
	}
	listKinds := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "pods"}:                                  "PodList",
		{Group: "", Version: "v1", Resource: "configmaps"}:                            "ConfigMapList",
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

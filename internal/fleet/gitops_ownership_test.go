package fleet

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

func kustomization(ns, name string, prune bool, targetNS string, inventoryIDs ...string) *unstructured.Unstructured {
	entries := make([]interface{}, 0, len(inventoryIDs))
	for _, id := range inventoryIDs {
		entries = append(entries, map[string]interface{}{"id": id, "v": "v1"})
	}
	obj := map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"namespace": ns, "name": name},
		"spec":       map[string]interface{}{"prune": prune},
		"status":     map[string]interface{}{"inventory": map[string]interface{}{"entries": entries}},
	}
	if targetNS != "" {
		obj["spec"].(map[string]interface{})["targetNamespace"] = targetNS
	}
	return &unstructured.Unstructured{Object: obj}
}

func argoApp(ns, name, destNS string, autoPrune bool, resources ...map[string]interface{}) *unstructured.Unstructured {
	res := make([]interface{}, 0, len(resources))
	for _, r := range resources {
		res = append(res, r)
	}
	spec := map[string]interface{}{
		"destination": map[string]interface{}{"namespace": destNS},
	}
	if autoPrune {
		spec["syncPolicy"] = map[string]interface{}{"automated": map[string]interface{}{"prune": true}}
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"namespace": ns, "name": name},
		"spec":       spec,
		"status":     map[string]interface{}{"resources": res},
	}}
}

func ownershipConn(t *testing.T, fluxPresent, argoPresent bool, objs ...*unstructured.Unstructured) *ClusterConn {
	t.Helper()
	scheme := dynScheme()
	listKinds := map[schema.GroupVersionResource]string{
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: "KustomizationList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}:             "ApplicationList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for _, o := range objs {
		var gvr schema.GroupVersionResource
		switch o.GetKind() {
		case "Kustomization":
			gvr = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
		case "Application":
			gvr = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
		}
		if _, err := dyn.Resource(gvr).Namespace(o.GetNamespace()).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", o.GetKind(), err)
		}
	}
	c := NewClusterConn("x", nil, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	c.caps = capability.Set{}
	c.caps.GitOps.Flux.Present = fluxPresent
	c.caps.GitOps.Argo.Present = argoPresent
	return c
}

func TestGitOpsOwnership_FluxInventoryOwnsObject(t *testing.T) {
	ks := kustomization("flux-system", "apps", true, "",
		"web_api_apps_Deployment",                             // ns=web name=api group=apps kind=Deployment
		"_cluster-role_rbac.authorization.k8s.io_ClusterRole") // cluster-scoped entry
	c := ownershipConn(t, true, false, ks)

	o := c.GitOpsOwnership(context.Background(), "apps", "Deployment", "web", "api")
	if o.OwnerKind != "Kustomization" || o.OwnerName != "apps" || o.OwnerNamespace != "flux-system" {
		t.Fatalf("want Kustomization flux-system/apps as owner, got %+v", o)
	}
	if !o.OwnerPrunes || o.Basis != "flux-inventory" {
		t.Fatalf("owner must carry prune + basis, got %+v", o)
	}
	// Namespace reach follows from the inventory entry in "web".
	if o.NamespaceOwnerKind != "Kustomization" || !o.NamespacePrunes {
		t.Fatalf("namespace reach must be detected, got %+v", o)
	}

	// An unrelated object is NOT owned - and with no reach signals for its ns,
	// no namespace owner is claimed either.
	o2 := c.GitOpsOwnership(context.Background(), "", "ConfigMap", "elsewhere", "cm")
	if o2.OwnerKind != "" || o2.NamespaceOwnerKind != "" {
		t.Fatalf("unrelated object must be unowned, got %+v", o2)
	}
}

func TestGitOpsOwnership_FluxTargetNamespaceReach(t *testing.T) {
	// No inventory yet (first reconcile pending) but spec.targetNamespace names
	// the namespace: creates there must still warn.
	ks := kustomization("flux-system", "web", true, "web")
	c := ownershipConn(t, true, false, ks)

	o := c.GitOpsOwnership(context.Background(), "", "ConfigMap", "web", "new-cm")
	if o.OwnerKind != "" {
		t.Fatalf("object not in inventory must not read as owned, got %+v", o)
	}
	if o.NamespaceOwnerKind != "Kustomization" || o.NamespaceOwnerName != "web" || !o.NamespacePrunes || o.NamespaceBasis != "flux" {
		t.Fatalf("targetNamespace reach must be detected with prune, got %+v", o)
	}
}

func TestGitOpsOwnership_ArgoResourcesAndDestination(t *testing.T) {
	app := argoApp("argocd", "shop", "shop", true,
		map[string]interface{}{"group": "apps", "kind": "Deployment", "namespace": "shop", "name": "cart"})
	c := ownershipConn(t, false, true, app)

	o := c.GitOpsOwnership(context.Background(), "apps", "Deployment", "shop", "cart")
	if o.OwnerKind != "Application" || o.OwnerName != "shop" || o.Basis != "argo-resources" {
		t.Fatalf("want Application shop as owner, got %+v", o)
	}
	if !o.OwnerPrunes {
		t.Fatalf("automated.prune must carry through, got %+v", o)
	}
	// Destination reach for a create into the same namespace.
	o2 := c.GitOpsOwnership(context.Background(), "", "ConfigMap", "shop", "scratch")
	if o2.NamespaceOwnerKind != "Application" || !o2.NamespacePrunes || o2.NamespaceBasis != "argo" {
		t.Fatalf("argo destination reach must be detected, got %+v", o2)
	}
}

func TestGitOpsOwnership_AbsentToolsCheckNothing(t *testing.T) {
	// Neither tool present: no lists issued, clean empty verdict.
	c := ownershipConn(t, false, false)
	o := c.GitOpsOwnership(context.Background(), "apps", "Deployment", "web", "api")
	if o.OwnerKind != "" || o.NamespaceOwnerKind != "" || len(o.Warnings) != 0 {
		t.Fatalf("absent tools must yield a clean empty verdict, got %+v", o)
	}
}

func TestGitOpsOwnership_FailedListDegradesToWarning(t *testing.T) {
	// Flux present but the CRD list fails (e.g. RBAC): the verdict must carry a
	// warning, never a silent "unmanaged".
	c := ownershipConn(t, true, false)
	c.dyn = nil // forces the "no cluster connection" degraded path
	o := c.GitOpsOwnership(context.Background(), "apps", "Deployment", "web", "api")
	if len(o.Warnings) == 0 || !strings.Contains(o.Warnings[0], "unavailable") {
		t.Fatalf("degraded check must warn, got %+v", o)
	}
}

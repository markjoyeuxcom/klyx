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

// Review 2026-10-03 #13: reaching a namespace is context. A pruning unit that
// merely deploys into the namespace does not manage the Namespace object, so
// an untracked scratch object there is not at risk from its pruning;
// NamespaceManaged marks only the unit that could delete the namespace itself
// - and is preferred when several units reach it.
func TestGitOpsOwnership_NamespaceManagedIsTheConditionalRisk(t *testing.T) {
	deploysOnly := kustomization("flux-system", "apps", true, "web", "web_api_apps_Deployment")
	c := ownershipConn(t, true, false, deploysOnly)
	o := c.GitOpsOwnership(context.Background(), "", "ConfigMap", "web", "scratch")
	if o.OwnerKind != "" || o.NamespaceOwnerName != "apps" || !o.NamespacePrunes || o.NamespaceManaged {
		t.Fatalf("an untracked object in a deploys-only namespace: %+v", o)
	}

	managesNS := kustomization("flux-system", "namespaces", true, "", "_web__Namespace")
	c = ownershipConn(t, true, false, deploysOnly, managesNS)
	o = c.GitOpsOwnership(context.Background(), "", "ConfigMap", "web", "scratch")
	if o.NamespaceOwnerName != "namespaces" || !o.NamespaceManaged || !o.NamespacePrunes {
		t.Fatalf("the unit managing the Namespace object must be reported: %+v", o)
	}

	app := argoApp("argocd", "shop", "shop", true,
		map[string]interface{}{"group": "", "kind": "Namespace", "namespace": "", "name": "shop"})
	c = ownershipConn(t, false, true, app)
	if o := c.GitOpsOwnership(context.Background(), "", "ConfigMap", "shop", "scratch"); !o.NamespaceManaged {
		t.Fatalf("an Argo app tracking the Namespace manages it: %+v", o)
	}
}

// Batch-4 review R2: tracking evidence follows Argo's documented tracking
// modes. A copied tracking-id that names another object is excluded by Argo;
// the instance label tracks only under label tracking; with the method
// unknown, matching metadata is only "possible".
func TestArgoTrackingEvidence(t *testing.T) {
	obj := func(labels, annos map[string]string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]interface{}{}}
		u.SetLabels(labels)
		u.SetAnnotations(annos)
		return u
	}
	label := map[string]string{"app.kubernetes.io/instance": "shop"}
	self := map[string]string{"argocd.argoproj.io/tracking-id": "shop:/ConfigMap:shop/copy"}
	copied := map[string]string{"argocd.argoproj.io/tracking-id": "shop:/ConfigMap:shop/original"}
	anyNS := map[string]string{"argocd.argoproj.io/tracking-id": "team_shop:/ConfigMap:shop/copy"}
	labelMode := argoTrackingCfg{known: true, method: "label", labelKey: "app.kubernetes.io/instance"}
	annoMode := argoTrackingCfg{known: true, method: "annotation", labelKey: "app.kubernetes.io/instance"}
	both := argoTrackingCfg{known: true, method: "annotation+label", labelKey: "app.kubernetes.io/instance"}
	unknown := argoTrackingCfg{labelKey: "app.kubernetes.io/instance"}
	withID := func(c argoTrackingCfg) argoTrackingCfg { c.installationID = "production"; return c }
	merge := func(a, b map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range a {
			out[k] = v
		}
		for k, v := range b {
			out[k] = v
		}
		return out
	}

	cases := []struct {
		name  string
		u     *unstructured.Unstructured
		appNS string
		cfg   argoTrackingCfg
		want  string
	}{
		{"self-referencing id, annotation mode", obj(nil, self), "argocd", annoMode, "tracked"},
		{"copied id is not self-referencing", obj(nil, copied), "argocd", annoMode, ""},
		{"instance label, annotation mode", obj(label, nil), "argocd", annoMode, ""},
		{"instance label, annotation+label mode", obj(label, nil), "argocd", both, ""},
		{"self-referencing id, annotation+label mode", obj(nil, self), "argocd", both, "tracked"},
		{"instance label, label mode", obj(label, nil), "argocd", labelMode, "tracked"},
		{"tracking id ignored in label mode", obj(nil, self), "argocd", labelMode, ""},
		{"method unknown, label", obj(label, nil), "argocd", unknown, "possible"},
		{"method unknown, copied id", obj(nil, copied), "argocd", unknown, ""},
		{"apps-in-any-namespace reference", obj(nil, anyNS), "team", annoMode, "tracked"},
		{"no metadata", obj(nil, nil), "argocd", annoMode, ""},
		// Codex batch-4 re-review R2a: with installationID configured, annotation
		// tracking also needs a matching installation-id annotation; label
		// tracking is unaffected.
		{"installationID set, annotation missing", obj(nil, self), "argocd", withID(annoMode), ""},
		{"installationID set, annotation differs", obj(nil, merge(self, map[string]string{"argocd.argoproj.io/installation-id": "other"})), "argocd", withID(annoMode), ""},
		{"installationID set, annotation matches", obj(nil, merge(self, map[string]string{"argocd.argoproj.io/installation-id": "production"})), "argocd", withID(annoMode), "tracked"},
		{"installationID set, label mode", obj(label, nil), "argocd", withID(labelMode), "tracked"},
	}
	for _, tc := range cases {
		if got := argoTrackingEvidence(tc.u, "", "ConfigMap", "shop", "copy", tc.appNS, "shop", tc.cfg); got != tc.want {
			t.Errorf("%s: evidence = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Codex batch-4 review probes, adopted end to end through the composer's dry
// run: an Argo app's tracking is found even when a Flux unit manages the
// namespace, and the controller's tracking method (argocd-cm) decides.
func TestAdhocDryRun_ArgoTrackingFollowsTheController(t *testing.T) {
	setup := func(t *testing.T, method string, objs ...*unstructured.Unstructured) *ClusterConn {
		c := ownershipConn(t, true, true, objs...)
		base, _ := adhocConn(t)
		c.typed = base.typed
		if method != "" {
			cm := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]interface{}{"name": "argocd-cm", "namespace": "argocd"},
				"data":     map[string]interface{}{"application.resourceTrackingMethod": method},
			}}
			if _, err := c.dyn.Resource(argoCMGVR).Namespace("argocd").Create(context.Background(), cm, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	dry := func(t *testing.T, c *ClusterConn, doc string) AdhocResult {
		out := c.CreateResourceYAML(context.Background(), doc, true)
		if len(out) != 1 || !out[0].OK {
			t.Fatalf("dry run: %+v", out)
		}
		return out[0]
	}
	selfDoc := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: copy\n  namespace: shop\n  annotations:\n    argocd.argoproj.io/tracking-id: shop:/ConfigMap:shop/copy\n"
	labelDoc := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: scratch\n  namespace: shop\n  labels:\n    app.kubernetes.io/instance: shop\n"

	// A Flux unit managing the namespace does not hide the Argo app's tracking.
	c := setup(t, "annotation", argoApp("argocd", "shop", "shop", true), kustomization("flux-system", "namespaces", true, "", "_shop__Namespace"))
	r := dry(t, c, selfDoc)
	if r.NamespaceOwnerKind != "Kustomization" || r.ArgoTracking != "tracked" || r.ArgoTrackingApp != "argocd/shop" || !r.ArgoTrackingPrunes || !r.ArgoConfigKnown {
		t.Fatalf("self-referencing tracking under annotation mode: %+v", r)
	}

	// Annotation mode: the instance label alone is not tracking.
	c = setup(t, "annotation", argoApp("argocd", "shop", "shop", true))
	if r := dry(t, c, labelDoc); r.ArgoTracking != "" || !r.ArgoConfigKnown {
		t.Fatalf("instance label under annotation mode: %+v", r)
	}

	// Method unreadable: the label is only a possibility.
	c = setup(t, "", argoApp("argocd", "shop", "shop", true))
	if r := dry(t, c, labelDoc); r.ArgoTracking != "possible" || r.ArgoConfigKnown {
		t.Fatalf("unknown tracking method: %+v", r)
	}
}

// Codex batch-4 re-review R2a probe, adopted end to end: installationID from
// argocd-cm gates annotation tracking.
func TestAdhocDryRun_ArgoInstallationIDGatesTracking(t *testing.T) {
	for id, want := range map[string]string{"": "", "other": "", "production": "tracked"} {
		t.Run("installation-id="+id, func(t *testing.T) {
			c := ownershipConn(t, false, true, argoApp("argocd", "shop", "shop", true))
			base, _ := adhocConn(t)
			c.typed = base.typed
			cm := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": map[string]interface{}{"name": "argocd-cm", "namespace": "argocd"},
				"data":     map[string]interface{}{"application.resourceTrackingMethod": "annotation", "installationID": "production"},
			}}
			if _, err := c.dyn.Resource(argoCMGVR).Namespace("argocd").Create(context.Background(), cm, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			doc := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: copy\n  namespace: shop\n  annotations:\n    argocd.argoproj.io/tracking-id: shop:/ConfigMap:shop/copy\n"
			if id != "" {
				doc += "    argocd.argoproj.io/installation-id: " + id + "\n"
			}
			out := c.CreateResourceYAML(context.Background(), doc, true)
			if len(out) != 1 || !out[0].OK {
				t.Fatalf("dry run: %+v", out)
			}
			if out[0].ArgoTracking != want {
				t.Fatalf("ArgoTracking = %q, want %q", out[0].ArgoTracking, want)
			}
		})
	}
}

// Codex batch-4 re-review R2b: the object's own Prune= sync option reaches
// the verdict.
func TestArgoPruneOption(t *testing.T) {
	for v, want := range map[string]string{
		"":                                    "",
		"Prune=false":                         "false",
		"ServerSideApply=true, Prune=confirm": "confirm",
		"PruneLast=true":                      "",
	} {
		u := &unstructured.Unstructured{Object: map[string]interface{}{}}
		if v != "" {
			u.SetAnnotations(map[string]string{"argocd.argoproj.io/sync-options": v})
		}
		if got := argoPruneOption(u); got != want {
			t.Errorf("%q: argoPruneOption = %q, want %q", v, got, want)
		}
	}
}

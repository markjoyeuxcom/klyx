package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gitops/flux"
)

func ksGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
}

func newActionConn(dyn *fake.FakeDynamicClient) *ClusterConn {
	typed := typedfake.NewSimpleClientset()
	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.ctx = context.Background()
	return c
}

func seedKustomization(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system", "uid": "uid-" + name},
		"spec":       map[string]interface{}{},
	}}
}

func dynScheme() *runtime.Scheme {
	return runtime.NewScheme()
}

func TestReconcilePatchesAnnotation(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKustomization("app"))
	c := newActionConn(dyn)

	if err := c.Reconcile(context.Background(), "Kustomization", "flux-system", "app", "uid-app"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var patched bool
	for _, a := range dyn.Actions() {
		if pa, ok := a.(k8stesting.PatchAction); ok && pa.GetName() == "app" {
			patched = true
			if !strings.Contains(string(pa.GetPatch()), flux.ReconcileRequestedAtAnnotation) {
				t.Fatalf("patch missing annotation: %s", pa.GetPatch())
			}
		}
	}
	if !patched {
		t.Fatal("expected a patch action on app")
	}
}

func seedKustomizationWithSource(name, srcKind, srcName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system", "uid": "uid-" + name},
		"spec":       map[string]interface{}{"sourceRef": map[string]interface{}{"kind": srcKind, "name": srcName}},
	}}
}

func seedGitRepoSource(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system"},
		"spec":       map[string]interface{}{"url": "https://github.com/org/repo"},
	}}
}

func TestReconcileWithSourcePatchesBoth(t *testing.T) {
	gitRepoGVR := schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	listKinds := map[schema.GroupVersionResource]string{
		ksGVR():    "KustomizationList",
		gitRepoGVR: "GitRepositoryList",
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds,
		seedKustomizationWithSource("app", "GitRepository", "flux-system"),
		seedGitRepoSource("flux-system"))
	c := newActionConn(dyn)

	if err := c.ReconcileWithSource(context.Background(), "Kustomization", "flux-system", "app", "uid-app"); err != nil {
		t.Fatalf("reconcile with source: %v", err)
	}

	patchedKs, patchedSrc := false, false
	for _, a := range dyn.Actions() {
		pa, ok := a.(k8stesting.PatchAction)
		if !ok || !strings.Contains(string(pa.GetPatch()), flux.ReconcileRequestedAtAnnotation) {
			continue
		}
		switch pa.GetResource().Resource {
		case "kustomizations":
			if pa.GetName() == "app" {
				patchedKs = true
			}
		case "gitrepositories":
			if pa.GetName() == "flux-system" {
				patchedSrc = true
			}
		}
	}
	if !patchedKs {
		t.Fatal("expected the Kustomization to be reconciled")
	}
	if !patchedSrc {
		t.Fatal("expected the bound GitRepository to be reconciled")
	}
}

func TestReconcileWithSourceDegradesWhenNoSource(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"}
	// A Kustomization with no sourceRef: the action must still reconcile it.
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKustomization("app"))
	c := newActionConn(dyn)

	if err := c.ReconcileWithSource(context.Background(), "Kustomization", "flux-system", "app", "uid-app"); err != nil {
		t.Fatalf("reconcile with source: %v", err)
	}
	patchedKs := false
	for _, a := range dyn.Actions() {
		if pa, ok := a.(k8stesting.PatchAction); ok && pa.GetName() == "app" && strings.Contains(string(pa.GetPatch()), flux.ReconcileRequestedAtAnnotation) {
			patchedKs = true
		}
	}
	if !patchedKs {
		t.Fatal("expected the Kustomization to still be reconciled without a source")
	}
}

func TestSetSuspendPatchesSpec(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKustomization("app"))
	c := newActionConn(dyn)

	if err := c.SetSuspend(context.Background(), "Kustomization", "flux-system", "app", "uid-app", true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	found := false
	for _, a := range dyn.Actions() {
		if pa, ok := a.(k8stesting.PatchAction); ok && strings.Contains(string(pa.GetPatch()), `"suspend":true`) {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a suspend:true patch")
	}
}

func TestReconcileUnknownKindErrors(t *testing.T) {
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{})
	c := newActionConn(dyn)
	if err := c.Reconcile(context.Background(), "Service", "default", "x", "uid-x"); err == nil {
		t.Fatal("want error for unsupported kind")
	}
}

func TestReconcileSurfacesForbidden(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKustomization("app"))
	dyn.PrependReactor("patch", "kustomizations", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "kustomizations"}, "app", nil)
	})
	c := newActionConn(dyn)
	err := c.Reconcile(context.Background(), "Kustomization", "flux-system", "app", "uid-app")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "forbidden") {
		t.Fatalf("want forbidden error, got %v", err)
	}
}

// Design §5b (Codex review of 7d389eb #1): a verb writes only the object it
// was armed against. A same-name replacement - or an object gone since - is
// refused before anything is written, the source of a with-source reconcile
// included.
func TestFluxVerbsWriteOnlyTheArmedObject(t *testing.T) {
	gitRepoGVR := schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	verbs := map[string]func(c *ClusterConn, uid string) error{
		"reconcile": func(c *ClusterConn, uid string) error {
			return c.Reconcile(context.Background(), "Kustomization", "flux-system", "app", uid)
		},
		"with-source": func(c *ClusterConn, uid string) error {
			return c.ReconcileWithSource(context.Background(), "Kustomization", "flux-system", "app", uid)
		},
		"suspend": func(c *ClusterConn, uid string) error {
			return c.SetSuspend(context.Background(), "Kustomization", "flux-system", "app", uid, true)
		},
		"resume": func(c *ClusterConn, uid string) error {
			return c.SetSuspend(context.Background(), "Kustomization", "flux-system", "app", uid, false)
		},
	}
	for name, verb := range verbs {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				what  string
				seed  []runtime.Object
				armed string
				want  error
			}{
				{"replaced", []runtime.Object{seedKustomizationWithSource("app", "GitRepository", "flux-system"), seedGitRepoSource("flux-system")}, "uid-old-app", ErrReplaced},
				{"gone", []runtime.Object{seedGitRepoSource("flux-system")}, "uid-app", ErrReplaced},
				{"unarmed", []runtime.Object{seedKustomizationWithSource("app", "GitRepository", "flux-system"), seedGitRepoSource("flux-system")}, "", nil},
			} {
				dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList", gitRepoGVR: "GitRepositoryList"}, tc.seed...)
				c := newActionConn(dyn)
				err := verb(c, tc.armed)
				if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || (tc.want == nil && !strings.Contains(err.Error(), "no object identity was armed")) {
					t.Fatalf("%s: want a refusal (%v), got %v", tc.what, tc.want, err)
				}
				for _, a := range dyn.Actions() {
					if _, ok := a.(k8stesting.PatchAction); ok {
						t.Fatalf("%s: nothing may be written, got a patch of %s", tc.what, a.GetResource().Resource)
					}
				}
			}
		})
	}
}

// The armed write itself carries the UID, so the API server refuses a
// replacement that lands after the check; that refusal reads as replaced.
func TestFluxVerbPatchIsUIDGuarded(t *testing.T) {
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"}, seedKustomization("app"))
	c := newActionConn(dyn)
	if err := c.SetSuspend(context.Background(), "Kustomization", "flux-system", "app", "uid-app", true); err != nil {
		t.Fatal(err)
	}
	guarded := false
	for _, a := range dyn.Actions() {
		if pa, ok := a.(k8stesting.PatchAction); ok && strings.Contains(string(pa.GetPatch()), `"uid":"uid-app"`) && strings.Contains(string(pa.GetPatch()), `"suspend":true`) {
			guarded = true
		}
	}
	if !guarded {
		t.Fatal("the write must carry the armed UID")
	}

	dyn.PrependReactor("patch", "kustomizations", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}, "app",
			field.ErrorList{field.Invalid(field.NewPath("metadata", "uid"), "uid-app", "field is immutable")})
	})
	if err := c.Reconcile(context.Background(), "Kustomization", "flux-system", "app", "uid-app"); !errors.Is(err, ErrReplaced) {
		t.Fatalf("a server refusal of the UID guard is a replacement: %v", err)
	}
}

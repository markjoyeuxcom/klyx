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
				if tc.want != nil && !strings.Contains(err.Error(), "nothing was written") {
					t.Fatalf("%s: a refusal before any write says so: %v", tc.what, err)
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

// Codex review of 6b43068: the race path of a with-source reconcile - the
// target is still the armed object when checked, its source is written, then
// the target's guarded write is refused (a replacement in between). The
// error keeps the replacement detectable and says what happened to the
// source; it never claims nothing was written.
func TestReconcileWithSourceReportsAPartialWrite(t *testing.T) {
	gitRepoGVR := schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	refuse := func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization"}, "app",
			field.ErrorList{field.Invalid(field.NewPath("metadata", "uid"), "uid-app", "field is immutable")})
	}
	for _, tc := range []struct {
		name       string
		sourceErr  error
		wantSource string
	}{
		{"source written", nil, "its source GitRepository flux-system/flux-system was already asked to reconcile"},
		// a definite API refusal: nothing was committed for the source
		{"source refused", apierrors.NewForbidden(schema.GroupResource{Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories"}, "flux-system", errors.New("rbac")),
			"its source GitRepository flux-system/flux-system was not asked to reconcile either: "},
		// Codex review of 9c58cca: a transport loss or deadline may have
		// committed before the answer was lost - unverified, never "not asked"
		{"source transport lost", errors.New("read tcp 10.0.0.1:443: connection reset by peer"),
			"its source GitRepository flux-system/flux-system reconcile request failed and its outcome is unverified: read tcp"},
		{"source timed out", context.DeadlineExceeded,
			"its source GitRepository flux-system/flux-system reconcile request failed and its outcome is unverified: context deadline exceeded"},
		{"source 504", apierrors.NewTimeoutError("the request timed out", 1),
			"its source GitRepository flux-system/flux-system reconcile request failed and its outcome is unverified: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList", gitRepoGVR: "GitRepositoryList"},
				seedKustomizationWithSource("app", "GitRepository", "flux-system"), seedGitRepoSource("flux-system"))
			dyn.PrependReactor("patch", "kustomizations", refuse)
			if tc.sourceErr != nil {
				dyn.PrependReactor("patch", "gitrepositories", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, tc.sourceErr })
			}
			c := newActionConn(dyn)
			err := c.ReconcileWithSource(context.Background(), "Kustomization", "flux-system", "app", "uid-app")
			if !errors.Is(err, ErrReplaced) || !strings.Contains(err.Error(), tc.wantSource) || strings.Contains(err.Error(), "nothing was written") ||
				(tc.sourceErr != nil && !definiteRefusal(tc.sourceErr) && strings.Contains(err.Error(), "was not asked")) {
				t.Fatalf("the partial outcome must be said: %v", err)
			}
			sourcePatched := false
			for _, a := range dyn.Actions() {
				if pa, ok := a.(k8stesting.PatchAction); ok && pa.GetResource().Resource == "gitrepositories" {
					sourcePatched = true
				}
			}
			if !sourcePatched {
				t.Fatal("the source write was attempted before the target's")
			}
		})
	}

}

// Coverage S1b §5: the image kinds' verbs - "Scan now", "Run now" and a
// policy's reconcile are the request annotation, suspension is
// spec.suspend, every write UID-guarded; an ImagePolicy at v1beta2 has
// neither; reconcile with source is not offered.
func TestImageKindVerbs(t *testing.T) {
	gvr := func(v, res string) schema.GroupVersionResource {
		return schema.GroupVersionResource{Group: "image.toolkit.fluxcd.io", Version: v, Resource: res}
	}
	seed := func(v, kind, name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "image.toolkit.fluxcd.io/" + v, "kind": kind,
			"metadata": map[string]interface{}{"name": name, "namespace": "apps", "uid": "uid-" + name}, "spec": map[string]interface{}{}}}
	}
	lists := map[schema.GroupVersionResource]string{gvr("v1", "imagerepositories"): "ImageRepositoryList", gvr("v1", "imagepolicies"): "ImagePolicyList",
		gvr("v1", "imageupdateautomations"): "ImageUpdateAutomationList"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), lists, seed("v1", "ImageRepository", "repo"), seed("v1", "ImagePolicy", "pol"), seed("v1", "ImageUpdateAutomation", "auto"))
	c := newActionConn(dyn)
	ctx := context.Background()
	for kind, name := range map[string]string{"ImageRepository": "repo", "ImagePolicy": "pol", "ImageUpdateAutomation": "auto"} {
		if err := c.Reconcile(ctx, kind, "apps", name, "uid-"+name); err != nil {
			t.Errorf("%s reconcile: %v", kind, err)
		}
		if err := c.SetSuspend(ctx, kind, "apps", name, "uid-"+name, true); err != nil {
			t.Errorf("%s suspend: %v", kind, err)
		}
		if err := c.ReconcileWithSource(ctx, kind, "apps", name, "uid-"+name); err == nil || !strings.Contains(err.Error(), "not offered") {
			t.Errorf("%s with source: %v", kind, err)
		}
	}
	guarded := 0
	for _, a := range dyn.Actions() {
		if pa, ok := a.(k8stesting.PatchAction); ok && strings.Contains(string(pa.GetPatch()), `"uid":"uid-`) {
			guarded++
		}
	}
	if guarded != 6 {
		t.Fatalf("every write carries the armed UID: %d of 6", guarded)
	}
	if err := imagePolicyVerb(flux.ImagePolicyKind, gvr("v1beta2", "imagepolicies"), "reconcile", "why"); err == nil {
		t.Fatal("an ImagePolicy at v1beta2 is refused")
	}
	if err := imagePolicyVerb(flux.ImageRepositoryKind, gvr("v1beta2", "imagerepositories"), "reconcile", "why"); err != nil {
		t.Fatalf("a v1beta2 ImageRepository is acted on: %v", err)
	}
}

// Coverage S1c §5: a resource bound to an ExternalArtifact is never
// reconciled with its source - the producer reconciles the artifact - and
// nothing is written.
func TestReconcileWithSourceRefusesAnArtifactSource(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKustomizationWithSource("app", "ExternalArtifact", "flux-system"))
	c := newActionConn(dyn)
	err := c.ReconcileWithSource(context.Background(), "Kustomization", "flux-system", "app", "uid-app")
	if err == nil || !strings.Contains(err.Error(), "its producer reconciles") {
		t.Fatalf("refused: %v", err)
	}
	for _, a := range dyn.Actions() {
		if _, ok := a.(k8stesting.PatchAction); ok {
			t.Fatalf("nothing is written: %v", a)
		}
	}
}

package fleet

import (
	"context"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"
)

// The Flux detail reads (1c slice 3 design §5b): a direct GET at the
// served version, UID identity, and NotFound told apart from unreadable.

func ksObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system"},
		"status": map[string]interface{}{
			"conditions":          []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}},
			"lastAppliedRevision": "main@sha1:abc",
		},
	}}
}

func seedGitRepo(name, url string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system"},
		"spec":       map[string]interface{}{"url": url},
	}}
}

func withObjUID(u *unstructured.Unstructured, uid string) *unstructured.Unstructured {
	u.SetUID(types.UID(uid))
	return u
}

func TestFluxObjectReadsAtTheServedVersion(t *testing.T) {
	c, _, cancel := splitConn(t, withObjUID(ksObj("apps"), "ks-1"), seedGitRepo("flux-system", "https://gitlab.com/org/repo.git"))
	defer cancel()
	ks, err := c.FluxObject(context.Background(), "Kustomization", "flux-system", "apps")
	if err != nil || ks.GetUID() != "ks-1" {
		t.Fatalf("Kustomization = %v, %v", ks, err)
	}
	src, err := c.FluxObject(context.Background(), "GitRepository", "flux-system", "flux-system")
	if err != nil {
		t.Fatal(err)
	}
	if url, _, _ := unstructured.NestedString(src.Object, "spec", "url"); url != "https://gitlab.com/org/repo.git" {
		t.Fatalf("source url %q", url)
	}
}

func TestFluxObjectSameNameRecreationIsANewUID(t *testing.T) {
	ctx := context.Background()
	c, dyn, cancel := splitConn(t, withObjUID(ksObj("apps"), "old"))
	defer cancel()
	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	if err := dyn.Resource(gvr).Namespace("flux-system").Delete(ctx, "apps", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(gvr).Namespace("flux-system").Create(ctx, withObjUID(ksObj("apps"), "new"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if u, err := c.FluxObject(ctx, "Kustomization", "flux-system", "apps"); err != nil || u.GetUID() != "new" {
		t.Fatalf("a recreation is read as the new object: %v %v", u, err)
	}
}

func TestFluxObjectNotFoundIsNotForbidden(t *testing.T) {
	ctx := context.Background()
	c, dyn, cancel := splitConn(t)
	defer cancel()
	if _, err := c.FluxObject(ctx, "Kustomization", "flux-system", "gone"); !apierrors.IsNotFound(err) {
		t.Fatalf("a missing Kustomization is NotFound: %v", err)
	}
	dyn.PrependReactor("get", "kustomizations", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations"}, "apps", errors.New("rbac"))
	})
	if _, err := c.FluxObject(ctx, "Kustomization", "flux-system", "apps"); err == nil || apierrors.IsNotFound(err) || !apierrors.IsForbidden(err) {
		t.Fatalf("a forbidden read stays forbidden: %v", err)
	}
}

func TestFluxObjectUnsupportedKindIsAnErrorNotAbsence(t *testing.T) {
	c, _, cancel := splitConn(t)
	defer cancel()
	if _, err := c.FluxObject(context.Background(), "Widget", "flux-system", "x"); err == nil || apierrors.IsNotFound(err) {
		t.Fatalf("an unsupported kind is an error of its own: %v", err)
	}
}

package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gitops/composition"
	"github.com/moomora/klyx/internal/gitops/fluxop"
)

// verbConn serves group/version/resource in discovery and holds one object
// of kind, recording every merge patch.
func verbConn(t *testing.T, group, version, resource, kind string, served ...*metav1.APIResourceList) (*ClusterConn, *[]map[string]any) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/" + version, "kind": kind,
		"metadata": map[string]any{"namespace": "flux-system", "name": "x", "uid": "u1"}}}
	typed := fake.NewSimpleClientset()
	if served == nil {
		served = []*metav1.APIResourceList{{GroupVersion: group + "/" + version, APIResources: []metav1.APIResource{{Name: resource, Namespaced: true, Kind: kind}}}}
	}
	typed.Resources = served
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: kind + "List"}, obj)
	var patches []map[string]any
	dyn.PrependReactor("patch", resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
		var m map[string]any
		_ = json.Unmarshal(a.(k8stesting.PatchAction).GetPatch(), &m)
		patches = append(patches, m)
		return true, obj, nil
	})
	c := NewClusterConn("x", typed, nil, dyn, nil, clock.Real{}, config.MetricsConfig{})
	c.ctx = context.Background()
	return c, &patches
}

// Coverage S1c §5: a generator's verbs are source-watcher's annotations
// under its own key, UID-guarded; only the version Klyx reads is written.
func TestArtifactGeneratorVerbs(t *testing.T) {
	old := fluxOpToken
	t.Cleanup(func() { fluxOpToken = old })
	fluxOpToken = func() string { return "2026-10-17T11:00:00.1Z" }
	c, patches := verbConn(t, composition.GeneratorGroup, composition.GeneratorVersion, "artifactgenerators", composition.KindGenerator)
	for _, verb := range []string{FluxOpReconcile, FluxOpSuspend, FluxOpResume} {
		if err := c.FluxOpAct(context.Background(), composition.KindGenerator, "flux-system", "x", "u1", verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	want := []map[string]any{
		{fluxop.AnnRequestedAt: "2026-10-17T11:00:00.1Z"},
		{composition.AnnReconcile: "disabled"},
		{composition.AnnReconcile: "enabled", fluxop.AnnRequestedAt: "2026-10-17T11:00:00.1Z"},
	}
	if len(*patches) != len(want) {
		t.Fatalf("patches: %v", *patches)
	}
	for i, p := range *patches {
		if p["metadata"].(map[string]any)["uid"] != "u1" {
			t.Errorf("patch %d is not UID-guarded: %v", i, p)
		}
		a := annOf(p)
		if len(a) != len(want[i]) {
			t.Errorf("patch %d: %v, want %v", i, a, want[i])
		}
		for k, v := range want[i] {
			if a[k] != v {
				t.Errorf("patch %d: %s=%v, want %v", i, k, a[k], v)
			}
		}
	}
	for _, verb := range []string{FluxOpFetch, FluxOpRefresh} {
		if err := c.FluxOpAct(context.Background(), composition.KindGenerator, "flux-system", "x", "u1", verb); err == nil {
			t.Errorf("a generator has no %s", verb)
		}
	}
	if err := c.FluxOpAct(context.Background(), composition.KindGenerator, "flux-system", "x", "other", FluxOpReconcile); !errors.Is(err, ErrReplaced) {
		t.Errorf("replaced: %v", err)
	}
	// another served version is never written
	v1, p1 := verbConn(t, composition.GeneratorGroup, composition.GeneratorVersion, "artifactgenerators", composition.KindGenerator,
		&metav1.APIResourceList{GroupVersion: composition.GeneratorGroup + "/v1", APIResources: []metav1.APIResource{{Name: "artifactgenerators", Namespaced: true, Kind: composition.KindGenerator}}})
	if err := v1.FluxOpAct(context.Background(), composition.KindGenerator, "flux-system", "x", "u1", FluxOpSuspend); err == nil || !strings.Contains(err.Error(), "does not serve artifactgenerators at v1beta1") || len(*p1) != 0 {
		t.Fatalf("another version: %v (%d patches)", err, len(*p1))
	}
}

// S1c §5: a Receiver reconciles by requestedAt and suspends by
// spec.suspend; a Provider and an Alert only suspend; none reconciles
// with a source; an ExternalArtifact has no verb.
func TestNotificationVerbs(t *testing.T) {
	rc, rp := verbConn(t, "notification.toolkit.fluxcd.io", "v1", "receivers", "Receiver")
	if err := rc.Reconcile(context.Background(), "Receiver", "flux-system", "x", "u1"); err != nil {
		t.Fatalf("receiver reconcile: %v", err)
	}
	if err := rc.SetSuspend(context.Background(), "Receiver", "flux-system", "x", "u1", true); err != nil {
		t.Fatalf("receiver suspend: %v", err)
	}
	if len(*rp) != 2 || annOf((*rp)[0])["reconcile.fluxcd.io/requestedAt"] == nil || (*rp)[1]["spec"].(map[string]any)["suspend"] != true {
		t.Fatalf("receiver patches: %v", *rp)
	}
	if err := rc.ReconcileWithSource(context.Background(), "Receiver", "flux-system", "x", "u1"); err == nil {
		t.Error("a Receiver binds no source")
	}
	for _, kind := range []string{"Provider", "Alert"} {
		c, p := verbConn(t, "notification.toolkit.fluxcd.io", "v1beta3", strings.ToLower(kind)+"s", kind)
		if err := c.Reconcile(context.Background(), kind, "flux-system", "x", "u1"); err == nil || !strings.Contains(err.Error(), "static") || len(*p) != 0 {
			t.Fatalf("%s reconcile: %v", kind, err)
		}
		if err := c.SetSuspend(context.Background(), kind, "flux-system", "x", "u1", false); err != nil {
			t.Fatalf("%s resume: %v", kind, err)
		}
		if len(*p) != 1 || (*p)[0]["spec"].(map[string]any)["suspend"] != false || (*p)[0]["metadata"].(map[string]any)["uid"] != "u1" {
			t.Fatalf("%s patches: %v", kind, *p)
		}
	}
	if err := rc.Reconcile(context.Background(), "ExternalArtifact", "flux-system", "x", "u1"); err == nil {
		t.Error("an ExternalArtifact is reconciled by its producer")
	}
}

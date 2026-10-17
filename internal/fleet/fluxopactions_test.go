package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/gitops/fluxop"
)

func fluxOpConn(t *testing.T, kind, resource string) (*ClusterConn, *[]map[string]any) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: fluxop.Group, Version: fluxop.Version, Resource: resource}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "fluxcd.controlplane.io/v1", "kind": kind,
		"metadata": map[string]any{"namespace": "team", "name": "x", "uid": "u1"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: kind + "List"}, obj)
	var patches []map[string]any
	dyn.PrependReactor("patch", resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
		var m map[string]any
		_ = json.Unmarshal(a.(k8stesting.PatchAction).GetPatch(), &m)
		patches = append(patches, m)
		return true, obj, nil
	})
	return NewClusterConn("x", nil, nil, dyn, nil, clock.Real{}, config.MetricsConfig{}), &patches
}

func annOf(p map[string]any) map[string]any {
	md, _ := p["metadata"].(map[string]any)
	a, _ := md["annotations"].(map[string]any)
	return a
}

// Coverage S1a §5: each verb writes its annotations in one UID-guarded
// patch, the matrix refuses what a kind does not support, and a replaced
// object is never written.
func TestFluxOpVerbs(t *testing.T) {
	old := fluxOpToken
	t.Cleanup(func() { fluxOpToken = old })
	fluxOpToken = func() string { return "2026-10-17T10:00:00.123456789Z" }

	c, patches := fluxOpConn(t, fluxop.KindProvider, "resourcesetinputproviders")
	for _, verb := range []string{FluxOpFetch, FluxOpResume, FluxOpSuspend, FluxOpReconcile} {
		if err := c.FluxOpAct(context.Background(), fluxop.KindProvider, "team", "x", "u1", verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	want := []map[string]any{
		{fluxop.AnnRequestedAt: "2026-10-17T10:00:00.123456789Z", fluxop.AnnForceAt: "2026-10-17T10:00:00.123456789Z"},
		{fluxop.AnnReconcile: "enabled", fluxop.AnnRequestedAt: "2026-10-17T10:00:00.123456789Z"},
		{fluxop.AnnReconcile: "disabled"},
		{fluxop.AnnRequestedAt: "2026-10-17T10:00:00.123456789Z"},
	}
	for i, p := range *patches {
		md := p["metadata"].(map[string]any)
		if md["uid"] != "u1" {
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
	// the matrix
	if err := c.FluxOpAct(context.Background(), fluxop.KindProvider, "team", "x", "u1", FluxOpRefresh); err == nil {
		t.Error("a provider has no refresh")
	}
	rs, _ := fluxOpConn(t, fluxop.KindSet, "resourcesets")
	if err := rs.FluxOpAct(context.Background(), fluxop.KindSet, "team", "x", "u1", FluxOpFetch); err == nil {
		t.Error("a ResourceSet has no forced fetch")
	}
	// a replaced object: nothing written
	n := len(*patches)
	if err := c.FluxOpAct(context.Background(), fluxop.KindProvider, "team", "x", "other", FluxOpReconcile); !errors.Is(err, ErrReplaced) || len(*patches) != n {
		t.Errorf("replaced: %v (%d patches)", err, len(*patches)-n)
	}
	if err := c.FluxOpAct(context.Background(), fluxop.KindProvider, "team", "x", "", FluxOpReconcile); err == nil {
		t.Error("no armed identity")
	}
}

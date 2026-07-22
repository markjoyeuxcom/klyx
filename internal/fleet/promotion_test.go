package fleet

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/clock"
	"github.com/moomora/klyx/internal/config"
)

var (
	gitGVRProm = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	ociGVRProm = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "ocirepositories"}
)

func promKS(name, revision, sourceName string, ready bool) *unstructured.Unstructured {
	readyStatus := "False"
	if ready {
		readyStatus = "True"
	}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system"},
		"spec": map[string]interface{}{
			"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": sourceName},
		},
		"status": map[string]interface{}{
			"lastAppliedRevision": revision,
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": readyStatus, "message": "ok"},
			},
		},
	}}
}

func promGitRepo(name, revision string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "source.toolkit.fluxcd.io/v1",
		"kind":       "GitRepository",
		"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system"},
		"status": map[string]interface{}{
			"artifact": map[string]interface{}{"revision": revision},
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
		},
	}}
}

func newPromConn(fluxPresent bool, objs ...runtime.Object) *ClusterConn {
	listKinds := map[schema.GroupVersionResource]string{
		ksGVRSummary: "KustomizationList",
		hrGVRSummary: "HelmReleaseList",
		gitGVRProm:   "GitRepositoryList",
		ociGVRProm:   "OCIRepositoryList",
	}
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
	typed := typedfake.NewSimpleClientset()
	det := capability.NewDetector(typed)
	c := NewClusterConn("x", typed, nil, dyn, det, clock.Real{}, config.MetricsConfig{})
	c.mu.Lock()
	c.caps.GitOps.Flux.Present = fluxPresent
	c.mu.Unlock()
	return c
}

func TestGitOpsPromotion_UnitsAndSources(t *testing.T) {
	c := newPromConn(true,
		promKS("apps", "main@sha1:abc123", "fleet-repo", true),
		promKS("infra", "main@sha1:older9", "fleet-repo", false),
		promGitRepo("fleet-repo", "main@sha1:abc123"),
	)
	snap, err := c.GitOpsPromotion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.FluxPresent || snap.ArgoPresent {
		t.Fatalf("presence: %+v", snap)
	}
	if len(snap.Units) != 2 {
		t.Fatalf("units: %+v", snap.Units)
	}
	apps := snap.Units[0]
	if apps.Name != "apps" || apps.Revision != "main@sha1:abc123" || apps.Ready != "ready" || apps.SourceName != "fleet-repo" {
		t.Fatalf("apps unit: %+v", apps)
	}
	if snap.Units[1].Ready != "not-ready" {
		t.Fatalf("infra unit: %+v", snap.Units[1])
	}
	if len(snap.Sources) != 1 || snap.Sources[0].Revision != "main@sha1:abc123" || snap.Sources[0].Kind != "GitRepository" {
		t.Fatalf("sources: %+v", snap.Sources)
	}
}

func TestGitOpsPromotion_NeitherToolIsHonestEmpty(t *testing.T) {
	c := newPromConn(false)
	snap, err := c.GitOpsPromotion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.FluxPresent || snap.ArgoPresent || len(snap.Units) != 0 || len(snap.Sources) != 0 {
		t.Fatalf("want honest empty, got %+v", snap)
	}
}

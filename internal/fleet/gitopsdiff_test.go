package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/fluxcli"
)

type fakeFluxRunner struct {
	stdout string
	stderr string
	err    error
}

func (f *fakeFluxRunner) Run(_ context.Context, _ ...string) ([]byte, string, error) {
	return []byte(f.stdout), f.stderr, f.err
}

func seedKs(name string, suspend bool, ready string, path string) *unstructured.Unstructured {
	conds := []interface{}{map[string]interface{}{"type": "Ready", "status": ready}}
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]interface{}{"name": name, "namespace": "flux-system"},
		"spec":       map[string]interface{}{"suspend": suspend, "path": path},
		"status":     map[string]interface{}{"conditions": conds},
	}}
}

func TestFluxDiffGatedToSuspendedOrFailing(t *testing.T) {
	prev := newFluxRunner
	defer func() { newFluxRunner = prev }()
	runner := &fakeFluxRunner{stdout: "± spec.replicas: 2 -> 3", err: errors.New("exit 1")}
	newFluxRunner = func([]string) fluxcli.Runner { return runner }

	listKinds := ksListKinds()
	// A suspended Kustomization -> diff is allowed and returns changes.
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKs("app", true, "True", "./apps"))
	c := newActionConn(dyn)
	res, err := c.FluxDiffKustomization(context.Background(), "flux-system", "app", "")
	if err != nil {
		t.Fatalf("suspended diff: %v", err)
	}
	if !res.HasChanges {
		t.Fatalf("want changes, got %+v", res)
	}

	// A healthy (Ready, not suspended) Kustomization -> refused, no shell-out.
	dyn2 := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme(), listKinds, seedKs("ok", false, "True", "./apps"))
	c2 := newActionConn(dyn2)
	if _, err := c2.FluxDiffKustomization(context.Background(), "flux-system", "ok", ""); err == nil || !strings.Contains(err.Error(), "suspended or failing") {
		t.Fatalf("want gate refusal, got %v", err)
	}
}

func ksListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{ksGVR(): "KustomizationList"}
}

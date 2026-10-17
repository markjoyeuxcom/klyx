package healthsrc

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/gitops/fluxop"
)

// Coverage S1a §3's calibration: the budget counts encoded bytes, so the
// heap actually held per encoded byte is measured and recorded, and the
// transient cost of one decoded list page (templates included, before the
// projection drops them) is measured apart. Run with
//   go test ./internal/healthsrc -run TestFluxOperatorHeapCalibration -v
// and record the logged figures in the S1a note's As built.

func calibrationSet(i int, template int) *unstructured.Unstructured {
	deps := []any{}
	for d := 0; d < 3; d++ {
		deps = append(deps, map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "name": fmt.Sprintf("infra-%d", d), "namespace": "flux-system", "ready": true})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSet",
		"metadata": map[string]any{"name": fmt.Sprintf("tenant-%04d", i), "namespace": fmt.Sprintf("team-%02d", i%40), "uid": fmt.Sprintf("uid-%06d", i),
			"generation": int64(3), "creationTimestamp": "2026-10-17T10:00:00Z", "resourceVersion": "123456",
			"labels": map[string]any{"kustomize.toolkit.fluxcd.io/name": "tenants", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}},
		"spec": map[string]any{
			"inputsFrom":        []any{map[string]any{"apiVersion": "fluxcd.controlplane.io/v1", "kind": "ResourceSetInputProvider", "name": "branches"}},
			"dependsOn":         deps,
			"resourcesTemplate": strings.Repeat("x", template),
		},
		"status": map[string]any{
			"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded",
				"message": "Reconciliation finished in 2s", "lastTransitionTime": "2026-10-17T10:00:00Z", "observedGeneration": int64(3)}},
			"lastAppliedRevision": "sha256:" + strings.Repeat("a", 64),
			"inventory":           map[string]any{"entries": []any{map[string]any{"id": "team_app_apps_Deployment", "v": "v1"}}},
		},
	}}
}

func heapNow() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func TestFluxOperatorHeapCalibration(t *testing.T) {
	if testing.Short() {
		t.Skip("calibration")
	}
	project := fluxop.Project(fluxop.KindSet)
	// retained: projections up to the ResourceSets budget, the sources gone
	before := heapNow()
	var held []any
	encoded := 0
	for i := 0; encoded < budgetSets; i++ {
		v, err := project(calibrationSet(i, 0))
		if err != nil {
			t.Fatal(err)
		}
		n := fluxop.SizeOf(v)
		if encoded+n > budgetSets {
			break
		}
		held = append(held, v)
		encoded += n
	}
	after := heapNow()
	ratio := float64(after-before) / float64(encoded)
	t.Logf("retained: %d ResourceSet projections, %d encoded bytes (budget %d), heap %d bytes, %.2f heap bytes per encoded byte",
		len(held), encoded, budgetSets, after-before, ratio)
	runtime.KeepAlive(held)
	held = nil

	// transient: one list page of 500 ResourceSets with 64 KiB templates,
	// decoded before projection (the reflector's default page size)
	before = heapNow()
	page := make([]*unstructured.Unstructured, 0, 500)
	for i := 0; i < 500; i++ {
		page = append(page, calibrationSet(i, 64<<10))
	}
	peak := heapNow()
	t.Logf("transient: one 500-object page with 64 KiB templates holds %d heap bytes before projection", peak-before)
	runtime.KeepAlive(page)
	if ratio <= 0 || ratio > 10 {
		t.Fatalf("an implausible ratio %.2f", ratio)
	}
}

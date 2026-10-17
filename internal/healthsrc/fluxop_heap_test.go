package healthsrc

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	krt "k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/gitops/fluxop"
	"github.com/moomora/klyx/internal/observe"
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

// The whole Observer, not a slice of projections (Codex's S1a
// implementation review, the heap question): a cluster holding more
// ResourceSets than the budget admits, read through the real kinds, parts,
// rows and tracker. Retained is the heap the running Observer holds after
// its first complete list, with both parts' rows subscribed, over a
// baseline taken with the fake client already holding every object. The
// list's transient peak is not measured here: the fake shares its strings
// with what it returns, where a real decode allocates them. It is the
// decoded list (the pager and the watch list both hold it whole before the
// model is replaced), measured per page by TestFluxOperatorHeapCalibration.
//
//	go test ./internal/healthsrc -run TestFluxOperatorObserverHeap -v
func TestFluxOperatorObserverHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("calibration")
	}
	for _, tc := range []struct {
		n, template int
	}{{1000, 4 << 10}, {1000, 64 << 10}} {
		objs := make([]krt.Object, 0, tc.n)
		for i := 0; i < tc.n; i++ {
			objs = append(objs, calibrationSet(i, tc.template))
		}
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(krt.NewScheme(), fluxopListKinds(), objs...)
		objs = nil
		served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
			return observe.Probe{Served: group == fluxop.Group, Version: fluxop.Version}, nil
		}
		kinds, parts := FluxOperator(NativeDeps{Cluster: "c1", Dyn: dyn, Served: served})
		s := &sink{}
		o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: []observe.DomainSpec{{ID: TaskFlux, Parts: parts}}, Throttle: 10 * time.Millisecond})
		before := heapNow()
		ctx, cancel := context.WithCancel(context.Background())
		o.Start(ctx)
		o.SubscribeRows(TaskFlux, "resourcesets", observe.Owner{Kind: "lens", ID: "t"})
		o.SubscribeRows(TaskFlux, "fluxinstance", observe.Owner{Kind: "lens", ID: "t"})
		var kept, left int
		waitFor(t, "the first complete list, capped", func() bool {
			p, ok := nativePart(s, TaskFlux, "resourcesets")
			if !ok || p.Coverage != observe.Live {
				return false
			}
			for _, k := range p.Kinds {
				if k.Kind == KindResourceSets && k.Truncated {
					kept, left = k.Objects, k.NotRetained
					return true
				}
			}
			return false
		})
		if _, ok := o.RowsNow(TaskFlux, "resourcesets"); !ok {
			t.Fatal("rows not published")
		}
		after := heapNow()
		retained := int64(after) - int64(before)
		t.Logf("%d ResourceSets with %d KiB templates: %d retained, %d not retained; the Observer holds %d heap bytes (%.2f MiB) against a %d-byte ResourceSet budget, %.2f heap bytes per budget byte",
			tc.n, tc.template>>10, kept, left, retained, float64(retained)/(1<<20), budgetSets, float64(retained)/float64(budgetSets))
		if kept+left != tc.n || kept == 0 {
			t.Fatalf("admission: %d kept, %d left of %d", kept, left, tc.n)
		}
		cancel()
		<-o.Done()
		runtime.KeepAlive(dyn)
	}
}

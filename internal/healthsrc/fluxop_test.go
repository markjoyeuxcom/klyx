package healthsrc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/gitops/fluxop"
	"github.com/moomora/klyx/internal/observe"
)

var fluxopGVR = func(res string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: fluxop.Group, Version: fluxop.Version, Resource: res}
}

func fluxopListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		fluxopGVR("fluxinstances"): "FluxInstanceList", fluxopGVR("fluxreports"): "FluxReportList",
		fluxopGVR("resourcesets"): "ResourceSetList", fluxopGVR("resourcesetinputproviders"): "ResourceSetInputProviderList",
	}
}

func opObj(kind, ns, name string, disabled bool, ready map[string]any, spec map[string]any) *unstructured.Unstructured {
	if spec == nil {
		spec = map[string]any{}
	}
	u := cr("fluxcd.controlplane.io/v1", kind, ns, name, spec, []any{ready}, nil)
	if disabled {
		u.SetAnnotations(map[string]string{fluxop.AnnReconcile: "disabled"})
	}
	return u
}

func readyCond(status, reason, msg, at string) map[string]any {
	return map[string]any{"type": "Ready", "status": status, "reason": reason, "message": msg, "lastTransitionTime": at, "observedGeneration": int64(1)}
}

type opHarness struct {
	dyn *dynamicfake.FakeDynamicClient
	s   *sink
	o   *observe.Observer
}

func startFluxop(t *testing.T, objs ...runtime.Object) *opHarness {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), fluxopListKinds(), objs...)
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		return observe.Probe{Served: group == fluxop.Group, Version: fluxop.Version}, nil
	}
	kinds, parts := FluxOperator(NativeDeps{Cluster: "c1", Dyn: dyn, Served: served})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: []observe.DomainSpec{{ID: TaskFlux, Parts: parts}}, Throttle: 10 * time.Millisecond})
	o.SetFact(FactFluxOperator, capability.FluxOperatorInfo{Present: true, Verified: true, Complete: true,
		Operators: []capability.FluxOperator{{Namespace: "flux-system", Name: "flux-operator", RuntimeNamespace: "flux-system", Ready: true}}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	o.SubscribeRows(TaskFlux, "resourcesets", observe.Owner{Kind: "lens", ID: "t"})
	o.SubscribeRows(TaskFlux, "fluxinstance", observe.Owner{Kind: "lens", ID: "t"})
	return &opHarness{dyn: dyn, s: s, o: o}
}

func (h *opHarness) update(t *testing.T, res string, u *unstructured.Unstructured) {
	t.Helper()
	if _, err := h.dyn.Resource(fluxopGVR(res)).Namespace(u.GetNamespace()).Update(context.Background(), u, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func (h *opHarness) setRow(t *testing.T, name string) (Row[FluxSetRow], bool) {
	r, ok := h.o.RowsNow(TaskFlux, "resourcesets")
	if !ok {
		return Row[FluxSetRow]{}, false
	}
	for _, row := range r.Rows.(RowSet[FluxSetRow]).Rows {
		if (row.Data.Set != nil && row.Data.Set.Name == name) || (row.Data.Provider != nil && row.Data.Provider.Name == name) {
			return row, true
		}
	}
	return Row[FluxSetRow]{}, false
}

// S1a §3 and §4 through the observer: binding, counted parts and the
// evidence-only report.
func TestFluxOperatorPartsOnFixtures(t *testing.T) {
	h := startFluxop(t,
		opObj("FluxInstance", "flux-system", "flux", false, readyCond("True", "ReconciliationSucceeded", "ok", "2026-10-17T10:00:00Z"), map[string]any{"distribution": map[string]any{"version": "2.x"}}),
		opObj("FluxInstance", "elsewhere", "flux", false, readyCond("False", "BuildFailed", "boom", "2026-10-17T10:00:00Z"), nil),
		opObj("FluxReport", "flux-system", "flux", false, readyCond("True", "ReconciliationSucceeded", "Reporting finished", "2026-10-17T10:00:00Z"), map[string]any{"distribution": map[string]any{"version": "v2.9.6", "status": "Installed"}}),
		opObj("ResourceSet", "team", "apps", false, readyCond("False", "DependencyNotReady", "Retrying dependency check", "2026-10-17T10:00:00Z"), nil),
		opObj("ResourceSetInputProvider", "team", "prs", false, readyCond("True", "ReconciliationSucceeded", "ok", "2026-10-17T10:00:00Z"), map[string]any{"type": "GitHubPullRequest"}),
	)
	waitFor(t, "instance counted only where an operator runs", func() bool {
		p, ok := nativePart(h.s, TaskFlux, "fluxinstance")
		return ok && p.Coverage == observe.Live && p.Native != nil && p.Native.Total == 1 && p.Native.Included == 0
	})
	waitFor(t, "resourcesets", func() bool {
		p, ok := nativePart(h.s, TaskFlux, "resourcesets")
		return ok && p.Native != nil && p.Native.Total == 2 && p.Native.Included == 1 && p.Top[0].Name == "apps"
	})
	waitFor(t, "report as evidence", func() bool {
		p, ok := nativePart(h.s, TaskFlux, "fluxreport")
		return ok && p.Evidence != nil && *p.Evidence == 1 && p.Native == nil
	})
	r, _ := h.o.RowsNow(TaskFlux, "fluxinstance")
	for _, row := range r.Rows.(RowSet[FluxInstanceRow]).Rows {
		if row.Data.Namespace == "elsewhere" && (row.Data.Binding != capability.BindingOrphan || row.Eval.Label != LabelOrphan) {
			t.Fatalf("orphan row: %+v", row)
		}
	}
}

// S1a §4 (revisions 3 and 4) through the projected Observer path: a resume
// stays awaiting until Ready changes losslessly; a display-clipped message
// cannot manufacture evidence; a change beyond the clip is evidence.
func TestResumeThroughTheObserver(t *testing.T) {
	long := strings.Repeat("m", 2000)
	frozen := readyCond("False", "BuildFailed", long, "2026-10-16T09:00:00Z")
	rs := opObj("ResourceSet", "team", "apps", true, frozen, nil)
	h := startFluxop(t, rs)
	waitFor(t, "suspended", func() bool { row, ok := h.setRow(t, "apps"); return ok && row.Eval.Label == fluxop.LabelSuspended })

	// resumed, nothing evaluated: awaiting, not counted
	rs = rs.DeepCopy()
	rs.SetAnnotations(nil)
	h.update(t, "resourcesets", rs)
	waitFor(t, "awaiting", func() bool {
		row, ok := h.setRow(t, "apps")
		return ok && row.Data.Resume == ResumeAwaiting && row.Eval.Label == fluxop.LabelAwaiting && !row.Eval.Included
	})

	// unrelated growth tightens the display clip: still no evidence
	grown := rs.DeepCopy()
	deps := []any{}
	for i := 0; i < 40; i++ {
		deps = append(deps, map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": fmt.Sprintf("%s-%d", strings.Repeat("d", 180), i)})
	}
	_ = unstructured.SetNestedSlice(grown.Object, deps, "spec", "dependsOn")
	h.update(t, "resourcesets", grown)
	waitFor(t, "clipped further", func() bool {
		row, ok := h.setRow(t, "apps")
		return ok && len(row.Data.Set.DependsOn) > 0 && len(row.Data.Set.Common.Clipped) > 0
	})
	if row, _ := h.setRow(t, "apps"); row.Data.Resume != ResumeAwaiting {
		t.Fatalf("a tighter clip manufactured evidence: %+v", row.Data.Resume)
	}

	// a message differing only beyond the clip is evidence, and final
	changed := grown.DeepCopy()
	_ = unstructured.SetNestedSlice(changed.Object, []any{readyCond("False", "BuildFailed", long+"X", "2026-10-16T09:00:00Z")}, "status", "conditions")
	h.update(t, "resourcesets", changed)
	waitFor(t, "evaluated", func() bool {
		row, ok := h.setRow(t, "apps")
		return ok && row.Data.Resume == "" && row.Eval.Label == "Failed" && row.Eval.Included
	})
}

// S1a §3: past the per-cluster budget a kind reads truncated, counts are
// lower bounds, and the ledger is capped rather than told of deletions.
func TestFluxOperatorBudgetTruncates(t *testing.T) {
	var objs []runtime.Object
	big := []any{}
	for i := 0; i < 30; i++ {
		big = append(big, map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "name": fmt.Sprintf("%s-%d", strings.Repeat("d", 200), i)})
	}
	for i := 0; i < 400; i++ {
		objs = append(objs, opObj("ResourceSet", "team", fmt.Sprintf("rs-%03d", i), false,
			readyCond("True", "ReconciliationSucceeded", "ok", "2026-10-17T10:00:00Z"), map[string]any{"dependsOn": big}))
	}
	h := startFluxop(t, objs...)
	waitFor(t, "truncated", func() bool {
		p, ok := nativePart(h.s, TaskFlux, "resourcesets")
		if !ok || p.Coverage != observe.Live {
			return false
		}
		for _, k := range p.Kinds {
			if k.Kind == KindResourceSets {
				return k.Truncated && k.Objects < 400 && k.NotRetained > 0 && k.Objects+k.NotRetained == 400
			}
		}
		return false
	})
}

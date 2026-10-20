package healthsrc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
)

// Coverage S11b §2 and §3 through the Observer.

func draGVR(r string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: draGroup, Version: "v1", Resource: r}
}

func draObj(kind, ns, name string, spec, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": draGroup + "/v1", "kind": kind,
		"metadata": map[string]any{"name": name, "uid": "uid-" + name}, "spec": spec}}
	if ns != "" {
		u.SetNamespace(ns)
	}
	if status != nil {
		u.Object["status"] = status
	}
	return u
}

func draSlice(name, pool string, count int64, node string, devices ...string) *unstructured.Unstructured {
	devs := []any{}
	for _, d := range devices {
		devs = append(devs, map[string]any{"name": d})
	}
	spec := map[string]any{"driver": "gpu.example.com", "pool": map[string]any{"name": pool, "generation": int64(1), "resourceSliceCount": count}, "devices": devs}
	if node == "" {
		spec["allNodes"] = true
	} else {
		spec["nodeName"] = node
	}
	return draObj("ResourceSlice", "", name, spec, nil)
}

func draClaim(name string, allocated bool, reserved int) *unstructured.Unstructured {
	spec := map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"name": "gpu",
		"exactly": map[string]any{"deviceClassName": "gpu.example.com", "allocationMode": "ExactCount", "count": int64(1)}}}}}
	var status map[string]any
	if allocated {
		var res []any
		for i := 0; i < reserved; i++ {
			res = append(res, map[string]any{"resource": "pods", "name": "user", "uid": "uid-user"})
		}
		status = map[string]any{"allocation": map[string]any{"devices": map[string]any{"results": []any{map[string]any{"request": "gpu", "driver": "gpu.example.com", "pool": "n1", "device": "gpu-0"}}}},
			"reservedFor": res}
	}
	return draObj("ResourceClaim", "lab", name, spec, status)
}

func TestDRAThroughTheObserver(t *testing.T) {
	lists := map[schema.GroupVersionResource]string{draGVR(KindResourceSlices): "ResourceSliceList", draGVR(KindResourceClaims): "ResourceClaimList",
		draGVR(KindDeviceClasses): "DeviceClassList"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), lists)
	res := map[string]string{"ResourceSlice": KindResourceSlices, "ResourceClaim": KindResourceClaims, "DeviceClass": KindDeviceClasses}
	for _, o := range []*unstructured.Unstructured{
		draSlice("n1-gpus", "n1", 1, "n1", "gpu-0", "gpu-1"),
		draSlice("inc-a", "incomplete", 2, "", "x0"),
		draSlice("dup-a", "dup", 2, "", "d0"), draSlice("dup-b", "dup", 2, "", "d0"),
		draClaim("in-use", true, 1), draClaim("waiting", false, 0), draClaim("unused", false, 0),
		draObj("DeviceClass", "", "gpu.example.com", map[string]any{"selectors": []any{map[string]any{"cel": map[string]any{"expression": "device.driver == \"gpu.example.com\""}}},
			"extendedResourceName": "example.com/gpu"}, nil),
	} {
		if _, err := dyn.Resource(draGVR(res[o.GetKind()])).Namespace(o.GetNamespace()).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	claim := "waiting"
	consumer := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "trainer", Namespace: "lab", UID: types.UID("uid-trainer")},
		Spec: corev1.PodSpec{ResourceClaims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimName: &claim}}, Containers: []corev1.Container{{Name: "c", Image: "x"}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
			Reason: corev1.PodReasonUnschedulable, Message: "0/1 nodes are available: 1 cannot allocate all claims. still not schedulable"}}}}
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	served := func(_ context.Context, group, _, _ string) (observe.Probe, error) {
		return observe.Probe{Served: group == draGroup, Version: "v1"}, nil
	}
	core, _ := Core(CoreDeps{Cluster: "c1", Typed: fake.NewClientset(consumer), Meta: metafake.NewSimpleMetadataClient(scheme)})
	kinds, domains := DRA(NativeDeps{Cluster: "c1", Dyn: dyn, Served: served})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: append(core, kinds...), Domains: domains, NamespacesKind: KindNamespaces,
		Throttle: 10 * time.Millisecond, ReprobeEvery: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	o.SetFact(FactServerMinor, 37)
	for _, p := range []string{"claims", "pools", "classes"} {
		o.SubscribeRows(TaskDRA, p, observe.Owner{Kind: "lens", ID: "t"})
	}

	waitFor(t, "claims: one degraded (the pod's only claim cannot be allocated), three in all", func() bool {
		p, ok := nativePart(s, TaskDRA, "claims")
		return ok && p.Native != nil && p.Native.Total == 3 && p.Native.Included == 1
	})
	rows, _ := o.RowsNow(TaskDRA, "claims")
	byName := map[string]Row[DRAClaimRow]{}
	for _, r := range rows.Rows.(RowSet[DRAClaimRow]).Rows {
		byName[r.Data.Claim.Name] = r
	}
	if v := byName["waiting"].Data.Verdict; v.Rule != "claim.cannot-allocate" || len(byName["waiting"].Data.Consumers) != 1 {
		t.Fatalf("waiting: %+v %+v", v, byName["waiting"].Data.Consumers)
	}
	if v := byName["in-use"].Data.Verdict; v.Kind != health.OK || v.Message != "in use by 1" {
		t.Fatalf("in use: %+v", v)
	}
	if v := byName["unused"].Data.Verdict; v.Rule != "claim.unused" {
		t.Fatalf("unused under a complete pods read: %+v", v)
	}

	waitFor(t, "pools: the invalid one included, the incomplete one progressing", func() bool {
		p, ok := nativePart(s, TaskDRA, "pools")
		return ok && p.Native != nil && p.Native.Total == 3 && p.Native.Included == 1
	})
	pools := map[string]DRAPoolRow{}
	prow, _ := o.RowsNow(TaskDRA, "pools")
	for _, r := range prow.Rows.(RowSet[DRAPoolRow]).Rows {
		pools[r.Ref.Name] = r.Data
	}
	if v := pools["gpu.example.com/dup"].Verdict; v.Kind != health.Degraded || !strings.Contains(v.Message, "duplicate device name d0") {
		t.Fatalf("dup: %+v", v)
	}
	if v := pools["gpu.example.com/incomplete"].Verdict; v.Kind != health.Progressing || v.ReclassifyAt != nil {
		t.Fatalf("incomplete, no episode: %+v", v)
	}
	if v := pools["gpu.example.com/n1"].Verdict; v.Kind != health.OK || !strings.Contains(v.Note, "1.36 or 1.37") {
		t.Fatalf("n1: %+v", v)
	}

	// the episode fact moves the incomplete pool past its grace
	o.SetFact(FactDRAEpisodes, map[string]time.Time{dra.EpisodeKey("gpu.example.com", "incomplete", 1): time.Now().Add(-6 * time.Minute)})
	waitFor(t, "the incomplete pool degraded past its grace", func() bool {
		p, ok := nativePart(s, TaskDRA, "pools")
		return ok && p.Native != nil && p.Native.Included == 2
	})

	waitFor(t, "classes: one fact row", func() bool {
		r, ok := s.rowsOf(TaskDRA + "/classes")
		if !ok || r.Part.Evidence == nil || *r.Part.Evidence != 1 {
			return false
		}
		set, _ := r.Rows.(RowSet[DRAClassRow])
		return len(set.Rows) == 1 && set.Rows[0].Data.Class.ExtendedResourceName == "example.com/gpu" && set.Rows[0].Data.Class.Selectors == 1
	})
}

func TestMinorOf(t *testing.T) {
	for in, want := range map[string]int{"v1.37.0": 37, "v1.35.2-eks-abc123": 35, "v1.34.1+k3s1": 34, "": 0, "v2.0.0": 0, "garbage": 0} {
		if got := MinorOf(in); got != want {
			t.Errorf("MinorOf(%q) = %d, want %d", in, got, want)
		}
	}
}

func sizedSlice(s dra.Slice) any { return &dra.Sized[dra.Slice]{V: s} }

// The mutation gate's survivors (S11b): a capped slices read establishes
// no pool, a pool row keeps its 16 worst views, and a template's claim
// is a pod's only when the pod owns it.
func TestDRAPoolsAndConsumersBounds(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var slices []any
	for i := 0; i < 20; i++ {
		slices = append(slices, sizedSlice(dra.Slice{Name: fmt.Sprintf("s%02d", i), UID: fmt.Sprintf("u%02d", i), Driver: "d", Pool: "p", Generation: 1, Count: 20,
			NodeName: fmt.Sprintf("n%02d", i), Devices: []dra.Device{{Name: fmt.Sprintf("g%02d", i)}}}))
	}
	capture := func(complete bool) observe.Capture {
		return observe.Capture{Now: now, Objects: map[string][]any{KindResourceSlices: slices}, Complete: map[string]bool{KindResourceSlices: complete},
			Current: map[string]bool{KindResourceSlices: complete}, Facts: map[string]any{FactServerMinor: 37}, Rows: true}
	}
	rowOf := func(res observe.PartResult) Row[DRAPoolRow] {
		set := res.Rows.(RowSet[DRAPoolRow])
		if len(set.Rows) != 1 {
			t.Fatalf("one pool: %d rows", len(set.Rows))
		}
		return set.Rows[0]
	}
	r := rowOf(evalDRAPools("c1")(capture(true)))
	if r.Data.Verdict.Kind != health.OK || len(r.Data.Pool.Views) != dra.MaxViewsKept || r.Data.Pool.ViewsOmitted != 21-dra.MaxViewsKept || r.Data.Pool.Nodes != 20 {
		t.Fatalf("21 views read, 16 kept, the rest counted: %+v, %d kept, %d omitted", r.Data.Verdict, len(r.Data.Pool.Views), r.Data.Pool.ViewsOmitted)
	}
	r = rowOf(evalDRAPools("c1")(capture(false)))
	if r.Data.Verdict.Rule != "pool.unread" || !r.Data.Unread {
		t.Fatalf("a capped slices read establishes no pool: %+v", r.Data.Verdict)
	}

	// a pod naming a template's claim that another pod owns
	owner := dra.Claim{Namespace: "lab", Name: "p-gpu-x", UID: "u-x", OwnerKind: "Pod", OwnerUID: "uid-other"}
	tmpl, gen := "t", "p-gpu-x"
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "lab", UID: "uid-p"},
		Spec:   corev1.PodSpec{ResourceClaims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: &tmpl}}},
		Status: corev1.PodStatus{ResourceClaimStatuses: []corev1.PodResourceClaimStatus{{Name: "gpu", ResourceClaimName: &gen}}}}
	cons := claimConsumers([]*corev1.Pod{p}, map[string]dra.Claim{"lab/p-gpu-x": owner}, true)
	if len(cons["lab/p-gpu-x"]) != 0 {
		t.Fatalf("a template's claim another pod owns is not this pod's: %+v", cons)
	}
	owner.OwnerUID = "uid-p"
	if cons := claimConsumers([]*corev1.Pod{p}, map[string]dra.Claim{"lab/p-gpu-x": owner}, true); len(cons["lab/p-gpu-x"]) != 1 || !cons["lab/p-gpu-x"][0].Complete {
		t.Fatalf("the pod's own: %+v", cons)
	}
}

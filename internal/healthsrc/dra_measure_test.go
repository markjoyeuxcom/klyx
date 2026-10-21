package healthsrc

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/dra"
	"github.com/moomora/klyx/internal/health"
)

// Coverage S11b §8: the worst shapes, measured. Opt-in with the load test,
// and without -race:
//
//	KLYX_LOAD=1 go test ./internal/healthsrc -run TestDRAShapesMeasured -v

func measureRetained(f func() any, inputs ...any) (any, uint64) {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	v := f()
	runtime.GC()
	runtime.ReadMemStats(&b)
	runtime.KeepAlive(v)
	runtime.KeepAlive(inputs)
	if b.HeapAlloc < a.HeapAlloc {
		return v, 0
	}
	return v, b.HeapAlloc - a.HeapAlloc
}

func measureEncoded(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func measureSlices(n int, count int64, nodeLocal bool) []dra.Slice {
	out := make([]dra.Slice, 0, n)
	for i := 0; i < n; i++ {
		spec := map[string]any{"driver": "gpu.example.com", "pool": map[string]any{"name": "big", "generation": int64(1), "resourceSliceCount": count},
			"devices": []any{map[string]any{"name": fmt.Sprintf("gpu-%04d", i)}}}
		if nodeLocal {
			spec["nodeName"] = fmt.Sprintf("node-%04d", i)
		} else {
			spec["allNodes"] = true
		}
		out = append(out, dra.ProjectSlice(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": fmt.Sprintf("s-%04d", i), "uid": fmt.Sprintf("u-%04d", i)}, "spec": spec}}))
	}
	return out
}

func TestDRAShapesMeasured(t *testing.T) {
	if os.Getenv("KLYX_LOAD") == "" {
		t.Skip("set KLYX_LOAD=1 to measure S11b's worst shapes")
	}
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "\nS11b §8 worst shapes\n")
	size := 0
	for _, s := range measureSlices(2000, 2000, false) {
		size += dra.SizeOf(&dra.Sized[dra.Slice]{V: s, Size: measureEncoded(t, s)})
	}
	fmt.Fprintf(&b, "  2,000 one-device slices: %d B projected in all (the slices budget %d B)\n", size, budgetResourceSlices)

	// 1. 2,000 slices in one pool of 2,000, offered to all nodes, then each
	// on its own node
	for _, local := range []bool{false, true} {
		slices := measureSlices(2000, 2000, local)
		t0 := time.Now()
		pr, heap := measureRetained(func() any { return dra.ReadPool(slices, 37) }, slices)
		read := time.Since(t0)
		p := pr.(dra.PoolRead)
		v := p.Verdict(time.Time{}, now)
		row := DRAPoolRow{Pool: p.Kept(dra.MaxViewsKept), Verdict: v}
		form := "all nodes"
		if local {
			form = "a node each"
		}
		if v.Kind != health.OK || len(row.Pool.Views)+row.Pool.ViewsOmitted != len(p.Views) || len(row.Pool.Views) > dra.MaxViewsKept {
			t.Fatalf("%s: a whole pool reads complete, its views kept bounded and the rest counted: %+v, %d kept, %d omitted", form, v, len(row.Pool.Views), row.Pool.ViewsOmitted)
		}
		// what the kept row retains once the read itself is dropped (Codex's
		// implementation review, finding 4)
		kr, keptHeap := measureRetained(func() any { return dra.ReadPool(slices, 37).Kept(dra.MaxViewsKept) }, slices)
		runtime.KeepAlive(kr)
		if keptHeap > 64<<10 {
			t.Fatalf("%s: the kept row retains %d B", form, keptHeap)
		}
		fmt.Fprintf(&b, "  one pool of 2,000 slices (%s): %d views read, %d kept, %d counted; row %d B encoded; the read %d B retained, the kept row %d B; read in %v; %s\n",
			form, len(p.Views), len(row.Pool.Views), row.Pool.ViewsOmitted, measureEncoded(t, row), heap, keptHeap, read.Round(time.Microsecond), v.Label)
	}

	// 2. the same pool with one slice missing
	slices := measureSlices(1999, 2000, false)
	t0 := time.Now()
	p := dra.ReadPool(slices, 37)
	v := p.Verdict(now.Add(-6*time.Minute), now)
	read := time.Since(t0)
	if v.Kind != health.Degraded || !strings.Contains(v.Message, "1999 of 2000 slices") {
		t.Fatalf("one slice missing is said, past the grace: %+v", v)
	}
	fmt.Fprintf(&b, "  one pool missing one of 2,000 slices: %q; row %d B encoded; read %v\n", v.Message, measureEncoded(t, DRAPoolRow{Pool: p, Verdict: v}), read.Round(time.Microsecond))

	// 3. 1,000 claims pending on 1,000 pods, each cannot be allocated
	byKey := map[string]dra.Claim{}
	var pods []*corev1.Pod
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("c-%04d", i)
		byKey["ns/"+name] = dra.Claim{Namespace: "ns", Name: name, UID: "u-" + name, Requests: []dra.Request{{Name: "gpu", Class: "gpu.example.com", Mode: "ExactCount", Count: 1}}}
		pods = append(pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p-%04d", i), Namespace: "ns", UID: types.UID(fmt.Sprintf("p-%04d", i))},
			Spec: corev1.PodSpec{ResourceClaims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimName: &name}}},
			Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
				Reason: corev1.PodReasonUnschedulable, Message: "0/50 nodes are available: 50 cannot allocate all claims."}}}})
	}
	t0 = time.Now()
	rr, heap := measureRetained(func() any {
		cons := claimConsumers(pods, byKey, true)
		var rows []DRAClaimRow
		for _, cl := range byKey {
			v, readings := dra.ClaimVerdict(cl, cons[cl.Key()], true, now)
			rows = append(rows, DRAClaimRow{Claim: cl, Verdict: v, Consumers: readings})
		}
		return rows
	}, pods, byKey)
	eval := time.Since(t0)
	rows := rr.([]DRAClaimRow)
	for _, r := range rows {
		if r.Verdict.Rule != "claim.cannot-allocate" {
			t.Fatalf("each pending claim reads its pod's reason: %+v", r.Verdict)
		}
	}
	fmt.Fprintf(&b, "  1,000 claims pending on 1,000 pods: rows %d B encoded, %d B retained; evaluated in %v\n", measureEncoded(t, rows), heap, eval.Round(time.Microsecond))

	// 4. one claim with 256 consumers
	var reserved []any
	for i := 0; i < 256; i++ {
		reserved = append(reserved, map[string]any{"resource": "pods", "name": fmt.Sprintf("user-%03d", i), "uid": fmt.Sprintf("uid-%03d", i)})
	}
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "shared", "namespace": "ns", "uid": "u-shared"},
		"spec":   map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"name": "nic", "exactly": map[string]any{"deviceClassName": "nic", "allocationMode": "ExactCount", "count": int64(1)}}}}},
		"status": map[string]any{"allocation": map[string]any{"devices": map[string]any{"results": []any{map[string]any{"request": "nic", "driver": "d", "pool": "p", "device": "nic-0"}}}}, "reservedFor": reserved}}}
	cl := dra.ProjectClaim(u)
	v, _ = dra.ClaimVerdict(cl, nil, true, now)
	if cl.ReservedCount != 256 || len(cl.Reserved) != dra.MaxReservedKept || v.Message != "in use by 256" {
		t.Fatalf("256 consumers: counted, 8 kept, said: %d %d %+v", cl.ReservedCount, len(cl.Reserved), v)
	}
	fmt.Fprintf(&b, "  one claim with 256 consumers: %d kept, %q; projection %d B encoded\n", len(cl.Reserved), v.Message, measureEncoded(t, cl))
	t.Log(b.String())
}

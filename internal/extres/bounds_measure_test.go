package extres_test

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/extres"
	"github.com/moomora/klyx/internal/health"
)

// Coverage S11a §8: the worst shapes, measured at the bounds. Opt-in with
// the load test, and without -race:
//
//	KLYX_LOAD=1 go test ./internal/extres -run TestBoundsMeasured -v

// longName is the longest extended resource name Kubernetes counts, 308
// bytes, distinct per i: IsExtendedResourceName requires "requests." plus
// the name to be a qualified name, so the domain is at most 253 - 9 = 244
// bytes, then a slash and a 63-byte name. A longer node key is valid in
// node status but is not an extended resource, to the scheduler or Klyx.
func longName(i int) string {
	tag := fmt.Sprintf("%08d", i)
	label := func(n int) string { return strings.Repeat("a", n) }
	domain := label(63) + "." + label(63) + "." + label(63) + "." + label(52-len(tag)) + tag
	return domain + "/" + label(63-len(tag)) + tag
}

// retained is the heap f's result holds after a GC. The inputs are kept
// alive across it: freed inputs would read as a negative cost.
func retained(f func() any, inputs ...any) (any, uint64) {
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

func encoded(t *testing.T, v any) int {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func measureNode(name string, names []string, gapEvery int) *corev1.Node {
	capacity := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}
	alloc := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}
	for i, n := range names {
		capacity[corev1.ResourceName(n)] = resource.MustParse("8")
		a := "8"
		if gapEvery > 0 && i%gapEvery == 0 {
			a = "7"
		}
		alloc[corev1.ResourceName(n)] = resource.MustParse(a)
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)},
		Status: corev1.NodeStatus{Capacity: capacity, Allocatable: alloc, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
}

func measurePod(name, node string, names []string) *corev1.Pod {
	rl := corev1.ResourceList{}
	for _, n := range names {
		rl[corev1.ResourceName(n)] = resource.MustParse("1")
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "m", UID: types.UID("uid-" + name)},
		Spec:   corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "c", Resources: corev1.ResourceRequirements{Requests: rl, Limits: rl}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
}

func TestBoundsMeasured(t *testing.T) {
	if os.Getenv("KLYX_LOAD") == "" {
		t.Skip("set KLYX_LOAD=1 to measure S11a's worst shapes")
	}
	if n := len(longName(0)); n != 308 || !extres.IsExtended(corev1.ResourceName(longName(0))) || extres.IsExtended(corev1.ResourceName("x"+longName(0))) {
		t.Fatalf("a long name is %d bytes, and the longest counted", n)
	}
	var b strings.Builder

	// 1. A node advertising 100 names of 308 bytes, half of them with a gap.
	names := make([]string, 100)
	for i := range names {
		names[i] = longName(i)
	}
	node := measureNode("wide", names, 2)
	t0 := time.Now()
	// its retained heap is below the probe's resolution: the entries' names
	// share the node's own keys
	nr, _ := retained(func() any { return extres.OfNode(node) }, node)
	ofNode := time.Since(t0)
	t0 = time.Now()
	v := health.Node(node)
	reading := time.Since(t0)
	res := nr.(extres.NodeResources)
	if len(res.Entries) != extres.MaxNodeEntries || res.Omitted != 100-extres.MaxNodeEntries {
		t.Fatalf("the node's entries are cut at 16 and the rest counted: %d, omitted %d", len(res.Entries), res.Omitted)
	}
	if v.Rule != "node.resources-not-allocatable" || !strings.Contains(v.Message, "and 46 more") {
		t.Fatalf("the reading names 4 and counts the rest: %+v", v)
	}
	fmt.Fprintf(&b, "\nS11a §8 worst shapes\n")
	fmt.Fprintf(&b, "  node, 100 names of 308 B (50 with a gap): summary %d B encoded, OfNode %v; reading %v, message %d B\n",
		encoded(t, res), ofNode.Round(time.Microsecond), reading.Round(time.Microsecond), len(v.Message))

	// 2. 50 pods on one node, each requesting 1,000 distinct names the node
	// does not advertise.
	plain := measureNode("busy", nil, 0)
	var pods []*corev1.Pod
	for p := 0; p < 50; p++ {
		req := make([]string, 1000)
		for i := range req {
			req[i] = longName(p*1000 + i)
		}
		pods = append(pods, measurePod(fmt.Sprintf("p%02d", p), "busy", req))
	}
	t0 = time.Now()
	rr, heap := retained(func() any { return extres.Reserve([]*corev1.Node{plain}, pods) }, plain, pods)
	reserve := time.Since(t0)
	r := rr.(extres.Result)
	if len(r.Rows) != 1 || !r.Rows[0].Untracked || len(r.Rows[0].Entries) != extres.MaxRowEntries || r.Rows[0].Omitted == 0 {
		t.Fatalf("past 256 tracked names the row says so, keeps 16 and counts the rest: %+v", r.Rows)
	}
	fmt.Fprintf(&b, "  one node, 50 pods x 1,000 distinct unadvertised names: row %d B encoded (RowSize %d), untracked %v, omitted %d; result %d B retained; Reserve %v\n",
		encoded(t, r.Rows[0]), extres.RowSize(r.Rows[0]), r.Rows[0].Untracked, r.Rows[0].Omitted, heap, reserve.Round(time.Microsecond))

	// 3. The 2 MiB rows bound: 400 nodes each advertising 16 names of 308
	// bytes, every one requested.
	var nodes []*corev1.Node
	pods = nil
	for n := 0; n < 400; n++ {
		ns := make([]string, 16)
		for i := range ns {
			ns[i] = longName(n*16 + i)
		}
		nodes = append(nodes, measureNode(fmt.Sprintf("n%03d", n), ns, 0))
		pods = append(pods, measurePod(fmt.Sprintf("q%03d", n), fmt.Sprintf("n%03d", n), ns))
	}
	t0 = time.Now()
	rr, heap = retained(func() any { return extres.Reserve(nodes, pods) }, nodes, pods)
	reserve = time.Since(t0)
	r = rr.(extres.Result)
	kept := 0
	for _, row := range r.Rows {
		kept += extres.RowSize(row)
	}
	if r.RowsDropped == 0 || kept > extres.MaxRowsBytes || len(r.Rows)+r.RowsDropped != 400 {
		t.Fatalf("past 2 MiB rows are dropped and counted: %d kept (%d B), %d dropped", len(r.Rows), kept, r.RowsDropped)
	}
	if len(r.Totals) != 0 { // none of these names is an accelerator
		t.Fatalf("totals: %d kept, %d omitted", len(r.Totals), r.TotalsOmitted)
	}
	fmt.Fprintf(&b, "  400 nodes x 16 requested names of 308 B: %d rows kept (%d B by RowSize, %d B encoded), %d dropped; result %d B retained; Reserve %v\n",
		len(r.Rows), kept, encoded(t, r.Rows), r.RowsDropped, heap, reserve.Round(time.Millisecond))
	t.Log(b.String())
}

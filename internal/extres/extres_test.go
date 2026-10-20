package extres

import (
	"fmt"
	"math"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Coverage S11a §9, the pure package.

func rl(kv ...string) corev1.ResourceList {
	out := corev1.ResourceList{}
	for i := 0; i < len(kv); i += 2 {
		out[corev1.ResourceName(kv[i])] = resource.MustParse(kv[i+1])
	}
	return out
}

func node(name string, capacity, allocatable corev1.ResourceList, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), Labels: labels},
		Status: corev1.NodeStatus{Capacity: capacity, Allocatable: allocatable}}
}

func ctr(name string, req corev1.ResourceList) corev1.Container {
	return corev1.Container{Name: name, Resources: corev1.ResourceRequirements{Requests: req, Limits: req}}
}

func sidecar(name string, req corev1.ResourceList) corev1.Container {
	c := ctr(name, req)
	always := corev1.ContainerRestartPolicyAlways
	c.RestartPolicy = &always
	return c
}

func pod(name, nodeName string, containers ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID("uid-" + name)},
		Spec: corev1.PodSpec{NodeName: nodeName, Containers: containers}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
}

func TestNamePredicates(t *testing.T) {
	for _, ok := range []string{"nvidia.com/gpu", "nvidia.com/mig-1g.5gb", "amd.com/cpx_nps4", "example.com/resource", "gpu.intel.com/i915"} {
		if !IsExtended(corev1.ResourceName(ok)) || !IsDRAExtended(corev1.ResourceName(ok)) {
			t.Errorf("%s is extended", ok)
		}
	}
	for _, no := range []string{"cpu", "memory", "hugepages-2Mi", "kubernetes.io/x", "foo.kubernetes.io/x", "requests.nvidia.com/gpu", "nvidia.com/", "/gpu", "bad_domain/x"} {
		if IsExtended(corev1.ResourceName(no)) {
			t.Errorf("%s is not extended", no)
		}
	}
	// the implicit DeviceClass name is a pod's request, never a node's
	dc := corev1.ResourceName(DeviceClassPrefix + "gpu.example.com")
	if IsExtended(dc) || !IsDRAExtended(dc) {
		t.Fatalf("the implicit DRA name: extended %v, DRA %v", IsExtended(dc), IsDRAExtended(dc))
	}
}

func TestAcceleratorsAndUnits(t *testing.T) {
	for _, a := range []string{"nvidia.com/gpu", "nvidia.com/gpu.shared", "nvidia.com/mig-3g.20gb", "nvidia.com/anything", "amd.com/gpu", "amd.com/cpx_nps4",
		"gpu.intel.com/i915", "gpu.intel.com/xe", "habana.ai/gaudi", "aws.amazon.com/neuron", "aws.amazon.com/neuroncore"} {
		if !Accelerator(a) {
			t.Errorf("%s is an accelerator", a)
		}
	}
	for _, n := range []string{"example.com/resource", "devices.kubevirt.io/kvm", "gpu.intel.com/monitoring", "aws.amazon.com/neurondevice-x"} {
		if Accelerator(n) {
			t.Errorf("%s is not an accelerator", n)
		}
	}
	units := map[[2]string]string{
		{"nvidia.com/mig-1g.5gb", "mixed"}: "MIG device", {"nvidia.com/gpu", ""}: "GPU", {"nvidia.com/gpu", "single"}: "MIG device",
		{"nvidia.com/gpu.shared", "none"}: "GPU", {"amd.com/gpu", ""}: "GPU", {"amd.com/cpx_nps4", ""}: "partition", {"amd.com/xpx_nps4", ""}: "",
		{"nvidia.com/renamed", ""}: "", {"example.com/resource", ""}: "",
	}
	for in, want := range units {
		if got := Unit(in[0], in[1]); got != want {
			t.Errorf("Unit%v = %q, want %q", in, got, want)
		}
	}
	if Partition("amd.com/cpx_nps4") != "CPX compute, NPS4 memory" || Partition("example.com/cpx_nps4") != "" {
		t.Fatal("partition wording")
	}
}

func TestOfNodeOrderAndCap(t *testing.T) {
	capacity := rl("cpu", "4", "example.com/a", "1", "nvidia.com/gpu", "8")
	for i := 0; i < 20; i++ {
		capacity[corev1.ResourceName(fmt.Sprintf("vendor.example/r%02d", i))] = resource.MustParse("1")
	}
	n := node("n", capacity, capacity.DeepCopy(), nil)
	r := OfNode(n)
	if len(r.Entries) != MaxNodeEntries || r.Omitted != 22-MaxNodeEntries {
		t.Fatalf("cap: %d entries, %d omitted", len(r.Entries), r.Omitted)
	}
	if r.Entries[0].Name != "nvidia.com/gpu" || !r.Entries[0].Accelerator || r.Entries[1].Name != "example.com/a" {
		t.Fatalf("accelerators first, then by name: %v, %v", r.Entries[0].Name, r.Entries[1].Name)
	}
	// absent keys stay absent
	m := OfNode(node("m", rl("example.com/new", "4"), rl(), nil))
	if m.Entries[0].Capacity == nil || m.Entries[0].Capacity.V != 4 || m.Entries[0].Allocatable != nil {
		t.Fatalf("allocatable not yet reported: %+v", m.Entries[0])
	}
}

func factOf(e Entry, k string) string {
	for _, f := range e.Facts {
		if f.Key == k {
			return f.Value
		}
	}
	return ""
}

func entryOf(r NodeResources, name string) Entry {
	for _, e := range r.Entries {
		if e.Name == name {
			return e
		}
	}
	return Entry{}
}

// NVIDIA's facts are per resource, never borrowed (S11a revision 1).
func TestNVIDIAFactsPerResource(t *testing.T) {
	labels := map[string]string{
		"nvidia.com/mig.strategy":                 "mixed",
		"nvidia.com/gpu.product":                  "NVIDIA-A100-SXM4-80GB",
		"nvidia.com/gpu.replicas":                 "1",
		"nvidia.com/gpu.sharing-strategy":         "none",
		"nvidia.com/mig-1g.5gb.product":           "NVIDIA-A100-SXM4-80GB-MIG-1g.5gb-SHARED",
		"nvidia.com/mig-1g.5gb.replicas":          "4",
		"nvidia.com/mig-1g.5gb.sharing-strategy":  "time-slicing",
		"nvidia.com/mig-2g.10gb.replicas":         "2",
		"nvidia.com/mig-2g.10gb.sharing-strategy": "mps",
	}
	c := rl("nvidia.com/gpu", "2", "nvidia.com/mig-1g.5gb", "28", "nvidia.com/mig-2g.10gb", "6")
	r := OfNode(node("n", c, c.DeepCopy(), labels))
	if gpu := entryOf(r, "nvidia.com/gpu"); gpu.Sharing != "" || gpu.Unit != "GPU" || factOf(gpu, "product") != "NVIDIA-A100-SXM4-80GB" {
		t.Errorf("an unshared full GPU beside shared profiles: %+v", gpu)
	}
	if m := entryOf(r, "nvidia.com/mig-1g.5gb"); m.Sharing != "shared (time-slicing): each MIG device is advertised 4 times, so these count shares of a MIG device" {
		t.Errorf("a shared profile: %q", m.Sharing)
	}
	if m := entryOf(r, "nvidia.com/mig-2g.10gb"); !strings.Contains(m.Sharing, "advertised 2 times") || !strings.Contains(m.Sharing, "(mps)") {
		t.Errorf("a different factor per profile: %q", m.Sharing)
	}
	if r.MIGStrategy != "mixed" {
		t.Errorf("node-wide MIG strategy: %q", r.MIGStrategy)
	}
	// MIG single with sharing: the unit is a MIG device
	single := OfNode(node("s", rl("nvidia.com/gpu", "28"), rl("nvidia.com/gpu", "28"),
		map[string]string{"nvidia.com/mig.strategy": "single", "nvidia.com/gpu.replicas": "4", "nvidia.com/gpu.sharing-strategy": "time-slicing"}))
	if g := single.Entries[0]; g.Unit != "MIG device" || !strings.Contains(g.Sharing, "each MIG device is advertised 4 times") {
		t.Errorf("MIG single: %+v", g)
	}
	// the default rename takes its base's labels; a strategy without a
	// replica count says only that it is shared
	renamed := OfNode(node("r", rl("nvidia.com/gpu.shared", "8"), rl("nvidia.com/gpu.shared", "8"),
		map[string]string{"nvidia.com/gpu.sharing-strategy": "time-slicing", "nvidia.com/gpu.product": "NVIDIA-L4"}))
	if g := renamed.Entries[0]; g.RenamedFrom != "nvidia.com/gpu" || g.Sharing != "shared (replica count not reported)" || factOf(g, "product") != "NVIDIA-L4" {
		t.Errorf("default rename: %+v", g)
	}
	// any other name gets no facts of another resource
	other := OfNode(node("o", rl("nvidia.com/gpu-ts", "8"), rl("nvidia.com/gpu-ts", "8"), map[string]string{"nvidia.com/gpu.replicas": "4"}))
	if g := other.Entries[0]; len(g.Facts) != 0 || g.Sharing != "" || g.Unit != "" {
		t.Errorf("an unfamiliar name: %+v", g)
	}
	// a -SHARED product without a count
	shared := OfNode(node("p", rl("nvidia.com/gpu", "8"), rl("nvidia.com/gpu", "8"), map[string]string{"nvidia.com/gpu.product": "NVIDIA-L4-SHARED"}))
	if shared.Entries[0].Sharing != "shared (replica count not reported)" {
		t.Errorf("-SHARED product: %q", shared.Entries[0].Sharing)
	}
}

func TestAMDFactsAndFactCaps(t *testing.T) {
	labels := map[string]string{
		"amd.com/gpu.product-name":         "Instinct_MI300X",
		"beta.amd.com/gpu.vram":            "192G",
		"amd.com/compute-memory-partition": "cpx_nps4",
		"amd.com/gpu.family.MI300":         "6",
		"amd.com/gpu.family.MI250":         "2",
	}
	c := rl("amd.com/cpx_nps4", "48")
	e := OfNode(node("a", c, c.DeepCopy(), labels)).Entries[0]
	if e.Unit != "partition" || e.Partition != "CPX compute, NPS4 memory" || factOf(e, "product-name") != "Instinct_MI300X" ||
		factOf(e, "vram") != "192G" || factOf(e, "compute-memory-partition") != "cpx_nps4" || factOf(e, "family") != "mixed: MI250 ×2, MI300 ×6" {
		t.Errorf("AMD facts: %+v", e)
	}
	// facts for at most MaxFactResources resources; the rest counted
	labels2 := map[string]string{}
	c2 := corev1.ResourceList{}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("nvidia.com/mig-%dg.%dgb", i+1, i+5)
		c2[corev1.ResourceName(name)] = resource.MustParse("1")
		labels2[name+".product"] = strings.Repeat("p", 80)
	}
	r := OfNode(node("c", c2, c2.DeepCopy(), labels2))
	if r.FactsOmitted != 2 {
		t.Fatalf("facts omitted: %d", r.FactsOmitted)
	}
	if v := factOf(r.Entries[0], "product"); len(v) != MaxFactValue {
		t.Fatalf("a value is clipped to %d: %d", MaxFactValue, len(v))
	}
}

func TestGaps(t *testing.T) {
	cases := []struct {
		name        string
		capacity    corev1.ResourceList
		allocatable corev1.ResourceList
		want        []string
	}{
		{"unhealthy devices", rl("nvidia.com/gpu", "8"), rl("nvidia.com/gpu", "6"), []string{"nvidia.com/gpu 6/8"}},
		{"a manual capacity raised before the kubelet's sync", rl("example.com/dongle", "16"), rl("example.com/dongle", "8"), []string{"example.com/dongle 8/16"}},
		{"0 advertised", rl("nvidia.com/gpu", "0"), rl("nvidia.com/gpu", "0"), nil},
		{"allocatable not yet reported", rl("example.com/new", "4"), rl(), nil},
		{"equal", rl("nvidia.com/gpu", "8"), rl("nvidia.com/gpu", "8"), nil},
		{"cpu is no extended resource", rl("cpu", "8"), rl("cpu", "7"), nil},
	}
	for _, tc := range cases {
		var got []string
		for _, g := range Gaps(node("n", tc.capacity, tc.allocatable, nil)) {
			got = append(got, g.Name+" "+g.Allocatable+"/"+g.Capacity)
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func figuresOf(r Result, nodeName, name string) (Reserved, bool) {
	for _, row := range r.Rows {
		if row.Node != nodeName {
			continue
		}
		for _, e := range row.Entries {
			if e.Name == name {
				return e, true
			}
		}
	}
	return Reserved{}, false
}

func TestReserveFormulaAndClasses(t *testing.T) {
	gpu := rl("nvidia.com/gpu", "8")
	n := node("n", gpu, gpu.DeepCopy(), nil)
	other := node("other", gpu, gpu.DeepCopy(), nil)

	p1 := pod("p1", "n", ctr("a", rl("nvidia.com/gpu", "1")), ctr("b", rl("nvidia.com/gpu", "1")))
	p1.Spec.InitContainers = []corev1.Container{ctr("init", rl("nvidia.com/gpu", "3")), sidecar("side", rl("nvidia.com/gpu", "1"))}
	// regular 1+1, sidecar +1 = 3; init 3 (+0 sidecars before it): max 3
	p2 := pod("p2", "n", ctr("a", rl("nvidia.com/gpu", "1")))
	p2.Spec.InitContainers = []corev1.Container{sidecar("side", rl("nvidia.com/gpu", "1")), ctr("init", rl("nvidia.com/gpu", "2"))}
	// regular 1 + sidecar 1 = 2; init 2 + sidecar before it 1 = 3: max 3
	p3 := pod("p3", "n", ctr("a", rl("nvidia.com/gpu", "1")))
	p3.Spec.Overhead = rl("nvidia.com/gpu", "1") // overhead counts when advertised
	done := pod("done", "n", ctr("a", rl("nvidia.com/gpu", "5")))
	done.Status.Phase = corev1.PodSucceeded
	failed := pod("failed", "n", ctr("a", rl("nvidia.com/gpu", "5")))
	failed.Status.Phase = corev1.PodFailed
	terminating := pod("terminating", "n", ctr("a", rl("nvidia.com/gpu", "1")))
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	unscheduled := pod("pending", "", ctr("a", rl("nvidia.com/gpu", "5")))
	elsewhere := pod("elsewhere", "other", ctr("a", rl("nvidia.com/gpu", "2")))
	gone := pod("gone", "deleted-node", ctr("a", rl("nvidia.com/gpu", "2")))

	r := Reserve([]*corev1.Node{n, other}, []*corev1.Pod{p1, p2, p3, done, failed, terminating, unscheduled, elsewhere, gone})
	e, _ := figuresOf(r, "n", "nvidia.com/gpu")
	if e.Requested != Known(3+3+2+1) || !e.Advertised {
		t.Fatalf("requested on n: %+v (want 9: p1 3, p2 3, p3 1+1 overhead, terminating 1)", e)
	}
	if e2, _ := figuresOf(r, "other", "nvidia.com/gpu"); e2.Requested != Known(2) {
		t.Fatalf("requested on other: %+v", e2)
	}
}

func TestReserveDRAAndNotAdvertised(t *testing.T) {
	gpu := rl("nvidia.com/gpu", "8")
	n := node("n", gpu, gpu.DeepCopy(), nil)
	bare := node("bare", rl("cpu", "4"), rl("cpu", "4"), nil)

	mapped := pod("mapped", "n", ctr("a", rl("nvidia.com/gpu", "2")), ctr("b", rl("nvidia.com/gpu", "1")))
	mapped.Status.ExtendedResourceClaimStatus = &corev1.PodExtendedResourceClaimStatus{ResourceClaimName: "mapped-extended-resources-x",
		RequestMappings: []corev1.ContainerExtendedResourceRequest{{ContainerName: "a", ResourceName: "nvidia.com/gpu", RequestName: "container-0-request-0"},
			{ContainerName: "ghost", ResourceName: "nvidia.com/gpu", RequestName: "r"}}} // a mapping for a container requesting nothing
	notAdv := pod("cluster-level", "n", ctr("a", rl("example.com/license", "3")))
	implicit := pod("implicit", "bare", ctr("a", rl(DeviceClassPrefix+"gpu.example.com", "2")))

	r := Reserve([]*corev1.Node{n, bare}, []*corev1.Pod{mapped, notAdv, implicit})
	e, _ := figuresOf(r, "n", "nvidia.com/gpu")
	if e.Requested != Known(1) || e.ServedByDRA != Known(2) || e.NotAdvertised != Known(0) {
		t.Fatalf("a mapped pair is served by DRA, the unmapped one counts: %+v", e)
	}
	if l, _ := figuresOf(r, "n", "example.com/license"); l.Advertised || l.NotAdvertised != Known(3) || l.Requested != Known(0) {
		t.Fatalf("not advertised: %+v", l)
	}
	// an implicit-only pod on a node with no extended resource still
	// produces its entry (the whole path, not the prefix branch alone)
	if d, ok := figuresOf(r, "bare", DeviceClassPrefix+"gpu.example.com"); !ok || d.ServedByDRA != Known(2) || d.Advertised {
		t.Fatalf("implicit DRA name: %+v %v", d, ok)
	}
}

// Classification precedes the init maximum (Codex's design guidance): a
// DRA-served init pair must not shrink the counted maximum.
func TestReserveClassifiesBeforeTheInitMaximum(t *testing.T) {
	gpu := rl("nvidia.com/gpu", "8")
	n := node("n", gpu, gpu.DeepCopy(), nil)
	p := pod("p", "n", ctr("app", rl("nvidia.com/gpu", "1")))
	p.Spec.InitContainers = []corev1.Container{ctr("init", rl("nvidia.com/gpu", "4"))}
	p.Status.ExtendedResourceClaimStatus = &corev1.PodExtendedResourceClaimStatus{ResourceClaimName: "c",
		RequestMappings: []corev1.ContainerExtendedResourceRequest{{ContainerName: "init", ResourceName: "nvidia.com/gpu", RequestName: "r"}}}
	e, _ := figuresOf(Reserve([]*corev1.Node{n}, []*corev1.Pod{p}), "n", "nvidia.com/gpu")
	// counted: app 1 (the init pair is DRA's); DRA: init 4. Subtracting the
	// DRA 4 from the aggregated max (4) would wrongly read 0.
	if e.Requested != Known(1) || e.ServedByDRA != Known(4) {
		t.Fatalf("classification before the maximum: %+v", e)
	}
}

func TestReserveBounds(t *testing.T) {
	gpu := rl("nvidia.com/gpu", "8")
	n := node("n", gpu, gpu.DeepCopy(), nil)
	// more than MaxTracked distinct names the node does not advertise
	var cs []corev1.Container
	for i := 0; i < MaxTracked+10; i++ {
		cs = append(cs, ctr(fmt.Sprintf("c%03d", i), rl(fmt.Sprintf("vendor.example/r%03d", i), "1")))
	}
	cs = append(cs, ctr("gpu", rl("nvidia.com/gpu", "2")))
	r := Reserve([]*corev1.Node{n}, []*corev1.Pod{pod("p", "n", cs...)})
	row := r.Rows[0]
	if !row.Untracked || len(row.Entries) != MaxRowEntries || row.Omitted != MaxTracked+1-MaxRowEntries {
		t.Fatalf("untracked %v, %d entries, %d omitted", row.Untracked, len(row.Entries), row.Omitted)
	}
	if row.Entries[0].Name != "nvidia.com/gpu" || row.Entries[0].Requested != Known(2) {
		t.Fatalf("advertised first and exact: %+v", row.Entries[0])
	}
	// overflow is not established, never wrapped
	big := pod("big", "n", ctr("a", rl("nvidia.com/gpu", fmt.Sprint(int64(math.MaxInt64)))), ctr("b", rl("nvidia.com/gpu", "1")))
	if e, _ := figuresOf(Reserve([]*corev1.Node{n}, []*corev1.Pod{big}), "n", "nvidia.com/gpu"); e.Requested.OK {
		t.Fatalf("overflow: %+v", e)
	}
	// the per-cluster rows bound: accelerator nodes kept first
	var nodes []*corev1.Node
	long := strings.Repeat("x", 300)
	for i := 0; i < 1500; i++ {
		c := corev1.ResourceList{}
		for j := 0; j < 8; j++ {
			c[corev1.ResourceName(fmt.Sprintf("v%d.example/%s%d", j, long[:50], j))] = resource.MustParse("1")
		}
		nodes = append(nodes, node(fmt.Sprintf("n%04d", i), c, c.DeepCopy(), nil))
	}
	nodes = append(nodes, node("zz-gpu", gpu, gpu.DeepCopy(), nil))
	b := Reserve(nodes, nil)
	if b.RowsDropped == 0 || b.Rows[0].Node != "zz-gpu" {
		t.Fatalf("rows bound: dropped %d, first %s", b.RowsDropped, b.Rows[0].Node)
	}
	size := 0
	for _, row := range b.Rows {
		size += RowSize(row)
	}
	if size > MaxRowsBytes {
		t.Fatalf("kept rows exceed the bound: %d", size)
	}
}

// The totals come from the full input before any cut (S11a revision 2).
func TestTotalsBeforeAnyCut(t *testing.T) {
	// node A has 17 accelerator names, nvidia.com/z sorting last; node B
	// advertises only nvidia.com/z
	a := corev1.ResourceList{}
	for i := 0; i < 16; i++ {
		a[corev1.ResourceName(fmt.Sprintf("nvidia.com/a%02d", i))] = resource.MustParse("1")
	}
	a["nvidia.com/z"] = resource.MustParse("8")
	nodeA := node("a", a, a.DeepCopy(), nil)
	nodeB := node("b", rl("nvidia.com/z", "8"), rl("nvidia.com/z", "8"), nil)
	if r := OfNode(nodeA); r.Omitted != 1 || entryOf(r, "nvidia.com/z").Name != "" {
		t.Fatalf("the fixture cuts nvidia.com/z from node a's list: %+v", r.Omitted)
	}
	res := Reserve([]*corev1.Node{nodeA, nodeB}, []*corev1.Pod{pod("p", "a", ctr("c", rl("nvidia.com/z", "3")))})
	var z Total
	for _, tt := range res.Totals {
		if tt.Name == "nvidia.com/z" {
			z = tt
		}
	}
	if z.Allocatable != Known(16) || z.Capacity != Known(16) || z.Requested != Known(3) {
		t.Fatalf("the full-input total: %+v (the kept rows would sum 8)", z)
	}
	// a dropped reservations row leaves the requested total unchanged
	if res.RowsDropped != 0 {
		t.Fatal("no row dropped here")
	}
	// more than MaxTotals names: the rest are counted
	many := corev1.ResourceList{}
	for i := 0; i < MaxTotals+5; i++ {
		many[corev1.ResourceName(fmt.Sprintf("nvidia.com/mig-%dg.%dgb", i+1, i))] = resource.MustParse("1")
	}
	m := Reserve([]*corev1.Node{node("m", many, many.DeepCopy(), nil)}, nil)
	if len(m.Totals) != MaxTotals || m.TotalsOmitted != 5 {
		t.Fatalf("totals cap: %d kept, %d omitted", len(m.Totals), m.TotalsOmitted)
	}
}

func TestTotalsSurviveADroppedRow(t *testing.T) {
	gpu := rl("nvidia.com/gpu", "4")
	var nodes []*corev1.Node
	var pods []*corev1.Pod
	filler := strings.Repeat("y", 50)
	for i := 0; i < 3000; i++ {
		c := rl("nvidia.com/gpu", "4")
		for j := 0; j < 8; j++ {
			c[corev1.ResourceName(fmt.Sprintf("v%d.example/%s", j, filler))] = resource.MustParse("1")
		}
		name := fmt.Sprintf("n%04d", i)
		nodes = append(nodes, node(name, c, c.DeepCopy(), nil))
		pods = append(pods, pod("p"+name, name, ctr("c", rl("nvidia.com/gpu", "1"))))
	}
	_ = gpu
	res := Reserve(nodes, pods)
	if res.RowsDropped == 0 {
		t.Fatal("the fixture must drop rows")
	}
	if res.Totals[0].Name != "nvidia.com/gpu" || res.Totals[0].Requested != Known(3000) || res.Totals[0].Allocatable != Known(12000) {
		t.Fatalf("totals over every node: %+v", res.Totals[0])
	}
}

func TestOfPod(t *testing.T) {
	p := pod("p", "n", ctr("a", rl("nvidia.com/gpu", "2", "cpu", "1")), ctr("b", rl(DeviceClassPrefix+"gpu.example.com", "1")))
	p.Spec.InitContainers = []corev1.Container{ctr("init", rl("example.com/x", "1"))}
	p.Status.ExtendedResourceClaimStatus = &corev1.PodExtendedResourceClaimStatus{ResourceClaimName: "p-extended-resources-abc",
		RequestMappings: []corev1.ContainerExtendedResourceRequest{{ContainerName: "a", ResourceName: "nvidia.com/gpu", RequestName: "r"}}}
	r := OfPod(p)
	if r.Claim != "p-extended-resources-abc" || len(r.Containers) != 3 || !r.Containers[0].Init || r.Containers[0].Container != "init" {
		t.Fatalf("containers: %+v", r)
	}
	if e := r.Containers[1].Entries[0]; e.Name != "nvidia.com/gpu" || e.Quantity != Known(2) || !e.ServedByDRA {
		t.Fatalf("a mapped pair: %+v", e)
	}
	if e := r.Containers[2].Entries[0]; !e.ServedByDRA {
		t.Fatalf("an implicit name: %+v", e)
	}
	// the bounds
	var cs []corev1.Container
	for i := 0; i < 3; i++ {
		req := corev1.ResourceList{}
		for j := 0; j < 20; j++ {
			req[corev1.ResourceName(fmt.Sprintf("vendor.example/c%dr%02d", i, j))] = resource.MustParse("1")
		}
		cs = append(cs, ctr(fmt.Sprintf("c%d", i), req))
	}
	b := OfPod(pod("b", "n", cs...))
	kept := 0
	omittedPerContainer := 0
	for _, c := range b.Containers {
		kept += len(c.Entries)
		omittedPerContainer += c.Omitted
		if len(c.Entries) > MaxContainerEntries {
			t.Fatalf("per container: %d", len(c.Entries))
		}
	}
	if kept != MaxPodEntries || b.Omitted+omittedPerContainer != 60-MaxPodEntries {
		t.Fatalf("per pod: kept %d, omitted %d+%d", kept, b.Omitted, omittedPerContainer)
	}
}

// qtyOf reads every whole value within int64 exactly, and nothing else.
func TestQtyOf(t *testing.T) {
	for in, want := range map[string]Qty{
		"8": Known(8), "4611686018427387903": Known(4611686018427387903), "9223372036854775807": Known(math.MaxInt64),
		"9223372036854775808": {}, "1.5": {}, "-1": {}, "2k": Known(2000), "1Ki": Known(1024), "100m": {}, "1e3": Known(1000),
	} {
		if got := qtyOf(resource.MustParse(in)); got != want {
			t.Errorf("qtyOf(%s) = %+v, want %+v", in, got, want)
		}
	}
}

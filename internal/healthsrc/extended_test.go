package healthsrc

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/moomora/klyx/internal/extres"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// Coverage S11a §9, the Observer side.

func s11Node(name string, capacity, allocatable map[string]string, labels map[string]string) *corev1.Node {
	rl := func(m map[string]string) corev1.ResourceList {
		out := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("32Gi")}
		for k, v := range m {
			out[corev1.ResourceName(k)] = resource.MustParse(v)
		}
		return out
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), Labels: labels, Annotations: map[string]string{"a": "b"}},
		Status: corev1.NodeStatus{Capacity: rl(capacity), Allocatable: rl(allocatable), Images: []corev1.ContainerImage{{Names: []string{"x"}}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
}

func s11Pod(name, nodeName string, req map[string]string, mapped ...string) *corev1.Pod {
	rl := corev1.ResourceList{}
	for k, v := range req {
		rl[corev1.ResourceName(k)] = resource.MustParse(v)
	}
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "gpu", UID: types.UID("uid-" + name)},
		Spec:   corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "app", Image: "x", Env: []corev1.EnvVar{{Name: "SECRET", Value: "s"}}, Resources: corev1.ResourceRequirements{Requests: rl, Limits: rl}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if len(mapped) > 0 {
		st := &corev1.PodExtendedResourceClaimStatus{ResourceClaimName: name + "-extended-resources-x"}
		for _, m := range mapped {
			st.RequestMappings = append(st.RequestMappings, corev1.ContainerExtendedResourceRequest{ContainerName: "app", ResourceName: m, RequestName: "r"})
		}
		p.Status.ExtendedResourceClaimStatus = st
	}
	return p
}

func TestExtendedThroughTheObserver(t *testing.T) {
	gpuNode := s11Node("gpu-1", map[string]string{"nvidia.com/gpu": "8", "example.com/x": "2"}, map[string]string{"nvidia.com/gpu": "6", "example.com/x": "2"},
		map[string]string{"nvidia.com/gpu.product": "NVIDIA-L4"})
	plain := s11Node("plain", nil, nil, nil)
	objs := []runtime.Object{gpuNode, plain,
		s11Pod("train", "gpu-1", map[string]string{"nvidia.com/gpu": "2"}),
		s11Pod("dra", "gpu-1", map[string]string{"nvidia.com/gpu": "1"}, "nvidia.com/gpu"),
		s11Pod("license", "plain", map[string]string{"example.com/license": "1"}),
	}
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kinds, domains := Core(CoreDeps{Cluster: "c1", Typed: fake.NewClientset(objs...), Meta: metafake.NewSimpleMetadataClient(scheme)})
	s := &sink{}
	o := observe.New(observe.Config{Cluster: "c1", Sink: s, Kinds: kinds, Domains: domains, NamespacesKind: KindNamespaces, Throttle: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-o.Done() })
	o.Start(ctx)
	o.SubscribeRows(TaskExtended, "reservations", observe.Owner{Kind: "lens", ID: "nodes"})
	o.SubscribeRows(TaskExtended, "totals", observe.Owner{Kind: "lens", ID: "nodes"})

	var res RowSet[ReservationRow]
	waitFor(t, "the reservations: one row per node with extended resources, both reads live", func() bool {
		r, ok := s.rowsOf(TaskExtended + "/reservations")
		if !ok || r.Part.Coverage != observe.Live || r.Part.Evidence == nil || *r.Part.Evidence != 2 {
			return false
		}
		res, _ = r.Rows.(RowSet[ReservationRow])
		return len(res.Rows) == 2
	})
	r, _ := s.rowsOf(TaskExtended + "/reservations")
	roles := map[string]string{}
	for _, k := range r.Part.Kinds {
		roles[k.Kind] = k.Role
	}
	if roles[KindNodes] != "counted" || roles[KindPods] != "dependency" || r.Part.Classifier != "evidence" || r.Part.Native != nil {
		t.Fatalf("the part's kinds and role: %+v, %q", roles, r.Part.Classifier)
	}
	byNode := map[string]extres.NodeRow{}
	for _, row := range res.Rows {
		if row.Ref.UID == "" || row.Ref.GVR != "v1/nodes" {
			t.Fatalf("a row is pinned to its node: %+v", row.Ref)
		}
		byNode[row.Data.Node] = row.Data.NodeRow
	}
	gpu := byNode["gpu-1"].Entries[0]
	if gpu.Name != "nvidia.com/gpu" || gpu.Requested != extres.Known(2) || gpu.ServedByDRA != extres.Known(1) {
		t.Fatalf("gpu-1's reservations: %+v", gpu)
	}
	if lic := byNode["plain"].Entries[0]; lic.Name != "example.com/license" || lic.Advertised || lic.NotAdvertised != extres.Known(1) {
		t.Fatalf("a name the node does not advertise: %+v", lic)
	}
	waitFor(t, "the totals: one accelerator name, before any cut", func() bool {
		r, ok := s.rowsOf(TaskExtended + "/totals")
		if !ok || r.Part.Evidence == nil || *r.Part.Evidence != 1 {
			return false
		}
		set, _ := r.Rows.(RowSet[TotalRow])
		return len(set.Rows) == 1 && set.Rows[0].Data.Allocatable == extres.Known(6) && set.Rows[0].Data.Capacity == extres.Known(8) && set.Rows[0].Data.Requested == extres.Known(2)
	})
	// the node reading reaches the nodes part's verdict, from the node alone
	waitFor(t, "the nodes part: gpu-1 degraded, not allocatable", func() bool {
		p, ok := s.part(TaskNodes)
		return ok && p.Counts.Degraded == 1
	})
}

// Projection equality: everything S11a reads survives ProjectNode and
// ProjectPod (C1).
func TestExtendedProjectionEquality(t *testing.T) {
	labels := map[string]string{"nvidia.com/mig.strategy": "mixed", "nvidia.com/mig-1g.5gb.replicas": "4", "nvidia.com/mig-1g.5gb.sharing-strategy": "time-slicing",
		"amd.com/gpu.product-name": "Instinct_MI300X"}
	rawNodes := []*corev1.Node{
		s11Node("a", map[string]string{"nvidia.com/mig-1g.5gb": "28", "amd.com/cpx_nps4": "48"}, map[string]string{"nvidia.com/mig-1g.5gb": "24", "amd.com/cpx_nps4": "48"}, labels),
	}
	rawPods := []*corev1.Pod{
		s11Pod("p", "a", map[string]string{"nvidia.com/mig-1g.5gb": "3", extres.DeviceClassPrefix + "gpu.example.com": "1"}, "nvidia.com/mig-1g.5gb"),
	}
	rawPods[0].Spec.Overhead = corev1.ResourceList{"amd.com/cpx_nps4": resource.MustParse("1")}
	var projNodes []*corev1.Node
	for _, n := range rawNodes {
		projNodes = append(projNodes, mustProject(t, ProjectNode, n).(*corev1.Node))
	}
	var projPods []*corev1.Pod
	for _, p := range rawPods {
		projPods = append(projPods, mustProject(t, ProjectPod, p).(*corev1.Pod))
	}
	if a, b := extres.Reserve(rawNodes, rawPods), extres.Reserve(projNodes, projPods); !reflect.DeepEqual(a, b) {
		t.Fatalf("reservations differ:\nraw  %+v\nproj %+v", a, b)
	}
	if a, b := workloads.SummarizeNodes([]corev1.Node{*rawNodes[0]}, now), workloads.SummarizeNodes([]corev1.Node{*projNodes[0]}, now); !reflect.DeepEqual(a, b) {
		t.Fatalf("node summaries differ:\nraw  %+v\nproj %+v", a, b)
	}
	if v := health.Node(projNodes[0]); v.Kind != health.Degraded || v.Rule != "node.resources-not-allocatable" {
		t.Fatalf("the projected node's reading: %+v", v)
	}
}

// Each part's Evidence is its full count before the cut, so the lens knows
// how many rows a bound left out (S11a §2.0; a mutation follow-up).
func TestExtendedEvidenceCountsTheCut(t *testing.T) {
	long := func(i int) string {
		tag := fmt.Sprintf("%08d", i)
		return strings.Repeat("a", 63) + "." + strings.Repeat("a", 63) + "." + strings.Repeat("a", 63) + "." + strings.Repeat("a", 52-len(tag)) + tag + "/" + strings.Repeat("a", 63-len(tag)) + tag
	}
	var objs []any
	for n := 0; n < 400; n++ {
		names := map[string]string{}
		for i := 0; i < 16; i++ {
			names[long(n*16+i)] = "1"
		}
		node := s11Node(fmt.Sprintf("n%03d", n), names, names, nil)
		objs = append(objs, node)
	}
	c := observe.Capture{Objects: map[string][]any{KindNodes: objs}, Rows: true}
	res := evalReservations(c)
	set, _ := res.Rows.(RowSet[ReservationRow])
	if res.Evidence == nil || *res.Evidence != 400 || len(set.Rows) >= 400 || len(set.Rows) == 0 {
		t.Fatalf("reservations: evidence %v, %d rows kept", res.Evidence, len(set.Rows))
	}
	var accel []any
	for i := 0; i < extres.MaxTotals+3; i++ {
		accel = append(accel, s11Node(fmt.Sprintf("g%02d", i), map[string]string{fmt.Sprintf("nvidia.com/mig-%dg.5gb", i): "1"}, map[string]string{fmt.Sprintf("nvidia.com/mig-%dg.5gb", i): "1"}, nil))
	}
	res = evalExtendedTotals(observe.Capture{Objects: map[string][]any{KindNodes: accel}, Rows: true})
	tot, _ := res.Rows.(RowSet[TotalRow])
	if res.Evidence == nil || *res.Evidence != extres.MaxTotals+3 || len(tot.Rows) != extres.MaxTotals {
		t.Fatalf("totals: evidence %v, %d rows kept", res.Evidence, len(tot.Rows))
	}
}

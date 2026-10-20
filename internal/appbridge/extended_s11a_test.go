package appbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/moomora/klyx/internal/extres"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/workloads"
)

// Coverage S11a §2.0, §6, §7: the bridge.

func TestExtendedFiguresCrossExactly(t *testing.T) {
	big := extres.Known(9007199254740993) // 2^53 + 1: a float64 cannot hold it
	if got := figureDTO(&big); got != "9007199254740993" {
		t.Fatalf("above 2^53: %q", got)
	}
	if figureDTO(nil) != "" || figureDTO(&extres.Qty{}) != "-" {
		t.Fatal("absent is empty, not established is -")
	}
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "g", UID: "uid-g", Labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-L4"}},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")}, Allocatable: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("6")},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	d := toNodeSummaryDTO(workloads.SummarizeNodes([]corev1.Node{*n}, time.Now())[0])
	if len(d.Extended.Entries) != 1 || d.Extended.Entries[0].Capacity != "8" || d.Extended.Entries[0].Allocatable != "6" || d.Extended.Entries[0].Facts[0].Value != "NVIDIA-L4" {
		t.Fatalf("the node's DTO: %+v", d.Extended)
	}
	if d.Health.Kind != "degraded" || d.Health.Label != "Resources not allocatable" {
		t.Fatalf("the reading: %+v", d.Health)
	}
}

func TestExtendedRowsConverters(t *testing.T) {
	row := healthsrc.Row[healthsrc.ReservationRow]{Ref: healthsrc.RowRef{UID: "uid-g", GVR: "v1/nodes", Name: "g"},
		Data: healthsrc.ReservationRow{NodeRow: extres.NodeRow{Node: "g", UID: "uid-g", Omitted: 2, Untracked: true, Entries: []extres.Reserved{
			{Name: "nvidia.com/gpu", Advertised: true, Figures: extres.Figures{Requested: extres.Known(3), ServedByDRA: extres.Known(1), NotAdvertised: extres.Known(0)}},
			{Name: "example.com/x", Figures: extres.Figures{Requested: extres.Known(0), ServedByDRA: extres.Qty{}, NotAdvertised: extres.Known(2)}}}}}}
	out, err := rowsConverters[healthsrc.TaskExtended+"/reservations"](healthsrc.RowSet[healthsrc.ReservationRow]{Rows: []healthsrc.Row[healthsrc.ReservationRow]{row}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := out.([]ReservationDTO)[0]
	if r.UID != "uid-g" || r.Omitted != 2 || !r.Untracked || r.Entries[0].Requested != "3" || r.Entries[0].ServedByDRA != "1" || !r.Entries[0].Advertised ||
		r.Entries[1].ServedByDRA != "-" || r.Entries[1].NotAdvertised != "2" {
		t.Fatalf("reservations DTO: %+v", r)
	}
	tot, err := rowsConverters[healthsrc.TaskExtended+"/totals"](healthsrc.RowSet[healthsrc.TotalRow]{Rows: []healthsrc.Row[healthsrc.TotalRow]{
		{Ref: healthsrc.RowRef{Name: "nvidia.com/gpu"}, Data: healthsrc.TotalRow{Total: extres.Total{Name: "nvidia.com/gpu", Capacity: extres.Known(16), Allocatable: extres.Known(16), Requested: extres.Qty{}}}}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if tt := tot.([]ExtendedTotalDTO)[0]; tt.Allocatable != "16" || tt.Requested != "-" {
		t.Fatalf("totals DTO: %+v", tt)
	}
}

// §7: Explain's evidence for a node carries its extended resources and
// facts, through the allowlist; the reservations and totals have their own.
func TestExtendedAIAllowlist(t *testing.T) {
	project := func(key string, v any) map[string]any {
		b, _ := json.Marshal(v)
		var m any
		_ = json.Unmarshal(b, &m)
		out, _ := json.Marshal((&scrubber{}).project(m, aiEvidenceFields[key]))
		var res map[string]any
		_ = json.Unmarshal(out, &res)
		return res
	}
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "g", UID: "uid-g", Labels: map[string]string{"nvidia.com/gpu.product": "NVIDIA-L4", "nvidia.com/gpu.replicas": "4"}},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")}, Allocatable: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("8")}}}
	got := project(healthsrc.TaskNodes+"/nodes", toNodeSummaryDTO(workloads.SummarizeNodes([]corev1.Node{*n}, time.Now())[0]))
	b, _ := json.Marshal(got["extended"])
	if !strings.Contains(string(b), `"name":"nvidia.com/gpu"`) || !strings.Contains(string(b), `"NVIDIA-L4"`) || !strings.Contains(string(b), "advertised 4 times") {
		t.Fatalf("the node's evidence: %s", b)
	}
	if _, ok := aiEvidenceFields[healthsrc.TaskExtended+"/reservations"]; !ok {
		t.Fatal("the reservations part has an allowlist")
	}
	if _, ok := aiEvidenceFields[healthsrc.TaskExtended+"/totals"]; !ok {
		t.Fatal("the totals part has an allowlist")
	}
}

func TestExtendedPodDTO(t *testing.T) {
	rl := corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")}
	p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "a", Resources: corev1.ResourceRequirements{Requests: rl, Limits: rl}}}},
		Status: corev1.PodStatus{ExtendedResourceClaimStatus: &corev1.PodExtendedResourceClaimStatus{ResourceClaimName: "p-extended-resources-x",
			RequestMappings: []corev1.ContainerExtendedResourceRequest{{ContainerName: "a", ResourceName: "nvidia.com/gpu", RequestName: "r"}}}}}
	d := toExtendedPodDTO(extres.OfPod(p))
	if d.Claim != "p-extended-resources-x" || len(d.Containers) != 1 || d.Containers[0].Entries[0].Quantity != "2" || !d.Containers[0].Entries[0].ServedByDRA {
		t.Fatalf("pod DTO: %+v", d)
	}
}

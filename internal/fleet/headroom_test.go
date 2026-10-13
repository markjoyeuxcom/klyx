package fleet

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/moomora/klyx/internal/clock"
)

func headroomNode(name, zone string, cpu string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			"topology.kubernetes.io/zone": zone,
			"agentpool":                   "workers",
		}},
		Status: corev1.NodeStatus{
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse("16Gi")},
			Capacity:    corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse("16Gi")},
		},
	}
}

func headroomPod(ns, name, node, cpuReq string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{{
				Name: "app",
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpuReq),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestNodeHeadroom_ZoneLossWithPDBBlockers(t *testing.T) {
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "pg-pdb"},
		Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 0},
	}
	okPDB := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "web-pdb"},
		Status:     policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 1},
	}
	cs := k8sfake.NewSimpleClientset(
		headroomNode("a1", "zone-a", "4"),
		headroomNode("b1", "zone-b", "4"),
		headroomPod("team-a", "p1", "a1", "1"),
		headroomPod("team-a", "p2", "b1", "1"),
		pdb, okPDB,
	)
	c := &ClusterConn{typed: cs, clk: clock.NewFake(time.Unix(0, 0))}

	r, err := c.NodeHeadroom(context.Background(), "zone", "zone-a")
	if err != nil {
		t.Fatal(err)
	}
	if r.Result.LostNodes != 1 || r.Result.SurvivorNodes != 1 || r.Result.Displaced != 1 {
		t.Fatalf("counts: %+v", r.Result)
	}
	if r.Result.Rescheduled != 1 || len(r.Result.Unplaced) != 0 {
		t.Fatalf("p1 (1cpu) must fit on b1 (4cpu - 1cpu baseline): %+v", r.Result)
	}
	// Only the zero-disruption PDB is a blocker.
	if len(r.PDBBlockers) != 1 || r.PDBBlockers[0].Name != "pg-pdb" {
		t.Fatalf("pdb blockers: %+v", r.PDBBlockers)
	}
}

func TestNodeHeadroom_RejectsUnknownTarget(t *testing.T) {
	c := &ClusterConn{typed: k8sfake.NewSimpleClientset(), clk: clock.NewFake(time.Unix(0, 0))}
	if _, err := c.NodeHeadroom(context.Background(), "rack", "r1"); err == nil {
		t.Fatal("unknown target kind must error")
	}
}

// Codex review 2026-10-13 #9: the simulation uses the scheduler-effective
// request. A 4-core init container cannot land on a 2-core survivor even
// though the app container asks for 100m.
func TestNodeHeadroomCountsInitContainerRequests(t *testing.T) {
	p := headroomPod("ns", "heavy-init", "lost", "100m")
	p.Spec.InitContainers = []corev1.Container{{Name: "setup", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}}}}
	cs := k8sfake.NewSimpleClientset(headroomNode("lost", "a", "8"), headroomNode("survivor", "b", "2"), p)
	c := &ClusterConn{typed: cs}
	r, err := c.NodeHeadroom(context.Background(), "node", "lost")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Result.Unplaced) == 0 {
		t.Fatalf("a 4-core init container cannot fit a 2-core survivor: %+v", r.Result)
	}
}

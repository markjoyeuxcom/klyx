package healthsrc

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/capability"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/lb"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

var lbNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func lbService(name, uid string, ips ...string) *corev1.Service {
	s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: name, UID: types.UID(uid), Generation: 1},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	for _, ip := range ips {
		s.Status.LoadBalancer.Ingress = append(s.Status.LoadBalancer.Ingress, corev1.LoadBalancerIngress{IP: ip})
	}
	return s
}

func lbCapture(objs map[string][]any, complete map[string]bool, facts map[string]any) observe.Capture {
	return observe.Capture{Now: lbNow, Objects: objs, Complete: complete, Facts: facts, Rows: true}
}

func lbRows(t *testing.T, r observe.PartResult) []Row[LBServiceRow] {
	t.Helper()
	rs, ok := r.Rows.(RowSet[LBServiceRow])
	if !ok {
		t.Fatalf("rows: %T", r.Rows)
	}
	return rs.Rows
}

// Coverage S4 §3, §4: a LoadBalancer Service's verdict from its episode,
// the fault's reason from a UID-joined event, a name-only or other-UID
// event never joined, and a ClusterIP Service never counted.
func TestS4LBServices(t *testing.T) {
	pending := lbService("web", "uid-web")
	young := lbService("young", "uid-young")
	unseen := lbService("unseen", "uid-unseen")
	ok := lbService("ok", "uid-ok", "10.0.0.5")
	plain := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "plain", UID: "uid-plain"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}}
	events := []any{
		workloads.EventSummary{Type: "Warning", Reason: "AllocationFailed", Message: "Failed to allocate IP for \"app/web\": no available IPs", Kind: "Service", Namespace: "app", Name: "web", UID: "uid-web", LastSeenUnix: 100},
		workloads.EventSummary{Type: "Warning", Reason: "AllocationFailed", Message: "old", Kind: "Service", Namespace: "app", Name: "young", UID: "uid-replaced", LastSeenUnix: 90},
		workloads.EventSummary{Type: "Warning", Reason: "AllocationFailed", Message: "nameless", Kind: "Service", Namespace: "app", Name: "unseen", LastSeenUnix: 95},
	}
	eps := map[string]lb.Episode{"uid-web": {Start: lbNow.Add(-time.Hour), Observed: true}, "uid-young": {Start: lbNow.Add(-time.Minute)}}
	c := lbCapture(map[string][]any{KindServices: {pending, young, unseen, ok, plain}, SnapEvents: events, KindNodes: {}, KindPods: {}, KindDaemonSets: {},
		KindMetalLBPools: {}},
		map[string]bool{KindServices: true, SnapEvents: true, KindNodes: true, KindPods: true, KindDaemonSets: true, KindMetalLBPools: true, KindCiliumPools: true, KindConfigStates: true},
		map[string]any{FactLBEpisodes: eps, FactLB: LBFact{MetalLB: true, ControllersRead: true, Namespaces: []string{"metallb-system"}}})
	r := evalLBServices("c")(c)
	if r.Native.Total != 4 {
		t.Fatalf("only LoadBalancer Services count: %+v", r.Native)
	}
	by := map[string]Row[LBServiceRow]{}
	for _, row := range lbRows(t, r) {
		by[row.Ref.Name] = row
	}
	if e := by["web"].Eval; e.Role != health.Fault || !strings.Contains(e.Reason, "no available IPs") {
		t.Fatalf("past the grace, the UID-joined event leads: %+v", e)
	}
	if e := by["young"].Eval; e.Role != health.Progressing || len(by["young"].Data.Diagnosis.Events) != 0 {
		t.Fatalf("within the grace; another UID's event never joined: %+v %+v", e, by["young"].Data.Diagnosis)
	}
	if d := by["unseen"].Data; d.Episode == nil || !d.Episode.Start.Equal(lbNow) || d.Episode.Observed || len(d.Diagnosis.Events) != 0 {
		t.Fatalf("no tracker episode: a lower bound now; a name-only event never joined: %+v", d)
	}
	if d := by["unseen"].Data.Diagnosis; d.Silent != "MetalLB has no pool: it stays silent" {
		t.Fatalf("the silent case: %+v", d)
	}
	if e := by["ok"].Eval; e.Role != health.OK || by["ok"].Data.Diagnosis != nil {
		t.Fatalf("allocated: %+v", e)
	}
}

// Coverage S4 §4: the svclb evidence follows the current Service's
// generated DaemonSet; the requested pool is joined inside the one
// instance namespace; dependencies unread leave the evidence unverified.
func TestS4LBServiceEvidence(t *testing.T) {
	s := lbService("web", "abcdef0123-web")
	s.Annotations = map[string]string{"metallb.io/address-pool": "gold"}
	dsName := lb.SvcLBName(s)
	ds := &ProjectedDaemonSet{DaemonSet: appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: dsName, UID: "ds-uid"}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: dsName + "-x", OwnerReferences: []metav1.OwnerReference{{UID: "ds-uid"}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
			Message: "0/1 nodes are available: 1 node(s) didn't have free ports for the requested pod ports."}}}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.37.1+k3s1"}}}
	c := lbCapture(map[string][]any{KindServices: {s}, KindDaemonSets: {ds}, KindPods: {pod}, KindNodes: {node}, KindMetalLBPools: {}},
		map[string]bool{KindServices: true, KindDaemonSets: true, KindPods: true, KindNodes: true, KindMetalLBPools: true},
		map[string]any{FactLB: LBFact{MetalLB: true, ControllersRead: true, Namespaces: []string{"metallb-system"}}})
	d := lbRows(t, evalLBServices("c")(c))[0].Data.Diagnosis
	if d.SvcLB == nil || d.SvcLB.DaemonSet != dsName || !d.SvcLB.HostPortConflict || d.EventsRead != "unverified" {
		t.Fatalf("svclb joined by the generated DaemonSet: %+v", d)
	}
	if d.Pool == nil || d.Pool.State != "missing" || d.Pool.Namespace != "metallb-system" {
		t.Fatalf("the requested pool, missing from a complete read of the one instance namespace: %+v", d.Pool)
	}
	if !strings.Contains(d.Summary, "no free host port") {
		t.Fatalf("summary: %q", d.Summary)
	}
	// two instance namespaces: the join is qualified, never guessed
	c.Facts[FactLB] = LBFact{MetalLB: true, ControllersRead: true, Namespaces: []string{"a", "b"}}
	if d := lbRows(t, evalLBServices("c")(c))[0].Data.Diagnosis; d.Pool.State != "unverified" {
		t.Fatalf("several instance namespaces: %+v", d.Pool)
	}
}

// Coverage S4 §5: the pools part over both pool kinds, advertisements in
// the pool's namespace, and ConfigurationStates as their own part.
func TestS4LBPoolsAndStates(t *testing.T) {
	st := lb.PoolStatus{AssignedV4: i64p(0), AvailableV4: i64p(4), AssignedV6: i64p(0), AvailableV6: i64p(0)}
	gold := &lb.MetalLBPool{Meta: lb.Meta{Namespace: "metallb-system", Name: "gold", UID: "p1"}, AutoAssign: true, Status: st}
	bare := &lb.MetalLBPool{Meta: lb.Meta{Namespace: "metallb-system", Name: "bare", UID: "p2"}, AutoAssign: true, Status: st}
	l2 := &lb.Advertisement{Meta: lb.Meta{Namespace: "metallb-system", Name: "l2"}, Kind: "L2", Pools: []string{"gold"}}
	cp := &lb.CiliumPool{Meta: lb.Meta{APIVersion: "cilium.io/v2", Name: "c1", UID: "c1", Generation: 1},
		Conds: []lb.Cond{{Type: lb.CondPoolConflict, Status: "True", ObservedGeneration: 1}}}
	ctl := &lb.ConfigState{Meta: lb.Meta{Namespace: "metallb-system", Name: "controller", UID: "s1"}, Result: "Valid"}
	all := map[string]bool{KindMetalLBPools: true, KindCiliumPools: true, KindL2Ads: true, KindBGPAds: true, KindConfigStates: true}
	c := lbCapture(map[string][]any{KindMetalLBPools: {gold, bare}, KindCiliumPools: {cp}, KindL2Ads: {l2}, KindBGPAds: {}, KindConfigStates: {ctl}}, all, map[string]any{})
	r := evalLBPools("c")(c)
	rows := r.Rows.(RowSet[LBPoolRow]).Rows
	by := map[string]Row[LBPoolRow]{}
	for _, row := range rows {
		by[row.Ref.Name] = row
	}
	if by["gold"].Eval.Role != health.OK || by["gold"].Data.Instance != "state" {
		t.Fatalf("gold: %+v", by["gold"])
	}
	if by["bare"].Eval.Label != "No advertisement" {
		t.Fatalf("bare: %+v", by["bare"].Eval)
	}
	if by["c1"].Eval.Role != health.Fault || by["c1"].Ref.GVR != "cilium.io/v2/ciliumloadbalancerippools" {
		t.Fatalf("cilium conflict: %+v", by["c1"])
	}
	if r.Native.Included != 2 {
		t.Fatalf("included: %+v", r.Native)
	}
	// the BGP advertisements unread: the advertisement verdict waits
	delete(c.Complete, KindBGPAds)
	delete(c.Objects, KindBGPAds)
	r = evalLBPools("c")(c)
	for _, row := range r.Rows.(RowSet[LBPoolRow]).Rows {
		if row.Data.MetalLB != nil && row.Eval.Label != "Advertisements unverified" {
			t.Fatalf("an unread advertisement kind: %+v", row.Eval)
		}
	}
	ctl.Result = "Invalid"
	s := evalLBStates("c")(lbCapture(map[string][]any{KindConfigStates: {ctl}}, all, nil))
	if s.Native.Roles.Fault != 1 {
		t.Fatalf("an Invalid ConfigurationState is a fault: %+v", s.Native)
	}
}

func i64p(v int64) *int64 { return &v }

// Coverage S4 §2: the MetalLB controller task's issue, and the fact.
func TestS4LBControllerIssueAndFact(t *testing.T) {
	caps := capability.Set{LoadBalancing: capability.LoadBalancingCapability{MetalLB: true, Verified: true, Base: capability.Base{Reason: "MetalLB controller metallb-system/controller is not ready"},
		ControllersRead: true, Controllers: []capability.MetalLBController{{Namespace: "metallb-system", Name: "controller"}, {Namespace: "metallb-system", Name: "other"}}}}
	if is := ControllerIssues(caps)[TaskLB]; is == nil || is.State != ControllerNotReady {
		t.Fatalf("issue: %+v", is)
	}
	if f := LoadBalancingFact(caps.LoadBalancing); len(f.Namespaces) != 1 || !f.ControllersRead {
		t.Fatalf("fact: %+v", f)
	}
	caps.LoadBalancing.Verified = false
	if is := ControllerIssues(caps)[TaskLB]; is == nil || is.State != ControllerUnknown {
		t.Fatalf("unverified: %+v", is)
	}
}

package dra

import (
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/health"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func TestReasonsMatchTheSchedulersMessages(t *testing.T) {
	// the scheduler joins per-node reasons: "0/3 nodes are available: ..."
	cases := []struct {
		msg   string
		key   string
		scope Scope
		claim string
		class string
	}{
		{"0/1 nodes are available: 1 cannot allocate all claims. still not schedulable", "cannot-allocate", SetWide, "", ""},
		{"0/2 nodes are available: 2 resourceclaim not available on the node.", "not-available-on-node", SetWide, "", ""},
		{"0/1 nodes are available: 1 resourceclaim in use.", "in-use", SetWide, "", ""},
		{"0/1 nodes are available: 1 resource claim gpu/train-gpu-x7k is in the process of being allocated.", "in-progress", InProgress, "gpu/train-gpu-x7k", ""},
		{"request gpu: device class gpu.example.com does not exist", "class-missing", NamesClass, "", "gpu.example.com"},
		{"node n1: timed out trying to allocate devices", "timed-out", SetWide, "", ""},
		{"timed out trying to allocate devices", "timed-out", SetWide, "", ""},
		{"resource claim ns/c allocation requires DRAOptionalNodeOperations feature on the node", "needs-optional-node-operations", NamesClaim, "ns/c", ""},
		{"resource claim ns/c, request r: has subrequests, but the DRAPrioritizedList feature is disabled", "subrequests-disabled", NamesClaim, "ns/c", ""},
		{"0/1 nodes are available: 1 cannot schedule extended resource claim.", "extended-claim", SetWide, "", ""},
		// lab-s11b, live on 1.37: an invalid fixture pool of another driver
		{"0/1 nodes are available: 1 invalid resource pools were encountered. preemption: 0/1 nodes are available: 1 Preemption is not helpful for scheduling.", "invalid-pools", SetWide, "", ""},
	}
	for _, c := range cases {
		rs := Reasons(c.msg)
		if len(rs) == 0 || rs[0].Key != c.key || rs[0].Scope != c.scope || rs[0].Claim != c.claim || rs[0].Class != c.class {
			t.Errorf("%q: %+v", c.msg, rs)
		}
		if Meaning(c.key) == "" {
			t.Errorf("%s has no meaning", c.key)
		}
	}
	// a DRA error outside the fixed set (a CEL runtime error) and a non-DRA
	// one match nothing
	for _, msg := range []string{"class gpu: selector #0 on device driver/pool/d0: CEL runtime error: no such key: x",
		"0/3 nodes are available: 3 Insufficient cpu."} {
		if rs := Reasons(msg); len(rs) != 0 {
			t.Errorf("%q matched %+v", msg, rs)
		}
	}
}

func pod(name string, cond *corev1.PodCondition, claims ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", UID: types.UID("uid-" + name)}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	if cond != nil {
		p.Status.Conditions = []corev1.PodCondition{*cond}
	}
	for _, c := range claims {
		n := c
		p.Spec.ResourceClaims = append(p.Spec.ResourceClaims, corev1.PodResourceClaim{Name: c, ResourceClaimName: &n})
	}
	return p
}

func sched(reason, msg string) *corev1.PodCondition {
	return &corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: reason, Message: msg}
}

func pending(name string, classes ...string) Claim {
	c := Claim{Namespace: "ns", Name: name, UID: "uid-" + name}
	for i, cl := range classes {
		c.Requests = append(c.Requests, Request{Name: "r" + string(rune('0'+i)), Class: cl, Mode: "ExactCount", Count: 1})
	}
	return c
}

func consumer(p *corev1.Pod, complete bool, classes map[string][]string) ConsumerPod {
	cp := ConsumerPod{Pod: p, Classes: classes, Complete: complete}
	for _, pc := range p.Spec.ResourceClaims {
		cp.Claims = append(cp.Claims, "ns/"+*pc.ResourceClaimName)
	}
	return cp
}

func TestTheDecisionTable(t *testing.T) {
	one := map[string][]string{"ns/a": {"gpu"}}
	two := map[string][]string{"ns/a": {"gpu"}, "ns/b": {"nic"}}
	cases := []struct {
		name  string
		claim Claim
		cons  []ConsumerPod
		kind  health.Kind
		rule  string
	}{
		{"a: in progress for this claim", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("Unschedulable", "resource claim ns/a is in the process of being allocated")), true, one)},
			health.Progressing, "claim.allocating"},
		{"b: the pod's only claim", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("Unschedulable", "0/1 nodes are available: 1 cannot allocate all claims."), "a"), true, one)},
			health.Degraded, "claim.cannot-allocate"},
		{"c: two claims, set-wide reason", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("Unschedulable", "0/1 nodes are available: 1 cannot allocate all claims."), "a", "b"), true, two)},
			health.Degraded, "claim.pod-cannot-allocate"},
		{"b: two claims, a class only this claim uses", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("Unschedulable", "request r0: device class gpu does not exist"), "a", "b"), true, two)},
			health.Degraded, "claim.cannot-allocate"},
		{"c: the class proof needs complete evidence", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("Unschedulable", "request r0: device class gpu does not exist"), "a", "b"), false, two)},
			health.Degraded, "claim.pod-cannot-allocate"},
		{"c: the only claim, but its evidence is incomplete", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("Unschedulable", "0/1 nodes are available: 1 cannot allocate all claims."), "a"), false, one)},
			health.Degraded, "claim.pod-cannot-allocate"},
		{"b: a reason naming this claim", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("Unschedulable", "resource claim ns/a allocation requires DRAOptionalNodeOperations feature on the node"), "a", "b"), false, two)},
			health.Degraded, "claim.cannot-allocate"},
		{"d: a DRA error outside the fixed set", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("SchedulerError", "class gpu: selector #0 on device d/p/d0: CEL runtime error: no such key: x"), "a"), true, one)},
			health.Progressing, "claim.waiting-scheduler-error"},
		{"d: a non-DRA error", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("SchedulerError", "etcdserver: request timed out"), "a"), true, one)},
			health.Progressing, "claim.waiting-scheduler-error"},
		{"e: unschedulable for no DRA reason", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("Unschedulable", "0/3 nodes are available: 3 Insufficient cpu."), "a"), true, one)},
			health.Progressing, "claim.waiting-unschedulable"},
		{"f: no condition yet", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", nil, "a"), true, one)},
			health.Progressing, "claim.waiting-scheduler"},
		{"g: the pod is scheduled, its node not yet recorded", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", &corev1.PodCondition{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}, "a"), true, one)},
			health.Unknown, "claim.scheduled-unallocated"},
		{"g: the pod is on a node", pending("a", "gpu"),
			[]ConsumerPod{func() ConsumerPod { p := pod("p", nil, "a"); p.Spec.NodeName = "n1"; return consumer(p, true, one) }()},
			health.Unknown, "claim.scheduled-unallocated"},
		{"several consumers: the worst wins", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p1", nil, "a"), true, one), consumer(pod("p2", sched("Unschedulable", "cannot allocate all claims"), "a"), true, one)},
			health.Degraded, "claim.cannot-allocate"},
		{"several consumers: in progress decides first", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p1", sched("Unschedulable", "cannot allocate all claims"), "a"), true, one),
				consumer(pod("p2", sched("Unschedulable", "resource claim ns/a is in the process of being allocated"), "a"), true, one)},
			health.Progressing, "claim.allocating"},
		{"another claim in progress is not this claim's", pending("a", "gpu"),
			[]ConsumerPod{consumer(pod("p", sched("Unschedulable", "resource claim ns/b is in the process of being allocated"), "a", "b"), true, two)},
			health.Progressing, "claim.waiting-unschedulable"},
	}
	for _, c := range cases {
		v, rows := ClaimVerdict(c.claim, c.cons, true, now)
		if v.Kind != c.kind || v.Rule != c.rule {
			t.Errorf("%s: %s %s (%s) rows %+v", c.name, v.Kind, v.Rule, v.Label, rows)
		}
	}
	// several consumers keep each one's reading
	_, rows := ClaimVerdict(pending("a", "gpu"), []ConsumerPod{consumer(pod("p1", nil, "a"), true, one),
		consumer(pod("p2", sched("Unschedulable", "cannot allocate all claims"), "a"), true, one)}, true, now)
	if len(rows) != 2 || rows[0].Pod != "p2" || rows[1].Pod != "p1" {
		t.Fatalf("each consumer kept, worst first: %+v", rows)
	}
}

func TestTheClaimLadder(t *testing.T) {
	deleting := pending("d", "gpu")
	deleting.Deleting, deleting.DeletionTime, deleting.Finalizers = true, now.Add(-time.Minute), []string{"resource.kubernetes.io/delete-protection"}
	if v, _ := ClaimVerdict(deleting, nil, true, now); v.Kind != health.Progressing || v.ReclassifyAt == nil || !v.ReclassifyAt.Equal(now.Add(4*time.Minute)) {
		t.Fatalf("deleting within the grace: %+v", v)
	}
	if v, _ := ClaimVerdict(deleting, nil, true, now.Add(5*time.Minute)); v.Kind != health.Degraded || !strings.Contains(v.Message, "delete-protection") {
		t.Fatalf("deletion not finishing names its finalizers: %+v", v)
	}
	deleting.ReservedCount = 2
	if v, _ := ClaimVerdict(deleting, nil, true, now.Add(time.Hour)); v.Kind != health.Progressing || !strings.Contains(v.Message, "2 consumers") {
		t.Fatalf("deleting with consumers: %+v", v)
	}
	alloc := pending("x", "gpu")
	alloc.Allocated, alloc.ReservedCount = true, 1
	alloc.Devices = []DeviceStatus{{Driver: "gpu.example.com", Pool: "n1", Device: "gpu-3", Conditions: []Cond{{Type: "Ready", Status: "False", Reason: "ConfigFailed", Message: "driver free text"}}}}
	if v, _ := ClaimVerdict(alloc, nil, true, now); v.Kind != health.Degraded || v.Rule != "claim.device-not-ready" || !strings.Contains(v.Message, "gpu.example.com/n1/gpu-3") ||
		strings.Contains(v.Message, "driver free text") {
		t.Fatalf("device not ready, message local only: %+v", v)
	}
	alloc.Devices = nil
	if v, _ := ClaimVerdict(alloc, nil, true, now); v.Kind != health.OK || v.Message != "in use by 1" || v.Native != "allocated,reserved" {
		t.Fatalf("allocated: %+v", v)
	}
	alloc.ReservedCount = 0
	if v, _ := ClaimVerdict(alloc, nil, true, now); v.Kind != health.OK || v.Message != "not in use" {
		t.Fatalf("allocated, not in use: %+v", v)
	}
	if v, _ := ClaimVerdict(pending("u", "gpu"), nil, true, now); v.Kind != health.OK || v.Rule != "claim.unused" {
		t.Fatalf("pending, unused under a complete read: %+v", v)
	}
	if v, _ := ClaimVerdict(pending("u", "gpu"), nil, false, now); v.Kind != health.Unknown {
		t.Fatalf("pending, consumers unknown: %+v", v)
	}
}

func TestPodClaimsKeepsTheThreeResults(t *testing.T) {
	direct, tmpl := "shared", "t"
	gen := "p-gpu-abc"
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", UID: "uid-p"}, Spec: corev1.PodSpec{ResourceClaims: []corev1.PodResourceClaim{
		{Name: "a", ResourceClaimName: &direct}, {Name: "b", ResourceClaimTemplateName: &tmpl}, {Name: "c", ResourceClaimTemplateName: &tmpl}, {Name: "d", ResourceClaimTemplateName: &tmpl}}},
		Status: corev1.PodStatus{ResourceClaimStatuses: []corev1.PodResourceClaimStatus{{Name: "b", ResourceClaimName: &gen}, {Name: "c"}},
			ExtendedResourceClaimStatus: &corev1.PodExtendedResourceClaimStatus{ResourceClaimName: "p-extended-resources-x"}}}
	refs := PodClaims(p)
	want := []PodClaimRef{{"a", "claim", "shared", RefResolved}, {"b", "template", gen, RefResolved}, {"c", "template", "", RefNoneNeeded},
		{"d", "template", "", RefNotCreated}, {"", "extended", "p-extended-resources-x", RefResolved}}
	if len(refs) != len(want) {
		t.Fatalf("%+v", refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("%d: %+v, want %+v", i, refs[i], want[i])
		}
	}
	c := Claim{OwnerKind: "Pod", OwnerUID: "uid-p"}
	if !c.OwnedBy(p) || (Claim{OwnerKind: "Pod", OwnerUID: "other"}).OwnedBy(p) {
		t.Fatal("IsForPod")
	}
}

func slice(name, node string, count int64, devices ...string) Slice {
	s := Slice{Name: name, Driver: "d.example.com", Pool: "p", Generation: 1, Count: count, NodeName: node, AllNodes: node == ""}
	for _, d := range devices {
		s.Devices = append(s.Devices, Device{Name: d})
	}
	return s
}

func TestPoolVariantsAndAgreement(t *testing.T) {
	// node-local slices on two nodes: 1.34 counts per node, 1.35+ across the cluster
	twoNodes := []Slice{slice("a", "n1", 2, "d0"), slice("b", "n2", 2, "d1")}
	if v := ReadPool(twoNodes, 36).Verdict(time.Time{}, now); v.Kind != health.OK || !strings.Contains(v.Note, "1.35 or 1.36") {
		t.Fatalf("a 1.36 server allows 1.35 and 1.36, which agree: %+v", v)
	}
	v := ReadPool(twoNodes, 35).Verdict(time.Time{}, now)
	if v.Kind != health.Unknown || v.Rule != "pool.not-established" || !strings.Contains(v.Message, "1.34 (partitionable devices off): updating") {
		t.Fatalf("a 1.35 server may run a 1.34 scheduler: not established: %+v", v)
	}
	if v := ReadPool(twoNodes, 0).Verdict(time.Time{}, now); v.Kind != health.Unknown {
		t.Fatalf("an unknown server allows every family: %+v", v)
	}
	// a 1.34 server allows 1.33 and 1.34, which agree: per node, each view
	// holds one of two slices
	if v := ReadPool(twoNodes, 34).Verdict(time.Time{}, now); v.Kind != health.Progressing || !strings.Contains(v.Message, "1 of 2 slices") || !strings.Contains(v.Note, "1.33 or 1.34") {
		t.Fatalf("a 1.34 server: being updated: %+v", v)
	}
	// a 1.38 server (newer than the oracle) reads with the 1.35+ family
	if v := ReadPool(twoNodes, 38).Verdict(time.Time{}, now); v.Kind != health.OK || !strings.Contains(v.Note, "1.37 or 1.38") {
		t.Fatalf("a 1.38 server allows 1.37 and 1.38, which agree: %+v", v)
	}
	// the gate-dependent pair: invalid with the feature on, complete off
	gate := []Slice{slice("a", "", 2, "d0"), {Name: "b", Driver: "d.example.com", Pool: "p", Generation: 1, Count: 2, PerDevice: true, Devices: []Device{{Name: "d0", AllNodes: true}}}}
	if v := ReadPool(gate, 37).Verdict(time.Time{}, now); v.Kind != health.Unknown || !strings.Contains(v.Message, "invalid") || !strings.Contains(v.Message, "complete") {
		t.Fatalf("gate-dependent: not established, each variant listed: %+v", v)
	}
	// a per-device pool: off ignores it (none), on allocates: both OK
	perDevice := []Slice{{Name: "a", Driver: "d.example.com", Pool: "p", Generation: 1, Count: 1, PerDevice: true, Devices: []Device{{Name: "d0", NodeName: "n1"}}}}
	if v := ReadPool(perDevice, 37).Verdict(time.Time{}, now); v.Kind != health.OK || !strings.Contains(v.Note, "partitionable-devices feature on or off") {
		t.Fatalf("agreement in severity only: %+v", v)
	}
	// incomplete: progressing within the grace, degraded after; with no
	// episode, no deadline
	missing := []Slice{slice("a", "n1", 2, "d0")}
	p := ReadPool(missing, 37)
	if v := p.Verdict(time.Time{}, now); v.Kind != health.Progressing || v.ReclassifyAt != nil {
		t.Fatalf("no episode: %+v", v)
	}
	if v := p.Verdict(now.Add(-time.Minute), now); v.Kind != health.Progressing || v.ReclassifyAt == nil {
		t.Fatalf("within the grace: %+v", v)
	}
	if v := p.Verdict(now.Add(-6*time.Minute), now); v.Kind != health.Degraded || !strings.Contains(v.Message, "1 of 2 slices") {
		t.Fatalf("past the grace: %+v", v)
	}
	// invalid, on the named view
	dup := []Slice{slice("a", "n1", 2, "d0"), slice("b", "n1", 2, "d0")}
	if v := ReadPool(dup, 37).Verdict(time.Time{}, now); v.Kind != health.Degraded || !strings.Contains(v.Message, "on n1") || !strings.Contains(v.Message, "duplicate device name d0") {
		t.Fatalf("invalid on n1: %+v", v)
	}
	// a node selector: not established, never assumed
	sel := []Slice{{Name: "a", Driver: "d.example.com", Pool: "p", Generation: 1, Count: 1, NodeSelector: true, Devices: []Device{{Name: "d0"}}}}
	if v := ReadPool(sel, 37).Verdict(time.Time{}, now); v.Kind != health.Unknown || !strings.Contains(v.Message, "node selector") {
		t.Fatalf("selector: %+v", v)
	}
}

func TestProjectionKeepsTheLedger(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "s", "uid": "u1"},
		"spec": map[string]any{"driver": "d.example.com", "pool": map[string]any{"name": "p", "generation": int64(3), "resourceSliceCount": int64(2)},
			"allNodes":       true,
			"sharedCounters": []any{map[string]any{"name": "cs", "counters": map[string]any{"mem": map[string]any{"value": "8Gi"}, "cores": map[string]any{"value": "4"}}}},
			"devices": []any{map[string]any{"name": "d0", "attributes": map[string]any{"uuid": map[string]any{"string": "GPU-secret"}},
				"consumesCounters": []any{map[string]any{"counterSet": "cs", "counters": map[string]any{"mem": map[string]any{"value": "1Gi"}}, "compatibilityGroups": []any{"g"}}},
				"taints":           []any{map[string]any{"key": "k", "value": "v", "effect": "NoSchedule"}}}}}}}
	s := ProjectSlice(u)
	if s.Generation != 3 || s.Count != 2 || !s.AllNodes || len(s.CounterSets) != 1 || strings.Join(s.CounterSets[0].Counters, ",") != "cores,mem" ||
		s.Devices[0].Consumes[0].Set != "cs" || s.Devices[0].Consumes[0].Counters[0] != "mem" || !s.Devices[0].Consumes[0].CompatGroups ||
		s.Devices[0].TaintEffects[0] != "NoSchedule" {
		t.Fatalf("%+v", s)
	}
	if strings.Contains(strings.ToLower(fmtAny(s)), "gpu-secret") {
		t.Fatal("an attribute crossed the projection")
	}
	ctrl := true
	c := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "c", "namespace": "ns", "uid": "u2",
		"finalizers":      []any{"resource.kubernetes.io/delete-protection"},
		"annotations":     map[string]any{AnnotationPodClaimName: "gpu"},
		"ownerReferences": []any{map[string]any{"apiVersion": "v1", "kind": "Pod", "name": "p", "uid": "uid-p", "controller": ctrl}}},
		"spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"name": "gpu", "exactly": map[string]any{"deviceClassName": "gpu.example.com",
			"allocationMode": "ExactCount", "count": int64(1), "selectors": []any{map[string]any{"cel": map[string]any{"expression": "SECRET-CEL"}}}}}},
			"config": []any{map[string]any{"opaque": map[string]any{"driver": "d", "parameters": map[string]any{"x": "SECRET-OPAQUE"}}}}}},
		"status": map[string]any{"allocation": map[string]any{"devices": map[string]any{"results": []any{map[string]any{"request": "gpu", "driver": "d", "pool": "n1", "device": "gpu-0", "shareID": "s1"}}}},
			"reservedFor": []any{map[string]any{"resource": "pods", "name": "p", "uid": "uid-p"}},
			"devices": []any{map[string]any{"driver": "d", "pool": "n1", "device": "gpu-0", "data": map[string]any{"pci": "SECRET-DATA"},
				"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "Configured"}}}}}}}
	cl := ProjectClaim(c)
	if cl.Finalizers[0] != "resource.kubernetes.io/delete-protection" || cl.OwnerUID != "uid-p" || cl.PodClaimName != "gpu" || !cl.Allocated ||
		cl.Results[0].ShareID != "s1" || cl.ReservedCount != 1 || cl.Devices[0].Conditions[0].Reason != "Configured" {
		t.Fatalf("%+v", cl)
	}
	for _, secret := range []string{"SECRET-CEL", "SECRET-OPAQUE", "SECRET-DATA"} {
		if strings.Contains(fmtAny(cl), secret) {
			t.Fatalf("%s crossed the claim projection", secret)
		}
	}
}

// Raw-versus-projected equality (S11b §2.0) for the claim rules: the
// verdict read through ProjectClaim from the raw object equals the verdict
// of the claim built from every field. The slice rules are held to it by
// the pool oracle, which reads its raw fixtures through ProjectSlice.
func TestClaimRulesReadTheSameThroughTheProjection(t *testing.T) {
	gone := metav1.NewTime(now.Add(-6 * time.Minute))
	raw := map[string]map[string]any{
		"a stuck deletion with finalizers": {"metadata": map[string]any{"name": "d", "namespace": "ns", "uid": "u-d", "deletionTimestamp": gone.UTC().Format(time.RFC3339),
			"finalizers": []any{"resource.kubernetes.io/delete-protection", "example.com/keep"}},
			"spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"name": "gpu", "exactly": map[string]any{"deviceClassName": "gpu", "allocationMode": "ExactCount", "count": int64(1)}}}}}},
		"a shared allocation in use by two": {"metadata": map[string]any{"name": "s", "namespace": "ns", "uid": "u-s"},
			"spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"name": "gpu", "exactly": map[string]any{"deviceClassName": "gpu", "allocationMode": "ExactCount", "count": int64(2)}}}}},
			"status": map[string]any{"allocation": map[string]any{"devices": map[string]any{"results": []any{
				map[string]any{"request": "gpu", "driver": "d", "pool": "n1", "device": "gpu-0", "shareID": "s1"},
				map[string]any{"request": "gpu", "driver": "d", "pool": "n1", "device": "gpu-0", "shareID": "s2"}}}},
				"reservedFor": []any{map[string]any{"resource": "pods", "name": "a", "uid": "ua"}, map[string]any{"resource": "pods", "name": "b", "uid": "ub"}}}},
		"a shared device not ready": {"metadata": map[string]any{"name": "n", "namespace": "ns", "uid": "u-n"},
			"spec": map[string]any{"devices": map[string]any{"requests": []any{map[string]any{"name": "gpu", "exactly": map[string]any{"deviceClassName": "gpu", "allocationMode": "ExactCount", "count": int64(1)}}}}},
			"status": map[string]any{"allocation": map[string]any{"devices": map[string]any{"results": []any{map[string]any{"request": "gpu", "driver": "d", "pool": "n1", "device": "gpu-0", "shareID": "s1"}}}},
				"reservedFor": []any{map[string]any{"resource": "pods", "name": "a", "uid": "ua"}},
				"devices": []any{map[string]any{"driver": "d", "pool": "n1", "device": "gpu-0", "shareID": "s1",
					"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "Failed", "message": "free text"}}}}}},
	}
	full := map[string]Claim{
		"a stuck deletion with finalizers": {Namespace: "ns", Name: "d", UID: "u-d", Deleting: true, DeletionTime: gone.UTC(),
			Finalizers: []string{"resource.kubernetes.io/delete-protection", "example.com/keep"}, Requests: []Request{{Name: "gpu", Class: "gpu", Mode: "ExactCount", Count: 1}}},
		"a shared allocation in use by two": {Namespace: "ns", Name: "s", UID: "u-s", Requests: []Request{{Name: "gpu", Class: "gpu", Mode: "ExactCount", Count: 2}}, Allocated: true,
			Results:       []Result{{Request: "gpu", Driver: "d", Pool: "n1", Device: "gpu-0", ShareID: "s1"}, {Request: "gpu", Driver: "d", Pool: "n1", Device: "gpu-0", ShareID: "s2"}},
			ReservedCount: 2, Reserved: []Consumer{{Resource: "pods", Name: "a", UID: "ua"}, {Resource: "pods", Name: "b", UID: "ub"}}},
		"a shared device not ready": {Namespace: "ns", Name: "n", UID: "u-n", Requests: []Request{{Name: "gpu", Class: "gpu", Mode: "ExactCount", Count: 1}}, Allocated: true,
			Results: []Result{{Request: "gpu", Driver: "d", Pool: "n1", Device: "gpu-0", ShareID: "s1"}}, ReservedCount: 1, Reserved: []Consumer{{Resource: "pods", Name: "a", UID: "ua"}},
			Devices: []DeviceStatus{{Driver: "d", Pool: "n1", Device: "gpu-0", ShareID: "s1", Conditions: []Cond{{Type: "Ready", Status: "False", Reason: "Failed", Message: "free text"}}}}},
	}
	for name, obj := range raw {
		got, _ := ClaimVerdict(ProjectClaim(&unstructured.Unstructured{Object: obj}), nil, true, now)
		want, _ := ClaimVerdict(full[name], nil, true, now)
		if got.Kind != want.Kind || got.Rule != want.Rule || got.Label != want.Label || got.Message != want.Message || got.Native != want.Native {
			t.Errorf("%s: projected %+v, full %+v", name, got, want)
		}
		if rule := map[string]string{"a stuck deletion with finalizers": "claim.deletion-stuck", "a shared allocation in use by two": "claim.allocated",
			"a shared device not ready": "claim.device-not-ready"}[name]; got.Rule != rule {
			t.Errorf("%s: %s, want %s", name, got.Rule, rule)
		}
	}
}

func fmtAny(v any) string { return fmt.Sprintf("%+v", v) }

func TestClipKeepsRunesWhole(t *testing.T) {
	if got := Clip(strings.Repeat("a", 255)+"é", 256); got != strings.Repeat("a", 255)+"…" {
		t.Fatalf("a rune cut in two: %q", got)
	}
	if got := Clip("short", 256); got != "short" {
		t.Fatalf("%q", got)
	}
}

// The mutation gate's survivors (S11b): a cut claim, a 1.34 slice's
// counter sets, and a clipped condition type.
func TestTheProjectionsBounds(t *testing.T) {
	var results []any
	for i := 0; i < 33; i++ {
		results = append(results, map[string]any{"request": "gpu", "driver": "d", "pool": "p", "device": fmt.Sprintf("g%d", i)})
	}
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "c", "namespace": "ns", "uid": "u"},
		"status": map[string]any{"allocation": map[string]any{"devices": map[string]any{"results": results}},
			"devices": []any{map[string]any{"driver": "d", "pool": "p", "device": "g0",
				"conditions": []any{map[string]any{"type": strings.Repeat("T", 300), "status": "True", "reason": "R"}}}}}}}
	c := ProjectClaim(u)
	if !c.Truncated || len(c.Results) != 32 {
		t.Fatalf("results past the API's 32 are cut and said: %v %d", c.Truncated, len(c.Results))
	}
	if v, _ := ClaimVerdict(c, nil, true, now); v.Kind != health.Unknown || v.Rule != "claim.not-established" {
		t.Fatalf("a cut claim is not established: %+v", v)
	}
	if typ := c.Devices[0].Conditions[0].Type; len(typ) > MaxTokenBytes+len("…") {
		t.Fatalf("a condition type is clipped: %d bytes", len(typ))
	}
	var sets []any
	for i := 0; i < 20; i++ {
		sets = append(sets, map[string]any{"name": fmt.Sprintf("cs%d", i), "counters": map[string]any{"c": map[string]any{"value": "1"}}})
	}
	s := ProjectSlice(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "s"},
		"spec": map[string]any{"driver": "d", "pool": map[string]any{"name": "p", "generation": int64(1), "resourceSliceCount": int64(1)}, "allNodes": true, "sharedCounters": sets}}})
	if s.Truncated || len(s.CounterSets) != 20 {
		t.Fatalf("1.34 allows 32 counter sets: 20 are within the API's bounds: %v %d", s.Truncated, len(s.CounterSets))
	}
}

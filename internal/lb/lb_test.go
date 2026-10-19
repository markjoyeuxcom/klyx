package lb

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

var t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func lbSvc(name string, mut ...func(*corev1.Service)) *corev1.Service {
	s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: name, UID: types.UID("0123456789abcdef-" + name), Generation: 1},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	for _, m := range mut {
		m(s)
	}
	return s
}

func ingress(entries ...corev1.LoadBalancerIngress) func(*corev1.Service) {
	return func(s *corev1.Service) { s.Status.LoadBalancer.Ingress = entries }
}

func strp(s string) *string { return &s }

func TestAllocation(t *testing.T) {
	if a := AllocationOf(lbSvc("empty", ingress(corev1.LoadBalancerIngress{}))); a.Allocated || a.Entries != 1 {
		t.Fatalf("an entry with neither ip nor hostname is no allocation: %+v", a)
	}
	a := AllocationOf(lbSvc("host", ingress(corev1.LoadBalancerIngress{Hostname: "lb.example"})))
	if !a.Allocated || !a.HostnameOnly() || len(a.Families) != 0 {
		t.Fatalf("hostname-only is an allocation with unknown families: %+v", a)
	}
	a = AllocationOf(lbSvc("dual", ingress(corev1.LoadBalancerIngress{IP: "10.0.0.1"}, corev1.LoadBalancerIngress{IP: "fd00::1"})))
	if !a.Families[FamilyV4] || !a.Families[FamilyV6] {
		t.Fatalf("families: %+v", a)
	}
	// an error beyond the display cap is never lost (revision 1)
	var many []corev1.LoadBalancerIngress
	for i := 0; i < 9; i++ {
		many = append(many, corev1.LoadBalancerIngress{IP: fmt.Sprintf("10.0.0.%d", i+1)})
	}
	many[8].Ports = []corev1.PortStatus{{Port: 80, Protocol: corev1.ProtocolTCP, Error: strp(ErrorSet)}}
	s := lbSvc("ninth", ingress(many...))
	if e := ServiceRule(s, AllocationOf(s), Episode{}, t0, ""); e.Role != health.Degraded || e.Label != "Port error" {
		t.Fatalf("ninth entry's port error: %+v", e)
	}
	s = lbSvc("cond", ingress(corev1.LoadBalancerIngress{IP: "10.0.0.1"}), func(s *corev1.Service) {
		for i := 0; i < 8; i++ {
			s.Status.Conditions = append(s.Status.Conditions, metav1.Condition{Type: fmt.Sprintf("x/C%d", i), Status: "True"})
		}
		s.Status.Conditions = append(s.Status.Conditions, metav1.Condition{Type: "LoadBalancerPortsError", Status: "True"})
	})
	if e := ServiceRule(s, AllocationOf(s), Episode{}, t0, ""); e.Label != "Port error" {
		t.Fatalf("LoadBalancerPortsError after condition 8: %+v", e)
	}
}

func TestFamilies(t *testing.T) {
	req := corev1.IPFamilyPolicyRequireDualStack
	pref := corev1.IPFamilyPolicyPreferDualStack
	one := lbSvc("req", ingress(corev1.LoadBalancerIngress{IP: "10.0.0.1"}), func(s *corev1.Service) { s.Spec.IPFamilyPolicy = &req })
	if e := ServiceRule(one, AllocationOf(one), Episode{}, t0, ""); e.Role != health.Degraded || !strings.Contains(e.Reason, "IPv6 is not assigned") {
		t.Fatalf("RequireDualStack with one family: %+v", e)
	}
	one.Status.Conditions = []metav1.Condition{{Type: CondRequestSatisfied, Status: "False", Reason: "out_of_ips", ObservedGeneration: 1}}
	if e := ServiceRule(one, AllocationOf(one), Episode{}, t0, ""); !strings.Contains(e.Reason, "Cilium: out_of_ips") {
		t.Fatalf("current Cilium unsatisfied condition joins the reason: %+v", e)
	}
	p := lbSvc("pref", ingress(corev1.LoadBalancerIngress{IP: "10.0.0.1"}), func(s *corev1.Service) { s.Spec.IPFamilyPolicy = &pref })
	if e := ServiceRule(p, AllocationOf(p), Episode{}, t0, ""); e.Role != health.OK {
		t.Fatalf("PreferDualStack with one family is ok: %+v", e)
	}
	h := lbSvc("host", ingress(corev1.LoadBalancerIngress{Hostname: "lb.example"}), func(s *corev1.Service) { s.Spec.IPFamilyPolicy = &req })
	if e := ServiceRule(h, AllocationOf(h), Episode{}, t0, ""); e.Role != health.OK {
		t.Fatalf("hostname-only has unknown families, never judged: %+v", e)
	}
}

func TestGrace(t *testing.T) {
	s := lbSvc("p")
	ep := Episode{Start: t0}
	e := ServiceRule(s, AllocationOf(s), ep, t0.Add(Grace-time.Second), "why")
	if e.Role != health.Progressing || e.Next == nil || !e.Next.Equal(t0.Add(Grace)) || e.Included {
		t.Fatalf("within the grace: %+v", e)
	}
	e = ServiceRule(s, AllocationOf(s), ep, t0.Add(Grace), "why")
	if e.Role != health.Fault || !e.Included || e.Boundary == nil || !strings.Contains(e.Reason, "why") || e.Since != nil {
		t.Fatalf("at the grace: %+v", e)
	}
}

func TestExpected(t *testing.T) {
	ex := ExpectedFor("", Env{MetalLB: true, K3s: true, Cloud: "aws"})
	if len(ex.Candidates) != 3 || ex.Note != "" {
		t.Fatalf("unset with several candidates: %+v", ex)
	}
	if ex := ExpectedFor("", Env{}); ex.Note != "no controller Klyx can see serves Services without a class" {
		t.Fatalf("unset with none: %+v", ex)
	}
	if ex := ExpectedFor("io.cilium/l2-announcer", Env{}); !ex.Known || ex.Note != "no Cilium LB IPAM detected for class io.cilium/l2-announcer" {
		t.Fatalf("known class not detected: %+v", ex)
	}
	if ex := ExpectedFor("io.cilium/node", Env{CiliumLB: true}); ex.Candidates[0].Controller != CtlCiliumNode || ex.Note != "" {
		t.Fatalf("io.cilium/node: %+v", ex)
	}
	if ex := ExpectedFor("networking.gke.io/l4-regional-external-legacy", Env{Cloud: "gce"}); !ex.Candidates[0].Detected {
		t.Fatalf("GKE legacy class: %+v", ex)
	}
	if ex := ExpectedFor("networking.gke.io/whatever", Env{Cloud: "gce"}); ex.Known {
		t.Fatalf("no GKE wildcard: %+v", ex)
	}
	if ex := ExpectedFor("example.com/custom", Env{MetalLB: true}); ex.Known || !strings.Contains(ex.Note, "may be configured for it") {
		t.Fatalf("custom class with MetalLB detected stays unproven: %+v", ex)
	}
	if ex := ExpectedFor("example.com/custom", Env{}); ex.Note != "no controller Klyx knows serves class example.com/custom" {
		t.Fatalf("unknown class: %+v", ex)
	}
}

func TestCloudAndK3s(t *testing.T) {
	n := func(pid, ver string) *corev1.Node {
		return &corev1.Node{Spec: corev1.NodeSpec{ProviderID: pid}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: ver}}}
	}
	if c := CloudOf([]*corev1.Node{n("kind://docker/klyx/klyx-control-plane", "v1.37.1")}); c != "" {
		t.Fatalf("kind is not a cloud: %q", c)
	}
	if c := CloudOf([]*corev1.Node{n("k3s://node1", "v1.37.1+k3s1"), n("aws:///eu-west-1a/i-1", "v1.37.1")}); c != "aws" {
		t.Fatalf("aws: %q", c)
	}
	if !K3sOf([]*corev1.Node{n("", "v1.37.1+k3s1")}) || K3sOf([]*corev1.Node{n("", "v1.37.1")}) {
		t.Fatal("k3s detection")
	}
}

func TestSvcLB(t *testing.T) {
	s := lbSvc(strings.Repeat("a", 47) + "--tail")
	if got := SvcLBName(s); got != "svclb-"+strings.Repeat("a", 47)+"-01234567" {
		t.Fatalf("trimmed at 48 without a trailing hyphen: %s", got)
	}
	s = lbSvc("web")
	own := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: SvcLBNamespace, Name: "svclb-web-1", OwnerReferences: []metav1.OwnerReference{{UID: "ds-uid"}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
			Reason: "Unschedulable", Message: "0/1 nodes are available: 1 node(s) didn't have free ports for the requested pod ports."}}}}
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: SvcLBNamespace, Name: "svclb-web-old",
		Labels:          map[string]string{SvcLBNameLabel: "web", SvcLBNamespaceLabel: "app"},
		OwnerReferences: []metav1.OwnerReference{{UID: "replaced-ds"}}}}
	ev := SvcLBOf(s, SvcLBName(s), "ds-uid", []*corev1.Pod{own, other})
	if ev == nil || ev.Pods != 1 || !ev.HostPortConflict || ev.Candidates != 1 || ev.Ready != 0 {
		t.Fatalf("svclb evidence: %+v", ev)
	}
	if ev := SvcLBOf(s, SvcLBName(s), "", []*corev1.Pod{other}); ev == nil || ev.DaemonSet != "" || ev.Candidates != 1 || ev.Pods != 0 {
		t.Fatalf("a pod owned by another DaemonSet is an unproven candidate: %+v", ev)
	}
}

func TestDiagnose(t *testing.T) {
	s := lbSvc("p")
	d := Diagnose(DiagnosisInput{Svc: s, Env: Env{MetalLB: true}, Events: []Event{{Reason: "AllocationFailed", Message: "no available IPs"}}, EventsRead: "complete"})
	if d.Summary != "AllocationFailed: no available IPs" {
		t.Fatalf("an event leads: %+v", d)
	}
	s.Status.Conditions = []metav1.Condition{{Type: CondRequestSatisfied, Status: "False", Reason: "no_pool", ObservedGeneration: 0}}
	d = Diagnose(DiagnosisInput{Svc: s, Env: Env{CiliumLB: true}, EventsRead: "complete"})
	if strings.HasPrefix(d.Summary, "Cilium") || d.Cilium == nil || d.Cilium.Current {
		t.Fatalf("a stale Cilium condition is historical, never current evidence: %+v", d)
	}
	s.Status.Conditions[0].ObservedGeneration = 1
	if d = Diagnose(DiagnosisInput{Svc: s, Env: Env{CiliumLB: true}}); d.Summary != "Cilium: no_pool" {
		t.Fatalf("current Cilium condition: %+v", d)
	}
	s = lbSvc("q", func(s *corev1.Service) { s.Annotations = map[string]string{"metallb.io/address-pool": "gold"} })
	d = Diagnose(DiagnosisInput{Svc: s, Env: Env{MetalLB: true}, Pool: &RequestedPool{Name: "gold", State: "missing"}})
	if d.Summary != "the requested pool gold does not exist" {
		t.Fatalf("named pool missing: %+v", d)
	}
	// silent cases
	if d = Diagnose(DiagnosisInput{Svc: lbSvc("r"), Env: Env{}}); d.Silent != "no controller Klyx can see serves Services without a class" {
		t.Fatalf("no controller: %+v", d)
	}
	if d = Diagnose(DiagnosisInput{Svc: lbSvc("r"), Env: Env{MetalLB: true}, NoPools: []string{CtlMetalLB}}); d.Silent != "MetalLB has no pool: it stays silent" {
		t.Fatalf("no pools: %+v", d)
	}
	cls := "io.cilium/l2-announcer"
	if d = Diagnose(DiagnosisInput{Svc: lbSvc("r", func(s *corev1.Service) { s.Spec.LoadBalancerClass = &cls }), Env: Env{CiliumLB: true}}); !strings.Contains(d.Silent, "served only when that Cilium feature is enabled") {
		t.Fatalf("a class whose feature may be off: %+v", d)
	}
	if d = Diagnose(DiagnosisInput{Svc: lbSvc("r"), Env: Env{K3s: true}, SvcLBRead: true}); !strings.Contains(d.Silent, "--disable=servicelb") {
		t.Fatalf("a disabled ServiceLB: %+v", d)
	}
	if d = Diagnose(DiagnosisInput{Svc: lbSvc("r"), Env: Env{MetalLB: true}}); d.Silent != "the expected controller wrote no event or condition for it" {
		t.Fatalf("a controller that writes nothing: %+v", d)
	}
}

func cpool(gen int64, disabled bool, conds ...Cond) *CiliumPool {
	return &CiliumPool{Meta: Meta{Name: "p", UID: "u", Generation: gen}, Disabled: disabled, Conds: conds}
}

func cnt(t string, msg string, og int64) Cond {
	return Cond{Type: t, Status: "Unknown", Message: msg, ObservedGeneration: og}
}

func TestCiliumPool(t *testing.T) {
	counts := func(og int64, total, avail string) []Cond {
		return []Cond{cnt(CondIPsTotal, total, og), cnt(CondIPsAvailable, avail, og), cnt(CondIPsUsed, "0", og)}
	}
	conflict := func(og int64) Cond {
		return Cond{Type: CondPoolConflict, Status: "True", Reason: "cidr_overlap", Message: "Pool conflicts", ObservedGeneration: og}
	}
	cases := []struct {
		name  string
		p     *CiliumPool
		role  health.Kind
		label string
	}{
		{"disabled", cpool(2, true), health.Paused, "Disabled"},
		{"current conflict, absent counts", cpool(2, false, conflict(2)), health.Fault, "Conflict"},
		{"current conflict, old counts", cpool(2, false, append(counts(1, "8", "8"), conflict(2))...), health.Fault, "Conflict"},
		{"stale conflict, current counts", cpool(2, false, append(counts(2, "8", "3"), conflict(1))...), health.OK, "3 of 8 available"},
		{"no status", cpool(1, false), health.Unknown, "Counts not reported yet"},
		{"old observedGeneration", cpool(2, false, counts(1, "8", "3")...), health.Unknown, "Counts for an earlier change"},
		{"malformed", cpool(1, false, counts(1, "eight", "3")...), health.Unknown, "Counts unreadable"},
		{"partial", cpool(1, false, cnt(CondIPsTotal, "8", 1)), health.Unknown, "Counts not reported yet"},
		{"exhausted", cpool(1, false, counts(1, "8", "0")...), health.Degraded, "Exhausted"},
		{"large IPv6", cpool(1, false, counts(1, "18446744073709551616", "18446744073709551615")...), health.OK, "18446744073709551615 of 18446744073709551616 available"},
	}
	for _, c := range cases {
		e := CiliumPoolRule(c.p)
		if e.Role != c.role || e.Label != c.label {
			t.Errorf("%s: %v %q", c.name, e.Role, e.Label)
		}
	}
}

func i64(v int64) *int64 { return &v }

func mpool(name string, lbls map[string]string, st PoolStatus) *MetalLBPool {
	return &MetalLBPool{Meta: Meta{Namespace: "metallb-system", Name: name, UID: "u-" + name}, Labels: lbls, AutoAssign: true, Status: st}
}

func ad(name string, mut func(*Advertisement)) *Advertisement {
	a := &Advertisement{Meta: Meta{Namespace: "metallb-system", Name: name}, Kind: advertisementL2}
	if mut != nil {
		mut(a)
	}
	return a
}

func TestMetalLBPool(t *testing.T) {
	inst := Instances{Namespaces: map[string]bool{"metallb-system": true}, Known: true}
	st := PoolStatus{AssignedV4: i64(1), AvailableV4: i64(3), AssignedV6: i64(0), AvailableV6: i64(0)}
	gold := mpool("gold", map[string]string{"tier": "gold"}, st)
	bronze := mpool("bronze", map[string]string{"tier": "bronze"}, st)
	expr := ad("expr", func(a *Advertisement) {
		a.SelectorsSet = true
		a.Selectors = []metav1.LabelSelector{{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"gold"}}}}}
	})
	run := func(p *MetalLBPool, ads []*Advertisement, states ...*ConfigState) health.NativeEval {
		return MetalLBPoolRule(MetalLBPoolInput{Pool: p, Instances: inst, Ads: ads, AdsRead: ReadComplete, States: states, StatesRead: ReadComplete})
	}
	if e := run(gold, []*Advertisement{expr}); e.Role != health.OK {
		t.Fatalf("expression selects gold: %+v", e)
	}
	if e := run(bronze, []*Advertisement{expr}); e.Role != health.Degraded || e.Label != "No advertisement" {
		t.Fatalf("tier In [gold] never selects bronze: %+v", e)
	}
	mixed := ad("mixed", func(a *Advertisement) {
		a.SelectorsSet = true
		a.Selectors = []metav1.LabelSelector{{MatchLabels: map[string]string{"tier": "gold"},
			MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "zone", Operator: metav1.LabelSelectorOpExists}}}}
	})
	if e := run(gold, []*Advertisement{mixed}); e.Label != "No advertisement" {
		t.Fatalf("mixed selector needs both: %+v", e)
	}
	bad := ad("bad", func(a *Advertisement) {
		a.SelectorsSet = true
		a.Selectors = []metav1.LabelSelector{{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "tier", Operator: "Bogus"}}}}
	})
	if e := run(gold, []*Advertisement{bad}); e.Role != health.Unknown || e.Label != "Selection unresolved" {
		t.Fatalf("a selector that does not convert is unresolved: %+v", e)
	}
	capped := ad("capped", func(a *Advertisement) { a.SelectorsSet, a.SelectorsCut = true, true })
	if e := run(gold, []*Advertisement{capped}); e.Label != "Selection unresolved" {
		t.Fatalf("a selector past the cap is unresolved: %+v", e)
	}
	// revision 3: a pool named only at the 33rd entry
	var names []string
	for i := 0; i < 40; i++ {
		names = append(names, fmt.Sprintf("p%02d", i))
	}
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "many", "namespace": "metallb-system"},
		"spec": map[string]any{"ipAddressPools": toAny(names)}}}
	many := ProjectAdvertisement(u, advertisementL2)
	if len(many.Pools) != maxPoolNames || !many.PoolsCut {
		t.Fatalf("cut list: %d %v", len(many.Pools), many.PoolsCut)
	}
	if e := run(mpool("p32", nil, st), []*Advertisement{many}); e.Role != health.Unknown || e.Label != "Selection unresolved" {
		t.Fatalf("a pool named only at entry 33 is unknown, never degraded: %+v", e)
	}
	if e := run(mpool("p05", nil, st), []*Advertisement{many}); e.Role != health.OK {
		t.Fatalf("a name within the first 32 of a cut list is selected: %+v", e)
	}
	// same-named advertisement in another namespace is never joined
	elsewhere := ad("gold-ad", func(a *Advertisement) { a.Namespace = "other"; a.Pools = []string{"gold"} })
	if e := run(gold, []*Advertisement{elsewhere}); e.Label != "No advertisement" {
		t.Fatalf("cross-namespace advertisement: %+v", e)
	}
	// a pool outside every instance namespace
	stray := mpool("gold", nil, st)
	stray.Namespace = "other"
	if e := run(stray, []*Advertisement{ad("all", nil)}); e.Role != health.Unknown || e.Label != "No MetalLB instance here" {
		t.Fatalf("pool outside instance namespaces: %+v", e)
	}
	// Invalid configuration withholds the advertisement verdict
	inv := &ConfigState{Meta: Meta{Namespace: "metallb-system", Name: "controller"}, Result: "Invalid", Error: "secret type mismatch"}
	if e := run(bronze, []*Advertisement{expr}, inv); e.Role != health.Unknown || e.Label != "Configuration invalid" {
		t.Fatalf("Invalid with an unselected pool is withheld: %+v", e)
	}
	if e := MetalLBPoolRule(MetalLBPoolInput{Pool: gold, Instances: inst, Ads: []*Advertisement{expr}, AdsRead: ReadStale}); e.Label != "Advertisements unverified" {
		t.Fatalf("stale advertisement read: %+v", e)
	}
	// counts after the advertisement verdict
	if e := run(mpool("n", nil, PoolStatus{}), []*Advertisement{ad("all", nil)}); e.Label != "Counts not reported" {
		t.Fatalf("no counts: %+v", e)
	}
	if e := run(mpool("n", nil, PoolStatus{}), nil); e.Label != "No advertisement" {
		t.Fatalf("missing counts never hide the advertisement verdict: %+v", e)
	}
	full := PoolStatus{AssignedV4: i64(1), AvailableV4: i64(0), AssignedV6: i64(0), AvailableV6: i64(0)}
	if e := run(mpool("f", nil, full), []*Advertisement{ad("all", nil)}); e.Label != "Exhausted" {
		t.Fatalf("exhausted: %+v", e)
	}
	empty := PoolStatus{AssignedV4: i64(0), AvailableV4: i64(0), AssignedV6: i64(0), AvailableV6: i64(0)}
	if e := run(mpool("e", nil, empty), []*Advertisement{ad("all", nil)}); e.Label != "0 available" {
		t.Fatalf("a pool with no addresses assigned is not exhausted: %+v", e)
	}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func TestLabelsCut(t *testing.T) {
	ls := map[string]any{}
	for i := 0; i < 40; i++ {
		ls[fmt.Sprintf("k%02d", i)] = "v"
	}
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "p", "namespace": "metallb-system", "labels": ls}}}
	p := ProjectMetalLBPool(u)
	if len(p.Labels) != maxLabels || !p.LabelsCut || p.Labels["k00"] != "v" || p.Labels["k39"] != "" {
		t.Fatalf("labels kept in sorted order and cut: %d %v", len(p.Labels), p.LabelsCut)
	}
	in := ad("sel", func(a *Advertisement) {
		a.SelectorsSet = true
		a.Selectors = []metav1.LabelSelector{{MatchLabels: map[string]string{"k39": "v"}}}
	})
	if proven, unresolved := Selects(in, p); proven || !unresolved {
		t.Fatalf("a cut label could have matched: %v %v", proven, unresolved)
	}
	notIn := ad("notin", func(a *Advertisement) {
		a.SelectorsSet = true
		a.Selectors = []metav1.LabelSelector{{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "k39", Operator: metav1.LabelSelectorOpDoesNotExist}}}}
	})
	if proven, unresolved := Selects(notIn, p); proven || !unresolved {
		t.Fatalf("DoesNotExist on cut labels proves nothing: %v %v", proven, unresolved)
	}
}

func TestProjectStatusPresence(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "p", "namespace": "m"},
		"status": map[string]any{"assignedIPv4": int64(2)}}}
	p := ProjectMetalLBPool(u)
	if p.Status.AssignedV4 == nil || *p.Status.AssignedV4 != 2 || p.Status.AvailableV4 != nil {
		t.Fatalf("presence kept: %+v", p.Status)
	}
	c := ProjectCiliumPool(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "old", "generation": int64(1)},
		"spec": map[string]any{"cidrs": []any{map[string]any{"cidr": "10.0.0.0/24"}}},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": CondIPsTotal, "status": "Unknown", "message": "256", "observedGeneration": int64(1)},
			map[string]any{"type": "other/Thing", "status": "True"},
		}}}})
	if c.Blocks != 1 || c.BlockList[0] != "10.0.0.0/24" || len(c.Conds) != 1 {
		t.Fatalf("v2alpha1 cidrs and the read conditions: %+v", c)
	}
}

func TestConfigState(t *testing.T) {
	cases := map[string]health.Kind{"Invalid": health.Fault, "Valid": health.OK, "Unknown": health.Unknown, "": health.Unknown}
	for r, want := range cases {
		if e := ConfigStateRule(&ConfigState{Result: r}); e.Role != want {
			t.Errorf("%q: %v", r, e.Role)
		}
	}
	s := &ConfigState{Meta: Meta{Name: "speaker-node-a"}}
	if s.Role() != "speaker" || s.Node() != "node-a" {
		t.Fatal("speaker identity")
	}
	in := InstancesOf([]*ConfigState{{Meta: Meta{Namespace: "lb", Name: "controller"}}}, ReadComplete, []string{"metallb-system"}, true)
	if !in.Has("lb") || !in.Has("metallb-system") || in.FromWorkload["lb"] || !in.FromWorkload["metallb-system"] {
		t.Fatalf("instances: %+v", in)
	}
	// MetalLB before ConfigurationState, its controllers unreadable: nothing
	// is known, so no pool reads "no instance here"
	if in := InstancesOf(nil, ReadComplete, nil, false); in.Known {
		t.Fatalf("an empty states read is no evidence: %+v", in)
	}
	p := mpool("gold", nil, PoolStatus{AvailableV4: i64(1), AssignedV4: i64(0)})
	if e := MetalLBPoolRule(MetalLBPoolInput{Pool: p, Instances: InstancesOf(nil, ReadComplete, nil, false), Ads: []*Advertisement{ad("all", nil)}, AdsRead: ReadComplete}); e.Label == "No MetalLB instance here" {
		t.Fatalf("unknown instances never exclude a pool: %+v", e)
	}
}

func TestTracker(t *testing.T) {
	var tr Tracker
	c1 := Continuity{Generation: 1, Epoch: 1}
	o := func(st SvcState, rv string) Obs { return Obs{State: st, RV: rv} }
	// a Service created after the kind went live whose first capture already
	// shows it pending (ClusterIP, allocated and lost coalesced before it)
	// gets the first-seen lower bound, never its creation time (revision 2)
	eps := tr.Observe(c1, t0, map[string]Obs{"a": o(StatePending, "1"), "b": o(StateAllocated, "1"), "c": o(StateOther, "1")}, false)
	if eps["a"].Start != t0 || eps["a"].Observed || len(eps) != 1 {
		t.Fatalf("first seen: %+v", eps)
	}
	// continuity: a unwritten stays; b loses its address and c becomes a
	// LoadBalancer (observed)
	eps = tr.Observe(c1, t0.Add(time.Minute), map[string]Obs{"a": o(StatePending, "1"), "b": o(StatePending, "2"), "c": o(StatePending, "2")}, false)
	if eps["a"].Start != t0 || !eps["b"].Observed || eps["b"].Start != t0.Add(time.Minute) || !eps["c"].Observed {
		t.Fatalf("observed starts: %+v", eps)
	}
	// clearing: an allocation ends the episode; pending again is a new one
	tr.Observe(c1, t0.Add(2*time.Minute), map[string]Obs{"a": o(StateAllocated, "2"), "b": o(StatePending, "2"), "c": o(StatePending, "2")}, false)
	eps = tr.Observe(c1, t0.Add(3*time.Minute), map[string]Obs{"a": o(StatePending, "3"), "b": o(StatePending, "2"), "c": o(StatePending, "2")}, false)
	if eps["a"].Start != t0.Add(3*time.Minute) || !eps["a"].Observed {
		t.Fatalf("cleared then pending: %+v", eps)
	}
	// Codex's S4 review, finding 2: written between two snapshots that
	// skipped changes, a pending Service may have held an address meanwhile:
	// a lower bound. One contiguous write (pending to pending) carries on.
	eps = tr.Observe(c1, t0.Add(4*time.Minute), map[string]Obs{"a": o(StatePending, "3"), "b": o(StatePending, "5"), "c": o(StatePending, "2")}, false)
	if eps["b"].Start != t0.Add(4*time.Minute) || eps["b"].Observed || eps["c"].Start != t0.Add(time.Minute) {
		t.Fatalf("a coalesced write restarts only that Service: %+v", eps)
	}
	eps = tr.Observe(c1, t0.Add(5*time.Minute), map[string]Obs{"a": o(StatePending, "3"), "b": o(StatePending, "6"), "c": o(StatePending, "2")}, true)
	if eps["b"].Start != t0.Add(4*time.Minute) {
		t.Fatalf("a contiguous pending-to-pending write carries on: %+v", eps)
	}
	// a relist (epoch) restarts every episode as a lower bound
	c2 := Continuity{Generation: 1, Epoch: 2}
	eps = tr.Observe(c2, t0.Add(6*time.Minute), map[string]Obs{"a": o(StatePending, "3"), "b": o(StatePending, "6")}, false)
	if eps["a"].Observed || eps["a"].Start != t0.Add(6*time.Minute) || eps["b"].Start != t0.Add(6*time.Minute) {
		t.Fatalf("a gap restarts: %+v", eps)
	}
	// absence: c is gone from a complete read; a new UID under its name is new
	eps = tr.Observe(c2, t0.Add(7*time.Minute), map[string]Obs{"a": o(StatePending, "3"), "c2": o(StatePending, "1")}, false)
	if eps["c2"].Observed || eps["c2"].Start != t0.Add(7*time.Minute) {
		t.Fatalf("replacement: %+v", eps)
	}
	// a break (the kind left live) restarts on the next observation
	tr.Break()
	eps = tr.Observe(c2, t0.Add(8*time.Minute), map[string]Obs{"a": o(StatePending, "3")}, false)
	if eps["a"].Start != t0.Add(8*time.Minute) || eps["a"].Observed {
		t.Fatalf("after a break: %+v", eps)
	}
	// restart: a new tracker sees every pending Service first
	var fresh Tracker
	if eps = fresh.Observe(c2, t0.Add(9*time.Minute), map[string]Obs{"a": o(StatePending, "3")}, true); eps["a"].Start != t0.Add(9*time.Minute) {
		t.Fatalf("restart: %+v", eps)
	}
}

// Codex's S4 implementation review, finding 5: IPv6 reported with its
// available count missing proves nothing about its capacity.
func TestS4ReviewIncompleteFamilyCannotProveExhaustion(t *testing.T) {
	inst := Instances{Namespaces: map[string]bool{"metallb-system": true}, Known: true}
	run := func(st PoolStatus) health.NativeEval {
		return MetalLBPoolRule(MetalLBPoolInput{Pool: mpool("p", nil, st), Instances: inst, Ads: []*Advertisement{ad("all", nil)}, AdsRead: ReadComplete})
	}
	if e := run(PoolStatus{AssignedV4: i64(1), AvailableV4: i64(0), AssignedV6: i64(1)}); e.Role != health.Unknown || e.Label != "Counts incomplete" {
		t.Fatalf("a missing count is not zero: %+v", e)
	}
	if e := run(PoolStatus{AssignedV4: i64(1), AvailableV4: i64(3), AssignedV6: i64(1)}); e.Role != health.OK {
		t.Fatalf("free capacity in a complete family settles it: %+v", e)
	}
}

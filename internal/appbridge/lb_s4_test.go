package appbridge

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/moomora/klyx/internal/crd"
	"github.com/moomora/klyx/internal/health"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/lb"
	"github.com/moomora/klyx/internal/observe"
)

const s4Needle = "S4LABneedle"

// The serialised boundary (S4 §7): a port error's text, an annotation off
// the allowlist, and credentials in a condition message or a configuration
// error never reach the rows DTOs; the AI Explain lens fields carry no
// address, hostname, address block, annotation value, requested pool name
// or event message.
func TestLBNeedleNeverCrosses(t *testing.T) {
	errText := "Port" + s4Needle
	raw := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "web", UID: "u1", Generation: 1,
		Annotations: map[string]string{"team": s4Needle, "metallb.io/address-pool": "pool-" + "secretname", "metallb.io/loadBalancerIPs": "10.9.8.7"}},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "10.1.2.3", Hostname: "lb.example.internal",
				Ports: []corev1.PortStatus{{Port: 80, Protocol: corev1.ProtocolTCP, Error: &errText}}}}},
			Conditions: []metav1.Condition{{Type: "x/Cond", Status: "False", Message: "see https://user:" + s4Needle + "@example.com/p"}}}}
	o, err := healthsrc.ProjectService(raw)
	if err != nil {
		t.Fatal(err)
	}
	svc := o.(*corev1.Service)
	a := lb.AllocationOf(svc)
	row := healthsrc.Row[healthsrc.LBServiceRow]{Ref: healthsrc.RowRef{UID: "u1", GVR: "v1/services", Namespace: "app", Name: "web"},
		Eval: health.NativeEval{Role: health.Degraded, Label: "Port error", Included: true},
		Data: healthsrc.LBServiceRow{Service: svc, Allocation: a, Expected: lb.ExpectedFor("", lb.Env{MetalLB: true})}}
	pending := svc.DeepCopy()
	pending.Status.LoadBalancer.Ingress = nil
	ep := lb.Episode{Start: time.Now()}
	d := lb.Diagnose(lb.DiagnosisInput{Svc: pending, Env: lb.Env{MetalLB: true}, Events: []lb.Event{{Reason: "AllocationFailed", Message: "no IP 10.1.2.3 for web"}},
		Pool: &lb.RequestedPool{Name: "pool-secretname", State: "missing"}})
	prow := healthsrc.Row[healthsrc.LBServiceRow]{Ref: healthsrc.RowRef{UID: "u2"}, Data: healthsrc.LBServiceRow{Service: pending, Episode: &ep, Diagnosis: &d}}
	state := lb.ProjectConfigState(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "controller", "namespace": "metallb-system"},
		"status": map[string]any{"result": "Invalid", "errorSummary": "peer https://admin:" + s4Needle + "@10.0.0.1 failed"}}})
	pool := lb.ProjectMetalLBPool(&unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "gold", "namespace": "metallb-system"},
		"spec": map[string]any{"addresses": []any{"192.168.77.0/24"}}}})

	dtos := []any{lbServiceDTO(row), lbServiceDTO(prow), lbStateDTO(healthsrc.Row[healthsrc.LBStateRow]{Data: healthsrc.LBStateRow{State: state}})}
	for i, v := range dtos {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), s4Needle) {
			t.Errorf("DTO %d disclosed: %s", i, b)
		}
	}
	project := func(key string, v any) string {
		b, _ := json.Marshal(v)
		var m any
		_ = json.Unmarshal(b, &m)
		out, _ := json.Marshal((&scrubber{}).project(m, aiEvidenceFields[key]))
		return string(out)
	}
	for _, s := range []string{project(healthsrc.TaskLB+"/services", lbServiceDTO(row)), project(healthsrc.TaskLB+"/services", lbServiceDTO(prow)),
		project(healthsrc.TaskLB+"/pools", lbPoolDTO(healthsrc.Row[healthsrc.LBPoolRow]{Data: healthsrc.LBPoolRow{MetalLB: pool}})),
		project(healthsrc.TaskLB+"/metallb", lbStateDTO(healthsrc.Row[healthsrc.LBStateRow]{Data: healthsrc.LBStateRow{State: state}}))} {
		for _, bad := range []string{"10.1.2.3", "lb.example.internal", "10.9.8.7", "secretname", "192.168.77", "no IP", s4Needle} {
			if strings.Contains(s, bad) {
				t.Errorf("AI evidence carries %q: %s", bad, s)
			}
		}
	}
}

// Codex's S4 implementation review, finding 1: the S4 policy holds for the
// whole Explain document - the rule's derived reason, the target's facts and
// condition messages, related objects and Warning events - through the
// serialized prompt, for a Service and for a pool.
func TestS4ReviewExplainDocumentExclusions(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app", UID: "u", Generation: 1}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer}}
	ep := lb.Episode{Start: evalAt.Add(-time.Hour)}
	msg := "EVENT-EGRESS-MARKER cannot allocate 10.77.66.55 from pool PRIVATE-POOL"
	d := lb.Diagnose(lb.DiagnosisInput{Svc: svc, Events: []lb.Event{{Reason: "AllocationFailed", Message: msg}}})
	row := healthsrc.Row[healthsrc.LBServiceRow]{Ref: healthsrc.RowRef{UID: "u", GVR: "v1/services", Namespace: "app", Name: "web", Generation: 1},
		Eval: lb.ServiceRule(svc, lb.AllocationOf(svc), ep, evalAt, d.Summary), Data: healthsrc.LBServiceRow{Service: svc, Allocation: lb.AllocationOf(svc), Episode: &ep, Diagnosis: &d}}
	rows := observe.Rows{Task: "lb", Cluster: "c", EvaluatedAt: evalAt, Part: observe.Part{ID: "services", Classifier: "native"},
		Rows: healthsrc.RowSet[healthsrc.LBServiceRow]{Rows: []healthsrc.Row[healthsrc.LBServiceRow]{row}}}
	detail := crd.InstanceDetail{Kind: "Service", Namespace: "app", Name: "web", UID: "u", Generation: 1,
		Events:     []crd.Event{{Type: "Warning", Reason: "AllocationFailed", Message: msg}, {Type: "Warning", Reason: "free text PRIVATE-POOL", Message: "x"}},
		Facts:      []crd.Fact{{Label: "LB address", Value: "10.77.66.55"}, {Label: "type", Value: "LoadBalancer"}},
		Conditions: []crd.Condition{{Type: "x/Cond", Status: "False", Reason: "NotReady", Message: msg}}}
	cp := &lb.CiliumPool{Meta: lb.Meta{APIVersion: "cilium.io/v2", Name: "p", UID: "pu", Generation: 1},
		Conds: []lb.Cond{{Type: lb.CondPoolConflict, Status: "True", Reason: "cidr_overlap", Message: "range '10.77.66.0/24' overlaps PRIVATE-POOL", ObservedGeneration: 1}}}
	n := lb.CountsOf(cp)
	cf, cur := cp.Conflict()
	prow := healthsrc.Row[healthsrc.LBPoolRow]{Ref: healthsrc.RowRef{UID: "pu", GVR: "cilium.io/v2/ciliumloadbalancerippools", Name: "p", Generation: 1},
		Eval: lb.CiliumPoolRule(cp), Data: healthsrc.LBPoolRow{Cilium: cp, Counts: &n, Conflict: cf, ConflictCurrent: cur}}
	prows := observe.Rows{Task: "lb", Cluster: "c", EvaluatedAt: evalAt, Part: observe.Part{ID: "pools", Classifier: "native"},
		Rows: healthsrc.RowSet[healthsrc.LBPoolRow]{Rows: []healthsrc.Row[healthsrc.LBPoolRow]{prow}}}
	pdetail := crd.InstanceDetail{Kind: "CiliumLoadBalancerIPPool", Name: "p", UID: "pu", Generation: 1,
		Conditions: []crd.Condition{{Type: lb.CondPoolConflict, Status: "True", Reason: "cidr_overlap", Message: "range '10.77.66.0/24' overlaps PRIVATE-POOL"}}}
	conn := &fakeExplainConn{rows: map[string]observe.Rows{"lb/services": rows, "lb/pools": prows},
		details: map[string]crd.InstanceDetail{"/services/app/web": detail, "cilium.io/ciliumloadbalancerippools//p": pdetail}}
	for _, target := range []ExplainTargetDTO{{Kind: "row", Task: "lb", Part: "services", UID: "u"}, {Kind: "row", Task: "lb", Part: "pools", UID: "pu"}} {
		cl := &fakeCompleter{reply: "x"}
		res := explainSvc(cl, conn).Explain("c", target)
		if !res.OK {
			t.Fatalf("%s: Explain failed: %+v", target.Part, res)
		}
		raw, _ := json.Marshal(evidenceOf(t, cl.gotUser))
		for _, needle := range []string{"EVENT-EGRESS-MARKER", "10.77.66", "PRIVATE-POOL"} {
			if strings.Contains(string(raw), needle) {
				t.Errorf("%s: %q reached the evidence: %s", target.Part, needle, raw)
			}
		}
		if target.Part == "services" && (!strings.Contains(string(raw), `"cause":"event AllocationFailed"`) || !strings.Contains(string(raw), `"LoadBalancer"`)) {
			t.Errorf("the structural cause and the safe facts stay: %s", raw)
		}
	}
}

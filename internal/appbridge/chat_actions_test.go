package appbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/config"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A3 §2: a proposal's steps are decided by Klyx from its own reads.

var (
	deploysGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	stsGVR     = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	dsGVR      = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}
	nodesGVR   = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	soGVR      = schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
)

func objFact(gvr schema.GroupVersionResource, m map[string]any) actionFact {
	return factOf(gvr, uobj(m), "r1", "2026-10-24T12:00:00Z")
}

func TestFactsAreReadOffTheObject(t *testing.T) {
	dep := objFact(deploysGVR, map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "d1", "generation": int64(3)},
		"spec": map[string]any{"replicas": int64(3), "paused": true, "strategy": map[string]any{"type": "RollingUpdate"}}})
	if o := dep.Object; o == nil || *o.Replicas != 3 || !o.Paused || o.Strategy != "RollingUpdate" || o.Generation != 3 || *o.Desired != 3 || dep.Group != "apps" {
		t.Fatalf("deployment: %+v %+v", dep, dep.Object)
	}
	noReplicas := objFact(deploysGVR, map[string]any{"kind": "Deployment", "metadata": map[string]any{"name": "web", "uid": "d2"}})
	if *noReplicas.Object.Replicas != 1 {
		t.Fatal("an unset spec.replicas is the API's default, 1")
	}
	sts := objFact(stsGVR, map[string]any{"kind": "StatefulSet", "metadata": map[string]any{"name": "db", "uid": "s1"},
		"spec": map[string]any{"replicas": int64(2), "updateStrategy": map[string]any{"type": "RollingUpdate", "rollingUpdate": map[string]any{"partition": int64(1)}}}})
	if sts.Object.Partition != 1 || sts.Object.Strategy != "RollingUpdate" {
		t.Fatalf("statefulset: %+v", sts.Object)
	}
	ds := objFact(dsGVR, map[string]any{"kind": "DaemonSet", "metadata": map[string]any{"name": "agent", "uid": "ds1"},
		"spec": map[string]any{"updateStrategy": map[string]any{"type": "OnDelete"}}, "status": map[string]any{"desiredNumberScheduled": int64(4)}})
	if ds.Object.Strategy != "OnDelete" || *ds.Object.Desired != 4 || ds.Object.Replicas != nil {
		t.Fatalf("daemonset: %+v", ds.Object)
	}
	ks := objFact(ksGVR, map[string]any{"kind": "Kustomization", "metadata": map[string]any{"name": "a1-web", "uid": "k1"}, "spec": map[string]any{"suspend": true, "interval": "1m"}})
	if !ks.Object.Suspended || ks.Object.Interval != "1m" {
		t.Fatalf("kustomization: %+v", ks.Object)
	}
	ctrl := true
	pod := objFact(podsGVR, map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "web-x", "uid": "p1",
		"ownerReferences": []any{map[string]any{"kind": "ReplicaSet", "name": "web-1", "uid": "rs1", "controller": ctrl, "apiVersion": "apps/v1"}}}})
	if pod.Object.OwnerKind != "ReplicaSet" || pod.Object.OwnerUID != "rs1" {
		t.Fatalf("pod: %+v", pod.Object)
	}
	node := objFact(nodesGVR, map[string]any{"kind": "Node", "metadata": map[string]any{"name": "n1", "uid": "n1"}, "spec": map[string]any{"unschedulable": true}})
	if !node.Object.Unschedulable {
		t.Fatal("node")
	}
	so := objFact(soGVR, map[string]any{"kind": "ScaledObject", "metadata": map[string]any{"name": "q", "uid": "so1", "annotations": map[string]any{"autoscaling.keda.sh/paused": "true"}}})
	if !so.Object.KEDAPaused {
		t.Fatal("scaledobject")
	}
}

// A3 §2.2: at most 64 subjects, the oldest dropped first; a newer read of a
// subject keeps it and replaces only its own part; an oversized record is
// not kept.
func TestTheFactsRecordIsBounded(t *testing.T) {
	a := newActionFacts()
	for i := 0; i < actionFactsMax+5; i++ {
		a.add(actionFact{Kind: "Pod", Namespace: "shop", Name: fmt.Sprintf("p%d", i), UID: fmt.Sprintf("u%d", i), Object: &objectFacts{ReadID: "r1"}})
	}
	if a.len() != actionFactsMax || len(a.named("Pod", "shop", "p0")) != 0 || len(a.named("Pod", "shop", "p5")) != 1 {
		t.Fatalf("the oldest are dropped: %d", a.len())
	}
	a.add(actionFact{Kind: "Pod", Namespace: "shop", Name: "p5", UID: "u5", History: &historyFacts{ReadID: "r9", PartnerRevision: 2}})
	got := a.named("Pod", "shop", "p5")[0]
	if got.Object == nil || got.History == nil || got.History.ReadID != "r9" {
		t.Fatalf("a newer read merges its own part: %+v", got)
	}
	a.add(actionFact{Kind: "Pod", Namespace: "shop", Name: "fresh", UID: "u-fresh", Object: &objectFacts{}})
	if len(a.named("Pod", "shop", "p5")) != 1 || len(a.named("Pod", "shop", "p6")) != 0 {
		t.Fatal("the re-read subject is now among the newest; the next oldest was dropped")
	}
	big := actionFact{Kind: "Pod", Namespace: "shop", Name: "big", UID: "u-big", Object: &objectFacts{OwnerName: strings.Repeat("x", actionFactBytesMax)}}
	a.add(big)
	if len(a.named("Pod", "shop", "big")) != 0 {
		t.Fatal("an oversized record is not kept")
	}
}

// planFixture: the §6.1 lab's facts and ledger - the Kustomization a1-web
// (the anchor) and its Deployment web, with rollout_history's partner.
func planFixture() (*actionFacts, []ChatRead, anchorFacts) {
	facts := newActionFacts()
	facts.add(actionFact{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Namespace: "klyx-a1-flux", Name: "a1-web", UID: "ks-uid",
		Object: &objectFacts{ReadID: "r1", ReadAt: "t1", Interval: "1m"}})
	three := int64(3)
	facts.add(actionFact{Kind: "Deployment", Group: "apps", Namespace: "klyx-a1-app", Name: "web", UID: "dep-uid",
		Object:  &objectFacts{ReadID: "r3", ReadAt: "t3", Replicas: &three, Desired: &three, Strategy: "RollingUpdate"},
		History: &historyFacts{ReadID: "r4", DesiredRevision: 5, PartnerRevision: 4, PartnerObjectUID: "rs4-uid", PartnerDigest: "dig4"}})
	ledger := []ChatRead{
		{ID: "r1", Tool: toolGet, Refs: []ChatRef{{UID: "ks-uid"}}, Checks: []ChatCheck{{ID: "r1.c1", Subject: ChatCheckSubject{Kind: "Kustomization", UID: "ks-uid"}}}},
		{ID: "r2", Tool: toolLogs, About: []ChatCheckSubject{byName("Pod", "klyx-a1-app", "web-x")}},
		{ID: "r3", Tool: toolGet, Refs: []ChatRef{{UID: "dep-uid"}}},
		{ID: "r4", Tool: toolRollout, Refs: []ChatRef{{UID: "dep-uid"}}, Checks: []ChatCheck{{ID: "r4.c1", Subject: ChatCheckSubject{Kind: "Deployment", UID: "dep-uid"}}}},
		{ID: "r5", Tool: toolGet, Refs: []ChatRef{{UID: "other-uid"}}},
	}
	return facts, ledger, anchorFacts{subject: ChatCheckSubject{Kind: "Kustomization", UID: "ks-uid"}}
}

func step(verb, kind, ns, name, params string, cites ...string) string {
	p := ""
	if params != "" {
		p = `,"params":` + params
	}
	c, _ := json.Marshal(cites)
	return fmt.Sprintf(`{"verb":%q,"target":{"kind":%q,"namespace":%q,"name":%q}%s,"cites":%s,"why":"because"}`, verb, kind, ns, name, p, c)
}

func plan(t *testing.T, steps ...string) ActionPlanDTO {
	t.Helper()
	in, err := parseActions(json.RawMessage(`{"steps":[` + strings.Join(steps, ",") + `]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	facts, ledger, a := planFixture()
	return validatePlan(in, facts, ledger, a).dto
}

// The roadmap's §6.1 plan: suspend the Kustomization, then roll the
// Deployment back to the partner revision. Both are offered; the undo is
// pinned to the partner rollout_history established.
func TestTheAcceptancePlanIsOffered(t *testing.T) {
	in, _ := parseActions(json.RawMessage(`{"steps":[` + step("flux-suspend", "Kustomization", "klyx-a1-flux", "a1-web", "", "r1.c1") + "," +
		step("rollout-undo", "Deployment", "klyx-a1-app", "web", `{"revision":4}`, "r4.c1") + `]}`))
	facts, ledger, a := planFixture()
	p := validatePlan(in, facts, ledger, a)
	for _, s := range p.dto.Steps {
		if s.State != stepOffered {
			t.Fatalf("step %d: %+v", s.N, s)
		}
	}
	if p.dto.Steps[1].Target.UID != "dep-uid" || !p.dto.Steps[1].Danger || p.dto.Steps[1].FactsRead != "r3" {
		t.Fatalf("the undo's target is Klyx's: %+v", p.dto.Steps[1])
	}
	if p.partner[1] == nil || p.partner[1].ObjectUID != "rs4-uid" || p.partner[1].TemplateDigest != "dig4" {
		t.Fatalf("the undo is pinned to the partner: %+v", p.partner[1])
	}
	if p.partner[0] != nil {
		t.Fatal("only an undo pins a partner")
	}
}

func TestEveryStepRule(t *testing.T) {
	for _, c := range []struct {
		name  string
		step  string
		state string
		why   string
	}{
		{"unknown verb", step("helm-rollback", "Deployment", "klyx-a1-app", "web", "", "r3"), stepRefused, "not a verb Klyx offers"},
		{"wrong kind", step("scale", "DaemonSet", "klyx-a1-app", "web", `{"replicas":1}`, "r3"), stepRefused, "does not act on a DaemonSet"},
		{"scale without replicas", step("scale", "Deployment", "klyx-a1-app", "web", "", "r3"), stepRefused, "takes replicas"},
		{"undo with replicas", step("rollout-undo", "Deployment", "klyx-a1-app", "web", `{"replicas":2}`, "r3"), stepRefused, "takes revision"},
		{"suspend with params", step("flux-suspend", "Kustomization", "klyx-a1-flux", "a1-web", `{"revision":2}`, "r1"), stepRefused, "takes no parameters"},
		{"out of range", step("scale", "Deployment", "klyx-a1-app", "web", `{"replicas":20000}`, "r3"), stepRefused, "replicas must be 0 to"},
		{"never read", step("scale", "Deployment", "klyx-a1-app", "api", `{"replicas":1}`, "r3"), stepRefused, "has not read Deployment klyx-a1-app/api by UID"},
		{"read by name only", step("delete-pod", "Pod", "klyx-a1-app", "web-x", "", "r2"), stepRefused, "has not read Pod klyx-a1-app/web-x by UID"},
		{"scale to the same count", step("scale", "Deployment", "klyx-a1-app", "web", `{"replicas":3}`, "r3"), stepRefused, "already at 3 replicas"},
		{"undo to the current revision", step("rollout-undo", "Deployment", "klyx-a1-app", "web", `{"revision":5}`, "r4"), stepRefused, "only the partner revision rollout_history established, 4"},
		{"undo to another revision", step("rollout-undo", "Deployment", "klyx-a1-app", "web", `{"revision":2}`, "r4"), stepRefused, "only the partner revision"},
		{"resume an active unit", step("flux-resume", "Kustomization", "klyx-a1-flux", "a1-web", "", "r1"), stepRefused, "no change: it is not suspended"},
		{"resume a running rollout", step("rollout-resume", "Deployment", "klyx-a1-app", "web", "", "r3"), stepRefused, "no change: it is not paused"},
		{"an unknown citation", step("scale", "Deployment", "klyx-a1-app", "web", `{"replicas":1}`, "r99"), stepUnsupported, "r99, which is not in this investigation's ledger"},
		{"a citation about another object", step("scale", "Deployment", "klyx-a1-app", "web", `{"replicas":1}`, "r5"), stepUnsupported, "none of its citations is about its target"},
		{"a citation about the anchor", step("scale", "Deployment", "klyx-a1-app", "web", `{"replicas":1}`, "r1.c1"), stepOffered, ""},
		{"scale down", step("scale", "Deployment", "klyx-a1-app", "web", `{"replicas":1}`, "r3"), stepOffered, ""},
	} {
		got := plan(t, c.step).Steps[0]
		if got.State != c.state || !strings.Contains(got.Reason, c.why) {
			t.Errorf("%s: %s %q", c.name, got.State, got.Reason)
		}
		if c.name == "scale down" && !got.Danger {
			t.Error("a scale down carries the danger treatment")
		}
	}
}

// §1's refusals: steps that could not take their effect.
func TestStepsThatCouldNotTakeEffectAreRefused(t *testing.T) {
	zero, two := int64(0), int64(2)
	for name, c := range map[string]struct {
		fact actionFact
		step string
		why  string
	}{
		"restart paused":       {actionFact{Kind: "Deployment", Group: "apps", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{Paused: true, Desired: &two, Replicas: &two}}, step("rollout-restart", "Deployment", "n", "w", "", "r1"), "paused: a restart would not roll out"},
		"restart OnDelete":     {actionFact{Kind: "DaemonSet", Group: "apps", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{Strategy: "OnDelete", Desired: &two}}, step("rollout-restart", "DaemonSet", "n", "w", "", "r1"), "OnDelete"},
		"restart partitioned":  {actionFact{Kind: "StatefulSet", Group: "apps", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{Partition: 1, Desired: &two, Replicas: &two}}, step("rollout-restart", "StatefulSet", "n", "w", "", "r1"), "partition 1"},
		"restart no pods":      {actionFact{Kind: "Deployment", Group: "apps", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{Desired: &zero, Replicas: &zero}}, step("rollout-restart", "Deployment", "n", "w", "", "r1"), "wants no pods"},
		"restart unknown":      {actionFact{Kind: "DaemonSet", Group: "apps", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{}}, step("rollout-restart", "DaemonSet", "n", "w", "", "r1"), "did not read how many pods"},
		"undo paused":          {actionFact{Kind: "Deployment", Group: "apps", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{Paused: true}, History: &historyFacts{PartnerRevision: 1, PartnerObjectUID: "x", PartnerDigest: "d"}}, step("rollout-undo", "Deployment", "n", "w", `{"revision":1}`, "r1"), "paused: the reverted template"},
		"undo no history":      {actionFact{Kind: "Deployment", Group: "apps", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{}}, step("rollout-undo", "Deployment", "n", "w", `{"revision":1}`, "r1"), "rollout_history has not been read"},
		"undo no partner":      {actionFact{Kind: "Deployment", Group: "apps", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{}, History: &historyFacts{DesiredRevision: 1}}, step("rollout-undo", "Deployment", "n", "w", `{"revision":1}`, "r1"), "established no partner"},
		"reconcile suspended":  {actionFact{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{Suspended: true}}, step("flux-reconcile", "Kustomization", "n", "w", "", "r1"), "suspended: Flux skips"},
		"suspend suspended":    {actionFact{Kind: "Kustomization", Group: "kustomize.toolkit.fluxcd.io", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{Suspended: true}}, step("flux-suspend", "Kustomization", "n", "w", "", "r1"), "already suspended"},
		"cordon cordoned":      {actionFact{Kind: "Node", Name: "w", UID: "u", Object: &objectFacts{Unschedulable: true}}, step("cordon", "Node", "", "w", "", "r1"), "already cordoned"},
		"uncordon schedulable": {actionFact{Kind: "Node", Name: "w", UID: "u", Object: &objectFacts{}}, step("uncordon", "Node", "", "w", "", "r1"), "not cordoned"},
		"keda pause paused":    {actionFact{Kind: "ScaledObject", Group: "keda.sh", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{KEDAPaused: true}}, step("keda-pause", "ScaledObject", "n", "w", "", "r1"), "already carries"},
		"keda resume active":   {actionFact{Kind: "ScaledObject", Group: "keda.sh", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{}}, step("keda-resume", "ScaledObject", "n", "w", "", "r1"), "carries no pause"},
		"pause paused":         {actionFact{Kind: "Deployment", Group: "apps", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{Paused: true}}, step("rollout-pause", "Deployment", "n", "w", "", "r1"), "already paused"},
		"another API group":    {actionFact{Kind: "Kustomization", Group: "kustomize.config.k8s.io", Namespace: "n", Name: "w", UID: "u", Object: &objectFacts{}}, step("flux-suspend", "Kustomization", "n", "w", "", "r1"), "was read from API group"},
	} {
		facts := newActionFacts()
		facts.add(c.fact)
		in, err := parseActions(json.RawMessage(`{"steps":[` + c.step + `]}`))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := validatePlan(in, facts, []ChatRead{{ID: "r1", Refs: []ChatRef{{UID: "u"}}}}, anchorFacts{}).dto.Steps[0]
		if got.State != stepRefused || !strings.Contains(got.Reason, c.why) {
			t.Errorf("%s: %s %q", name, got.State, got.Reason)
		}
	}
}

func TestThePlanAsAWhole(t *testing.T) {
	// a refused step holds every later one
	p := plan(t, step("scale", "Deployment", "klyx-a1-app", "api", `{"replicas":1}`, "r3"), step("flux-suspend", "Kustomization", "klyx-a1-flux", "a1-web", "", "r1"))
	if p.Steps[0].State != stepRefused || p.Steps[1].State != stepNotOffered || !strings.Contains(p.Steps[1].Reason, "step 1 was refused") {
		t.Fatalf("held: %+v", p.Steps)
	}
	// so does an unsupported one
	p = plan(t, step("scale", "Deployment", "klyx-a1-app", "web", `{"replicas":1}`, "r5"), step("flux-suspend", "Kustomization", "klyx-a1-flux", "a1-web", "", "r1"))
	if p.Steps[0].State != stepUnsupported || p.Steps[1].State != stepNotOffered {
		t.Fatalf("held by unsupported: %+v", p.Steps)
	}
	// a step before a refused one stays offered
	p = plan(t, step("flux-suspend", "Kustomization", "klyx-a1-flux", "a1-web", "", "r1"), step("scale", "Deployment", "klyx-a1-app", "api", `{"replicas":1}`, "r3"))
	if p.Steps[0].State != stepOffered || p.Steps[1].State != stepRefused {
		t.Fatalf("%+v", p.Steps)
	}
	// duplicates and contradictions
	p = plan(t, step("scale", "Deployment", "klyx-a1-app", "web", `{"replicas":1}`, "r3"), step("scale", "Deployment", "klyx-a1-app", "web", `{"replicas":2}`, "r3"))
	if p.Steps[1].State != stepRefused || !strings.Contains(p.Steps[1].Reason, "already in this plan") {
		t.Fatalf("duplicate: %+v", p.Steps[1])
	}
	p = plan(t, step("flux-suspend", "Kustomization", "klyx-a1-flux", "a1-web", "", "r1"), step("flux-reconcile", "Kustomization", "klyx-a1-flux", "a1-web", "", "r1"))
	if p.Steps[1].State != stepRefused || !strings.Contains(p.Steps[1].Reason, "step 1 suspends it") {
		t.Fatalf("reconcile after suspend: %+v", p.Steps[1])
	}
	p = plan(t, step("rollout-pause", "Deployment", "klyx-a1-app", "web", "", "r3"), step("rollout-resume", "Deployment", "klyx-a1-app", "web", "", "r3"))
	if p.Steps[1].State != stepRefused || !strings.Contains(p.Steps[1].Reason, "contradicts an earlier step's rollout-pause") {
		t.Fatalf("contradiction: %+v", p.Steps[1])
	}
}

func TestProposalShape(t *testing.T) {
	five := []string{}
	for i := 0; i < 5; i++ {
		five = append(five, step("flux-suspend", "Kustomization", "n", fmt.Sprintf("u%d", i), "", "r1"))
	}
	for name, raw := range map[string]string{
		"no steps":      `{"steps":[]}`,
		"five steps":    `{"steps":[` + strings.Join(five, ",") + `]}`,
		"unknown field": `{"steps":[` + step("scale", "Deployment", "n", "w", `{"replicas":1}`, "r1") + `],"cluster":"other"}`,
		"a uid field":   `{"steps":[{"verb":"scale","target":{"kind":"Deployment","namespace":"n","name":"w","uid":"x"},"params":{"replicas":1},"cites":["r1"],"why":"w"}]}`,
		"null":          `{"steps":null}`,
		"bad cite":      `{"steps":[` + step("scale", "Deployment", "n", "w", `{"replicas":1}`, "see r1") + `]}`,
		"no cites":      `{"steps":[` + step("scale", "Deployment", "n", "w", `{"replicas":1}`) + `]}`,
		"no why":        `{"steps":[{"verb":"scale","target":{"kind":"Deployment","namespace":"n","name":"w"},"params":{"replicas":1},"cites":["r1"],"why":" "}]}`,
		"long why":      `{"steps":[{"verb":"scale","target":{"kind":"Deployment","namespace":"n","name":"w"},"params":{"replicas":1},"cites":["r1"],"why":"` + strings.Repeat("y", actionWhyMax+1) + `"}]}`,
		"too large":     `{"steps":[{"verb":"scale","target":{"kind":"Deployment","namespace":"n","name":"w"},"params":{"replicas":1},"cites":["r1"],"why":"` + strings.Repeat("y", actionsArgsMax) + `"}]}`,
		"bad name":      `{"steps":[` + step("scale", "Deployment", "n", "w/../x", `{"replicas":1}`, "r1") + `]}`,
	} {
		if _, err := parseActions(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// What the model reads back carries states and Klyx's reasons, and says
// nothing runs; never a fact Klyx holds.
func TestTheModelReadsStatesOnly(t *testing.T) {
	in, _ := parseActions(json.RawMessage(`{"steps":[` + step("rollout-undo", "Deployment", "klyx-a1-app", "web", `{"revision":4}`, "r4.c1") + `]}`))
	facts, ledger, a := planFixture()
	out := actionsResult(validatePlan(in, facts, ledger, a).dto)
	for _, needle := range []string{"rs4-uid", "dig4", "dep-uid", "factsRead"} {
		if strings.Contains(out, needle) {
			t.Errorf("%q reached the model: %s", needle, out)
		}
	}
	if !strings.Contains(out, "Nothing runs") || !strings.Contains(out, `"state":"offered"`) {
		t.Fatal(out)
	}
}

// The turn: an investigation of a Deployment proposes from its seed read;
// with proposals off the tool is never offered and a call is refused.
func TestAnInvestigationProposesFromItsReads(t *testing.T) {
	seed := ChatSeedDTO{Kind: "object", Group: "apps", Version: "v1", Plural: "deployments", Namespace: "shop", Name: "web", UID: "uid-web"}
	propose := ai.ToolCall{ID: "a1", Name: toolActions, Args: json.RawMessage(`{"steps":[` + step("scale", "Deployment", "shop", "web", `{"replicas":1}`, "r1") + `]}`)}
	for _, off := range []bool{false, true} {
		f := newToolsFixture(t, calls(propose), answer("proposed"))
		f.conn.objects[key(deploysGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web"}, "spec": map[string]any{"replicas": int64(3)}})
		if off {
			st := *f.aiSvc.load()
			st.ProposalsOff = true
			SetAIState(f.aiSvc, &st)
		}
		inv := f.svc.StartInvestigation("x", seed)
		st := f.svc.Ask("x", inv.ConversationID, "", nil)
		ex := st.Exchanges[0]
		offered := false
		for _, tl := range f.prov.reqs[0].Tools {
			offered = offered || tl.Name == toolActions
		}
		if off {
			if offered || ex.Plan != nil || strings.Contains(f.prov.reqs[0].System, "propose_actions") {
				t.Fatalf("proposals off: offered=%v plan=%+v", offered, ex.Plan)
			}
			continue
		}
		if !offered || !strings.Contains(f.prov.reqs[0].System, "propose_actions") {
			t.Fatal("an investigation is offered propose_actions")
		}
		if ex.Plan == nil || len(ex.Plan.Steps) != 1 || ex.Plan.Steps[0].State != stepOffered || ex.Plan.Steps[0].Target.UID != "uid-web" {
			t.Fatalf("the plan rests on the seed read: %+v", ex.Plan)
		}
		for _, r := range ex.Reads {
			if r.Tool == toolActions {
				t.Fatal("a proposal is not a read")
			}
		}
	}
}

func TestChatIsNeverOfferedActions(t *testing.T) {
	f := newToolsFixture(t, answer("hi"))
	st := f.svc.Start("x")
	f.svc.Ask("x", st.ConversationID, "hello", nil)
	for _, tl := range f.prov.reqs[0].Tools {
		if tl.Name == toolActions {
			t.Fatal("chat stays read-only")
		}
	}
}

func TestTakeActionsSplitsTheLastBlock(t *testing.T) {
	ans := "prose\n```findings\n{}\n```\n```actions\n{\"a\":1}\n```\nmore\n```actions\n{\"steps\":[]}\n```"
	prose, block, ok := takeActions(ans)
	if !ok || block != `{"steps":[]}` || strings.Contains(prose, "```actions\n{\"steps\"") || !strings.Contains(prose, "findings") {
		t.Fatalf("%q %q %v", prose, block, ok)
	}
}

// AI A3 §2.5: a remedy part keeps its documented 2,000 characters, and the
// report's 24 KiB is an aggregate bound a report at its caps can exceed.
func TestRemedyAndTheReportsBound(t *testing.T) {
	long := strings.Repeat("g", findingsRemedyMax)
	raw := `{"findings":[],"remedy":{"git":"` + long + `","mitigation":"m"}}`
	in, err := parseFindings(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	out := labelFindings(in, nil, anchorFacts{})
	if out.Remedy == nil || len([]rune(out.Remedy.Git)) != findingsRemedyMax {
		t.Fatalf("the remedy kept %d characters", len([]rune(out.Remedy.Git)))
	}
	var fs []string
	cites := `["r1","r2","r3","r4","r5","r6","r7","r8","r9","r10"]`
	pod := strings.Repeat("p", 253)
	ns := strings.Repeat("n", 63)
	for i := 0; i < findingsMax; i++ {
		fs = append(fs, fmt.Sprintf(`{"type":"observation","statement":%q,"subject":{"kind":"Pod","namespace":%q,"name":%q},"cites":%s}`, strings.Repeat("s", findingStatementMax), ns, pod, cites))
	}
	var missing []string
	for i := 0; i < findingsMissingMax; i++ {
		missing = append(missing, fmt.Sprintf("%q", strings.Repeat("m", findingsMissingNote)))
	}
	capped := `{"findings":[` + strings.Join(fs, ",") + `],"missing":[` + strings.Join(missing, ",") + `],"remedy":{"git":"` + long + `","mitigation":"` + long + `"}}`
	if _, err := parseFindings(json.RawMessage(capped)); err == nil || !strings.Contains(err.Error(), "at most 24576 bytes") {
		t.Fatalf("an all-ASCII report at every cap exceeds the aggregate, named: %v", err)
	}
	typical := `{"findings":[` + strings.Join(fs[:4], ",") + `],"remedy":{"git":"` + long + `"}}`
	if _, err := parseFindings(json.RawMessage(typical)); err != nil {
		t.Fatalf("a typical report fits: %v", err)
	}
}

// Snapshot mode (AI A3 §2.1): the answer's last actions block is decided
// against the snapshot's settled reads; with proposals off it is taken out
// of the prose and never decided, and the prompt does not offer it.
func TestASnapshotInvestigationProposesInABlock(t *testing.T) {
	block := "\n```actions\n{\"steps\":[" + step("flux-suspend", "Kustomization", "flux-system", "app", "", "r1") + "]}\n```\n"
	reply := "It fails its health check.\n\n```findings\n{\"findings\":[]}\n```" + block
	for _, off := range []bool{false, true} {
		f, p := snapshotInvestigation(t, reply)
		planCluster(f)
		if off {
			st := *f.aiSvc.load()
			st.ProposalsOff = true
			SetAIState(f.aiSvc, &st)
		}
		inv := f.svc.StartInvestigation("x", ksAnchor)
		ex := f.svc.Ask("x", inv.ConversationID, "", nil).Exchanges[0]
		if strings.Contains(ex.Answer, "```actions") {
			t.Fatalf("the block is taken out of the prose: %q", ex.Answer)
		}
		system := p.creqs[len(p.creqs)-1].System
		if off {
			if ex.Plan != nil || strings.Contains(system, "```actions") {
				t.Fatalf("proposals off: %+v", ex.Plan)
			}
			continue
		}
		if !strings.Contains(system, "```actions") {
			t.Fatal("the snapshot prompt describes the actions block")
		}
		if ex.Plan == nil || len(ex.Plan.Steps) != 1 || ex.Plan.Steps[0].State != stepOffered || ex.Plan.Steps[0].Target.UID != "uid-ks" {
			t.Fatalf("the plan rests on the anchor's read: %+v", ex.Plan)
		}
	}
}

// The partner A1.1 pins is the one the undo path checks: the digest from
// rollout_history's unstructured read equals fleet's from a typed read.
func TestThePinnedPartnerDigestIsTheUndoPaths(t *testing.T) {
	grace := int64(9007199254740993)
	typed := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-4", UID: "rs4"},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web", "pod-template-hash": "abc"}},
			Spec:       corev1.PodSpec{TerminationGracePeriodSeconds: &grace, Containers: []corev1.Container{{Name: "web", Image: "web:4"}}}}}}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&typed)
	if err != nil {
		t.Fatal(err)
	}
	partner := &rolloutRev{num: 4, obj: &unstructured.Unstructured{Object: m}}
	h := historyFactsOf("Deployment", &rolloutRev{num: 5}, partner, "r4", "t")
	want, _ := workloads.TemplateDigest(typed.Spec.Template, true)
	if h.PartnerRevision != 4 || h.PartnerObjectUID != "rs4" || h.PartnerDigest != want || want == "" {
		t.Fatalf("%+v want %s", h, want)
	}
}

// The facts are Klyx's: no tool result carries them to the model.
func TestFactsNeverReachTheModel(t *testing.T) {
	f := newToolsFixture(t)
	f.conn.objects[key(deploysGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web"}, "spec": map[string]any{"replicas": int64(3), "paused": true}})
	res := newChatTools(f.conn, fixedNow).as("r1").getGVR(context.Background(), deploysGVR, "shop", "web", "uid-web")
	if len(res.facts) != 1 || res.facts[0].Object == nil || !res.facts[0].Object.Paused {
		t.Fatalf("the read's facts: %+v", res.facts)
	}
	for _, needle := range []string{"objectFacts", `"object":{"readId"`, "partnerDigest", `"facts"`} {
		if strings.Contains(string(res.JSON), needle) {
			t.Fatalf("%q reached the envelope: %s", needle, res.JSON)
		}
	}
}

// A name that has held two objects in the investigation is refused: Klyx
// cannot say which one the model means.
func TestANameThatHeldTwoObjectsIsRefused(t *testing.T) {
	facts := newActionFacts()
	for _, uid := range []string{"old-uid", "new-uid"} {
		facts.add(actionFact{Kind: "Deployment", Group: "apps", Namespace: "n", Name: "w", UID: uid, Object: &objectFacts{}})
	}
	in, _ := parseActions(json.RawMessage(`{"steps":[` + step("rollout-pause", "Deployment", "n", "w", "", "r1") + `]}`))
	got := validatePlan(in, facts, []ChatRead{{ID: "r1", Refs: []ChatRef{{UID: "new-uid"}}}}, anchorFacts{}).dto.Steps[0]
	if got.State != stepRefused || !strings.Contains(got.Reason, "more than one object") {
		t.Fatalf("%+v", got)
	}
}

// A proposal that cites a read of its own batch is decided again when the
// turn ends: the reads settle first.
func TestAProposalIsDecidedAgainstTheSettledTurn(t *testing.T) {
	seed := ChatSeedDTO{Kind: "object", Version: "v1", Plural: "pods", Namespace: "shop", Name: "web-x", UID: "uid-px"}
	propose := ai.ToolCall{ID: "a1", Name: toolActions, Args: json.RawMessage(`{"steps":[` + step("rollout-pause", "Deployment", "shop", "web", "", "r2") + `]}`)}
	get := ai.ToolCall{ID: "g1", Name: toolGet, Args: json.RawMessage(`{"kind":"Deployment","group":"apps","namespace":"shop","name":"web"}`)}
	f := newToolsFixture(t, calls(propose, get), answer("done"))
	f.conn.objects[key(podsGVR, "shop", "web-x")] = uobj(map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web-x", "namespace": "shop", "uid": "uid-px"}})
	f.conn.objects[key(deploysGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web"}, "spec": map[string]any{"replicas": int64(2)}})
	f.conn.kinds["deployment"] = []fleet.ChatKind{{GVR: deploysGVR, Kind: "Deployment", Namespaced: true}}
	inv := f.svc.StartInvestigation("x", seed)
	ex := f.svc.Ask("x", inv.ConversationID, "", nil).Exchanges[0]
	if ex.Plan == nil || ex.Plan.Steps[0].State != stepOffered || ex.Plan.Steps[0].Target.UID != "uid-web" {
		t.Fatalf("decided against the settled ledger and facts: %+v", ex.Plan)
	}
}

// BuildAIState, the startup path, carries the setting too.
func TestBuildAIStateCarriesProposals(t *testing.T) {
	off := false
	if st := BuildAIState(&config.AIConfig{Provider: "off", ProposeActions: &off}, nil, func(string) string { return "" }); !st.ProposalsOff {
		t.Fatal("off at startup")
	}
	if st := BuildAIState(nil, nil, func(string) string { return "" }); st.ProposalsOff {
		t.Fatal("on by default")
	}
}

package appbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/fleet"
	"github.com/moomora/klyx/internal/healthsrc"
	"github.com/moomora/klyx/internal/keda"
	"github.com/moomora/klyx/internal/observe"
	"github.com/moomora/klyx/internal/workloads"
)

// AI A3 §4: a step runs only after its review, through the manual verb's
// own inner function on the pinned connection.

// stepsConn is the chat stand-in with the matrix verbs' writes recorded.
type stepsConn struct {
	*fakeChatConn
	mu      sync.Mutex
	writes  []string
	err     error
	owner   fleet.GitOpsOwnership
	replica fleet.ReplicaOwnership
	getHook func()
}

func (c *stepsConn) note(format string, a ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, fmt.Sprintf(format, a...))
	return c.err
}

func (c *stepsConn) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.writes...)
}

func (c *stepsConn) RolloutRestart(_ context.Context, kind, ns, name, uid string) error {
	return c.note("restart %s %s/%s %s", kind, ns, name, uid)
}
func (c *stepsConn) ScaleWorkload(_ context.Context, kind, ns, name, uid string, n int32) error {
	return c.note("scale %s %s/%s %s %d", kind, ns, name, uid, n)
}
func (c *stepsConn) RolloutUndo(_ context.Context, kind, ns, name, uid string, rev int64, e *fleet.RevisionExpect) error {
	return c.note("undo %s %s/%s %s %d %s %s", kind, ns, name, uid, rev, e.ObjectUID, e.TemplateDigest)
}
func (c *stepsConn) RolloutPause(_ context.Context, ns, name, uid string, pause bool) error {
	return c.note("pause %s/%s %s %v", ns, name, uid, pause)
}
func (c *stepsConn) DeletePod(_ context.Context, ns, name, uid string) error {
	return c.note("delete %s/%s %s", ns, name, uid)
}
func (c *stepsConn) SetCordon(_ context.Context, node, uid string, cordon bool) error {
	return c.note("cordon %s %s %v", node, uid, cordon)
}
func (c *stepsConn) Reconcile(_ context.Context, kind, ns, name, uid string) error {
	return c.note("reconcile %s %s/%s %s", kind, ns, name, uid)
}
func (c *stepsConn) SetSuspend(_ context.Context, kind, ns, name, uid string, suspend bool) error {
	return c.note("suspend %s %s/%s %s %v", kind, ns, name, uid, suspend)
}
func (c *stepsConn) KEDASetPaused(_ context.Context, res, ns, name, uid string, pause bool) error {
	return c.note("keda %s %s/%s %s %v", res, ns, name, uid, pause)
}
func (c *stepsConn) GitOpsOwnership(context.Context, string, string, string, string) fleet.GitOpsOwnership {
	return c.owner
}
func (c *stepsConn) ReplicaOwner(context.Context, string, string, string) fleet.ReplicaOwnership {
	return c.replica
}

// ChatGet runs getHook after each read: a connection that changes while a
// step is observed.
func (c *stepsConn) ChatGet(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	u, err := c.fakeChatConn.ChatGet(ctx, gvr, ns, name)
	if c.getHook != nil {
		c.getHook()
	}
	return u, err
}

// stepsFixture: an investigation of Deployment shop/web whose turn proposed
// steps, on a stand-in that records the verbs' writes.
type stepsFixture struct {
	*chatFixture
	sc       *stepsConn
	conv, ex string
	now      time.Time
}

func newStepsFixture(t *testing.T, steps ...string) *stepsFixture {
	t.Helper()
	seed := ChatSeedDTO{Kind: "object", Group: "apps", Version: "v1", Plural: "deployments", Namespace: "shop", Name: "web", UID: "uid-web"}
	propose := ai.ToolCall{ID: "a1", Name: toolActions, Args: json.RawMessage(`{"steps":[` + strings.Join(steps, ",") + `]}`)}
	f := newToolsFixture(t, calls(propose), answer("proposed"))
	f.conn.objects[key(deploysGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web", "generation": int64(4)}, "spec": map[string]any{"replicas": int64(3)}})
	sc := &stepsConn{fakeChatConn: f.conn}
	f.mu.Lock()
	f.cur = sc
	f.mu.Unlock()
	sf := &stepsFixture{chatFixture: f, sc: sc, now: fixedNow()}
	f.svc.now = func() time.Time { return sf.now }
	inv := f.svc.StartInvestigation("x", seed)
	st := f.svc.Ask("x", inv.ConversationID, "", nil)
	sf.conv, sf.ex = inv.ConversationID, st.Exchanges[0].ID
	if st.Exchanges[0].Plan == nil {
		t.Fatalf("no plan: %+v", st.Exchanges[0])
	}
	return sf
}

func (sf *stepsFixture) web() *unstructured.Unstructured {
	return sf.conn.objects[key(deploysGVR, "shop", "web")]
}

func (sf *stepsFixture) review(n int) StepReviewDTO { return sf.svc.ReviewStep("x", sf.conv, sf.ex, n) }
func (sf *stepsFixture) run(n int, token string) StepRunDTO {
	return sf.svc.RunStep("x", sf.conv, sf.ex, n, token)
}

// establish makes every verification settle established (stage 4 decides
// for real).
func establish(t *testing.T) {
	prev := verifyStepHook
	verifyStepHook = func(s *ChatService, c *conversation, p *actionPlan, n int, _ stepSnap, _ ActionResultDTO) {
		s.settleEffect(c, p, n, stepEffectEstablished, "")
	}
	t.Cleanup(func() { verifyStepHook = prev })
}

var (
	pauseWeb = step("rollout-pause", "Deployment", "shop", "web", "", "r1")
	scaleWeb = step("scale", "Deployment", "shop", "web", `{"replicas":1}`, "r1")
)

func TestAStepRunsAsReviewed(t *testing.T) {
	establish(t)
	sf := newStepsFixture(t, pauseWeb)
	rv := sf.review(1)
	if !rv.Offered || rv.Token == "" || rv.Change != "spec.paused: false → true" || !strings.Contains(rv.Expected, "within 30 s") ||
		len(rv.Observed) == 0 || !strings.Contains(strings.Join(rv.Consequence, ";"), "no GitOps unit") {
		t.Fatalf("review: %+v", rv)
	}
	if w := sf.sc.recorded(); len(w) != 0 {
		t.Fatalf("a review writes nothing: %v", w)
	}
	got := sf.run(1, rv.Token)
	if !got.Ran || got.Result.Outcome != fleet.OutcomeWritten || got.Plan.Steps[0].Run == nil {
		t.Fatalf("run: %+v", got)
	}
	if w := sf.sc.recorded(); len(w) != 1 || w[0] != "pause shop/web uid-web true" {
		t.Fatalf("the manual verb's write, with the pinned UID: %v", w)
	}
	plan := sf.svc.InvestigationState("x").Exchanges[0].Plan
	if r := plan.Steps[0].Run; r.Request != fleet.OutcomeWritten || r.Effect != stepEffectEstablished {
		t.Fatalf("the step's run: %+v", r)
	}
	if again := sf.run(1, rv.Token); again.Ran || !strings.Contains(again.Reason, "has run") {
		t.Fatalf("a step runs once: %+v", again)
	}
}

func TestTheReviewTokenIsRequired(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	if r := sf.run(1, ""); r.Ran || !strings.Contains(r.Reason, "review the step first") {
		t.Fatalf("no review: %+v", r)
	}
	rv := sf.review(1)
	if r := sf.run(1, "not-the-token"); r.Ran || !strings.Contains(r.Reason, "review the step first") {
		t.Fatalf("another token: %+v", r)
	}
	sf.now = sf.now.Add(reviewTokenTTL + time.Second)
	if r := sf.run(1, rv.Token); r.Ran || !strings.Contains(r.Reason, "expired") {
		t.Fatalf("an expired review: %+v", r)
	}
	if len(sf.sc.recorded()) != 0 {
		t.Fatal("nothing was written")
	}
}

// §4.2: a material change between Review and Confirm refuses the run.
func TestAChangeAfterTheReviewRefusesTheRun(t *testing.T) {
	sf := newStepsFixture(t, scaleWeb)
	rv := sf.review(1)
	if !rv.Offered || rv.Change != "replicas 3 → 1" {
		t.Fatalf("%+v", rv)
	}
	_ = unstructured.SetNestedField(sf.web().Object, int64(2), "spec", "replicas")
	if r := sf.run(1, rv.Token); r.Ran || !strings.Contains(r.Reason, "changed since you reviewed it") {
		t.Fatalf("a changed count: %+v", r)
	}
	if len(sf.sc.recorded()) != 0 {
		t.Fatal("nothing was written")
	}
	if r := sf.review(1); !r.Offered || r.Change != "replicas 2 → 1" {
		t.Fatalf("a new review reads the change: %+v", r)
	}
}

func TestAReplacedTargetIsNotOffered(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	sf.web().SetUID("uid-web-2")
	if r := sf.review(1); r.Offered || !strings.Contains(r.Reason, "was replaced") {
		t.Fatalf("%+v", r)
	}
}

// §4.3: an earlier step's state effect is re-read before the next step.
func TestAStandingPrerequisiteIsReadAgain(t *testing.T) {
	establish(t)
	sf := newStepsFixture(t, pauseWeb, scaleWeb)
	if r := sf.review(2); r.Offered || !strings.Contains(r.Reason, "step 1 has not run") {
		t.Fatalf("step 2 waits for step 1: %+v", r)
	}
	rv := sf.review(1)
	if r := sf.run(1, rv.Token); !r.Ran {
		t.Fatal(r)
	}
	_ = unstructured.SetNestedField(sf.web().Object, true, "spec", "paused")
	_ = unstructured.SetNestedField(sf.web().Object, int64(4), "status", "observedGeneration")
	rv2 := sf.review(2)
	if !rv2.Offered {
		t.Fatalf("the prerequisite holds: %+v", rv2)
	}
	_ = unstructured.SetNestedField(sf.web().Object, false, "spec", "paused") // resumed from outside
	if r := sf.run(2, rv2.Token); r.Ran || !strings.Contains(r.Reason, "step 1's effect no longer holds") {
		t.Fatalf("between Review and Confirm: %+v", r)
	}
	if r := sf.review(2); r.Offered || !strings.Contains(r.Reason, "spec.paused is false") {
		t.Fatalf("and at Review: %+v", r)
	}
}

// Until its effect is established, a step holds the next one; stage 3's
// own verification never establishes one.
func TestAnUnverifiedEffectHoldsThePlan(t *testing.T) {
	prev := verifyStepHook
	verifyStepHook = func(s *ChatService, c *conversation, p *actionPlan, n int, _ stepSnap, _ ActionResultDTO) {
		s.settleEffect(c, p, n, stepEffectUnverified, "not established within 30s")
	}
	t.Cleanup(func() { verifyStepHook = prev })
	sf := newStepsFixture(t, pauseWeb, scaleWeb)
	rv := sf.review(1)
	if r := sf.run(1, rv.Token); !r.Ran {
		t.Fatal(r)
	}
	plan := sf.svc.InvestigationState("x").Exchanges[0].Plan
	if plan.Status != planStatusHeld || !strings.Contains(plan.StatusWhy, "step 1 held the plan") {
		t.Fatalf("held: %+v", plan)
	}
	if r := sf.review(2); r.Offered || !strings.Contains(r.Reason, "not established") {
		t.Fatalf("%+v", r)
	}
}

func TestARefusedRequestHoldsThePlan(t *testing.T) {
	establish(t)
	sf := newStepsFixture(t, pauseWeb, scaleWeb)
	sf.sc.err = apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("rbac"))
	rv := sf.review(1)
	r := sf.run(1, rv.Token)
	if !r.Ran || r.Result.Outcome != fleet.OutcomeRefused || r.Plan.Steps[0].Run.Effect != "" {
		t.Fatalf("refused: %+v", r)
	}
	plan := sf.svc.InvestigationState("x").Exchanges[0].Plan
	if plan.Status != planStatusHeld || !strings.Contains(plan.StatusWhy, "refused") {
		t.Fatalf("%+v", plan)
	}
}

// One step at a time: while a step's effect is decided, nothing else runs.
func TestOneStepRunsAtATime(t *testing.T) {
	prev := verifyStepHook
	verifyStepHook = func(*ChatService, *conversation, *actionPlan, int, stepSnap, ActionResultDTO) {} // never settles
	t.Cleanup(func() { verifyStepHook = prev })
	sf := newStepsFixture(t, pauseWeb, scaleWeb)
	rv := sf.review(1)
	sf.run(1, rv.Token)
	if r := sf.review(2); r.Offered || !strings.Contains(r.Reason, "step 1 is running") {
		t.Fatalf("%+v", r)
	}
	if r := sf.svc.DismissPlan("x", sf.conv, sf.ex); r.Plan != nil {
		t.Fatalf("a running plan is not dismissed: %+v", r)
	}
}

// §4.2: the grant is consumed first, a refusal included; each step needs
// its own.
func TestRunStepConsumesTheGrant(t *testing.T) {
	establish(t)
	prev := activeProtectGuard.Load()
	t.Cleanup(func() { activeProtectGuard.Store(prev) })
	SetProtectGuard(newGuardFor(func(c string) bool { return c == "x" }, time.Now))
	sf := newStepsFixture(t, pauseWeb, scaleWeb)
	rv := sf.review(1)
	if r := sf.run(1, rv.Token); r.Ran || r.Result == nil || !r.Result.NotSent {
		t.Fatalf("no grant: %+v", r)
	}
	_ = activeProtectGuard.Load().Arm("x", "x")
	if r := sf.run(1, "wrong"); r.Ran {
		t.Fatal(r)
	}
	if r := sf.run(1, rv.Token); r.Ran {
		t.Fatalf("the refused call spent the grant: %+v", r)
	}
	_ = activeProtectGuard.Load().Arm("x", "x")
	if r := sf.run(1, rv.Token); !r.Ran {
		t.Fatalf("with its own grant: %+v", r)
	}
	rv2 := sf.review(2)
	if r := sf.run(2, rv2.Token); r.Ran {
		t.Fatal("step 2 cannot use step 1's grant")
	}
	if len(sf.sc.recorded()) != 1 {
		t.Fatalf("one write: %v", sf.sc.recorded())
	}
}

// §4.4: a step never runs on a connection other than the pinned one.
func TestAChangedConnectionRefusesTheStep(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	rv := sf.review(1)
	other := &stepsConn{fakeChatConn: newFakeChat()}
	sf.mu.Lock()
	sf.cur = other
	sf.mu.Unlock()
	// the plan's fence withdraws it, or the run fence refuses: never run
	if r := sf.run(1, rv.Token); r.Ran || !(strings.Contains(r.Reason, "disconnected") || strings.Contains(r.Reason, "connection changed")) {
		t.Fatalf("%+v", r)
	}
	if len(sf.sc.recorded()) != 0 || len(other.recorded()) != 0 {
		t.Fatal("nothing was written on either connection")
	}
}

func TestSupersededDismissedAndWithdrawnPlans(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	later := &exchange{id: "later", plan: &actionPlan{dto: ActionPlanDTO{Steps: []ActionStepDTO{{N: 1, State: stepOffered}}}, runs: []*stepRun{{}}}}
	c.exchanges = append(c.exchanges, later)
	sf.svc.mu.Unlock()
	if r := sf.review(1); r.Offered || !strings.Contains(r.Reason, "superseded") {
		t.Fatalf("superseded: %+v", r)
	}
	if p := sf.svc.InvestigationState("x").Exchanges[0].Plan; p.Status != planStatusSuperseded {
		t.Fatalf("%+v", p)
	}
	sf.svc.mu.Lock()
	c.exchanges = c.exchanges[:1]
	sf.svc.mu.Unlock()
	if r := sf.svc.DismissPlan("x", sf.conv, sf.ex); r.Plan == nil || r.Plan.Status != planStatusDeclined {
		t.Fatalf("dismissed: %+v", r)
	}
	if r := sf.review(1); r.Offered || !strings.Contains(r.Reason, "dismissed") {
		t.Fatalf("%+v", r)
	}
	sf2 := newStepsFixture(t, pauseWeb)
	sf2.svc.mu.Lock()
	sf2.svc.convs[invKey("x")].ended = "the cluster disconnected"
	sf2.svc.mu.Unlock()
	if r := sf2.review(1); r.Offered || !strings.Contains(r.Reason, "withdrawn") {
		t.Fatalf("withdrawn: %+v", r)
	}
}

// §4.4's dispatch table: each verb reaches the write its manual method
// makes, with the pinned UID and the validated parameters.
func TestTheDispatchTable(t *testing.T) {
	one, four := 1, int64(4)
	sc := &stepsConn{fakeChatConn: newFakeChat()}
	for _, c := range []struct {
		verb string
		fact actionFact
		st   ActionStepDTO
		part *fleet.RevisionExpect
		want string
	}{
		{"flux-suspend", actionFact{Kind: "Kustomization", Namespace: "f", Name: "a", UID: "u"}, ActionStepDTO{}, nil, "suspend Kustomization f/a u true"},
		{"flux-resume", actionFact{Kind: "Kustomization", Namespace: "f", Name: "a", UID: "u"}, ActionStepDTO{}, nil, "suspend Kustomization f/a u false"},
		{"flux-reconcile", actionFact{Kind: "HelmRelease", Namespace: "f", Name: "a", UID: "u"}, ActionStepDTO{}, nil, "reconcile HelmRelease f/a u"},
		{"rollout-restart", actionFact{Kind: "StatefulSet", Namespace: "s", Name: "db", UID: "u"}, ActionStepDTO{}, nil, "restart StatefulSet s/db u"},
		{"scale", actionFact{Kind: "Deployment", Namespace: "s", Name: "w", UID: "u"}, ActionStepDTO{Replicas: &one}, nil, "scale Deployment s/w u 1"},
		{"rollout-undo", actionFact{Kind: "Deployment", Namespace: "s", Name: "w", UID: "u"}, ActionStepDTO{Revision: &four}, &fleet.RevisionExpect{ObjectUID: "rs4", TemplateDigest: "d4"}, "undo Deployment s/w u 4 rs4 d4"},
		{"rollout-pause", actionFact{Kind: "Deployment", Namespace: "s", Name: "w", UID: "u"}, ActionStepDTO{}, nil, "pause s/w u true"},
		{"rollout-resume", actionFact{Kind: "Deployment", Namespace: "s", Name: "w", UID: "u"}, ActionStepDTO{}, nil, "pause s/w u false"},
		{"delete-pod", actionFact{Kind: "Pod", Namespace: "s", Name: "p", UID: "u"}, ActionStepDTO{}, nil, "delete s/p u"},
		{"cordon", actionFact{Kind: "Node", Name: "n1", UID: "u"}, ActionStepDTO{}, nil, "cordon n1 u true"},
		{"uncordon", actionFact{Kind: "Node", Name: "n1", UID: "u"}, ActionStepDTO{}, nil, "cordon n1 u false"},
		{"keda-pause", actionFact{Kind: "ScaledObject", Resource: "scaledobjects", Namespace: "s", Name: "q", UID: "u"}, ActionStepDTO{}, nil, "keda scaledobjects s/q u true"},
		{"keda-resume", actionFact{Kind: "ScaledJob", Resource: "scaledjobs", Namespace: "s", Name: "q", UID: "u"}, ActionStepDTO{}, nil, "keda scaledjobs s/q u false"},
	} {
		c.st.Verb = c.verb
		before := len(sc.recorded())
		r := runVerb(context.Background(), sc, stepSnap{step: c.st, fact: c.fact, partner: c.part})
		w := sc.recorded()
		if r.Outcome != fleet.OutcomeWritten || len(w) != before+1 || w[len(w)-1] != c.want {
			t.Errorf("%s: %+v %v", c.verb, r, w)
		}
	}
	// an undo without its pinned partner never runs
	r := runVerb(context.Background(), sc, stepSnap{step: ActionStepDTO{Verb: "rollout-undo", Revision: &four}, fact: actionFact{Kind: "Deployment", UID: "u"}})
	if r.Outcome != fleet.OutcomeRefused || !r.NotSent {
		t.Fatalf("%+v", r)
	}
}

// A step that became a no-op since it was proposed is not offered.
func TestAStepThatWouldChangeNothingIsNotOffered(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	_ = unstructured.SetNestedField(sf.web().Object, true, "spec", "paused")
	if r := sf.review(1); r.Offered || !strings.Contains(r.Reason, "already paused") {
		t.Fatalf("%+v", r)
	}
}

// The fence again before the write: a connection that changes while the
// step is read again never carries its write.
func TestAConnectionChangedDuringTheRunNeverWrites(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	rv := sf.review(1)
	sf.sc.getHook = func() {
		sf.mu.Lock()
		sf.usable = false
		sf.mu.Unlock()
	}
	if r := sf.run(1, rv.Token); r.Ran || !strings.Contains(r.Reason, "connection changed or disconnected") {
		t.Fatalf("%+v", r)
	}
	if len(sf.sc.recorded()) != 0 {
		t.Fatal("nothing was written")
	}
}

// plantPlan puts a plan of the given steps, facts and partners on the
// fixture's exchange, as validatePlan would have decided it.
func (sf *stepsFixture) plantPlan(steps []ActionStepDTO, facts []actionFact, partners []*fleet.RevisionExpect) {
	sf.svc.mu.Lock()
	defer sf.svc.mu.Unlock()
	p := &actionPlan{dto: ActionPlanDTO{Steps: steps}, facts: facts, partner: partners, settled: true}
	for range steps {
		p.runs = append(p.runs, &stepRun{})
	}
	sf.svc.convs[invKey("x")].exchanges[0].plan = p
}

// An undo's partner revision is read again: the same object, with the same
// template, or the step is not offered.
func TestAnUndosPartnerIsReadAgain(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	tmpl := map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "web"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "web", "image": "web:4"}}}}
	rs := uobj(map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": map[string]any{"name": "web-4", "namespace": "shop", "uid": "rs4"}, "spec": map[string]any{"template": tmpl}})
	sf.conn.objects[key(replicaSetsGVR, "shop", "web-4")] = rs
	d, _ := workloads.TemplateDigest(tmpl, true)
	four := int64(4)
	three := int64(3)
	fact := actionFact{Kind: "Deployment", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "shop", Name: "web", UID: "uid-web",
		Object:  &objectFacts{Replicas: &three, Desired: &three},
		History: &historyFacts{DesiredRevision: 5, PartnerRevision: 4, PartnerObjectUID: "rs4", PartnerObject: "web-4", PartnerDigest: d}}
	sf.plantPlan([]ActionStepDTO{{N: 1, Verb: "rollout-undo", Revision: &four, State: stepOffered, Target: ChatCheckSubject{Kind: "Deployment", Namespace: "shop", Name: "web", UID: "uid-web"}}},
		[]actionFact{fact}, []*fleet.RevisionExpect{{ObjectUID: "rs4", TemplateDigest: d}})
	rv := sf.review(1)
	if !rv.Offered || !strings.Contains(rv.Change, "revision 5 → 4") {
		t.Fatalf("%+v", rv)
	}
	_ = unstructured.SetNestedField(rs.Object, "web:changed", "spec", "template", "spec", "containers", "0", "image")
	_ = unstructured.SetNestedSlice(rs.Object, []any{map[string]any{"name": "web", "image": "web:changed"}}, "spec", "template", "spec", "containers")
	if r := sf.run(1, rv.Token); r.Ran || !strings.Contains(r.Reason, "revision 4 is gone or changed") {
		t.Fatalf("a changed partner: %+v", r)
	}
	if len(sf.sc.recorded()) != 0 {
		t.Fatal("nothing was written")
	}
}

// The consequence comes from the authoritative ownership read, and says
// when an earlier step suspends the owner.
func TestTheConsequenceNamesTheOwnersSuspension(t *testing.T) {
	establish(t)
	sf := newStepsFixture(t, pauseWeb)
	ks := uobj(map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": map[string]any{"name": "a1-web", "namespace": "flux-system", "uid": "uid-ks"}, "spec": map[string]any{}})
	sf.conn.objects[key(ksGVR, "flux-system", "a1-web")] = ks
	ksFact := factOf(ksGVR, ks, "r9", "t", acquisition())
	webFact := factOf(deploysGVR, sf.web(), "r1", "t", acquisition())
	sf.plantPlan([]ActionStepDTO{
		{N: 1, Verb: "flux-suspend", State: stepOffered, Target: ChatCheckSubject{Kind: "Kustomization", Namespace: "flux-system", Name: "a1-web", UID: "uid-ks"}},
		{N: 2, Verb: "rollout-pause", State: stepOffered, Target: ChatCheckSubject{Kind: "Deployment", Namespace: "shop", Name: "web", UID: "uid-web"}},
	}, []actionFact{ksFact, webFact}, []*fleet.RevisionExpect{nil, nil})
	sf.sc.owner = fleet.GitOpsOwnership{OwnerKind: "Kustomization", OwnerNamespace: "flux-system", OwnerName: "a1-web", Basis: "flux-inventory"}
	rv := sf.review(1)
	if r := sf.run(1, rv.Token); !r.Ran {
		t.Fatal(r)
	}
	_ = unstructured.SetNestedField(ks.Object, true, "spec", "suspend")
	rv2 := sf.review(2)
	c := strings.Join(rv2.Consequence, "; ")
	if !rv2.Offered || !strings.Contains(c, "Kustomization flux-system/a1-web manages it (its Flux inventory)") || !strings.Contains(c, "step 1 suspends it") {
		t.Fatalf("%+v", rv2)
	}
	sf.sc.owner.Basis = "argo-resources"
	if c := strings.Join(sf.review(2).Consequence, "; "); !strings.Contains(c, "Argo CD's") || !strings.Contains(c, "when it syncs") {
		t.Fatalf("argo: %s", c)
	}
}

// §4.3: every state verb's effect is read again by the matrix's whole
// predicate, the one verification decides it by (stage 3 review, finding
// 3): KEDA's completed pause, an observed generation, a Deployment undo's
// current ReplicaSet.
func TestEveryPrerequisiteIsTheMatrixEffect(t *testing.T) {
	two := 2
	tmpl := map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "c", "image": "i:4"}}}}
	d, _ := workloads.TemplateDigest(tmpl, true)
	sd, _ := workloads.TemplateDigest(tmpl, false)
	soGVR := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	stsGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	ann := func(kv ...string) map[string]any {
		m := map[string]any{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	ready := func(s string) map[string]any {
		return map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": s}}}
	}
	observed := func(gen int64, extra map[string]any) map[string]any {
		st := map[string]any{"observedGeneration": gen}
		for k, v := range extra {
			st[k] = v
		}
		return st
	}
	// a native part's verdict: its state in Role, KEDA's completion in the
	// row's own evidence
	paused := subjectVerdict{v: chatVerdict{Role: "paused", Coverage: "live", Generation: 1}, found: true, state: "paused", completed: true}
	notDone := subjectVerdict{v: chatVerdict{Role: "fault", Coverage: "live", Generation: 1}, found: true, state: "fault"}
	stale := subjectVerdict{v: chatVerdict{Role: "paused", Coverage: "stale", Generation: 1}, found: true, state: "paused", completed: true}
	older := subjectVerdict{v: chatVerdict{Role: "paused", Coverage: "live", Generation: 0}, found: true, state: "paused", completed: true}
	controller := true
	rsOf := func(uid, owner, rev string, template map[string]any) *unstructured.Unstructured {
		r := uobj(map[string]any{"kind": "ReplicaSet", "metadata": map[string]any{"name": "o-4", "namespace": "n", "uid": uid, "labels": map[string]any{"app": "o"}, "annotations": ann("deployment.kubernetes.io/revision", rev)}, "spec": map[string]any{"template": template}})
		r.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "o", UID: types.UID(owner), Controller: &controller}})
		return r
	}
	rs := func(uid, rev string) *unstructured.Unstructured { return rsOf(uid, "u", rev, tmpl) }
	selector := map[string]any{"matchLabels": map[string]any{"app": "o"}}
	pausedConds := map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "Paused", "status": "True"}}}
	for _, c := range []struct {
		name        string
		verb        string
		gvr         schema.GroupVersionResource
		kind        string
		step        ActionStepDTO
		annotations map[string]any
		spec        map[string]any
		status      map[string]any
		digest      string
		rs          *unstructured.Unstructured
		siblings    []*unstructured.Unstructured // the Deployment's other ReplicaSets
		verdict     *subjectVerdict
		holds       bool
	}{
		{"suspended", "flux-suspend", ksGVR, "Kustomization", ActionStepDTO{}, nil, map[string]any{"suspend": true}, nil, "", nil, nil, nil, true},
		{"resumed from outside", "flux-suspend", ksGVR, "Kustomization", ActionStepDTO{}, nil, map[string]any{}, nil, "", nil, nil, nil, false},
		{"resumed", "flux-resume", ksGVR, "Kustomization", ActionStepDTO{}, nil, map[string]any{}, nil, "", nil, nil, nil, true},
		{"suspended again", "flux-resume", ksGVR, "Kustomization", ActionStepDTO{}, nil, map[string]any{"suspend": true}, nil, "", nil, nil, nil, false},
		{"scaled", "scale", deploysGVR, "Deployment", ActionStepDTO{Replicas: &two}, nil, map[string]any{"replicas": int64(2)}, observed(1, map[string]any{"replicas": int64(2)}), "", nil, nil, nil, true},
		{"count changed", "scale", deploysGVR, "Deployment", ActionStepDTO{Replicas: &two}, nil, map[string]any{"replicas": int64(3)}, observed(1, map[string]any{"replicas": int64(3)}), "", nil, nil, nil, false},
		{"status not the count", "scale", deploysGVR, "Deployment", ActionStepDTO{Replicas: &two}, nil, map[string]any{"replicas": int64(2)}, observed(1, map[string]any{"replicas": int64(3)}), "", nil, nil, nil, false},
		{"scale unobserved", "scale", deploysGVR, "Deployment", ActionStepDTO{Replicas: &two}, nil, map[string]any{"replicas": int64(2)}, observed(0, map[string]any{"replicas": int64(2)}), "", nil, nil, nil, false},
		{"paused", "rollout-pause", deploysGVR, "Deployment", ActionStepDTO{}, nil, map[string]any{"paused": true}, observed(1, nil), "", nil, nil, nil, true},
		{"pause unobserved", "rollout-pause", deploysGVR, "Deployment", ActionStepDTO{}, nil, map[string]any{"paused": true}, observed(0, nil), "", nil, nil, nil, false},
		{"resumed from outside", "rollout-pause", deploysGVR, "Deployment", ActionStepDTO{}, nil, map[string]any{}, observed(1, nil), "", nil, nil, nil, false},
		{"rollout resumed", "rollout-resume", deploysGVR, "Deployment", ActionStepDTO{}, nil, map[string]any{}, observed(1, nil), "", nil, nil, nil, true},
		{"paused again", "rollout-resume", deploysGVR, "Deployment", ActionStepDTO{}, nil, map[string]any{"paused": true}, observed(1, nil), "", nil, nil, nil, false},
		{"cordoned", "cordon", nodesGVR, "Node", ActionStepDTO{}, nil, map[string]any{"unschedulable": true}, nil, "", nil, nil, nil, true},
		{"uncordoned", "cordon", nodesGVR, "Node", ActionStepDTO{}, nil, map[string]any{}, nil, "", nil, nil, nil, false},
		{"schedulable", "uncordon", nodesGVR, "Node", ActionStepDTO{}, nil, map[string]any{}, nil, "", nil, nil, nil, true},
		{"cordoned again", "uncordon", nodesGVR, "Node", ActionStepDTO{}, nil, map[string]any{"unschedulable": true}, nil, "", nil, nil, nil, false},
		{"pause completed", "keda-pause", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused", "true"), map[string]any{}, pausedConds, "", nil, nil, &paused, true},
		{"verdict not completed", "keda-pause", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused", "true"), map[string]any{}, pausedConds, "", nil, nil, &notDone, false},
		{"verdict of an older generation", "keda-pause", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused", "true"), map[string]any{}, pausedConds, "", nil, nil, &older, false},
		{"paused condition not true", "keda-pause", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused", "true"), map[string]any{}, ready("True"), "", nil, nil, &paused, false},
		{"annotation false", "keda-pause", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused", "false"), map[string]any{}, nil, "", nil, nil, &paused, false},
		// Codex's counterexample: the annotation alone, Ready False and not paused
		{"pause not completed", "keda-pause", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused", "true"), map[string]any{}, map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False"}, map[string]any{"type": "Paused", "status": "True"}}}, "", nil, nil, &paused, false},
		{"no verdict", "keda-pause", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused", "true"), map[string]any{}, pausedConds, "", nil, nil, &subjectVerdict{}, false},
		{"stale verdict", "keda-pause", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused", "true"), map[string]any{}, pausedConds, "", nil, nil, &stale, false},
		{"resumed and ready", "keda-resume", soGVR, "ScaledObject", ActionStepDTO{}, nil, map[string]any{}, ready("True"), "", nil, nil, nil, true},
		{"paused again", "keda-resume", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused", "true"), map[string]any{}, ready("True"), "", nil, nil, nil, false},
		// Codex's counterexample: another pause annotation, Ready False
		{"another pause", "keda-resume", soGVR, "ScaledObject", ActionStepDTO{}, ann("autoscaling.keda.sh/paused-replicas", "0"), map[string]any{}, ready("True"), "", nil, nil, nil, false},
		{"ready false", "keda-resume", soGVR, "ScaledObject", ActionStepDTO{}, nil, map[string]any{}, ready("False"), "", nil, nil, nil, false},
		{"reverted", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": tmpl}, observed(1, nil), d, rs("rs-uid", "5"), nil, nil, true},
		{"template changed", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": map[string]any{"spec": map[string]any{}}}, observed(1, nil), d, rs("rs-uid", "5"), nil, nil, false},
		{"undo unobserved", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": tmpl}, observed(0, nil), d, rs("rs-uid", "5"), nil, nil, false},
		{"partner replaced", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": tmpl}, observed(1, nil), d, rs("rs-other", "5"), nil, nil, false},
		{"partner not current", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "6"), map[string]any{"template": tmpl}, observed(1, nil), d, rs("rs-uid", "5"), nil, nil, false},
		{"partner unread", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": tmpl}, observed(1, nil), d, nil, nil, nil, false},
		// stage 3 re-review, finding 2: A1.1's evidence rule, the revision
		// annotation only corroborating
		{"partner owned by another", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": tmpl}, observed(1, nil), d, rsOf("rs-uid", "other", "5", tmpl), nil, nil, false},
		{"partner template changed", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": tmpl}, observed(1, nil), d, rsOf("rs-uid", "u", "5", map[string]any{"spec": map[string]any{}}), nil, nil, false},
		{"ambiguous", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": tmpl}, observed(1, nil), d, rs("rs-uid", "5"), []*unstructured.Unstructured{rsOf("rs-twin", "u", "2", tmpl)}, nil, false},
		{"a twin of another Deployment", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": tmpl}, observed(1, nil), d, rs("rs-uid", "5"), []*unstructured.Unstructured{rsOf("rs-twin", "other", "2", tmpl)}, nil, true},
		{"replicasets read in part", "rollout-undo", deploysGVR, "Deployment", ActionStepDTO{}, ann("deployment.kubernetes.io/revision", "5"), map[string]any{"template": tmpl}, observed(1, nil), d, rs("rs-uid", "5"), manyReplicaSets(chatReplicaSetsMax), nil, false},
		{"statefulset reverted", "rollout-undo", stsGVR, "StatefulSet", ActionStepDTO{}, nil, map[string]any{"template": tmpl}, observed(1, nil), sd, nil, nil, nil, true},
	} {
		t.Run(c.verb+"/"+c.name, func(t *testing.T) {
			f := newToolsFixture(t)
			md := map[string]any{"name": "o", "namespace": "n", "uid": "u", "generation": int64(1)}
			if c.annotations != nil {
				md["annotations"] = c.annotations
			}
			spec := map[string]any{}
			for k, v := range c.spec {
				spec[k] = v
			}
			if c.kind == "Deployment" {
				spec["selector"] = selector
			}
			obj := map[string]any{"kind": c.kind, "metadata": md, "spec": spec}
			if c.status != nil {
				obj["status"] = c.status
			}
			f.conn.objects[key(c.gvr, "n", "o")] = uobj(obj)
			if c.rs != nil {
				f.conn.objects[key(replicaSetsGVR, "n", "o-4")] = c.rs
				l := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*c.rs}}
				for _, s := range c.siblings {
					l.Items = append(l.Items, *s)
				}
				f.conn.lists[replicaSetsGVR.String()+"|n"] = l
			}
			prev := verdictOfHook
			verdictOfHook = func(actionFact) subjectVerdict {
				if c.verdict == nil {
					t.Error("the Observer was read for " + c.verb)
					return subjectVerdict{}
				}
				return *c.verdict
			}
			t.Cleanup(func() { verdictOfHook = prev })
			fact := actionFact{Kind: c.kind, Group: c.gvr.Group, Version: c.gvr.Version, Resource: c.gvr.Resource, Namespace: "n", Name: "o", UID: "u"}
			var partner *fleet.RevisionExpect
			if c.verb == "rollout-undo" {
				fact.History = &historyFacts{PartnerRevision: 4, PartnerObject: "o-4", PartnerObjectUID: "rs-uid", PartnerDigest: c.digest}
				partner = &fleet.RevisionExpect{ObjectUID: "rs-uid", TemplateDigest: c.digest}
			}
			// the prior's verb decides, not a copy in its step
			_, why := f.svc.observePrior(context.Background(), f.conn, stepSnapPrior{n: 1, verb: c.verb, fact: fact, step: c.step, partner: partner})
			if c.holds != (why == "") {
				t.Errorf("holds=%v: %q", c.holds, why)
			}
		})
	}
}

// Stage 2 re-review 2: a withdrawal observed while the turn runs survives
// the turn's settlement, and no step is offered before it settles.
func TestAWithdrawalSurvivesTheTurnsSettlement(t *testing.T) {
	seed := ChatSeedDTO{Kind: "object", Group: "apps", Version: "v1", Plural: "deployments", Namespace: "shop", Name: "web", UID: "uid-web"}
	propose := ai.ToolCall{ID: "a1", Name: toolActions, Args: json.RawMessage(`{"steps":[` + pauseWeb + `]}`)}
	release := make(chan struct{})
	reached := make(chan struct{})
	f := newToolsFixture(t, calls(propose), func(r ai.ConvRequest) (ai.Turn, error) {
		close(reached)
		<-release
		return answer("done")(r)
	})
	f.conn.objects[key(deploysGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web"}, "spec": map[string]any{"replicas": int64(3)}})
	inv := f.svc.StartInvestigation("x", seed)
	done := make(chan ChatStateDTO, 1)
	go func() { done <- f.svc.Ask("x", inv.ConversationID, "", nil) }()
	<-reached
	ex := f.svc.InvestigationState("x").Exchanges[0]
	if r := f.svc.ReviewStep("x", inv.ConversationID, ex.ID, 1); r.Offered || !strings.Contains(r.Reason, "still running") {
		t.Fatalf("no step is offered while its turn runs: %+v", r)
	}
	f.mu.Lock()
	f.usable = false
	f.mu.Unlock()
	if st := f.svc.InvestigationState("x"); st.Exchanges[0].Plan.Steps[0].State != stepWithdrawn {
		t.Fatalf("withdrawn mid-turn: %+v", st.Exchanges[0].Plan)
	}
	f.mu.Lock()
	f.usable = true
	f.mu.Unlock()
	close(release)
	settled := <-done
	if settled.Exchanges[0].Plan.Steps[0].State != stepWithdrawn {
		t.Fatalf("the settled plan keeps the withdrawal: %+v", settled.Exchanges[0].Plan)
	}
	if r := f.svc.ReviewStep("x", inv.ConversationID, ex.ID, 1); r.Offered || !strings.Contains(r.Reason, "withdrawn") {
		t.Fatalf("and its steps stay withdrawn: %+v", r)
	}
}

// Stage 3 review, finding 1: the consequence the operator confirmed is part
// of the review's digest - a changed owner, an ended suspension, an
// incomplete ownership read or a new autoscaler needs a new review.
func TestAChangedConsequenceNeedsANewReview(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(sc *stepsConn)
	}{
		{"another owner", func(sc *stepsConn) { sc.owner.OwnerName = "other" }},
		{"suspension ended", func(sc *stepsConn) { sc.owner.OwnerSuspended = false }},
		{"ownership not fully read", func(sc *stepsConn) { sc.owner.Warnings = []string{"the inventory read failed"} }},
		{"an autoscaler appeared", func(sc *stepsConn) { sc.replica.Owners = []string{"HorizontalPodAutoscaler shop/web"} }},
		{"autoscaler unproven", func(sc *stepsConn) { sc.replica.Autoscaler = "unverified" }},
		{"unchanged", func(*stepsConn) {}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sf := newStepsFixture(t, scaleWeb)
			sf.sc.owner = fleet.GitOpsOwnership{OwnerKind: "Kustomization", OwnerNamespace: "flux-system", OwnerName: "a1-web", Basis: "flux-inventory", OwnerSuspended: true}
			rv := sf.review(1)
			if !rv.Offered {
				t.Fatalf("%+v", rv)
			}
			c.change(sf.sc)
			r := sf.run(1, rv.Token)
			if c.name == "unchanged" {
				if !r.Ran || len(sf.sc.recorded()) != 1 {
					t.Fatalf("the control runs: %+v %v", r, sf.sc.recorded())
				}
				return
			}
			if r.Ran || len(sf.sc.recorded()) != 0 || !strings.Contains(r.Reason, "changed since you reviewed it") {
				t.Fatalf("%+v %v", r, sf.sc.recorded())
			}
			if rv2 := sf.review(1); !rv2.Offered || strings.Join(rv2.Consequence, "; ") == strings.Join(rv.Consequence, "; ") {
				t.Fatalf("the new review words the new consequence: %+v", rv2)
			}
		})
	}
}

// Stage 3 review, finding 2: one step runs per conversation, whichever plan
// holds it - through its write and through its verification.
func TestOneStepRunsPerConversation(t *testing.T) {
	var settle func()
	prev := verifyStepHook
	verifyStepHook = func(s *ChatService, c *conversation, p *actionPlan, n int, _ stepSnap, _ ActionResultDTO) {
		settle = func() { s.settleEffect(c, p, n, stepEffectEstablished, "") }
	}
	t.Cleanup(func() { verifyStepHook = prev })
	sf := newStepsFixture(t, pauseWeb)
	if r := sf.run(1, sf.review(1).Token); !r.Ran {
		t.Fatal(r)
	}
	sf.prov.turns = append(sf.prov.turns, calls(ai.ToolCall{ID: "n", Name: toolActions, Args: json.RawMessage(`{"steps":[` + scaleWeb + `]}`)}), answer("another"))
	st := sf.svc.Ask("x", sf.conv, "and then?", nil)
	ex := st.Exchanges[len(st.Exchanges)-1].ID
	if r := sf.svc.ReviewStep("x", sf.conv, ex, 1); r.Offered || !strings.Contains(r.Reason, "step 1 of an earlier plan is running") {
		t.Fatalf("while the first plan's step verifies: %+v", r)
	}
	settle()
	rv := sf.svc.ReviewStep("x", sf.conv, ex, 1)
	if !rv.Offered {
		t.Fatalf("once it settles: %+v", rv)
	}
	if r := sf.svc.RunStep("x", sf.conv, ex, 1, rv.Token); !r.Ran {
		t.Fatal(r)
	}
}

// blockingPause holds the first write until released.
type blockingPause struct {
	*stepsConn
	reached, release chan struct{}
}

func (c *blockingPause) RolloutPause(_ context.Context, ns, name, uid string, pause bool) error {
	err := c.note("pause %s/%s %s %v", ns, name, uid, pause)
	close(c.reached)
	<-c.release
	return err
}

func TestNoSecondWriteWhileAWriteIsInFlight(t *testing.T) {
	establish(t)
	sf := newStepsFixture(t, pauseWeb)
	bc := &blockingPause{stepsConn: sf.sc, reached: make(chan struct{}), release: make(chan struct{})}
	sf.mu.Lock()
	sf.cur = bc
	sf.mu.Unlock()
	sf.svc.mu.Lock()
	sf.svc.convs[invKey("x")].conn = bc
	sf.svc.mu.Unlock()
	rv := sf.review(1)
	done := make(chan StepRunDTO, 1)
	go func() { done <- sf.run(1, rv.Token) }()
	<-bc.reached
	sf.prov.turns = append(sf.prov.turns, calls(ai.ToolCall{ID: "n", Name: toolActions, Args: json.RawMessage(`{"steps":[` + scaleWeb + `]}`)}), answer("another"))
	st := sf.svc.Ask("x", sf.conv, "and then?", nil)
	ex := st.Exchanges[len(st.Exchanges)-1].ID
	r := sf.svc.ReviewStep("x", sf.conv, ex, 1)
	close(bc.release)
	if first := <-done; !first.Ran {
		t.Fatal(first)
	}
	if r.Offered || !strings.Contains(r.Reason, "of an earlier plan is running") || len(sf.sc.recorded()) != 1 {
		t.Fatalf("%+v %v", r, sf.sc.recorded())
	}
}

// Stage 3 review, finding 4: a dismissal made while the proposing turn
// runs survives its settlement.
func TestADismissalSurvivesTheTurnsSettlement(t *testing.T) {
	seed := ChatSeedDTO{Kind: "object", Group: "apps", Version: "v1", Plural: "deployments", Namespace: "shop", Name: "web", UID: "uid-web"}
	reached, release := make(chan struct{}), make(chan struct{})
	f := newToolsFixture(t, calls(ai.ToolCall{ID: "p", Name: toolActions, Args: json.RawMessage(`{"steps":[` + pauseWeb + `]}`)}), func(r ai.ConvRequest) (ai.Turn, error) {
		close(reached)
		<-release
		return answer("done")(r)
	})
	f.conn.objects[key(deploysGVR, "shop", "web")] = uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "uid-web"}, "spec": map[string]any{"replicas": int64(3)}})
	inv := f.svc.StartInvestigation("x", seed)
	done := make(chan ChatStateDTO, 1)
	go func() { done <- f.svc.Ask("x", inv.ConversationID, "", nil) }()
	<-reached
	ex := f.svc.InvestigationState("x").Exchanges[0]
	d := f.svc.DismissPlan("x", inv.ConversationID, ex.ID)
	close(release)
	st := <-done
	if d.Plan == nil || d.Plan.Status != planStatusDeclined || st.Exchanges[0].Plan.Status != planStatusDeclined {
		t.Fatalf("%+v %+v", d.Plan, st.Exchanges[0].Plan)
	}
	if r := f.svc.ReviewStep("x", inv.ConversationID, ex.ID, 1); r.Offered || !strings.Contains(r.Reason, "dismissed") {
		t.Fatalf("%+v", r)
	}
}

// Stage 3 review, finding 5: a step's record is at most 1 KiB and counted
// by retention.
func TestARunRecordIsBoundedAndCounted(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	sf.sc.err = errors.New(strings.Repeat("é", 50<<10))
	if r := sf.run(1, sf.review(1).Token); !r.Ran {
		t.Fatal(r)
	}
	sf.svc.mu.Lock()
	defer sf.svc.mu.Unlock()
	e := sf.svc.convs[invKey("x")].exchanges[0]
	r := e.plan.runs[0]
	b, _ := json.Marshal(StepRunStateDTO{Request: r.request, NotSent: r.notSent, Error: r.errText, Effect: r.effect, EffectWhy: r.effectWhy, RanAt: r.ranAt.UTC().Format(time.RFC3339)})
	if len(b) > 1024 || !utf8.ValidString(r.errText) || !strings.HasSuffix(r.errText, "…") {
		t.Fatalf("%d bytes: %q", len(b), r.errText)
	}
	with := exchangeBytes(e)
	e.plan.runs[0] = &stepRun{}
	without := exchangeBytes(e)
	e.plan.runs[0] = r
	if with-without != len(r.request)+len(r.errText)+len(r.effect)+len(r.effectWhy) {
		t.Fatalf("counted %d of the record's texts", with-without)
	}
}

func TestAConversationKeepsAtMost16StepRecords(t *testing.T) {
	sf := newStepsFixture(t, pauseWeb)
	sf.sc.err = apierrors.NewForbidden(deploysGVR.GroupResource(), "web", errors.New("denied"))
	ex := sf.ex
	for i := 0; i < 17; i++ {
		if i > 0 {
			sf.prov.turns = append(sf.prov.turns, calls(ai.ToolCall{ID: "again", Name: toolActions, Args: json.RawMessage(`{"steps":[` + pauseWeb + `]}`)}), answer("proposal"))
			st := sf.svc.Ask("x", sf.conv, "again", nil)
			ex = st.Exchanges[len(st.Exchanges)-1].ID
		}
		if r := sf.svc.RunStep("x", sf.conv, ex, 1, sf.svc.ReviewStep("x", sf.conv, ex, 1).Token); !r.Ran {
			t.Fatalf("%d: %+v", i, r)
		}
	}
	st := sf.svc.InvestigationState("x")
	steps := 0
	for _, e := range st.Exchanges {
		if e.Plan != nil {
			steps += len(e.Plan.Steps)
		}
	}
	first, last := st.Exchanges[0], st.Exchanges[len(st.Exchanges)-1]
	if steps != 16 || first.Plan != nil || !strings.Contains(first.Note, "at most 16 step records") || last.Plan == nil || last.ID != ex {
		t.Fatalf("%d step records; first %+v", steps, first)
	}
}

// Retention never drops the exchange whose plan's step is running.
func TestRetentionKeepsTheRunningPlan(t *testing.T) {
	running := &actionPlan{dto: ActionPlanDTO{Steps: []ActionStepDTO{{N: 1}}}, runs: []*stepRun{{}}, running: 1}
	c := &conversation{stepRunning: running}
	for i := 0; i <= chatRetainExchange; i++ {
		c.exchanges = append(c.exchanges, &exchange{id: fmt.Sprint(i)})
	}
	c.exchanges[0].plan = running
	(&ChatService{}).retainLocked(c)
	if len(c.exchanges) != chatRetainExchange || c.exchanges[0].plan != running || c.exchanges[1].id != "2" {
		t.Fatalf("%d kept, first %s, second %s", len(c.exchanges), c.exchanges[0].id, c.exchanges[1].id)
	}
}

func TestCapRecordCutsOnARuneBoundary(t *testing.T) {
	for _, s := range []string{strings.Repeat("a", 600), strings.Repeat("é", 300), strings.Repeat("日", 200)} {
		got := capRecord(s, stepErrMax)
		if len(got) > stepErrMax || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
			t.Fatalf("%d bytes, valid %v", len(got), utf8.ValidString(got))
		}
	}
	if capRecord("short", stepErrMax) != "short" {
		t.Fatal("a short text is kept")
	}
}

// A run's record can take the conversation over its byte bound: retention
// runs after the run, not only at the next question.
func TestARunIsRetainedWithinTheBound(t *testing.T) {
	prev := verifyStepHook
	verifyStepHook = func(*ChatService, *conversation, *actionPlan, int, stepSnap, ActionResultDTO) {} // the effect stays pending
	t.Cleanup(func() { verifyStepHook = prev })
	sf := newStepsFixture(t, pauseWeb)
	sf.sc.err = errors.New(strings.Repeat("z", 4096))
	rv := sf.review(1)
	sf.svc.mu.Lock()
	c := sf.svc.convs[invKey("x")]
	size := 0
	for _, e := range c.exchanges {
		size += exchangeBytes(e)
	}
	filler := &exchange{id: "filler"}
	filler.answer = strings.Repeat("a", chatRetainBytes-size-exchangeBytes(filler)-200)
	c.exchanges = append([]*exchange{filler}, c.exchanges...)
	removed := c.removed
	sf.svc.mu.Unlock()
	if r := sf.run(1, rv.Token); !r.Ran {
		t.Fatal(r)
	}
	sf.svc.mu.Lock()
	defer sf.svc.mu.Unlock()
	if len(c.exchanges) != 1 || c.exchanges[0].id != sf.ex || c.removed != removed+1 {
		t.Fatalf("%d exchanges, first %s", len(c.exchanges), c.exchanges[0].id)
	}
}

// The step-record cap drops superseded plans only: never the current plan,
// never the running one, whatever the count.
func TestTheStepRecordCapKeepsTheCurrentAndRunningPlans(t *testing.T) {
	plan := func(n int) *actionPlan {
		p := &actionPlan{}
		for i := 0; i < n; i++ {
			p.dto.Steps = append(p.dto.Steps, ActionStepDTO{N: i + 1})
			p.runs = append(p.runs, &stepRun{})
		}
		return p
	}
	old, running, current := plan(4), plan(4), plan(17)
	c := &conversation{stepRunning: running, exchanges: []*exchange{{plan: old}, {plan: running}, {plan: current}}}
	retainStepRecords(c, func(e *exchange) bool { return e.plan != nil && e.plan == c.stepRunning })
	if c.exchanges[0].plan != nil || c.exchanges[1].plan != running || c.exchanges[2].plan != current {
		t.Fatalf("%+v", c.exchanges)
	}
}

// manyReplicaSets is a full page of unrelated ReplicaSets: a read in part.
func manyReplicaSets(n int) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for i := 0; i < n; i++ {
		out = append(out, uobj(map[string]any{"kind": "ReplicaSet", "metadata": map[string]any{"name": fmt.Sprint("x", i), "namespace": "n", "uid": fmt.Sprint("x", i)}}))
	}
	return out
}

// Stage 3 re-review, finding 1: a real completed pause, as the Observer
// captures it - a native part, its state in Role, the row's own Completed -
// permits the dependent step; the row's evidence and the fresh read must
// both show it.
func TestARealCompletedKEDAPauseStands(t *testing.T) {
	conds := func(paused, ready string) map[string]any {
		return map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": ready}, map[string]any{"type": "Paused", "status": paused}}}
	}
	for _, c := range []struct {
		name        string
		kind        string
		row, get    map[string]any // the captured object's status, the fresh read's
		hpaComplete bool
		holds       bool
	}{
		{"completed", "ScaledObject", conds("True", "True"), conds("True", "True"), true, true},
		{"completed", "ScaledJob", conds("True", "True"), conds("True", "True"), true, true},
		{"its HPA not proven absent", "ScaledObject", conds("True", "True"), conds("True", "True"), false, false},
		{"the capture lags the read", "ScaledObject", conds("False", "True"), conds("True", "True"), true, false},
		{"the capture lags the read", "ScaledJob", conds("False", "True"), conds("True", "True"), true, false},
		{"the read shows Ready False", "ScaledObject", conds("True", "True"), conds("True", "False"), true, false},
		{"the read shows Ready False", "ScaledJob", conds("True", "True"), conds("True", "False"), true, false},
	} {
		t.Run(c.kind+"/"+c.name, func(t *testing.T) {
			f := newToolsFixture(t)
			res := strings.ToLower(c.kind) + "s"
			gvr := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: res}
			obj := func(status map[string]any) *unstructured.Unstructured {
				return uobj(map[string]any{"apiVersion": "keda.sh/v1alpha1", "kind": c.kind, "metadata": map[string]any{"name": "q", "namespace": "shop", "uid": "q-uid", "generation": int64(3), "annotations": map[string]any{"autoscaling.keda.sh/paused": "true"}}, "status": status})
			}
			ver := keda.Version{Known: true, Major: 2, Minor: 21}
			ref := healthsrc.RowRef{UID: "q-uid", GVR: "keda.sh/v1alpha1/" + res, Namespace: "shop", Name: "q", Generation: 3}
			var rowset any
			var role string
			if c.kind == "ScaledObject" {
				proj := keda.ProjectScaledObject(obj(c.row))
				v := keda.ScaledObjectRule(proj, keda.SOInputs{Now: fixedNow(), Version: ver, HPA: keda.HPAEvidence{Current: true, Complete: c.hpaComplete}})
				role = v.Eval.Role.String()
				rowset = healthsrc.RowSet[healthsrc.ScaledObjectRow]{Rows: []healthsrc.Row[healthsrc.ScaledObjectRow]{{Ref: ref, Data: healthsrc.ScaledObjectRow{ScaledObject: proj, Verdict: v, Version: ver, HPACurrent: true, HPAComplete: c.hpaComplete}, Eval: v.Eval}}}
			} else {
				proj := keda.ProjectScaledJob(obj(c.row))
				v := keda.ScaledJobRule(proj, keda.JobInputs{Now: fixedNow(), Version: ver})
				role = v.Eval.Role.String()
				rowset = healthsrc.RowSet[healthsrc.ScaledJobRow]{Rows: []healthsrc.Row[healthsrc.ScaledJobRow]{{Ref: ref, Data: healthsrc.ScaledJobRow{ScaledJob: proj, Verdict: v, Version: ver}, Eval: v.Eval}}}
			}
			part := chatParts["keda.sh/"+res]
			f.conn.rows = map[string]observe.Rows{part[0] + "/" + part[1]: {Task: part[0], EvaluatedAt: fixedNow(), Part: observe.Part{ID: part[1], Classifier: "native", Coverage: observe.Live}, Rows: rowset}}
			get := obj(c.get)
			f.conn.objects[key(gvr, "shop", "q")] = get
			fact := factOf(gvr, get, "r1", "now", acquisition())
			if sv := f.svc.verdictOf(context.Background(), f.conn, fact, nil); !sv.found || sv.v.Kind != "" || sv.state == "" || sv.state != role {
				t.Fatalf("a native verdict's state is its Role: %+v (role %q)", sv, role)
			}
			_, why := f.svc.observePrior(context.Background(), f.conn, stepSnapPrior{n: 1, verb: "keda-pause", fact: fact})
			if c.holds != (why == "") {
				t.Fatalf("holds=%v: %q", c.holds, why)
			}
			if !c.holds && c.name != "the read shows Ready False" && !strings.Contains(why, "Klyx reads it "+role) {
				t.Fatalf("the reason names Klyx's reading: %q", why)
			}
		})
	}
}

// Stage 3 re-review, finding 3: the record's bound is its serialized size,
// escapes included.
func TestARecordSerializesWithinOneKiB(t *testing.T) {
	for _, worst := range []string{"\x00", "<", "\u2028", "\"", "\xff", "日", "a"} {
		r := StepRunStateDTO{Request: fleet.OutcomeUnverified, NotSent: true, Error: capRecord(strings.Repeat(worst, 2048), stepErrMax), Effect: stepEffectEstablished,
			EffectWhy: capRecord(strings.Repeat(worst, 2048), stepWhyMax), RanAt: fixedNow().UTC().Format(time.RFC3339)}
		b, _ := json.Marshal(r)
		if len(b) > 1024 || !utf8.ValidString(r.Error) && worst != "\xff" {
			t.Fatalf("%q: %d bytes", worst, len(b))
		}
		if e, _ := json.Marshal(r.Error); len(e)-2 > stepErrMax {
			t.Fatalf("%q: the error encodes to %d bytes", worst, len(e)-2)
		}
	}
}

// Stage 3 re-review 2: the partner must be among the Deployment's
// ReplicaSets as their complete LIST by its selector reads them - the
// controller releases one that stops matching, whatever its controller
// reference says.
func TestAPartnerMustBeAmongTheDeploymentsReplicaSets(t *testing.T) {
	tmpl := map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "web"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "web", "image": "web:4"}}}}
	dg, _ := workloads.TemplateDigest(tmpl, true)
	controller := true
	for _, c := range []struct {
		name   string
		labels map[string]string
		listed bool
		holds  bool
		// listedOwner: the list's copy names another controller than the read's
		listedOwner string
	}{
		{"a member", map[string]string{"app": "web"}, true, true, ""},
		{"relabelled", map[string]string{"app": "other"}, true, false, ""},
		{"absent from a complete list", map[string]string{"app": "web"}, false, false, ""},
		{"the list disagrees with the read", map[string]string{"app": "web"}, true, false, "other-uid"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newToolsFixture(t)
			d := uobj(map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "web", "namespace": "shop", "uid": "dep-uid", "generation": int64(9), "annotations": map[string]any{"deployment.kubernetes.io/revision": "8"}},
				"spec": map[string]any{"template": tmpl, "selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}}, "status": map[string]any{"observedGeneration": int64(9)}})
			rs := uobj(map[string]any{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": map[string]any{"name": "web-r4", "namespace": "shop", "uid": "rs-uid", "annotations": map[string]any{"deployment.kubernetes.io/revision": "8"}}, "spec": map[string]any{"template": tmpl}})
			rs.SetLabels(c.labels)
			rs.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "dep-uid", Controller: &controller}})
			f.conn.objects[key(replicaSetsGVR, "shop", "web-r4")] = rs
			l := &unstructured.UnstructuredList{}
			if c.listed {
				cp := rs.DeepCopy()
				if c.listedOwner != "" {
					cp.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "other", UID: types.UID(c.listedOwner), Controller: &controller}})
				}
				l.Items = append(l.Items, *cp)
			}
			f.conn.lists[replicaSetsGVR.String()+"|shop"] = l
			fact := factOf(deploysGVR, d, "r1", "t", acquisition())
			fact.History = &historyFacts{PartnerRevision: 4, PartnerObject: "web-r4", PartnerObjectUID: "rs-uid", PartnerDigest: dg}
			ok, why := f.svc.stateEffect(context.Background(), f.conn, ActionStepDTO{Verb: "rollout-undo"}, fact, &fleet.RevisionExpect{ObjectUID: "rs-uid", TemplateDigest: dg}, d, time.Time{})
			if ok != c.holds {
				t.Fatalf("holds=%v: %q", c.holds, why)
			}
		})
	}
}

package evalrun

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/appbridge"
)

// AI A3 §7's lab runner, on a stand-in chat and plant: a case reviews and
// runs each offered step in order, waits for its effect and the incident,
// asks the lab script for its own writes, and judges its expectation.

type a3Plant struct{ ok []bool } // successive checks

func (p *a3Plant) Check(context.Context, *Scenario) Plant {
	ok := p.ok[0]
	if len(p.ok) > 1 {
		p.ok = p.ok[1:]
	}
	return Plant{OK: ok, AnchorUID: "uid-ks", Failed: map[bool]string{false: "not planted"}[ok]}
}

type a3Chat struct {
	mu      sync.Mutex
	plan    appbridge.ActionPlanDTO
	reviews map[int]appbridge.StepReviewDTO
	effects map[int]string // what each run step's effect becomes
	calls   []string
	cleared bool
}

func (c *a3Chat) note(s string) { c.mu.Lock(); c.calls = append(c.calls, s); c.mu.Unlock() }

func (c *a3Chat) StartInvestigation(string, appbridge.ChatSeedDTO) appbridge.ChatStateDTO {
	return appbridge.ChatStateDTO{ConversationID: "c1"}
}
func (c *a3Chat) Ask(string, string, string, *appbridge.ChatSeedDTO) appbridge.ChatStateDTO {
	c.note("ask")
	return c.state()
}
func (c *a3Chat) Clear(string, string) { c.cleared = true }
func (c *a3Chat) InvestigationState(string) appbridge.ChatStateDTO {
	return c.state()
}
func (c *a3Chat) state() appbridge.ChatStateDTO {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.plan
	p.Steps = append([]appbridge.ActionStepDTO(nil), c.plan.Steps...)
	return appbridge.ChatStateDTO{ConversationID: "c1", Exchanges: []appbridge.ChatExchangeDTO{{ID: "e1", Plan: &p}}}
}
func (c *a3Chat) ReviewStep(_, _, _ string, n int) appbridge.StepReviewDTO {
	c.note("review " + string(rune('0'+n)))
	if r, ok := c.reviews[n]; ok {
		return r
	}
	return appbridge.StepReviewDTO{N: n, Offered: true, Token: "t"}
}
func (c *a3Chat) RunStep(_, _, _ string, n int, _ string) appbridge.StepRunDTO {
	c.note("run " + string(rune('0'+n)))
	c.mu.Lock()
	c.plan.Steps[n-1].Run = &appbridge.StepRunStateDTO{Request: "written", Effect: c.effects[n]}
	if n == len(c.plan.Steps) && c.effects[n] == "established" {
		c.plan.Incident = &appbridge.IncidentDTO{State: "resolved"}
	}
	c.mu.Unlock()
	return appbridge.StepRunDTO{Ran: true, Result: &appbridge.ActionResultDTO{Outcome: "written"}}
}

func a3Steps(verbs ...string) []appbridge.ActionStepDTO {
	var out []appbridge.ActionStepDTO
	for i, v := range verbs {
		out = append(out, appbridge.ActionStepDTO{N: i + 1, Verb: v, State: "offered", Target: appbridge.ChatCheckSubject{Kind: "Deployment", Namespace: "shop", Name: "web"}})
	}
	return out
}

func a3Lab(chat *a3Chat, plant *a3Plant, writes *[]string) *A3Lab {
	return &A3Lab{
		Chat: chat, Cluster: "lab", Plant: plant,
		Ready: func(context.Context, *Scenario, string, time.Time) (bool, string) { return true, "" },
		Write: func(_ context.Context, action string) error {
			*writes = append(*writes, action)
			chat.note("write " + action)
			return nil
		},
		ReadyWait: time.Second, EffectWait: time.Second, IncidentWait: time.Second, RestoreWait: time.Second,
		Poll: time.Millisecond, sleep: func(time.Duration) {},
	}
}

func TestTheA3PlanCaseRunsEachStepInOrderAndRestores(t *testing.T) {
	chat := &a3Chat{plan: appbridge.ActionPlanDTO{Steps: a3Steps("flux-suspend", "rollout-undo")}, effects: map[int]string{1: "established", 2: "established"}}
	var writes []string
	rec := a3Lab(chat, &a3Plant{ok: []bool{true}}, &writes).Run(context.Background(), &Scenario{}, A3Plan)
	if !rec.Held || rec.Status != "completed" || !rec.Restored || rec.Incident == nil || rec.Incident.State != "resolved" {
		t.Fatalf("%+v", rec)
	}
	if got := strings.Join(chat.calls, ","); got != "ask,review 1,run 1,review 2,run 2,write restore" || rec.Duration == "" {
		t.Fatalf("%s", got)
	}
	if !chat.cleared {
		t.Fatal("the conversation is cleared")
	}
}

func TestTheA3ResumeBetweenCaseWritesBeforeStep2sReview(t *testing.T) {
	chat := &a3Chat{plan: appbridge.ActionPlanDTO{Steps: a3Steps("flux-suspend", "rollout-undo")}, effects: map[int]string{1: "established"},
		reviews: map[int]appbridge.StepReviewDTO{2: {N: 2, Reason: "step 1's effect no longer holds on Kustomization klyx-a1-flux/a1-web: it is not suspended"}}}
	var writes []string
	rec := a3Lab(chat, &a3Plant{ok: []bool{true}}, &writes).Run(context.Background(), &Scenario{}, A3ResumeBetween)
	if got := strings.Join(chat.calls, ","); got != "ask,review 1,run 1,write resume,review 2,write restore" {
		t.Fatalf("%s", got)
	}
	if !rec.Held {
		t.Fatalf("%s: %+v", rec.Why, rec)
	}
}

func TestTheA3ExpectationsHoldOnlyAsTheyShould(t *testing.T) {
	established := func(n int, verb string) A3StepRecord {
		return A3StepRecord{N: n, Verb: verb, State: "offered", Reviewed: true, Ran: true, Request: "written", Effect: "established"}
	}
	resolved := &appbridge.IncidentDTO{State: "resolved"}
	for _, c := range []struct {
		name string
		rec  A3Record
		held bool
	}{
		{"plan held", A3Record{Case: A3Plan, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "flux-suspend"), established(2, "rollout-undo")}, Incident: resolved}, true},
		{"plan unresolved", A3Record{Case: A3Plan, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "flux-suspend"), established(2, "rollout-undo")}, Incident: &appbridge.IncidentDTO{State: "unverified"}}, false},
		{"plan unestablished", A3Record{Case: A3Plan, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "flux-suspend"), {N: 2, Verb: "rollout-undo", Ran: true, Effect: "unverified"}}}, false},
		{"plan not restored", A3Record{Case: A3Plan, Status: "completed", Steps: []A3StepRecord{established(1, "flux-suspend"), established(2, "rollout-undo")}, Incident: resolved}, false},
		{"undo-only held", A3Record{Case: A3UndoOnly, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "rollout-undo")}, Incident: &appbridge.IncidentDTO{State: "not resolved"}}, true},
		{"undo-only resolved", A3Record{Case: A3UndoOnly, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "rollout-undo")}, Incident: resolved}, false},
		// stage 6 review, finding 3 (Codex's probe): the counterexample, or nothing
		{"undo-only refused", A3Record{Case: A3UndoOnly, Status: "completed", Restored: true, Steps: []A3StepRecord{{N: 1, Verb: "rollout-undo", State: "offered", Reviewed: true, Ran: true, Request: "refused", Error: "Forbidden"}}}, false},
		{"undo-only pending", A3Record{Case: A3UndoOnly, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "rollout-undo")}, Incident: &appbridge.IncidentDTO{State: "pending"}}, false},
		{"undo-only never read", A3Record{Case: A3UndoOnly, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "rollout-undo")}}, false},
		{"undo-only unverified", A3Record{Case: A3UndoOnly, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "rollout-undo")}, Incident: &appbridge.IncidentDTO{State: "unverified"}}, false},
		{"undo-only lost answer", A3Record{Case: A3UndoOnly, Status: "completed", Restored: true, Steps: []A3StepRecord{{N: 1, Verb: "rollout-undo", State: "offered", Reviewed: true, Ran: true, Request: "unverified", Effect: "established"}}, Incident: &appbridge.IncidentDTO{State: "not resolved"}}, false},
		{"undo-only unestablished", A3Record{Case: A3UndoOnly, Status: "completed", Restored: true, Steps: []A3StepRecord{{N: 1, Verb: "rollout-undo", State: "offered", Reviewed: true, Ran: true, Request: "written", Effect: "unverified"}}}, false},
		{"plan refused", A3Record{Case: A3Plan, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "flux-suspend"), {N: 2, Verb: "rollout-undo", Ran: true, Request: "refused", Effect: "established"}}, Incident: resolved}, false},
		{"resume-between unwritten", A3Record{Case: A3ResumeBetween, Status: "completed", Restored: true, Steps: []A3StepRecord{{N: 1, Verb: "flux-suspend", Ran: true, Request: "unverified", Effect: "established"}, {N: 2, Verb: "rollout-undo", ReviewReason: "step 1's effect no longer holds on Kustomization a1-web"}}}, false},
		{"resume-between held", A3Record{Case: A3ResumeBetween, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "flux-suspend"), {N: 2, Verb: "rollout-undo", ReviewReason: "step 1's effect no longer holds on Kustomization a1-web"}}}, true},
		{"resume-between, another step 2", A3Record{Case: A3ResumeBetween, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "flux-suspend"), {N: 2, Verb: "scale", ReviewReason: "step 1's effect no longer holds on Kustomization a1-web"}}}, false},
		{"resume-between offered", A3Record{Case: A3ResumeBetween, Status: "completed", Restored: true, Steps: []A3StepRecord{established(1, "flux-suspend"), established(2, "rollout-undo")}}, false},
		{"a failed plant", A3Record{Case: A3Plan, Status: "plant-failed"}, false},
	} {
		if held, why := c.rec.expect(); held != c.held {
			t.Errorf("%s: held=%v %s", c.name, held, why)
		}
	}
}

func TestAnA3CaseStopsAtAFailedPlantOrAnEmptyPlan(t *testing.T) {
	var writes []string
	rec := a3Lab(&a3Chat{}, &a3Plant{ok: []bool{false}}, &writes).Run(context.Background(), &Scenario{}, A3Plan)
	if rec.Status != "plant-failed" || len(writes) != 0 {
		t.Fatalf("%+v %v", rec, writes)
	}
	chat := &a3Chat{}
	rec = a3Lab(chat, &a3Plant{ok: []bool{true}}, &writes).Run(context.Background(), &Scenario{}, A3Plan)
	if rec.Status != "no-plan" || !rec.Restored || rec.Held {
		t.Fatalf("an empty plan still restores the lab, and holds nothing: %+v", rec)
	}
}

// An undo-only case that is not the counterexample says which evidence is
// missing: the roll back's effect, or the incident's fault at the bound.
func TestAnUndoOnlyCaseSaysWhatIsMissing(t *testing.T) {
	for _, c := range []struct {
		step     A3StepRecord
		incident *appbridge.IncidentDTO
		want     string
	}{
		{A3StepRecord{N: 1, Verb: "rollout-undo", Ran: true, Request: "written", Effect: "unverified", EffectWhy: "not established within 5m"}, nil, "the roll back's effect is unverified"},
		{A3StepRecord{N: 1, Verb: "rollout-undo", Ran: true, Request: "written", Effect: "established"}, &appbridge.IncidentDTO{State: "unverified"}, "the incident is unverified, not a fault in force"},
	} {
		r := A3Record{Case: A3UndoOnly, Status: "completed", Restored: true, Steps: []A3StepRecord{c.step}, Incident: c.incident}
		if held, why := r.expect(); held || !strings.Contains(why, c.want) {
			t.Errorf("held=%v %q, want %q", held, why, c.want)
		}
	}
}

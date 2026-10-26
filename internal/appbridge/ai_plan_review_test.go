package appbridge

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Settings F2a implementation review (Codex's probes, adopted).

// R1: a replacement for "Use the file's values" adopts the file as it is
// now, never the block it was first planned from.
func TestAFilePlansReplacementAdoptsTheCurrentFile(t *testing.T) {
	f := newSettingsFixture(t, localFleet)
	f.edit(t, strings.Replace(localFleet, "model: m", "model: file-one", 1))
	p := f.svc.PlanAIFromFile()
	f.edit(t, strings.Replace(localFleet, "model: m", "model: file-two", 1))
	r := f.svc.ApplyAI(p.Digest, "")
	if r.OK || r.Plan == nil {
		t.Fatalf("must re-plan: %+v", r)
	}
	if r2 := f.svc.ApplyAI(r.Plan.Digest, ""); !r2.OK {
		t.Fatalf("the new plan: %+v", r2)
	}
	if got := f.ai.load().Model; got != "file-two" || f.svc.GetAISettings().Diverged {
		t.Fatalf("activated %q with file-two on disk; diverged=%v", got, f.svc.GetAISettings().Diverged)
	}
}

// R1: a Revert planned before a successful Retry cannot bring the earlier
// terms back, nor can a replacement of it.
func TestASupersededRevertCannotReturn(t *testing.T) {
	f := newSettingsFixture(t, localFleet)
	d := localDraft
	d.Model, d.KeySource = "next-model", "keychain"
	f.svc.build, _ = failingBuild(2)
	p := f.svc.PlanAI(d)
	if r := f.svc.ApplyAI(p.Digest, ""); r.SavedNotActive == "" {
		t.Fatalf("fixture: %+v", r)
	}
	f.svc.build = ai.Build
	revert := f.svc.PlanAIRevert()
	if r := f.svc.RetryAI(); !r.OK {
		t.Fatal(r.Error)
	}
	r := f.svc.ApplyAI(revert.Digest, "")
	if r.OK || r.Plan != nil {
		t.Fatalf("a superseded revert, or a replacement for it: %+v", r)
	}
	if f.ai.load().Model != "next-model" {
		t.Fatal("the earlier terms came back")
	}
}

// R2: Revert restores the last active terms with their own legacy key -
// not one read from the failed candidate's file.
func TestRevertKeepsTheLastActiveLegacyKey(t *testing.T) {
	legacy := strings.Replace(localFleet, "keySource: none", "keySource: legacy\n  apiKey: last-active-key", 1)
	f := newSettingsFixture(t, legacy)
	d := localDraft
	d.Model, d.KeySource = "next-model", "keychain"
	f.svc.build, _ = failingBuild(2)
	p := f.svc.PlanAI(d)
	if r := f.svc.ApplyAI(p.Digest, ""); r.SavedNotActive == "" {
		t.Fatalf("fixture: %+v", r)
	}
	f.svc.build = ai.Build
	p = f.svc.PlanAIRevert()
	if p.Refused != "" {
		t.Fatal(p.Refused)
	}
	r := f.svc.ApplyAI(p.Digest, "")
	if !r.OK || f.ai.load().Provider == nil || !strings.Contains(f.file(t), "apiKey: last-active-key") {
		t.Fatalf("revert lost the last active legacy terms: %+v", r)
	}
}

// R2, for a hosted provider whose key is required.
func TestRevertRequiresItsOriginalHostedLegacyKey(t *testing.T) {
	f := newSettingsFixture(t, baseFleet+"ai:\n  provider: anthropic\n  keySource: legacy\n  apiKey: last-active-key\n")
	if err := f.keys.Set(ai.ProviderAnthropic, "replacement-key"); err != nil {
		t.Fatal(err)
	}
	d := toDTO(f.svc.applied)
	d.KeySource = "keychain"
	f.svc.build, _ = failingBuild(2)
	p := f.svc.PlanAI(d)
	if r := f.svc.ApplyAI(p.Digest, ""); r.SavedNotActive == "" {
		t.Fatalf("fixture: %+v", r)
	}
	f.svc.build = ai.Build
	p = f.svc.PlanAIRevert()
	if p.Refused != "" {
		t.Fatal(p.Refused)
	}
	if r := f.svc.ApplyAI(p.Digest, ""); !r.OK {
		t.Fatalf("hosted Revert lost the original key: %+v", r)
	}
}

// R3: Clear writes against the version it read: an edit made since stays,
// and the revocation holds.
func TestClearCannotOverwriteAnInterveningFileEdit(t *testing.T) {
	keyed := strings.Replace(localFleet, "keySource: none", "keySource: keychain", 1)
	f := newSettingsFixture(t, keyed)
	if r := f.svc.SetAPIKey("openaiCompatible", "test-key"); !r.OK {
		t.Fatal(r.Error)
	}
	realLoad := f.svc.loadAI
	edited := strings.Replace(keyed, "model: m", "model: hand-edited", 1)
	f.svc.loadAI = func(path string) (*config.AIConfig, string, error) {
		a, v, e := realLoad(path)
		f.edit(t, edited)
		return a, v, e
	}
	r := f.svc.ClearAPIKey("openaiCompatible")
	if f.file(t) != edited || r.OK || !strings.Contains(r.Error, "left as it is") {
		t.Fatalf("Clear replaced the intervening edit: %+v\n%s", r, f.file(t))
	}
	if f.ai.load().Provider != nil {
		t.Fatal("the revocation must hold")
	}
}

// R4: a confirmed step's request continues across an AI change, and the
// plan says so apart from the verification it ends.
func TestAContinuingStepIsNotListedAsEnding(t *testing.T) {
	establish(t)
	sf := newStepsFixture(t, pauseWeb)
	rv := sf.review(1)
	if !rv.Offered {
		t.Fatal(rv.Reason)
	}
	f := newSettingsFixture(t, baseFleet+"ai:\n  provider: anthropic\n  model: m\n  baseUrl: https://api.example\n  keySource: legacy\n  apiKey: fake-key\n")
	f.svc.aiSvc = sf.aiSvc
	WireAIWork(f.svc, sf.svc)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	sf.sc.getHook = func() { once.Do(func() { close(entered); <-release }) }
	done := make(chan StepRunDTO, 1)
	go func() { done <- sf.run(1, rv.Token) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the run did not start")
	}
	d := toDTO(f.svc.applied)
	d.Model = "next-model"
	p := f.svc.PlanAI(d)
	r := f.svc.ApplyAI(p.Digest, "")
	close(release)
	if !r.OK {
		t.Fatalf("apply: %+v", r)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the run did not return")
	}
	if len(sf.sc.recorded()) == 0 {
		t.Fatal("control: the confirmed request continues")
	}
	request, verify := false, false
	for _, w := range p.Continues {
		request = request || strings.HasSuffix(w.ID, ":1:request")
	}
	for _, w := range p.Ends {
		verify = verify || strings.HasSuffix(w.ID, ":1:verify")
	}
	if !request || !verify {
		t.Fatalf("the step's request continues and its verification ends: continues %+v, ends %+v", p.Continues, p.Ends)
	}
}

// R5's backend shape: a fresh plan after a comment edit changes nothing in
// what is shown, writes nothing, and still activates.
func TestAFreshRetryPlanSaysItActivates(t *testing.T) {
	f := newSettingsFixture(t, localFleet)
	d := localDraft
	d.Model, d.KeySource = "next-model", "keychain"
	f.svc.build, _ = failingBuild(2)
	p := f.svc.PlanAI(d)
	if r := f.svc.ApplyAI(p.Digest, ""); r.SavedNotActive == "" {
		t.Fatalf("fixture: %+v", r)
	}
	f.svc.build = ai.Build
	f.edit(t, f.file(t)+"# another setting was edited\n")
	r := f.svc.RetryAI()
	if r.Plan == nil || r.Plan.Refused != "" {
		t.Fatalf("fixture: %+v", r)
	}
	p = *r.Plan
	if p.Writes || len(p.Changes) != 0 || len(p.Ends) != 0 || !p.Activates {
		t.Fatalf("the fresh plan: %+v", p)
	}
	if r := f.svc.ApplyAI(p.Digest, ""); !r.OK || f.ai.load().Provider == nil {
		t.Fatalf("it activates: %+v", r)
	}
}

// Codex's control: an inactive Revert takes the interlock, and refusing it
// changes nothing.
func TestAnInactiveRevertTakesTheRunningGrantsInterlock(t *testing.T) {
	f := newSettingsFixture(t, localFleet+"  allowProtectedEgress: true\n")
	d := localDraft
	d.Model, d.AllowProtectedEgress, d.KeySource = "next-model", true, "keychain"
	f.svc.build, _ = failingBuild(2)
	p := f.svc.PlanAI(d)
	if p.Interlock != nil {
		t.Fatal("control: the active terms already allow egress")
	}
	if r := f.svc.ApplyAI(p.Digest, ""); r.SavedNotActive == "" {
		t.Fatalf("fixture: %+v", r)
	}
	f.svc.build = ai.Build
	p = f.svc.PlanAIRevert()
	if p.Interlock == nil {
		t.Fatal("an inactive Revert restored a grant without confirming")
	}
	before, rev := f.file(t), f.ai.load().Rev
	if r := f.svc.ApplyAI(p.Digest, ""); r.OK || f.file(t) != before || f.ai.load().Rev != rev || f.ai.applyingNow() {
		t.Fatalf("a refusal before the write changed state: %+v", r)
	}
	if r := f.svc.ApplyAI(p.Digest, aiEgressPhrase); !r.OK || !f.ai.load().AllowEgress {
		t.Fatalf("confirmed: %+v", r)
	}
}

// R4: once a step's request is recorded, only its verification is left,
// and it ends.
func TestAVerifyingStepOnlyEnds(t *testing.T) {
	establish(t)
	sf := newStepsFixture(t, pauseWeb)
	prev := verifyStepHook
	verifyStepHook = func(*ChatService, *conversation, *actionPlan, int, stepSnap, ActionResultDTO) {} // the verification stays pending
	t.Cleanup(func() { verifyStepHook = prev })
	rv := sf.review(1)
	if r := sf.run(1, rv.Token); !r.Ran {
		t.Fatalf("control: %+v", r)
	}
	w := sf.svc.aiWork()
	verify := false
	for _, e := range w.ends {
		verify = verify || strings.HasSuffix(e.ID, ":1:verify")
	}
	if len(w.continues) != 0 || !verify {
		t.Fatalf("a verifying step: continues %+v, ends %+v", w.continues, w.ends)
	}
}

// The F2a re-review (Codex's probes, adopted).

func settingsForSteps(t *testing.T, sf *stepsFixture) *settingsFixture {
	t.Helper()
	f := newSettingsFixture(t, baseFleet+"ai:\n  provider: anthropic\n  model: m\n  baseUrl: https://api.example\n  keySource: legacy\n  apiKey: fake-key\n")
	f.svc.aiSvc = sf.aiSvc
	WireAIWork(f.svc, sf.svc)
	return f
}

// RR1: another plan's step with the same number is other work: the plan's
// digest moves, and the stale confirmation is refused.
func TestTheSameStepNumberInAnotherPlanIsOtherWork(t *testing.T) {
	prev := verifyStepHook
	var finishA func()
	verifyStepHook = func(s *ChatService, c *conversation, p *actionPlan, n int, _ stepSnap, _ ActionResultDTO) {
		if finishA == nil {
			finishA = func() { s.settleEffect(c, p, n, stepEffectEstablished, "") }
		}
	}
	t.Cleanup(func() { verifyStepHook = prev })
	sf := newStepsFixture(t, pauseWeb)
	if r := sf.run(1, sf.review(1).Token); !r.Ran {
		t.Fatalf("A's run: %+v", r)
	}
	// B is offered while A's step 1 still verifies
	sf.prov.turns = append(sf.prov.turns, calls(ai.ToolCall{ID: "b", Name: toolActions, Args: json.RawMessage(`{"steps":[` + scaleWeb + `]}`)}), answer("new proposal"))
	st := sf.svc.Ask("x", sf.conv, "next?", nil)
	b := st.Exchanges[len(st.Exchanges)-1].ID
	if b == sf.ex || st.Exchanges[len(st.Exchanges)-1].Plan == nil {
		t.Fatal("no distinct plan B")
	}
	f := settingsForSteps(t, sf)
	d := toDTO(f.svc.applied)
	d.Model = "next-model"
	p := f.svc.PlanAI(d)
	finishA()
	rv := sf.svc.ReviewStep("x", sf.conv, b, 1)
	if !rv.Offered {
		t.Fatalf("B's review: %+v", rv)
	}
	if r := sf.svc.RunStep("x", sf.conv, b, 1, rv.Token); !r.Ran {
		t.Fatalf("B's run: %+v", r)
	}
	now := f.svc.PlanAI(d)
	if p.Digest == now.Digest {
		t.Fatalf("A's verification became B's without the digest moving: %+v", now.Ends)
	}
	if r := f.svc.ApplyAI(p.Digest, ""); r.OK || r.Plan == nil {
		t.Fatalf("the stale confirmation applied: %+v", r)
	}
	turn := false
	for _, w := range now.Ends {
		turn = turn || strings.Contains(w.What, "proposed in turn 2")
	}
	if !turn {
		t.Fatalf("the verification names its plan's turn: %+v", now.Ends)
	}
	// the identities themselves differ, the wording apart
	verifyOf := func(ends []PlanWorkDTO) string {
		for _, w := range ends {
			if strings.HasSuffix(w.ID, ":verify") {
				return w.ID
			}
		}
		return ""
	}
	if a, b := verifyOf(p.Ends), verifyOf(now.Ends); a == "" || a == b {
		t.Fatalf("A's verification %q and B's %q", a, b)
	}
}

// RR2: a confirmed request still in flight stays under Continues after an
// AI Apply moved its conversation's revision, until it returns.
func TestAConfirmedRequestStaysVisibleAfterAnAIApply(t *testing.T) {
	establish(t)
	sf := newStepsFixture(t, pauseWeb)
	rv := sf.review(1)
	if !rv.Offered {
		t.Fatal(rv.Reason)
	}
	f := settingsForSteps(t, sf)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	sf.sc.getHook = func() { once.Do(func() { close(entered); <-release }) }
	done := make(chan StepRunDTO, 1)
	go func() { done <- sf.run(1, rv.Token) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the run did not start")
	}
	d := toDTO(f.svc.applied)
	d.Model = "next-model"
	if r := f.svc.ApplyAI(f.svc.PlanAI(d).Digest, ""); !r.OK {
		close(release)
		<-done
		t.Fatalf("the first apply: %+v", r)
	}
	d.Model = "third-model"
	second := f.svc.PlanAI(d)
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the run did not return")
	}
	if len(sf.sc.recorded()) == 0 {
		t.Fatal("control: the admitted write continued")
	}
	if len(second.Continues) != 1 || !strings.HasSuffix(second.Continues[0].ID, ":1:request") {
		t.Fatalf("the first Apply hid a request that still writes: %+v", second)
	}
	// once it returned, it is no longer work
	if w := sf.svc.aiWork(); len(w.continues) != 0 {
		t.Fatalf("a returned request is still named: %+v", w.continues)
	}
}

// RR2: a run refused before its write leaves no request behind.
func TestARefusedRunLeavesNoRequest(t *testing.T) {
	sf := newStepsFixture(t, scaleWeb)
	rv := sf.review(1)
	_ = unstructured.SetNestedField(sf.web().Object, int64(2), "spec", "replicas")
	if r := sf.run(1, rv.Token); r.Ran {
		t.Fatalf("control: %+v", r)
	}
	if w := sf.svc.aiWork(); len(w.continues) != 0 {
		t.Fatalf("a refused run is still named: %+v", w.continues)
	}
}

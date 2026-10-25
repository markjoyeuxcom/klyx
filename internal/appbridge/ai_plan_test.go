package appbridge

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/config"
)

// Settings slice F §5 and §7 (F2): the AI section's plans.

const localFleet = baseFleet + "ai:\n  provider: openaiCompatible\n  model: m\n  baseUrl: http://localhost:11434\n  keySource: none\n"

var localDraft = AISettingsDTO{Provider: "openaiCompatible", Model: "m", BaseURL: "http://localhost:11434", KeySource: "none", RefusalFallback: true}

func (f *settingsFixture) edit(t *testing.T, fleet string) {
	t.Helper()
	if err := os.WriteFile(f.path, []byte(fleet), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A hand edit turning egress on, then "Use the file's values", takes the
// interlock: the plan compares with the running grant, not the file with
// the file.
func TestUsingTheFilesValuesTakesTheInterlock(t *testing.T) {
	f := newSettingsFixture(t, localFleet)
	f.edit(t, localFleet+"  allowProtectedEgress: true\n")
	if !f.svc.GetAISettings().Diverged {
		t.Fatal("the hand edit is drift")
	}
	p := f.svc.PlanAIFromFile()
	if p.Writes || p.Interlock == nil || p.Kind != planFile {
		t.Fatalf("the file's plan: %+v", p)
	}
	if r := f.svc.ApplyAI(p.Digest, ""); r.OK || f.ai.load().AllowEgress {
		t.Fatalf("adopted without the interlock: %+v", r)
	}
	before := f.file(t)
	if r := f.svc.ApplyAI(p.Digest, aiEgressPhrase); !r.OK || !f.ai.load().AllowEgress {
		t.Fatalf("with it: %+v", r)
	}
	if f.file(t) != before || f.svc.GetAISettings().Diverged {
		t.Fatal("Use the file's values writes nothing, and the drift is gone")
	}
}

// The file, the revision or the work moving while a plan is open refuses
// its Apply with a new plan, which never inherits the confirmation.
func TestAMoveWhileAPlanIsOpenRefusesItsApply(t *testing.T) {
	d := localDraft
	d.Model = "next-model"
	t.Run("the file", func(t *testing.T) {
		f := newSettingsFixture(t, localFleet)
		p := f.svc.PlanAI(d)
		f.edit(t, localFleet+"# a hand edit\n")
		r := f.svc.ApplyAI(p.Digest, "")
		if r.OK || r.Plan == nil || r.Plan.Digest == p.Digest || strings.Contains(f.file(t), "model: next-model") {
			t.Fatalf("applied over a hand edit: %+v", r)
		}
		if r := f.svc.ApplyAI(r.Plan.Digest, ""); !r.OK {
			t.Fatalf("the new plan applies: %+v", r)
		}
	})
	t.Run("the revision", func(t *testing.T) {
		f := newSettingsFixture(t, localFleet)
		p := f.svc.PlanAI(d)
		SetAIState(f.ai, f.ai.load()) // another activation
		if r := f.svc.ApplyAI(p.Digest, ""); r.OK || r.Plan == nil {
			t.Fatalf("applied under a moved revision: %+v", r)
		}
	})
	t.Run("the work", func(t *testing.T) {
		f := newSettingsFixture(t, localFleet)
		var work []PlanWorkDTO
		f.svc.work = func() aiWorkList { return aiWorkList{ends: work} }
		p := f.svc.PlanAI(d)
		if len(p.Ends) != 0 {
			t.Fatalf("control: %+v", p.Ends)
		}
		// a turn started from a second window
		work = []PlanWorkDTO{{ID: "turn:t1", Kind: "turn", Cluster: "a", What: "a question being answered on a"}}
		r := f.svc.ApplyAI(p.Digest, "")
		if r.OK || r.Plan == nil || len(r.Plan.Ends) != 1 {
			t.Fatalf("applied over new work: %+v", r)
		}
		// Save and leave stays: the plan ends something
		q := f.svc.ApplyAIQuiet(d)
		if q.OK || q.Plan == nil || len(q.Plan.Ends) != 1 || strings.Contains(f.file(t), "model: next-model") {
			t.Fatalf("Save and leave applied a plan that ends work: %+v", q)
		}
		work = nil
		if q := f.svc.ApplyAIQuiet(d); !q.OK || !strings.Contains(f.file(t), "model: next-model") {
			t.Fatalf("Save and leave with nothing to end: %+v", q)
		}
	})
	t.Run("Save and leave never takes an interlock", func(t *testing.T) {
		f := newSettingsFixture(t, localFleet)
		on := localDraft
		on.AllowProtectedEgress = true
		if q := f.svc.ApplyAIQuiet(on); q.OK || q.Plan == nil || q.Plan.Interlock == nil || f.ai.load().AllowEgress {
			t.Fatalf("Save and leave turned egress on: %+v", q)
		}
	})
}

// Plans name the work they end, by identity, and a plan that activates
// nothing ends nothing.
func TestAPlanNamesTheWorkItEnds(t *testing.T) {
	f := newSettingsFixture(t, localFleet)
	f.svc.work = func() aiWorkList {
		return aiWorkList{ends: []PlanWorkDTO{{ID: "conv:c1", Kind: "investigation", Cluster: "a", What: "an investigation on a"}}}
	}
	done, why := f.ai.admitCall("explain", "a")
	if why != "" {
		t.Fatal(why)
	}
	d := localDraft
	d.Model = "next-model"
	p := f.svc.PlanAI(d)
	if len(p.Ends) != 2 || p.Ends[0].ID != "conv:c1" || p.Ends[1].Kind != "explain" {
		t.Fatalf("ends: %+v", p.Ends)
	}
	done()
	if p := f.svc.PlanAI(localDraft); len(p.Ends) != 0 || len(p.Changes) != 0 || p.Writes {
		t.Fatalf("an unchanged draft: %+v", p)
	}
}

// failingBuild builds normally for its first n calls, then fails as a
// locked keychain does.
func failingBuild(n int32) (func(ai.Settings, ai.KeyStore, func(string) string) (*ai.Built, error), *atomic.Int32) {
	var calls atomic.Int32
	return func(s ai.Settings, k ai.KeyStore, g func(string) string) (*ai.Built, error) {
		if calls.Add(1) > n {
			return nil, &ai.KeychainError{Err: errors.New("locked")}
		}
		return ai.Build(s, k, g)
	}, &calls
}

// Activation failing after the write: saved, not active, and AI refused
// everywhere with the reason; Retry and Revert as §5 defines them.
func TestAnActivationThatFailsIsSavedNotActive(t *testing.T) {
	d := localDraft
	d.Model = "next-model"
	saved := func(t *testing.T) *settingsFixture {
		f := newSettingsFixture(t, localFleet)
		f.svc.build, _ = failingBuild(2) // the plan and the re-plan build; activation does not
		p := f.svc.PlanAI(d)
		r := f.svc.ApplyAI(p.Digest, "")
		if r.OK || r.SavedNotActive == "" || !strings.Contains(f.file(t), "model: next-model") {
			t.Fatalf("the failed activation: %+v", r)
		}
		st := f.svc.GetAISettings()
		if st.SavedNotActive == "" || st.Diverged || st.Active.Ready {
			t.Fatalf("the state: %+v", st)
		}
		ex := NewAIService(func(string) (AIConn, bool) { return podConn(), true }, nil, func(string) bool { return false }, nil)
		ex.state.Store(f.ai.load())
		if r := ex.Explain("a", rowTarget()); r.OK || !strings.Contains(r.Error, "Saved, not active") {
			t.Fatalf("AI is refused everywhere: %+v", r)
		}
		return f
	}
	t.Run("Retry with unchanged terms activates", func(t *testing.T) {
		f := saved(t)
		f.svc.build = ai.Build
		if r := f.svc.RetryAI(); !r.OK || f.ai.load().Model != "next-model" || f.svc.GetAISettings().SavedNotActive != "" {
			t.Fatalf("retry: %+v", r)
		}
	})
	t.Run("Retry after a file edit widening egress is refused with a fresh plan", func(t *testing.T) {
		f := saved(t)
		f.svc.build = ai.Build
		f.edit(t, baseFleet+"ai:\n  provider: openaiCompatible\n  model: next-model\n  baseUrl: http://10.0.0.9:11434\n  keySource: none\n  allowProtectedEgress: true\n")
		r := f.svc.RetryAI()
		if r.OK || r.Plan == nil || r.Plan.Interlock == nil || r.Plan.Kind != planFile {
			t.Fatalf("retry over an edit: %+v", r)
		}
		if f.ai.load().AllowEgress || f.ai.load().Provider != nil {
			t.Fatal("the edit's terms activated")
		}
	})
	t.Run("Retry after the AI settings moved is refused", func(t *testing.T) {
		f := saved(t)
		f.svc.build = ai.Build
		SetAIState(f.ai, f.ai.load())
		if r := f.svc.RetryAI(); r.OK || r.Plan == nil {
			t.Fatalf("retry under a moved revision: %+v", r)
		}
	})
	t.Run("Revert", func(t *testing.T) {
		f := saved(t)
		f.svc.build = ai.Build
		p := f.svc.PlanAIRevert()
		if p.Refused != "" || !p.Writes || p.Kind != planRevert {
			t.Fatalf("revert plan: %+v", p)
		}
		if r := f.svc.ApplyAI(p.Digest, ""); !r.OK || f.ai.load().Model != "m" || !strings.Contains(f.file(t), "model: m") {
			t.Fatalf("revert: %+v", r)
		}
		if st := f.svc.GetAISettings(); st.SavedNotActive != "" || st.Diverged {
			t.Fatalf("after revert: %+v", st)
		}
	})
}

// A refusal before the write changes nothing: the gate is released, nothing
// is published, and nothing becomes saved-not-active (§11).
func TestARefusalBeforeTheWriteChangesNothing(t *testing.T) {
	f := newSettingsFixture(t, localFleet)
	d := localDraft
	d.Model = "next-model"
	p := f.svc.PlanAI(d)
	f.edit(t, localFleet+"# moved\n")
	rev := f.ai.load().Rev
	if r := f.svc.ApplyAI(p.Digest, ""); r.OK {
		t.Fatal("applied")
	}
	if f.ai.load().Rev != rev || f.ai.applyingNow() || f.svc.GetAISettings().SavedNotActive != "" {
		t.Fatal("a refused Apply changed something")
	}
}

// While an Apply decides, no new AI work starts.
func TestNoNewWorkStartsWhileAnApplyDecides(t *testing.T) {
	f := newSettingsFixture(t, localFleet)
	f.ai.beginApply()
	if _, why := f.ai.admitCall("explain", "a"); why != aiApplying {
		t.Fatalf("an Explain started: %q", why)
	}
	f.ai.endApply()
	done, why := f.ai.admitCall("explain", "a")
	if why != "" {
		t.Fatal(why)
	}
	done()

	cf := newToolsFixture(t, answer("hello"))
	cf.aiSvc.beginApply()
	if st := cf.svc.Start("x"); st.ConversationID != "" || st.Error != aiApplying {
		t.Fatalf("a conversation started: %+v", st)
	}
	cf.aiSvc.endApply()
	st := cf.svc.Start("x")
	if st.ConversationID == "" {
		t.Fatalf("control: %+v", st)
	}
	cf.aiSvc.beginApply()
	if got := cf.svc.Ask("x", st.ConversationID, "how is it?", nil); got.Notice != aiApplying {
		t.Fatalf("a turn started: %+v", got.Notice)
	}
	cf.aiSvc.endApply()
	// the chat names its conversation as work
	work := cf.svc.aiWork().ends
	if len(work) != 1 || work[0].ID != "conv:"+st.ConversationID || work[0].Started == "" {
		t.Fatalf("work: %+v", work)
	}
}

// The credential actions with a divergent file (§5, R5).
func TestTheCredentialActionsUseTheRunningTerms(t *testing.T) {
	keyed := baseFleet + "ai:\n  provider: openaiCompatible\n  model: m\n  baseUrl: http://localhost:11434\n  keySource: keychain\n"
	t.Run("SetAPIKey activates the running terms, not the edited egress", func(t *testing.T) {
		f := newSettingsFixture(t, keyed)
		f.edit(t, keyed+"  allowProtectedEgress: true\n")
		edited := f.file(t)
		if r := f.svc.SetAPIKey("openaiCompatible", "local-key"); !r.OK {
			t.Fatalf("set: %+v", r)
		}
		st := f.ai.load()
		if st.Provider == nil || st.AllowEgress {
			t.Fatalf("the hand-edited egress activated: %+v", st)
		}
		if f.file(t) != edited || !f.svc.GetAISettings().Diverged {
			t.Fatal("the file differs beyond the key: it stays drift, unwritten")
		}
	})
	t.Run("MoveKeyToKeychain refuses a divergent file", func(t *testing.T) {
		legacy := baseFleet + "ai:\n  provider: openaiCompatible\n  model: m\n  baseUrl: http://localhost:11434\n  keySource: legacy\n  apiKey: plain-key\n"
		f := newSettingsFixture(t, legacy)
		f.edit(t, strings.Replace(legacy, "model: m", "model: other", 1))
		r := f.svc.MoveKeyToKeychain()
		if r.OK || !strings.Contains(r.Error, "differ from the ones in use") {
			t.Fatalf("moved over a divergent file: %+v", r)
		}
		if _, err := f.keys.Get(ai.ProviderOpenAICompatible); err == nil {
			t.Fatal("the key moved")
		}
	})
	t.Run("an in-flight call is refused after the move", func(t *testing.T) {
		f := newSettingsFixture(t, keyed)
		_ = f.keys.Set(ai.ProviderOpenAICompatible, "k1")
		SetAIState(f.ai, BuildAIState(&config.AIConfig{Provider: "openaiCompatible", Model: "m", BaseURL: "http://localhost:11434", KeySource: "keychain"}, f.keys, nil))
		f.svc.applied = normalizedAI(&config.AIConfig{Provider: "openaiCompatible", Model: "m", BaseURL: "http://localhost:11434", KeySource: "keychain"})
		installRegistry(t, false, "c")
		fence, why := f.ai.callFence(f.ai.load(), "c")
		if why != "" || fence() != nil {
			t.Fatalf("control: %q", why)
		}
		if r := f.svc.SetAPIKey("openaiCompatible", "k2"); !r.OK {
			t.Fatalf("set: %+v", r)
		}
		if err := fence(); err == nil || !strings.Contains(err.Error(), "AI settings changed") {
			t.Fatalf("an in-flight call continued: %v", err)
		}
	})
}

// The candidate cache: bounded by entries and bytes, expiring, consumed.
func TestThePlanCacheIsBounded(t *testing.T) {
	now := time.Now()
	c := newPlanCache(func() time.Time { return now })
	for i := range planCacheEntries + 4 {
		c.put(string(rune('a'+i)), i)
	}
	if len(c.entries) != planCacheEntries {
		t.Fatalf("%d entries", len(c.entries))
	}
	if _, ok := c.get("a"); ok {
		t.Fatal("the oldest entry was kept")
	}
	big := strings.Repeat("x", planCacheBytes/2)
	c.put("b1", big)
	c.put("b2", big)
	if c.size > planCacheBytes {
		t.Fatalf("%d bytes", c.size)
	}
	if _, ok := c.get("b1"); ok {
		t.Fatal("over the byte bound, the older entry stays")
	}
	c.put("huge", strings.Repeat("x", planCacheBytes+1))
	if _, ok := c.get("huge"); ok {
		t.Fatal("a value over the whole budget was kept")
	}
	c.put("x", 1)
	now = now.Add(planCacheTTL)
	if _, ok := c.get("x"); ok {
		t.Fatal("an expired entry was returned")
	}
	c.put("y", 1)
	c.consume("y")
	if _, ok := c.get("y"); ok {
		t.Fatal("a consumed entry was returned")
	}
}

// A plan never carries a key: not in its changes, not in what it binds.
func TestAPlanCarriesNoKey(t *testing.T) {
	legacy := baseFleet + "ai:\n  provider: openaiCompatible\n  model: m\n  baseUrl: http://localhost:11434\n  keySource: legacy\n  apiKey: plain-key-123\n"
	f := newSettingsFixture(t, legacy)
	d := f.svc.GetAISettings().Persisted
	d.Model = "next-model"
	p := f.svc.PlanAI(d)
	raw, _ := json.Marshal(p)
	cached, _ := f.svc.cache.get(p.Digest)
	held, _ := json.Marshal(cached)
	if strings.Contains(string(raw), "plain-key-123") || strings.Contains(string(held), "plain-key-123") {
		t.Fatalf("a key in the plan or the cache: %s %s", raw, held)
	}
	if r := f.svc.ApplyAI(p.Digest, ""); !r.OK || !strings.Contains(f.file(t), "apiKey: plain-key-123") {
		t.Fatalf("the legacy key is kept through the file: %+v", r)
	}
}

// A hand edit between the re-plan and the write refuses the write: the
// write carries the plan's file version (§5).
func TestAnEditBetweenThePlanAndTheWriteRefusesIt(t *testing.T) {
	f := newSettingsFixture(t, localFleet)
	d := localDraft
	d.Model = "next-model"
	p := f.svc.PlanAI(d)
	f.svc.saveAI = func(path, version string, a config.AIConfig) (string, error) {
		f.edit(t, localFleet+"# edited while applying\n")
		return config.SaveAIAt(path, version, a)
	}
	r := f.svc.ApplyAI(p.Digest, "")
	if r.OK || r.Plan == nil || !strings.Contains(f.file(t), "# edited while applying") || strings.Contains(f.file(t), "next-model") {
		t.Fatalf("written over an edit: %+v\n%s", r, f.file(t))
	}
	if f.ai.load().Model == "next-model" {
		t.Fatal("activated after a refused write")
	}
}

// While an Apply decides - here, while it writes - new work is refused.
func TestNewWorkIsRefusedDuringAnApply(t *testing.T) {
	f := newSettingsFixture(t, localFleet)
	d := localDraft
	d.Model = "next-model"
	p := f.svc.PlanAI(d)
	why := ""
	f.svc.saveAI = func(path, version string, a config.AIConfig) (string, error) {
		_, why = f.ai.admitCall("explain", "a")
		return config.SaveAIAt(path, version, a)
	}
	if r := f.svc.ApplyAI(p.Digest, ""); !r.OK {
		t.Fatalf("apply: %+v", r)
	}
	if why != aiApplying {
		t.Fatalf("an Explain started during the Apply: %q", why)
	}
	if f.ai.applyingNow() {
		t.Fatal("the gate stayed after the Apply")
	}
}

// Every new-work entry point refuses while an Apply decides: Explain,
// Suggest-fix, a comparison set, and a step's review and run.
func TestEveryEntryPointRefusesWhileAnApplyDecides(t *testing.T) {
	installRegistry(t, false, "c", "x", "stg")
	conn := podConn()
	s := NewAIService(func(string) (AIConn, bool) { return conn, true }, &AIState{Provider: &fencedProvider{}, ProviderID: ai.ProviderAnthropic, Origin: ai.DefaultAnthropicOrigin}, func(string) bool { return false }, nil)
	s.beginApply()
	if r := s.Explain("c", rowTarget()); r.OK || r.Error != aiApplying {
		t.Fatalf("Explain: %+v", r)
	}
	if r := s.SuggestFix("c", "apps", "v1", "deployments", "app", "api", "api-uid"); r.OK || r.Error != aiApplying {
		t.Fatalf("Suggest-fix: %+v", r)
	}
	s.endApply()

	ff := newFleetFixture(t, answer("done"))
	inv := ff.svc.StartInvestigation("x", podAnchor)
	ff.svc.ai.beginApply()
	if st := ff.svc.SetComparison("x", inv.ConversationID, []string{"stg"}); st.Notice != aiApplying {
		t.Fatalf("a comparison set: %+v", st.Notice)
	}
	ff.svc.ai.endApply()

	sf := newStepsFixture(t, pauseWeb)
	sf.aiSvc.beginApply()
	if r := sf.review(1); r.Offered || r.Reason != aiApplying {
		t.Fatalf("a review: %+v", r)
	}
	sf.aiSvc.endApply()
	rv := sf.review(1)
	if !rv.Offered {
		t.Fatalf("control: %+v", rv)
	}
	sf.aiSvc.beginApply()
	if r := sf.run(1, rv.Token); r.Ran || r.Reason != aiApplying {
		t.Fatalf("a run: %+v", r)
	}
	sf.aiSvc.endApply()
}

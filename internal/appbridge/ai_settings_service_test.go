package appbridge

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/config"
)

// memKeys is a mutable in-memory ai.KeyStore; failSet/failGet simulate a
// locked keychain.
type memKeys struct {
	mu      sync.Mutex
	keys    map[ai.ProviderID]string
	failSet error
	failGet error
}

func newMemKeys() *memKeys { return &memKeys{keys: map[ai.ProviderID]string{}} }

func (m *memKeys) Get(p ai.ProviderID) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet != nil {
		return "", m.failGet
	}
	if k, ok := m.keys[p]; ok {
		return k, nil
	}
	return "", ai.ErrNoKey
}
func (m *memKeys) Set(p ai.ProviderID, k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSet != nil {
		return m.failSet
	}
	m.keys[p] = k
	return nil
}
func (m *memKeys) Delete(p ai.ProviderID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, p)
	return nil
}

// settingsFixture is a fleet file, a keystore, the AI service and its writer,
// wired as main.go wires them.
type settingsFixture struct {
	path string
	keys *memKeys
	env  map[string]string
	ai   *AIService
	svc  *AISettingsService
}

func newSettingsFixture(t *testing.T, fleet string) *settingsFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(fleet), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &settingsFixture{path: path, keys: newMemKeys(), env: map[string]string{}}
	getenv := func(k string) string { return f.env[k] }
	f.ai = NewAIService(func(string) (AIConn, bool) { return nil, false }, BuildAIState(cfg.AI, f.keys, getenv), func(string) bool { return true }, nil)
	f.svc = NewAISettingsService(path, f.keys, getenv, f.ai, cfg.AI)
	return f
}

func (f *settingsFixture) file(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const baseFleet = "clusters:\n  - name: a\n"

func TestAISettings_SaveAppliesAndPersists(t *testing.T) {
	f := newSettingsFixture(t, baseFleet)
	rev := f.ai.load().Rev
	r := f.svc.SaveAISettings(AISettingsDTO{Provider: "openaiCompatible", Model: "llama3.1", BaseURL: "http://localhost:11434", KeySource: "none"})
	if !r.OK {
		t.Fatalf("save: %+v", r)
	}
	if !strings.Contains(f.file(t), "provider: openaiCompatible") {
		t.Fatalf("not persisted:\n%s", f.file(t))
	}
	st := f.ai.load()
	if st.Provider == nil || st.Origin != "http://localhost:11434" || !st.OnMachine || st.Rev == rev {
		t.Fatalf("not active: %+v", st)
	}
	if got := f.svc.GetAISettings(); got.Diverged || got.Persisted.Provider != "openaiCompatible" {
		t.Fatalf("state after save: %+v", got)
	}
}

// Spec §2.3: a setting that cannot be built writes nothing and keeps the
// active provider.
func TestAISettings_UnbuildableSaveWritesNothing(t *testing.T) {
	f := newSettingsFixture(t, baseFleet)
	before, rev := f.file(t), f.ai.load().Rev
	r := f.svc.SaveAISettings(AISettingsDTO{Provider: "anthropic", KeySource: "keychain"})
	if r.OK || !strings.Contains(r.Error, "No API key") {
		t.Fatalf("want a no-key refusal, got %+v", r)
	}
	if f.file(t) != before || f.ai.load().Rev != rev {
		t.Fatal("a refused save wrote the file or swapped the provider")
	}
}

// Spec §2.3: turning protected egress on is authorized at the backend by a
// one-shot, expiring grant - not only by the dialog.
func TestAISettings_EgressNeedsTheBackendGrant(t *testing.T) {
	f := newSettingsFixture(t, baseFleet)
	now := time.Now()
	f.svc.now = func() time.Time { return now }
	on := AISettingsDTO{Provider: "openaiCompatible", Model: "m", BaseURL: "http://192.168.1.10:11434", KeySource: "none", AllowProtectedEgress: true}

	if r := f.svc.SaveAISettings(on); r.OK || !strings.Contains(r.Error, "typed confirmation") {
		t.Fatalf("without a grant: %+v", r)
	}
	if r := f.svc.ArmAIEgress("allow"); r.OK {
		t.Fatal("a wrong phrase must not arm")
	}
	if r := f.svc.ArmAIEgress("allow protected egress"); !r.OK {
		t.Fatalf("arm: %+v", r)
	}
	if r := f.svc.SaveAISettings(on); !r.OK {
		t.Fatalf("with a grant: %+v", r)
	}
	if !f.ai.load().AllowEgress {
		t.Fatal("egress not active")
	}
	// Off needs no grant; on again needs a fresh one (one-shot).
	off := on
	off.AllowProtectedEgress = false
	if r := f.svc.SaveAISettings(off); !r.OK {
		t.Fatalf("off: %+v", r)
	}
	if r := f.svc.SaveAISettings(on); r.OK {
		t.Fatal("the grant must be one-shot")
	}
	// An expired grant is refused.
	f.svc.ArmAIEgress("allow protected egress")
	now = now.Add(aiEgressGrantTTL + time.Second)
	if r := f.svc.SaveAISettings(on); r.OK {
		t.Fatal("an expired grant must be refused")
	}
}

// Spec §2.1: setting a key for the selected provider moves it to the keychain
// source and rebuilds, so the key the panel describes is the key in use; a
// legacy plain-text key leaves the file.
func TestAISettings_SetKeyForTheSelectedProvider(t *testing.T) {
	f := newSettingsFixture(t, baseFleet+"ai:\n  backend: hosted\n  apiKey: sk-legacy-plain\n")
	if r := f.svc.SetAPIKey("anthropic", "  sk-ant-new-key  "); !r.OK {
		t.Fatalf("set: %+v", r)
	}
	if got := f.file(t); strings.Contains(got, "sk-legacy-plain") || !strings.Contains(got, "keySource: keychain") {
		t.Fatalf("file after set:\n%s", got)
	}
	if k, _ := f.keys.Get(ai.ProviderAnthropic); k != "sk-ant-new-key" {
		t.Fatalf("keychain holds %q", k)
	}
	if st := f.ai.load(); st.Provider == nil || st.KeySource != ai.KeySourceKeychain {
		t.Fatalf("active not rebuilt from the keychain: %+v", st)
	}
	// A key for another provider is only stored.
	before := f.file(t)
	if r := f.svc.SetAPIKey("openai", "sk-openai-other"); !r.OK || f.file(t) != before {
		t.Fatalf("another provider's key changed the file: %+v", r)
	}
	if r := f.svc.SetAPIKey("claudeCLI", "x"); r.OK {
		t.Fatal("a CLI provider takes no key")
	}
}

// Spec §2.1: clearing the selected provider's key sets the source to none -
// AI turns unavailable and an environment key does not take over.
func TestAISettings_ClearNeverReactivatesAnotherSource(t *testing.T) {
	f := newSettingsFixture(t, baseFleet+"ai:\n  provider: anthropic\n  keySource: keychain\n  apiKeyEnv: ANTHROPIC_API_KEY\n")
	f.env["ANTHROPIC_API_KEY"] = "sk-from-env"
	_ = f.keys.Set(ai.ProviderAnthropic, "sk-ant-k")
	SetAIState(f.ai, BuildAIState(&config.AIConfig{Provider: "anthropic", KeySource: "keychain"}, f.keys, nil))

	if r := f.svc.ClearAPIKey("anthropic"); !r.OK {
		t.Fatalf("clear: %+v", r)
	}
	if !strings.Contains(f.file(t), "keySource: none") {
		t.Fatalf("source not set to none:\n%s", f.file(t))
	}
	st := f.ai.load()
	if st.Provider != nil || !strings.Contains(st.Problem, "No API key") {
		t.Fatalf("after clear AI must be unavailable, not on the env key: %+v", st)
	}
}

// Spec §2.1: the move writes the keychain first; a failed keychain write
// changes nothing, and a failed removal from fleet.yaml is reported with the
// source still legacy.
func TestAISettings_MoveKeyToKeychain(t *testing.T) {
	legacy := baseFleet + "ai:\n  backend: hosted\n  apiKey: sk-legacy-plain\n"

	t.Run("keychain write fails", func(t *testing.T) {
		f := newSettingsFixture(t, legacy)
		f.keys.failSet = &ai.KeychainError{Err: errors.New("locked")}
		r := f.svc.MoveKeyToKeychain()
		if r.OK || !strings.Contains(r.Error, "Nothing moved") || f.file(t) != legacy {
			t.Fatalf("got %+v, file:\n%s", r, f.file(t))
		}
	})
	t.Run("fleet.yaml write fails", func(t *testing.T) {
		f := newSettingsFixture(t, legacy)
		f.svc.saveAI = func(string, config.AIConfig) error { return errors.New("disk full") }
		r := f.svc.MoveKeyToKeychain()
		if r.OK || !strings.Contains(r.Error, "still holds it in plain text") {
			t.Fatalf("got %+v", r)
		}
		if strings.Contains(r.Error, "sk-legacy-plain") {
			t.Fatal("the error leaks the key")
		}
		if k, _ := f.keys.Get(ai.ProviderAnthropic); k != "sk-legacy-plain" {
			t.Fatal("the keychain copy should exist")
		}
		if got := f.svc.GetAISettings(); got.Persisted.KeySource != "legacy" || !got.LegacyKeyInFile {
			t.Fatalf("the source must stay legacy: %+v", got.Persisted)
		}
	})
	t.Run("moves", func(t *testing.T) {
		f := newSettingsFixture(t, legacy)
		if r := f.svc.MoveKeyToKeychain(); !r.OK {
			t.Fatalf("move: %+v", r)
		}
		if got := f.file(t); strings.Contains(got, "sk-legacy-plain") || !strings.Contains(got, "keySource: keychain") {
			t.Fatalf("file:\n%s", got)
		}
	})
}

// Spec §2.3: an outside edit makes persisted and active diverge visibly.
func TestAISettings_DivergenceIsReported(t *testing.T) {
	f := newSettingsFixture(t, baseFleet+"ai:\n  provider: openaiCompatible\n  model: a\n  keySource: none\n")
	if f.svc.GetAISettings().Diverged {
		t.Fatal("fresh start must not diverge")
	}
	if err := os.WriteFile(f.path, []byte(baseFleet+"ai:\n  provider: openaiCompatible\n  model: b\n  keySource: none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !f.svc.GetAISettings().Diverged {
		t.Fatal("an outside edit must be reported as diverged")
	}
}

// The state DTO never carries a key, and a keychain failure is reported as
// such rather than as "no key".
func TestAISettings_StateCarriesNoKey(t *testing.T) {
	f := newSettingsFixture(t, baseFleet+"ai:\n  backend: hosted\n  apiKey: sk-legacy-plain\n")
	_ = f.keys.Set(ai.ProviderOpenAI, "sk-openai-stored")
	st := f.svc.GetAISettings()
	raw, _ := json.Marshal(st)
	for _, leak := range []string{"sk-legacy-plain", "sk-openai-stored"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("state leaks %q: %s", leak, raw)
		}
	}
	if !st.KeysStored["openai"] || st.KeysStored["gemini"] || !st.LegacyKeyInFile {
		t.Fatalf("state: %+v", st)
	}
	f.keys.failGet = &ai.KeychainError{Err: errors.New("user interaction is not allowed")}
	if st := f.svc.GetAISettings(); !strings.Contains(st.KeychainError, "keychain unavailable") {
		t.Fatalf("keychain error not reported: %+v", st)
	}
}

// Test and the model list use the unsaved draft and send no cluster data.
func TestAISettings_TestAndModelsUseTheDraft(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"llama3.1"},{"id":"qwen3"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"model":"llama3.1","choices":[{"finish_reason":"stop","message":{"content":"ready"}}]}`)
	}))
	defer srv.Close()
	f := newSettingsFixture(t, baseFleet)
	draft := AISettingsDTO{Provider: "openaiCompatible", Model: "llama3.1", BaseURL: srv.URL, KeySource: "none"}

	if m := f.svc.ListModels(draft); m.Error != "" || len(m.Models) != 2 {
		t.Fatalf("models: %+v", m)
	}
	res := f.svc.TestProvider(draft)
	if !res.OK || res.Reply != "ready" || res.Origin != srv.URL {
		t.Fatalf("test: %+v", res)
	}
	if strings.Contains(f.file(t), "openaiCompatible") {
		t.Fatal("Test and ListModels must not save the draft")
	}
	if len(bodies) != 2 || !strings.Contains(bodies[1], "connectivity check") {
		t.Fatalf("unexpected request bodies: %v", bodies)
	}
}

// Codex review of c488e34 (P1): once the running provider's keychain key is
// deleted, new calls stop using it - even when fleet.yaml cannot be read or
// written - and the failed file update shows as divergence. The decision
// follows the running snapshot, not the file.
func TestAISettings_ClearStopsTheRunningProviderWhateverTheFile(t *testing.T) {
	keyed := baseFleet + "ai:\n  provider: openaiCompatible\n  model: m\n  baseUrl: http://localhost:11434\n  keySource: keychain\n"
	setup := func(t *testing.T, fleet string) *settingsFixture {
		f := newSettingsFixture(t, fleet)
		_ = f.keys.Set(ai.ProviderOpenAICompatible, "local-key-123")
		SetAIState(f.ai, BuildAIState(&config.AIConfig{Provider: "openaiCompatible", Model: "m", BaseURL: "http://localhost:11434", KeySource: "keychain"}, f.keys, nil))
		if f.ai.load().Provider == nil {
			t.Fatal("fixture: the running provider must be up")
		}
		return f
	}

	t.Run("fleet.yaml unreadable", func(t *testing.T) {
		f := setup(t, keyed)
		f.svc.loadAI = func(string) (*config.AIConfig, error) { return nil, errors.New("permission denied") }
		r := f.svc.ClearAPIKey("openaiCompatible")
		if r.OK || !strings.Contains(r.Error, "no longer uses it") {
			t.Fatalf("got %+v", r)
		}
		if st := f.ai.load(); st.Provider != nil || !strings.Contains(st.Problem, "removed") {
			t.Fatalf("the running provider must stop: %+v", st)
		}
	})
	t.Run("fleet.yaml write fails", func(t *testing.T) {
		f := setup(t, keyed)
		f.svc.saveAI = func(string, config.AIConfig) error { return errors.New("disk full") }
		r := f.svc.ClearAPIKey("openaiCompatible")
		if r.OK || !strings.Contains(r.Error, "was not updated") {
			t.Fatalf("got %+v", r)
		}
		if f.ai.load().Provider != nil {
			t.Fatal("the running provider must stop")
		}
		if !f.svc.GetAISettings().Diverged {
			t.Fatal("the unwritten file must show as divergence")
		}
	})
	t.Run("file selects another provider", func(t *testing.T) {
		f := setup(t, baseFleet+"ai:\n  provider: openai\n  model: m\n  keySource: keychain\n")
		before := f.file(t)
		if r := f.svc.ClearAPIKey("openaiCompatible"); !r.OK {
			t.Fatalf("got %+v", r)
		}
		if f.ai.load().Provider != nil {
			t.Fatal("the running provider used the deleted key; it must stop")
		}
		if f.file(t) != before {
			t.Fatal("the file selects another provider; it must not be rewritten")
		}
	})
	t.Run("another provider's key", func(t *testing.T) {
		f := setup(t, keyed)
		_ = f.keys.Set(ai.ProviderOpenAI, "sk-openai")
		if r := f.svc.ClearAPIKey("openai"); !r.OK || f.ai.load().Provider == nil {
			t.Fatalf("clearing an unused key must not stop the running provider: %+v", r)
		}
	})
}

// Codex review of c488e34 (P2): the model list needs no model, so first-time
// setup is not circular; Test and Save still require one.
func TestAISettings_ModelsListWithoutAModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"llama3.1"}]}`)
	}))
	defer srv.Close()
	f := newSettingsFixture(t, baseFleet)
	draft := AISettingsDTO{Provider: "openaiCompatible", BaseURL: srv.URL, KeySource: "none"}
	if m := f.svc.ListModels(draft); m.Error != "" || len(m.Models) != 1 {
		t.Fatalf("models without a model: %+v", m)
	}
	if res := f.svc.TestProvider(draft); res.OK || !strings.Contains(res.Error, "model") {
		t.Fatalf("Test still needs a model: %+v", res)
	}
	if r := f.svc.SaveAISettings(draft); r.OK {
		t.Fatal("Save still needs a model")
	}
}

// AI A3 §6: the panel's setting reaches the AI state, and a draft that
// does not carry it keeps the current value.
func TestAISettings_ProposeActions(t *testing.T) {
	f := newSettingsFixture(t, baseFleet)
	if f.ai.load().ProposalsOff {
		t.Fatal("proposals are on by default")
	}
	off := false
	base := AISettingsDTO{Provider: "openaiCompatible", Model: "llama3.1", BaseURL: "http://localhost:11434", KeySource: "none"}
	if r := f.svc.SaveAISettings(base); !r.OK || f.ai.load().ProposalsOff { // a draft without the field, on
		t.Fatalf("a draft without the setting keeps it on: %+v", r)
	}
	d := base
	d.ProposeActions = &off
	if r := f.svc.SaveAISettings(d); !r.OK {
		t.Fatal(r)
	}
	if !f.ai.load().ProposalsOff || !strings.Contains(f.file(t), "proposeActions: false") {
		t.Fatalf("off: %+v", f.ai.load())
	}
	if got := f.svc.GetAISettings().Persisted.ProposeActions; got == nil || *got {
		t.Fatalf("the panel reads it back: %v", got)
	}
	if r := f.svc.SaveAISettings(base); !r.OK { // a draft without the field
		t.Fatal(r)
	}
	if !f.ai.load().ProposalsOff {
		t.Fatal("a draft that does not carry the setting kept it off")
	}
	on := true
	d.ProposeActions = &on
	if r := f.svc.SaveAISettings(d); !r.OK || f.ai.load().ProposalsOff {
		t.Fatalf("on again: %+v", r)
	}
}

package appbridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/config"
)

// aiEgressPhrase is what the operator types to turn on protected-cluster
// egress (spec 2026-10-13 §2.3): the AI plan's interlock (Settings slice F
// §5).
const aiEgressPhrase = "allow protected egress"

// aiKeyMax bounds a pasted key.
const aiKeyMax = 4096

// keyedProviders are the providers that take an API key.
var keyedProviders = []ai.ProviderID{ai.ProviderAnthropic, ai.ProviderOpenAI, ai.ProviderGemini, ai.ProviderOpenAICompatible}

// AISettingsDTO is the editable, non-secret AI settings (fleet.yaml ai:).
type AISettingsDTO struct {
	Provider             string `json:"provider"`
	Model                string `json:"model"`
	KeySource            string `json:"keySource"`
	APIKeyEnv            string `json:"apiKeyEnv"`
	BaseURL              string `json:"baseUrl"`
	AllowProtectedEgress bool   `json:"allowProtectedEgress"`
	TLSSkipVerify        bool   `json:"tlsSkipVerify"`
	RefusalFallback      bool   `json:"refusalFallback"`
	CLIPath              string `json:"cliPath"` // claudeCLI: an explicit binary; empty finds claude
	// ProposeActions is AI A3 §6's setting; nil (a draft that does not
	// carry it) keeps the current value, never turns proposals off
	ProposeActions *bool `json:"proposeActions,omitempty"`
}

// AISettingsStateDTO is everything the Settings > AI panel shows. It never
// carries a key: only whether one is stored, and where.
type AISettingsStateDTO struct {
	Path            string            `json:"path"`
	Persisted       AISettingsDTO     `json:"persisted"`
	PersistedError  string            `json:"persistedError,omitempty"`
	LegacyKeyInFile bool              `json:"legacyKeyInFile"`
	KeysStored      map[string]bool   `json:"keysStored"`
	KeychainError   string            `json:"keychainError,omitempty"`
	Active          AIAvailabilityDTO `json:"active"`
	ActiveRev       uint64            `json:"activeRev"`
	// Diverged: the settings in fleet.yaml are not the ones in use (an
	// outside edit); "Use the file's values" plans them (Settings slice F
	// §5).
	Diverged bool `json:"diverged"`
	// SavedNotActive: the settings were saved and their activation failed,
	// with why; Retry and Revert are offered.
	SavedNotActive string `json:"savedNotActive,omitempty"`
}

// AIModelsDTO is a provider's model list for the picker.
type AIModelsDTO struct {
	Models []ai.ModelInfo `json:"models"`
	Error  string         `json:"error,omitempty"`
}

// AITestDTO is the result of a Test: a tiny completion with no cluster data.
type AITestDTO struct {
	OK     bool   `json:"ok"`
	Model  string `json:"model,omitempty"`
	Origin string `json:"origin,omitempty"`
	Reply  string `json:"reply,omitempty"`
	Error  string `json:"error,omitempty"`
}

// AISettingsService is the Wails-bound AI settings writer (spec 2026-10-13
// §2.3; Settings slice F §5). A change is a plan from what runs, applied by
// its digest: the block is written under the shared fleet write lock and
// the file version the plan was made from, then the provider is built and
// published. Every activation route - Apply, Retry, the credential actions
// - holds one lock and the AI section's Applying state, so an older
// publication never lands after a newer one.
type AISettingsService struct {
	path   string
	keys   ai.KeyStore
	getenv func(string) string
	aiSvc  *AIService
	now    func() time.Time
	saveAI func(path, version string, a config.AIConfig) (string, error)          // seam: config.SaveAIAt
	loadAI func(string) (*config.AIConfig, string, error)                         // seam: the block and its file version
	build  func(ai.Settings, ai.KeyStore, func(string) string) (*ai.Built, error) // seam: ai.Build
	work   func() []PlanWorkDTO                                                   // the chat's AI work (WireAIWork); nil: none
	cache  *planCache

	mu      sync.Mutex
	applied config.AIConfig // the last terms activated
	// notActive is a saved block whose activation failed: nothing runs
	notActive *aiNotActive
}

// aiNotActive is a saved, not active candidate: never holding a key.
type aiNotActive struct {
	cand      config.AIConfig // without a legacy key
	legacyKey bool
	version   string // the file version it was saved as
	rev       uint64 // the AI revision its failure published
	reason    string
}

// NewAISettingsService wires the writer. initial is the ai block the startup
// snapshot was built from (nil = off).
func NewAISettingsService(path string, keys ai.KeyStore, getenv func(string) string, aiSvc *AIService, initial *config.AIConfig) *AISettingsService {
	s := &AISettingsService{path: path, keys: keys, getenv: getenv, aiSvc: aiSvc, now: time.Now, saveAI: config.SaveAIAt, loadAI: loadAIBlock, build: ai.Build}
	s.cache = newPlanCache(func() time.Time { return s.now() })
	s.applied = normalizedAI(initial)
	return s
}

// WireAIWork gives the writer the chat's AI work, which a plan names as
// what it ends. A package function: every exported method of a bound
// service reaches the webview.
func WireAIWork(s *AISettingsService, chat *ChatService) {
	s.work = chat.aiWork
}

// loadAIBlock reads the ai block currently in fleet.yaml, with the file's
// version from the same read.
func loadAIBlock(path string) (*config.AIConfig, string, error) {
	cfg, version, err := config.LoadVersioned(path)
	if err != nil {
		return nil, version, err
	}
	return cfg.AI, version, nil
}

// normalizedAI is a with defaults applied (nil = off), for comparison.
func normalizedAI(a *config.AIConfig) config.AIConfig {
	var c config.AIConfig
	if a != nil {
		c = *a
	}
	c.ApplyDefaults()
	return c
}

func toDTO(a config.AIConfig) AISettingsDTO {
	return AISettingsDTO{
		Provider: a.Provider, Model: a.Model, KeySource: a.KeySource, APIKeyEnv: a.APIKeyEnv, BaseURL: a.BaseURL,
		AllowProtectedEgress: a.AllowProtectedEgress, TLSSkipVerify: a.TLSSkipVerify, RefusalFallback: a.UsesRefusalFallback(),
		CLIPath: a.CLIPath, ProposeActions: boolPtr(a.UsesProposeActions()),
	}
}

// fromDTO builds the ai block to save from the panel's draft, carrying over
// what the panel never edits (a legacy key).
func fromDTO(d AISettingsDTO, cur config.AIConfig) config.AIConfig {
	fallback := d.RefusalFallback
	propose := cur.ProposeActions
	if d.ProposeActions != nil {
		propose = boolPtr(*d.ProposeActions)
	}
	a := config.AIConfig{
		Provider: strings.TrimSpace(d.Provider), Model: strings.TrimSpace(d.Model), KeySource: d.KeySource,
		APIKeyEnv: strings.TrimSpace(d.APIKeyEnv), BaseURL: strings.TrimSpace(d.BaseURL), CLIPath: strings.TrimSpace(d.CLIPath),
		AllowProtectedEgress: d.AllowProtectedEgress, TLSSkipVerify: d.TLSSkipVerify, RefusalFallback: &fallback,
		ProposeActions: propose,
	}
	if a.KeySource == string(ai.KeySourceLegacy) {
		a.APIKey = cur.APIKey
	}
	return normalizedAI(&a)
}

// GetAISettings reports the persisted settings, which keys are stored, and the
// active provider - and whether the two disagree.
func (s *AISettingsService) GetAISettings() AISettingsStateDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := AISettingsStateDTO{Path: s.path, KeysStored: map[string]bool{}}
	cur, _, err := s.loadAI(s.path)
	persisted := normalizedAI(cur)
	if err != nil {
		st.PersistedError = ai.Scrub(err.Error())
	} else {
		st.Persisted = toDTO(persisted)
		st.LegacyKeyInFile = persisted.APIKey != ""
		st.Diverged = !config.AIEqual(persisted, s.inUseLocked(persisted))
	}
	if na := s.notActive; na != nil {
		st.SavedNotActive = na.reason
	}
	for _, p := range keyedProviders {
		_, kerr := s.keys.Get(p)
		var kc *ai.KeychainError
		switch {
		case kerr == nil:
			st.KeysStored[string(p)] = true
		case errors.As(kerr, &kc):
			st.KeychainError = aiProblem(kerr)
		}
	}
	active := s.aiSvc.load()
	st.Active = s.aiSvc.Available()
	st.ActiveRev = active.Rev
	return st
}

// inUseLocked is the terms the file is compared with: the active ones, or
// the saved candidate whose activation failed (its key from file, the
// version it was saved as).
func (s *AISettingsService) inUseLocked(file config.AIConfig) config.AIConfig {
	na := s.notActive
	if na == nil {
		return s.applied
	}
	c := na.cand
	if na.legacyKey {
		c.APIKey = file.APIKey
	}
	return c
}

func keyedProvider(p string) (ai.ProviderID, bool) {
	for _, k := range keyedProviders {
		if string(k) == p {
			return k, true
		}
	}
	return "", false
}

// keyOnlyDiffers: a and b are the same terms apart from where the key comes
// from (Settings slice F §5, R5).
func keyOnlyDiffers(a, b config.AIConfig) bool {
	a.KeySource, a.APIKey, a.APIKeyEnv = "", "", ""
	b.KeySource, b.APIKey, b.APIKeyEnv = "", "", ""
	return config.AIEqual(a, b)
}

// SetAPIKey stores a key in the OS keychain. For the running provider it
// activates the running terms with the keychain as the key source - never
// the file's block (Settings slice F §5, R5): fleet.yaml is updated only
// when its block is the running one apart from the key, and a block that
// differs further stays drift. A key for another provider is stored for
// when it is chosen.
func (s *AISettingsService) SetAPIKey(provider, key string) ActionResultDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := keyedProvider(provider)
	key = strings.TrimSpace(key)
	switch {
	case !ok:
		return ActionResultDTO{Error: "this provider does not take an API key"}
	case key == "":
		return ActionResultDTO{Error: "the key is empty"}
	case len(key) > aiKeyMax:
		return ActionResultDTO{Error: "the key is too long"}
	}
	if err := s.keys.Set(p, key); err != nil {
		return ActionResultDTO{Error: aiProblem(err)}
	}
	if s.notActive != nil || s.applied.Provider != string(p) {
		return ActionResultDTO{OK: true} // stored; nothing running uses it
	}
	next := s.applied
	next.KeySource, next.APIKey = string(ai.KeySourceKeychain), ""
	s.aiSvc.beginApply()
	defer s.aiSvc.endApply()
	cur, version, err := s.loadAI(s.path)
	written := ""
	if err == nil && keyOnlyDiffers(normalizedAI(cur), s.applied) {
		w, werr := s.saveAI(s.path, version, next)
		if werr != nil {
			return ActionResultDTO{Error: "Key stored in the keychain, but fleet.yaml was not updated, so the provider in use was not changed: " + ai.Scrub(werr.Error(), key)}
		}
		written = w
	}
	if reason := s.activateLocked(next, written); reason != "" {
		return ActionResultDTO{Error: "Key stored in the keychain, but the provider could not use it: " + ai.Scrub(reason, key)}
	}
	return ActionResultDTO{OK: true}
}

// ClearAPIKey deletes a keychain key. It stays immediate (Settings slice F
// §5, R5): a tightening takes no plan. If the RUNNING provider used that
// keychain key, new calls stop at once - even if fleet.yaml cannot then be
// updated - so a deleted credential is never used again (Codex review of
// c488e34). For the provider fleet.yaml selects, the key source becomes none:
// AI turns unavailable, and no environment or legacy key takes over (spec
// §2.1). A failed file update is reported and shows as divergence.
func (s *AISettingsService) ClearAPIKey(provider string) ActionResultDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := keyedProvider(provider)
	if !ok {
		return ActionResultDTO{Error: "this provider does not take an API key"}
	}
	if err := s.keys.Delete(p); err != nil {
		return ActionResultDTO{Error: aiProblem(err)}
	}
	s.aiSvc.beginApply()
	defer s.aiSvc.endApply()

	// The running snapshot first, decided from the snapshot itself, not from
	// the file. In-flight calls are refused at their next send: the
	// revision moved.
	if active := s.aiSvc.load(); active.ProviderID == p && active.KeySource == ai.KeySourceKeychain {
		off := &AIState{ProviderID: p, AllowEgress: active.AllowEgress, Problem: "The API key was removed. Add one in Settings > AI."}
		SetAIState(s.aiSvc, off)
		if s.applied.Provider == string(p) {
			s.applied.KeySource = string(ai.KeySourceNone)
		}
	}

	cur, _, err := s.loadAI(s.path)
	if err != nil {
		return ActionResultDTO{Error: "Key removed from the keychain; AI no longer uses it, but fleet.yaml cannot be read: " + ai.Scrub(err.Error())}
	}
	curN := normalizedAI(cur)
	if curN.Provider != string(p) || curN.KeySource != string(ai.KeySourceKeychain) {
		return ActionResultDTO{OK: true}
	}
	next := curN
	next.KeySource = string(ai.KeySourceNone)
	if _, err := s.saveAI(s.path, "", next); err != nil {
		return ActionResultDTO{Error: "Key removed from the keychain; AI no longer uses it, but fleet.yaml was not updated: " + err.Error()}
	}
	if s.notActive == nil && keyOnlyDiffers(next, s.applied) {
		// the running terms, now without a key: published unavailable with
		// its reason, never falling back to another key
		settings := AISettingsFromConfig(&next)
		b, berr := s.build(settings, s.keys, s.getenv)
		st := NewAIState(settings.Provider, b, berr, next.AllowProtectedEgress)
		st.ProposalsOff = !next.UsesProposeActions()
		SetAIState(s.aiSvc, st)
		s.applied = next
	}
	return ActionResultDTO{OK: true}
}

// MoveKeyToKeychain moves a legacy plain-text key into the keychain: the
// keychain write first, then the removal from fleet.yaml, then the running
// terms with the keychain as the key source (Settings slice F §5, R5). Its
// block condition: the file's block is the running one apart from the key;
// otherwise it refuses, and nothing moves.
func (s *AISettingsService) MoveKeyToKeychain() ActionResultDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notActive != nil {
		return ActionResultDTO{Error: "The AI settings are saved but not active: retry or revert them first."}
	}
	cur, version, err := s.loadAI(s.path)
	if err != nil {
		return ActionResultDTO{Error: "fleet.yaml cannot be read: " + ai.Scrub(err.Error())}
	}
	curN := normalizedAI(cur)
	p, ok := keyedProvider(curN.Provider)
	if !ok || curN.APIKey == "" {
		return ActionResultDTO{Error: "fleet.yaml holds no plain-text key to move"}
	}
	if !keyOnlyDiffers(curN, s.applied) {
		return ActionResultDTO{Error: "fleet.yaml's AI settings differ from the ones in use: use the file's values, or save, first"}
	}
	if err := s.keys.Set(p, curN.APIKey); err != nil {
		return ActionResultDTO{Error: "Nothing moved: " + aiProblem(err)}
	}
	next := s.applied
	next.KeySource, next.APIKey = string(ai.KeySourceKeychain), ""
	s.aiSvc.beginApply()
	defer s.aiSvc.endApply()
	written, err := s.saveAI(s.path, version, next)
	if err != nil {
		return ActionResultDTO{Error: "Key copied to the keychain, but fleet.yaml still holds it in plain text: " + ai.Scrub(err.Error(), curN.APIKey)}
	}
	if reason := s.activateLocked(next, written); reason != "" {
		return ActionResultDTO{Error: "Key moved to the keychain, but the provider could not use it: " + ai.Scrub(reason, curN.APIKey)}
	}
	return ActionResultDTO{OK: true}
}

// draftBuild builds from the panel's draft without saving it: the full
// provider for Test, or only the model lister (no model required) for the
// picker.
func (s *AISettingsService) draftBuild(d AISettingsDTO, listerOnly bool) (*ai.Built, error) {
	cur, _, _ := s.loadAI(s.path)
	next := fromDTO(d, normalizedAI(cur))
	build := s.build
	if listerOnly {
		build = ai.BuildLister
	}
	b, err := build(AISettingsFromConfig(&next), s.keys, s.getenv)
	if err != nil {
		return nil, errors.New(aiProblem(err))
	}
	if b == nil {
		return nil, errors.New("choose a provider first")
	}
	return b, nil
}

// ListModels lists the draft provider's models. It sends no cluster data.
func (s *AISettingsService) ListModels(d AISettingsDTO) AIModelsDTO {
	s.mu.Lock()
	b, err := s.draftBuild(d, true)
	s.mu.Unlock()
	if err != nil {
		return AIModelsDTO{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models, err := b.Lister.ListModels(ctx)
	if err != nil {
		return AIModelsDTO{Error: "could not list models: " + ai.Scrub(err.Error())}
	}
	if models == nil {
		models = []ai.ModelInfo{}
	}
	return AIModelsDTO{Models: models}
}

// aiProbeSystem is the whole of a Test prompt: no cluster data.
const aiProbeSystem = "This is a connectivity check from Klyx. Reply with exactly one word: ready"

// TestProvider sends the draft provider one tiny completion with no cluster
// data and reports what came back. For the Claude Code CLI, Test re-admits
// it (lifting a revocation) and sends the tool-exposing probe of spec §1.4
// instead: one small prompt on the owner's Claude sign-in.
func (s *AISettingsService) TestProvider(d AISettingsDTO) AITestDTO {
	s.mu.Lock()
	b, err := s.draftBuild(d, false)
	s.mu.Unlock()
	if err != nil {
		return AITestDTO{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ai.Deadline(ai.PurposeProbe))
	defer cancel()
	var res ai.Result
	if prober, ok := b.Provider.(ai.CLIProber); ok {
		res, err = prober.Probe(ctx)
	} else {
		res, err = b.Provider.Complete(ctx, ai.Request{System: aiProbeSystem, User: "ready?", Purpose: ai.PurposeProbe})
	}
	if err != nil {
		return AITestDTO{Origin: b.Origin, Error: modelError(err)}
	}
	return AITestDTO{OK: true, Model: res.ModelUsed, Origin: b.Origin, Reply: ai.Scrub(res.Text)}
}

// AICLIStatusDTO is what Settings shows about an installed CLI: where it is,
// whether this build passed Test on this machine, and whether it is
// admitted. It never runs the CLI.
type AICLIStatusDTO struct {
	Path      string `json:"path"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"` // short
	Tested    bool   `json:"tested"`
	Admitted  bool   `json:"admitted"`
	CheckedAt string `json:"checkedAt,omitempty"` // RFC 3339
	Problem   string `json:"problem,omitempty"`
}

// CLIStatus describes the draft's CLI provider without running it.
func (s *AISettingsService) CLIStatus(d AISettingsDTO) AICLIStatusDTO {
	s.mu.Lock()
	b, err := s.draftBuild(d, false)
	s.mu.Unlock()
	if err != nil {
		return AICLIStatusDTO{Problem: err.Error()}
	}
	r, ok := b.Provider.(ai.CLIStatusReporter)
	if !ok {
		return AICLIStatusDTO{Problem: "this provider is not a CLI"}
	}
	st := r.Status()
	out := AICLIStatusDTO{Path: st.Path, Version: st.Version, SHA256: st.SHA256, Tested: st.Tested, Admitted: st.Admitted, Problem: st.Problem}
	if !st.CheckedAt.IsZero() {
		out.CheckedAt = st.CheckedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func boolPtr(b bool) *bool { return &b }

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
// egress (spec 2026-10-13 §2.3).
const aiEgressPhrase = "allow protected egress"

// aiEgressGrantTTL bounds the one-shot egress grant, like ProtectGuard's.
const aiEgressGrantTTL = 30 * time.Second

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
	// Diverged: the settings in fleet.yaml are not the ones the running
	// provider was built from (an outside edit, or a failed apply).
	Diverged bool `json:"diverged"`
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
// §2.3). Every operation is serialised; each save builds the new provider
// first, writes fleet.yaml under the shared fleet write lock, then swaps the
// AIService snapshot - so persisted and active settings never diverge
// silently, and an invalid or unbuildable setting writes nothing.
type AISettingsService struct {
	path   string
	keys   ai.KeyStore
	getenv func(string) string
	aiSvc  *AIService
	now    func() time.Time
	saveAI func(string, config.AIConfig) error // seam: config.SaveAI
	loadAI func(string) (*config.AIConfig, error)

	mu          sync.Mutex
	applied     config.AIConfig // what the active snapshot was built from
	egressUntil time.Time       // one-shot egress grant
}

// NewAISettingsService wires the writer. initial is the ai block the startup
// snapshot was built from (nil = off).
func NewAISettingsService(path string, keys ai.KeyStore, getenv func(string) string, aiSvc *AIService, initial *config.AIConfig) *AISettingsService {
	s := &AISettingsService{path: path, keys: keys, getenv: getenv, aiSvc: aiSvc, now: time.Now, saveAI: config.SaveAI, loadAI: loadAIBlock}
	s.applied = normalizedAI(initial)
	return s
}

// loadAIBlock reads the ai block currently in fleet.yaml.
func loadAIBlock(path string) (*config.AIConfig, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return cfg.AI, nil
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
	}
}

// fromDTO builds the ai block to save from the panel's draft, carrying over
// what the panel never edits (a legacy key, the CLI path).
func fromDTO(d AISettingsDTO, cur config.AIConfig) config.AIConfig {
	fallback := d.RefusalFallback
	a := config.AIConfig{
		Provider: strings.TrimSpace(d.Provider), Model: strings.TrimSpace(d.Model), KeySource: d.KeySource,
		APIKeyEnv: strings.TrimSpace(d.APIKeyEnv), BaseURL: strings.TrimSpace(d.BaseURL), CLIPath: cur.CLIPath,
		AllowProtectedEgress: d.AllowProtectedEgress, TLSSkipVerify: d.TLSSkipVerify, RefusalFallback: &fallback,
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
	cur, err := s.loadAI(s.path)
	persisted := normalizedAI(cur)
	if err != nil {
		st.PersistedError = ai.Scrub(err.Error())
	} else {
		st.Persisted = toDTO(persisted)
		st.LegacyKeyInFile = persisted.APIKey != ""
		st.Diverged = !config.AIEqual(persisted, s.applied)
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

// apply builds the provider for a, writes it, then swaps the snapshot. With
// requireBuild a build failure writes nothing; without it (clearing a key) a
// provider that can no longer be built is written and published as
// unavailable with its reason - it never falls back.
func (s *AISettingsService) apply(a config.AIConfig, requireBuild bool) error {
	settings := AISettingsFromConfig(&a)
	b, err := ai.Build(settings, s.keys, s.getenv)
	if err != nil && requireBuild {
		return errors.New(aiProblem(err))
	}
	if werr := s.saveAI(s.path, a); werr != nil {
		return werr
	}
	SetAIState(s.aiSvc, NewAIState(settings.Provider, b, err, a.AllowProtectedEgress))
	s.applied = a
	return nil
}

// consumeEgressGrant spends the one-shot grant, pass or fail.
func (s *AISettingsService) consumeEgressGrant() bool {
	ok := !s.egressUntil.IsZero() && s.now().Before(s.egressUntil)
	s.egressUntil = time.Time{}
	return ok
}

// ArmAIEgress records the typed confirmation for turning protected-cluster
// egress on: a one-shot grant SaveAISettings consumes (spec §2.3 - enforced at
// the backend, not only in the dialog).
func (s *AISettingsService) ArmAIEgress(typed string) ActionResultDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(typed) != aiEgressPhrase {
		s.egressUntil = time.Time{}
		return ActionResultDTO{Error: "type \"" + aiEgressPhrase + "\" to confirm"}
	}
	s.egressUntil = s.now().Add(aiEgressGrantTTL)
	return ActionResultDTO{OK: true}
}

// SaveAISettings validates and applies the panel's draft.
func (s *AISettingsService) SaveAISettings(d AISettingsDTO) ActionResultDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.loadAI(s.path)
	if err != nil {
		return ActionResultDTO{Error: "fleet.yaml cannot be read, so nothing was saved: " + ai.Scrub(err.Error())}
	}
	curN := normalizedAI(cur)
	next := fromDTO(d, curN)
	if next.AllowProtectedEgress && !curN.AllowProtectedEgress && !s.consumeEgressGrant() {
		return ActionResultDTO{Error: "Turning on protected-cluster egress needs the typed confirmation; nothing was saved."}
	}
	if err := s.apply(next, true); err != nil {
		return ActionResultDTO{Error: err.Error()}
	}
	return ActionResultDTO{OK: true}
}

func keyedProvider(p string) (ai.ProviderID, bool) {
	for _, k := range keyedProviders {
		if string(k) == p {
			return k, true
		}
	}
	return "", false
}

// SetAPIKey stores a key in the OS keychain. For the provider fleet.yaml
// selects, the key source becomes the keychain (a legacy plain-text key is
// removed from the file) and the provider is rebuilt, so the key the panel
// describes is the key in use (spec §2.1).
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
	cur, err := s.loadAI(s.path)
	if err != nil {
		return ActionResultDTO{Error: "Key stored in the keychain, but fleet.yaml cannot be read to use it: " + ai.Scrub(err.Error(), key)}
	}
	curN := normalizedAI(cur)
	if curN.Provider != string(p) {
		return ActionResultDTO{OK: true} // stored for when this provider is chosen
	}
	next := curN
	next.KeySource, next.APIKey = string(ai.KeySourceKeychain), ""
	if err := s.apply(next, true); err != nil {
		return ActionResultDTO{Error: "Key stored in the keychain, but the provider could not use it: " + ai.Scrub(err.Error(), key)}
	}
	return ActionResultDTO{OK: true}
}

// ClearAPIKey deletes a keychain key. If the RUNNING provider used that
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

	// The running snapshot first, decided from the snapshot itself, not from
	// the file. In-flight calls finish on the snapshot they loaded.
	if active := s.aiSvc.load(); active.ProviderID == p && active.KeySource == ai.KeySourceKeychain {
		off := &AIState{ProviderID: p, AllowEgress: active.AllowEgress, Problem: "The API key was removed. Add one in Settings > AI."}
		SetAIState(s.aiSvc, off)
		if s.applied.Provider == string(p) {
			s.applied.KeySource = string(ai.KeySourceNone)
		}
	}

	cur, err := s.loadAI(s.path)
	if err != nil {
		return ActionResultDTO{Error: "Key removed from the keychain; AI no longer uses it, but fleet.yaml cannot be read: " + ai.Scrub(err.Error())}
	}
	curN := normalizedAI(cur)
	if curN.Provider != string(p) || curN.KeySource != string(ai.KeySourceKeychain) {
		return ActionResultDTO{OK: true}
	}
	next := curN
	next.KeySource = string(ai.KeySourceNone)
	if err := s.apply(next, false); err != nil {
		return ActionResultDTO{Error: "Key removed from the keychain; AI no longer uses it, but fleet.yaml was not updated: " + err.Error()}
	}
	return ActionResultDTO{OK: true}
}

// MoveKeyToKeychain moves a legacy plain-text key into the keychain: the
// keychain write first, then the removal from fleet.yaml. A failed removal is
// reported, and the source stays legacy until it succeeds (spec §2.1).
func (s *AISettingsService) MoveKeyToKeychain() ActionResultDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.loadAI(s.path)
	if err != nil {
		return ActionResultDTO{Error: "fleet.yaml cannot be read: " + ai.Scrub(err.Error())}
	}
	curN := normalizedAI(cur)
	p, ok := keyedProvider(curN.Provider)
	if !ok || curN.APIKey == "" {
		return ActionResultDTO{Error: "fleet.yaml holds no plain-text key to move"}
	}
	if err := s.keys.Set(p, curN.APIKey); err != nil {
		return ActionResultDTO{Error: "Nothing moved: " + aiProblem(err)}
	}
	next := curN
	next.KeySource, next.APIKey = string(ai.KeySourceKeychain), ""
	if err := s.apply(next, true); err != nil {
		return ActionResultDTO{Error: "Key copied to the keychain, but fleet.yaml still holds it in plain text: " + ai.Scrub(err.Error(), curN.APIKey)}
	}
	return ActionResultDTO{OK: true}
}

// draftBuild builds from the panel's draft without saving it: the full
// provider for Test, or only the model lister (no model required) for the
// picker.
func (s *AISettingsService) draftBuild(d AISettingsDTO, listerOnly bool) (*ai.Built, error) {
	cur, _ := s.loadAI(s.path)
	next := fromDTO(d, normalizedAI(cur))
	build := ai.Build
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
// data and reports what came back.
func (s *AISettingsService) TestProvider(d AISettingsDTO) AITestDTO {
	s.mu.Lock()
	b, err := s.draftBuild(d, false)
	s.mu.Unlock()
	if err != nil {
		return AITestDTO{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ai.Deadline(ai.PurposeProbe))
	defer cancel()
	res, err := b.Provider.Complete(ctx, ai.Request{System: aiProbeSystem, User: "ready?", Purpose: ai.PurposeProbe})
	if err != nil {
		return AITestDTO{Origin: b.Origin, Error: modelError(err)}
	}
	return AITestDTO{OK: true, Model: res.ModelUsed, Origin: b.Origin, Reply: ai.Scrub(res.Text)}
}

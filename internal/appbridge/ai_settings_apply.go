package appbridge

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/config"
)

// The AI section's plans (Settings slice F §5): planned from what runs,
// applied by digest under the section's Applying state.

// aiCandidate is what a plan's digest holds in the cache: never a key. A
// legacy key is re-read from the file at Apply, whose version the plan
// binds.
type aiCandidate struct {
	Kind      string          `json:"kind"`
	Cand      config.AIConfig `json:"cand"`
	LegacyKey bool            `json:"legacyKey"`
	Writes    bool            `json:"writes"`
}

func withoutKey(a config.AIConfig) (config.AIConfig, bool) {
	had := a.APIKey != ""
	a.APIKey = ""
	return a, had
}

// PlanAI plans the panel's draft from what runs.
func (s *AISettingsService) PlanAI(d AISettingsDTO) PlanDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, version, err := s.loadAI(s.path)
	if err != nil {
		return refusedPlan(planSave, "fleet.yaml cannot be read, so nothing can be applied: "+ai.Scrub(err.Error()))
	}
	file := normalizedAI(cur)
	return s.planLocked(planSave, fromDTO(d, file), version, true, file)
}

// PlanAIFromFile plans "Use the file's values": the file's block, from what
// runs, writing nothing - under the same interlocks, so a hand edit
// turning egress on takes the confirmation.
func (s *AISettingsService) PlanAIFromFile() PlanDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, version, err := s.loadAI(s.path)
	if err != nil {
		return refusedPlan(planFile, "fleet.yaml cannot be read: "+ai.Scrub(err.Error()))
	}
	file := normalizedAI(cur)
	return s.planLocked(planFile, file, version, false, file)
}

// PlanAIRevert plans the way back from a saved, not active block to the
// last terms that were active: written, and activated.
func (s *AISettingsService) PlanAIRevert() PlanDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notActive == nil {
		return refusedPlan(planRevert, "the AI settings in use are active: there is nothing to revert")
	}
	cur, version, err := s.loadAI(s.path)
	if err != nil {
		return refusedPlan(planRevert, "fleet.yaml cannot be read: "+ai.Scrub(err.Error()))
	}
	return s.planLocked(planRevert, s.applied, version, true, normalizedAI(cur))
}

func refusedPlan(kind, why string) PlanDTO {
	return PlanDTO{Section: "ai", Kind: kind, Refused: why, Runtime: map[string]string{}, Changes: []PlanChangeDTO{}, Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{}}
}

// activates: applying cand publishes a new state (the revision moves).
func (s *AISettingsService) activatesLocked(cand config.AIConfig) bool {
	return s.notActive != nil || !config.AIEqual(s.applied, cand)
}

// planLocked plans cand from what runs. The interlock compares with the
// running grant: none while nothing is active.
func (s *AISettingsService) planLocked(kind string, cand config.AIConfig, version string, writes bool, file config.AIConfig) PlanDTO {
	p := PlanDTO{Section: "ai", Kind: kind, FileVersion: version, Writes: writes, Changes: []PlanChangeDTO{}, Ends: []PlanWorkDTO{}, Continues: []PlanWorkDTO{}}
	activates := s.activatesLocked(cand)
	if writes && !activates && config.AIEqual(file, cand) {
		p.Writes = false // the file already says so, and it runs
	}
	p.Changes = aiChanges(s.inUseLocked(file), cand)
	if activates {
		if _, err := s.build(AISettingsFromConfig(&cand), s.keys, s.getenv); err != nil {
			p.Refused = aiProblem(err)
		}
		p.Ends = s.workNow()
	}
	granted := s.notActive == nil && s.applied.AllowProtectedEgress
	if cand.AllowProtectedEgress && !granted && cand.Provider != "" && cand.Provider != string(ai.ProviderOff) {
		dest := providerName(cand.Provider)
		if cand.BaseURL != "" {
			dest += " at " + cand.BaseURL
		}
		p.Interlock = &InterlockDTO{Phrase: aiEgressPhrase, Why: "Chat, Explain, Suggest-fix and investigations on protected clusters will send their resolved state to " + dest + "."}
	}
	p.Runtime = map[string]string{"ai": strconv.FormatUint(s.aiSvc.load().Rev, 10), "work": workVersion(p.Ends)}
	stored, legacy := withoutKey(cand)
	c := aiCandidate{Kind: kind, Cand: stored, LegacyKey: legacy, Writes: p.Writes}
	bind, _ := json.Marshal(c)
	seal(&p, string(bind))
	if p.Refused == "" {
		s.cache.put(p.Digest, c)
	}
	return p
}

// workNow is the AI work an activation ends: the chat's and the in-flight
// calls, by identity.
func (s *AISettingsService) workNow() []PlanWorkDTO {
	var out []PlanWorkDTO
	if s.work != nil {
		out = append(out, s.work()...)
	}
	out = append(out, s.aiSvc.callsWork()...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []PlanWorkDTO{}
	}
	return out
}

// ApplyAI applies the plan with digest: re-planned from what runs under the
// Applying state, refused with the new plan when anything moved, the
// interlock checked, the block written against the plan's file version,
// then activated.
func (s *AISettingsService) ApplyAI(digest, typed string) ApplyResultDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(digest, typed, false)
}

// ApplyAIQuiet is the draft guard's "Save and leave" (Settings slice F §6):
// the draft is planned and applied only if, at Apply time and atomically
// under the Applying state, the plan ends nothing and needs no interlock;
// otherwise the plan comes back, applied by nothing.
func (s *AISettingsService) ApplyAIQuiet(d AISettingsDTO) ApplyResultDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, version, err := s.loadAI(s.path)
	if err != nil {
		return ApplyResultDTO{Error: "fleet.yaml cannot be read, so nothing was saved: " + ai.Scrub(err.Error())}
	}
	file := normalizedAI(cur)
	p := s.planLocked(planSave, fromDTO(d, file), version, true, file)
	if p.Refused != "" {
		return ApplyResultDTO{Error: p.Refused, Plan: &p}
	}
	return s.applyLocked(p.Digest, "", true)
}

func (s *AISettingsService) applyLocked(digest, typed string, quiet bool) ApplyResultDTO {
	v, ok := s.cache.get(digest)
	if !ok {
		return ApplyResultDTO{Error: "This plan expired or was already applied: review the changes again."}
	}
	c := v.(aiCandidate)
	s.aiSvc.beginApply()
	defer s.aiSvc.endApply()
	cur, version, err := s.loadAI(s.path)
	if err != nil {
		return ApplyResultDTO{Error: "fleet.yaml cannot be read, so nothing was applied: " + ai.Scrub(err.Error())}
	}
	file := normalizedAI(cur)
	cand := c.Cand
	if c.LegacyKey {
		cand.APIKey = file.APIKey // the version the plan binds holds it
	}
	re := s.planLocked(c.Kind, cand, version, c.Writes, file)
	switch {
	case re.Digest != digest:
		// the file, a revision or the work moved: the new plan never
		// inherits the confirmation (§5)
		return ApplyResultDTO{Error: "Something changed since this was planned: review the new plan.", Plan: &re}
	case re.Refused != "":
		// unreachable while a refused plan is never cached and its refusal
		// is in its digest; kept so no refused plan can ever apply
		return ApplyResultDTO{Error: re.Refused}
	case quiet && (len(re.Ends) > 0 || re.Interlock != nil):
		return ApplyResultDTO{Plan: &re}
	case re.Interlock != nil && strings.TrimSpace(typed) != re.Interlock.Phrase:
		return ApplyResultDTO{Error: "Type \"" + re.Interlock.Phrase + "\" to confirm; nothing was applied."}
	}
	written := version
	if re.Writes {
		w, err := s.saveAI(s.path, version, cand)
		if errors.Is(err, config.ErrFileChanged) {
			return ApplyResultDTO{Error: "fleet.yaml changed while this was applied, so nothing was written: review the new plan.", Plan: s.replanLocked(c)}
		}
		if err != nil {
			return ApplyResultDTO{Error: err.Error()}
		}
		written = w
	}
	s.cache.consume(digest)
	if !s.activatesLocked(cand) {
		return ApplyResultDTO{OK: true}
	}
	if reason := s.activateLocked(cand, written); reason != "" {
		return ApplyResultDTO{SavedNotActive: reason}
	}
	return ApplyResultDTO{OK: true}
}

// replanLocked is c's plan from the file as it is now; "Use the file's
// values" adopts the new block.
func (s *AISettingsService) replanLocked(c aiCandidate) *PlanDTO {
	cur, version, err := s.loadAI(s.path)
	if err != nil {
		p := refusedPlan(c.Kind, "fleet.yaml cannot be read: "+ai.Scrub(err.Error()))
		return &p
	}
	file := normalizedAI(cur)
	cand := c.Cand
	if c.Kind == planFile {
		cand = file
	} else if c.LegacyKey {
		cand.APIKey = file.APIKey
	}
	p := s.planLocked(c.Kind, cand, version, c.Writes, file)
	return &p
}

// RetryAI retries the failed activation's exact candidate, with its bound
// file version and terms: identical terms need no second confirmation. If
// the file or what runs moved since, it is refused with a fresh plan from
// the file, with that plan's own interlocks (Settings slice F §5, R4).
func (s *AISettingsService) RetryAI() ApplyResultDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	na := s.notActive
	if na == nil {
		return ApplyResultDTO{Error: "the AI settings in use are active: there is nothing to retry"}
	}
	s.aiSvc.beginApply()
	defer s.aiSvc.endApply()
	cur, version, err := s.loadAI(s.path)
	if err != nil {
		return ApplyResultDTO{Error: "fleet.yaml cannot be read, so nothing was retried: " + ai.Scrub(err.Error())}
	}
	file := normalizedAI(cur)
	if version != na.version || s.aiSvc.load().Rev != na.rev {
		p := s.planLocked(planFile, file, version, false, file)
		return ApplyResultDTO{Error: "fleet.yaml or the AI settings in use changed since the activation failed: review the plan from the file.", Plan: &p}
	}
	cand := na.cand
	if na.legacyKey {
		cand.APIKey = file.APIKey
	}
	if reason := s.activateLocked(cand, version); reason != "" {
		return ApplyResultDTO{SavedNotActive: reason}
	}
	return ApplyResultDTO{OK: true}
}

// activateLocked builds and publishes cand, moving the AI revision either
// way. A build that fails leaves the block saved, not active: AI is
// unavailable everywhere with that reason, under neither the old terms nor
// the new (§5). "" when active.
func (s *AISettingsService) activateLocked(cand config.AIConfig, version string) string {
	settings := AISettingsFromConfig(&cand)
	b, err := s.build(settings, s.keys, s.getenv)
	if err != nil {
		reason := aiProblem(err)
		id := settings.Provider
		if id == "" {
			id = ai.ProviderOff
		}
		SetAIState(s.aiSvc, &AIState{ProviderID: id, Problem: "Saved, not active: " + reason})
		stored, legacy := withoutKey(cand)
		s.notActive = &aiNotActive{cand: stored, legacyKey: legacy, version: version, rev: s.aiSvc.load().Rev, reason: reason}
		return reason
	}
	st := NewAIState(settings.Provider, b, nil, cand.AllowProtectedEgress)
	st.ProposalsOff = !cand.UsesProposeActions()
	SetAIState(s.aiSvc, st)
	s.applied, s.notActive = cand, nil
	return ""
}

// aiChanges are the effective changes from what is in use to cand, by
// registry id; a key is said only by its state.
func aiChanges(from, to config.AIConfig) []PlanChangeDTO {
	out := []PlanChangeDTO{}
	add := func(id, label, a, b string) {
		if a != b {
			out = append(out, PlanChangeDTO{ID: id, Label: label, From: a, To: b})
		}
	}
	or := func(v, empty string) string {
		if v == "" {
			return empty
		}
		return v
	}
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	add("ai.provider", "Provider", providerName(from.Provider), providerName(to.Provider))
	add("ai.model", "Model", or(from.Model, "(default)"), or(to.Model, "(default)"))
	add("ai.keySource", "Key source", or(from.KeySource, "(none)"), or(to.KeySource, "(none)"))
	add("ai.apiKeyEnv", "Key variable", or(from.APIKeyEnv, "(none)"), or(to.APIKeyEnv, "(none)"))
	switch {
	case from.APIKey == to.APIKey:
	case to.APIKey == "":
		out = append(out, PlanChangeDTO{ID: "ai.apiKey", Label: "Plain-text key in fleet.yaml", From: "present", To: "none"})
	case from.APIKey == "":
		out = append(out, PlanChangeDTO{ID: "ai.apiKey", Label: "Plain-text key in fleet.yaml", From: "none", To: "present"})
	default:
		out = append(out, PlanChangeDTO{ID: "ai.apiKey", Label: "Plain-text key in fleet.yaml", From: "present", To: "another key"})
	}
	add("ai.baseUrl", "Origin", or(from.BaseURL, "(default)"), or(to.BaseURL, "(default)"))
	add("ai.cliPath", "Claude Code path", or(from.CLIPath, "(found automatically)"), or(to.CLIPath, "(found automatically)"))
	add("ai.allowProtectedEgress", "Protected-cluster egress", onOff(from.AllowProtectedEgress), onOff(to.AllowProtectedEgress))
	add("ai.tlsSkipVerify", "Certificate verification", onOff(!from.TLSSkipVerify), onOff(!to.TLSSkipVerify))
	add("ai.refusalFallback", "Refusal fallback", onOff(from.UsesRefusalFallback()), onOff(to.UsesRefusalFallback()))
	add("ai.proposeActions", "Propose actions", onOff(from.UsesProposeActions()), onOff(to.UsesProposeActions()))
	return out
}

// providerName is a provider as a plan says it.
func providerName(id string) string {
	switch ai.ProviderID(id) {
	case "", ai.ProviderOff:
		return "off"
	case ai.ProviderAnthropic:
		return "Claude (Anthropic API)"
	case ai.ProviderOpenAI:
		return "OpenAI"
	case ai.ProviderGemini:
		return "Gemini"
	case ai.ProviderOpenAICompatible:
		return "a local OpenAI-compatible server"
	case ai.ProviderClaudeCLI:
		return "the Claude Code CLI"
	}
	return id
}

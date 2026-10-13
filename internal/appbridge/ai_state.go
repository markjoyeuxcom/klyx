package appbridge

import (
	"errors"

	"github.com/moomora/klyx/internal/ai"
	"github.com/moomora/klyx/internal/config"
)

// AIState is one immutable snapshot of the active AI provider and its egress
// policy (spec 2026-10-13 §1.1). Every AI call loads the snapshot once and
// uses that same snapshot for the egress check, the completion and the
// provenance it returns: a settings swap mid-call can never pair a check made
// against a local provider with a send to a cloud one. A snapshot is never
// mutated after it is published.
type AIState struct {
	Provider    ai.Provider // nil when off or not buildable
	Lister      ai.ModelLister
	ProviderID  ai.ProviderID // the configured provider, even when Provider is nil
	Model       string
	Origin      string // where prompts go
	OnMachine   bool   // only a loopback openaiCompatible endpoint
	AllowEgress bool   // ai.allowProtectedEgress
	KeySource   ai.KeySource
	Problem     string // why Provider is nil when ProviderID is not off
	Rev         uint64 // assigned on publish
}

// NewAIState turns a build result into a snapshot. A build error leaves the
// provider nil with the (scrubbed) reason; nothing falls back.
func NewAIState(configured ai.ProviderID, b *ai.Built, buildErr error, allowEgress bool) *AIState {
	st := &AIState{ProviderID: configured, AllowEgress: allowEgress}
	if configured == "" {
		st.ProviderID = ai.ProviderOff
	}
	switch {
	case buildErr != nil:
		st.Problem = aiProblem(buildErr)
	case b != nil:
		st.Provider, st.Lister = b.Provider, b.Lister
		st.ProviderID, st.Model, st.Origin, st.OnMachine, st.KeySource = b.ID, b.Model, b.Origin, b.OnMachine, b.KeySource
	}
	return st
}

// aiProblem is the user-facing reason AI is unavailable.
func aiProblem(err error) string {
	var kc *ai.KeychainError
	switch {
	case errors.Is(err, ai.ErrNoKey):
		return "No API key is stored for this provider. Add one in Settings > AI."
	case errors.As(err, &kc):
		return ai.Scrub(kc.Error())
	default:
		return ai.Scrub(err.Error())
	}
}

// AISettingsFromConfig maps fleet.yaml's ai block to the transport's settings.
// A nil block is "off".
func AISettingsFromConfig(c *config.AIConfig) ai.Settings {
	if c == nil {
		return ai.Settings{Provider: ai.ProviderOff}
	}
	return ai.Settings{
		Provider:        ai.ProviderID(c.Provider),
		Model:           c.Model,
		BaseURL:         c.BaseURL,
		KeySource:       ai.KeySource(c.KeySource),
		APIKeyEnv:       c.APIKeyEnv,
		LegacyKey:       c.APIKey,
		TLSSkipVerify:   c.TLSSkipVerify,
		RefusalFallback: c.UsesRefusalFallback(),
		CLIPath:         c.CLIPath,
	}
}

// BuildAIState builds the snapshot for a fleet.yaml ai block.
func BuildAIState(c *config.AIConfig, keys ai.KeyStore, getenv func(string) string) *AIState {
	s := AISettingsFromConfig(c)
	b, err := ai.Build(s, keys, getenv)
	allow := c != nil && c.AllowProtectedEgress
	return NewAIState(s.Provider, b, err, allow)
}

package ai

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// KeySource is where a provider's key comes from. It is stored explicitly and
// never inferred by falling back: clearing a keychain key never reactivates an
// environment or fleet.yaml key (spec §2.1).
type KeySource string

const (
	KeySourceKeychain KeySource = "keychain"
	KeySourceEnv      KeySource = "env"
	KeySourceLegacy   KeySource = "legacy" // ai.apiKey in fleet.yaml, plain text
	KeySourceNone     KeySource = "none"
)

// Settings are the non-secret AI settings (fleet.yaml `ai:`), as the config
// package decoded them.
type Settings struct {
	Provider        ProviderID
	Model           string
	BaseURL         string
	KeySource       KeySource
	APIKeyEnv       string // KeySourceEnv: the variable's name
	LegacyKey       string // KeySourceLegacy: ai.apiKey
	TLSSkipVerify   bool   // openaiCompatible only
	RefusalFallback bool   // anthropic only
	CLIPath         string // claudeCLI: an explicit path to the claude binary
}

// Built is a ready provider plus the facts the egress policy and the UI need.
type Built struct {
	ID        ProviderID
	Provider  Provider
	Lister    ModelLister
	Model     string
	Origin    string // where prompts go, shown verbatim in Settings
	OnMachine bool   // only a loopback openaiCompatible endpoint
	KeySource KeySource
}

// Build resolves the key from the one configured source and builds the
// provider. It returns (nil, nil) for "off". An error means AI stays
// unavailable, with that reason; nothing falls back.
func Build(s Settings, keys KeyStore, getenv func(string) string) (*Built, error) {
	return build(s, keys, getenv, true)
}

// BuildLister builds only the model lister for s: the same key and origin
// rules as Build, without requiring a model - choosing one is what the list is
// for (Codex review of c488e34: first-time setup was circular). The result has
// no Provider, so nothing can be completed through it.
func BuildLister(s Settings, keys KeyStore, getenv func(string) string) (*Built, error) {
	b, err := build(s, keys, getenv, false)
	if b != nil {
		b.Provider = nil
	}
	return b, err
}

func build(s Settings, keys KeyStore, getenv func(string) string, requireModel bool) (*Built, error) {
	switch s.Provider {
	case "", ProviderOff:
		return nil, nil
	case ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderOpenAICompatible:
	case ProviderClaudeCLI:
		return buildClaudeCLI(s)
	default:
		return nil, fmt.Errorf("unknown AI provider %q", s.Provider)
	}

	key, err := resolveKey(s, keys, getenv)
	if err != nil {
		return nil, err
	}
	b := &Built{ID: s.Provider, Model: s.Model, KeySource: s.KeySource}

	switch s.Provider {
	case ProviderOpenAICompatible:
		if s.BaseURL == "" {
			return nil, errors.New("an OpenAI-compatible provider needs a base URL")
		}
		origin, loopback, err := checkOrigin(s.BaseURL, key != "")
		if err != nil {
			return nil, err
		}
		if s.Model == "" && requireModel {
			return nil, errors.New("choose a model for the OpenAI-compatible server")
		}
		hc := NewHTTPClient(s.TLSSkipVerify)
		p := NewOpenAICompatible(s.BaseURL, key, s.Model, hc).(*compatProvider)
		b.Provider, b.Lister, b.Origin, b.OnMachine = p, p, origin, loopback
		return b, nil
	}

	// The hosted vendors: a key is required, and a custom origin must be HTTPS.
	if key == "" {
		return nil, ErrNoKey
	}
	if s.TLSSkipVerify {
		return nil, errors.New("tlsSkipVerify applies only to an OpenAI-compatible server")
	}
	origin := map[ProviderID]string{ProviderAnthropic: DefaultAnthropicOrigin, ProviderOpenAI: DefaultOpenAIOrigin, ProviderGemini: DefaultGeminiOrigin}[s.Provider]
	if s.BaseURL != "" {
		// A hosted vendor's custom origin is HTTPS, always: the loopback HTTP
		// exception belongs to openaiCompatible only (Codex review of 104f4c8).
		o, _, err := checkOrigin(s.BaseURL, true)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(o, "https://") {
			return nil, errors.New("a custom origin for a hosted AI vendor must use HTTPS")
		}
		origin = o
	}
	hc := NewHTTPClient(false)
	switch s.Provider {
	case ProviderAnthropic:
		if b.Model == "" {
			b.Model = DefaultAnthropicModel
		}
		p := NewAnthropic(key, s.BaseURL, b.Model, hc, s.RefusalFallback).(*anthropicProvider)
		b.Provider, b.Lister = p, p
	case ProviderOpenAI:
		if b.Model == "" && requireModel {
			return nil, errors.New("choose an OpenAI model")
		}
		p := NewOpenAI(key, s.BaseURL, b.Model, hc).(*openaiProvider)
		b.Provider, b.Lister = p, p
	case ProviderGemini:
		if b.Model == "" && requireModel {
			return nil, errors.New("choose a Gemini model")
		}
		p := NewGemini(key, s.BaseURL, b.Model, hc).(*geminiProvider)
		b.Provider, b.Lister = p, p
	}
	b.Origin = origin
	return b, nil
}

// buildClaudeCLI builds the Claude Code provider. It runs nothing: the
// binary, sign-in and policy are checked before every call, and Ready says up
// front when the binary is not a reviewed build (spec §1.5).
func buildClaudeCLI(s Settings) (*Built, error) {
	switch {
	case s.KeySource != "" && s.KeySource != KeySourceNone:
		return nil, errors.New("the Claude Code CLI uses its own Claude sign-in, not an API key")
	case s.BaseURL != "":
		return nil, errors.New("a custom origin does not apply to the Claude Code CLI")
	case s.TLSSkipVerify:
		return nil, errors.New("tlsSkipVerify applies only to an OpenAI-compatible server")
	}
	model := s.Model
	if model == "" {
		model = DefaultAnthropicModel
	}
	p := newClaudeCLI(s.CLIPath, model)
	return &Built{ID: ProviderClaudeCLI, Provider: p, Lister: p, Model: model, Origin: ClaudeCLIOrigin, KeySource: KeySourceNone}, nil
}

// resolveKey reads the key from exactly the configured source.
func resolveKey(s Settings, keys KeyStore, getenv func(string) string) (string, error) {
	switch s.KeySource {
	case KeySourceKeychain:
		if keys == nil {
			return "", &KeychainError{Err: errors.New("no credential store")}
		}
		k, err := keys.Get(s.Provider)
		if errors.Is(err, ErrNoKey) && s.Provider == ProviderOpenAICompatible {
			return "", nil // the key is optional for a local server
		}
		return k, err
	case KeySourceEnv:
		if s.APIKeyEnv == "" {
			return "", errors.New("keySource env needs apiKeyEnv (the variable's name)")
		}
		k := ""
		if getenv != nil {
			k = getenv(s.APIKeyEnv)
		}
		if k == "" && s.Provider != ProviderOpenAICompatible {
			return "", fmt.Errorf("environment variable %s is empty or unset", s.APIKeyEnv)
		}
		return k, nil
	case KeySourceLegacy:
		if s.LegacyKey == "" && s.Provider != ProviderOpenAICompatible {
			return "", ErrNoKey
		}
		return s.LegacyKey, nil
	case KeySourceNone, "":
		return "", nil
	default:
		return "", fmt.Errorf("unknown key source %q", s.KeySource)
	}
}

// checkOrigin validates a configured base URL and returns its origin
// (scheme://host[:port]) and whether it is this machine. No userinfo, query
// or fragment; a credentialed endpoint must be HTTPS unless it is on this
// machine (spec §1.2). A keyless off-machine HTTP endpoint is allowed - a
// local-network model server - and Settings shows the unencrypted origin.
func checkOrigin(raw string, credentialed bool) (origin string, loopback bool, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false, fmt.Errorf("invalid base URL %q", raw)
	}
	if u.User != nil {
		return "", false, errors.New("the base URL must not carry a username or password")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", false, errors.New("the base URL must not carry a query or fragment")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", false, fmt.Errorf("the base URL must be http or https, not %q", u.Scheme)
	}
	loopback = IsLoopbackEndpoint(raw)
	if scheme == "http" && credentialed && !loopback {
		return "", false, errors.New("an endpoint that receives your API key must use HTTPS (or run on this machine)")
	}
	return scheme + "://" + u.Host, loopback, nil
}

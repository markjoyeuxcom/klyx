package config

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// aiYAML is the ai block as Klyx writes it: the 2026-10-13 spelling only
// (provider, never backend), in a fixed field order. apiKey appears only for
// keySource legacy - Klyx never writes a key it was not already holding there.
type aiYAML struct {
	Provider             string `yaml:"provider"`
	Model                string `yaml:"model,omitempty"`
	KeySource            string `yaml:"keySource,omitempty"`
	APIKeyEnv            string `yaml:"apiKeyEnv,omitempty"`
	APIKey               string `yaml:"apiKey,omitempty"`
	BaseURL              string `yaml:"baseUrl,omitempty"`
	CLIPath              string `yaml:"cliPath,omitempty"`
	AllowProtectedEgress bool   `yaml:"allowProtectedEgress"`
	TLSSkipVerify        bool   `yaml:"tlsSkipVerify,omitempty"`
	RefusalFallback      *bool  `yaml:"refusalFallback,omitempty"`
	ProposeActions       *bool  `yaml:"proposeActions,omitempty"`
}

// SaveAI replaces the fleet file's top-level `ai:` block with a (or appends
// one) by TEXTUAL replacement: every other byte - comments, formatting, the
// clusters list - is preserved. Comments inside the old ai block are not.
// The candidate is validated through the real loader, and must decode with
// every key other than ai unchanged and with exactly the intended ai settings,
// before it replaces the file. It shares the fleet write lock with
// AppendClusters and refuses to overwrite an edit made on disk meanwhile
// (spec 2026-10-13 §2.3).
func SaveAI(path string, a AIConfig) error {
	_, err := SaveAIAt(path, "", a)
	return err
}

// SaveAIAt is SaveAI from a plan: version is the file version it was made
// from (ErrFileChanged for any other; "" checks none), and it returns the
// written file's version (Settings slice F §5).
func SaveAIAt(path, version string, a AIConfig) (string, error) {
	if a.KeySource != "legacy" {
		a.APIKey = "" // only a legacy source keeps a key in the file
	}
	block, err := renderAIBlock(a)
	if err != nil {
		return "", err
	}
	want := a
	want.applyDefaults()
	return saveTopLevelBlock(path, "ai", block, "AI settings", version, nil, func(got *Config) error {
		if got.AI == nil || !AIEqual(*got.AI, want) {
			return errors.New("the written AI block does not read back as intended (file unchanged)")
		}
		return nil
	})
}

// AIEqual compares two applied-defaults AI blocks on every field that changes
// behaviour (the legacy backend spelling is ignored: provider supersedes it).
func AIEqual(x, y AIConfig) bool {
	return x.Provider == y.Provider && x.Model == y.Model && x.KeySource == y.KeySource &&
		x.APIKeyEnv == y.APIKeyEnv && x.APIKey == y.APIKey && x.BaseURL == y.BaseURL &&
		x.CLIPath == y.CLIPath && x.AllowProtectedEgress == y.AllowProtectedEgress &&
		x.TLSSkipVerify == y.TLSSkipVerify && x.UsesRefusalFallback() == y.UsesRefusalFallback() &&
		x.UsesProposeActions() == y.UsesProposeActions()
}

// renderAIBlock renders `ai:` plus its fields, indented two spaces, with
// yaml.v3 doing all the quoting.
func renderAIBlock(a AIConfig) (string, error) {
	provider := a.Provider
	if provider == "" {
		provider = "off"
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(aiYAML{
		Provider: provider, Model: a.Model, KeySource: a.KeySource, APIKeyEnv: a.APIKeyEnv, APIKey: a.APIKey,
		BaseURL: a.BaseURL, CLIPath: a.CLIPath, AllowProtectedEgress: a.AllowProtectedEgress,
		TLSSkipVerify: a.TLSSkipVerify, RefusalFallback: a.RefusalFallback, ProposeActions: a.ProposeActions,
	}); err != nil {
		return "", fmt.Errorf("render ai block: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("render ai block: %w", err)
	}
	var b strings.Builder
	b.WriteString("ai:\n")
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String(), nil
}

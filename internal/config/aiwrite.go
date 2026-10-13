package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	fleetWriteMu.Lock()
	defer fleetWriteMu.Unlock()

	original, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("there is no fleet config at %q yet - add a cluster first", path)
		}
		return fmt.Errorf("read fleet config %q: %w", path, err)
	}
	if a.KeySource != "legacy" {
		a.APIKey = "" // only a legacy source keeps a key in the file
	}
	block, err := renderAIBlock(a)
	if err != nil {
		return err
	}
	candidate, err := replaceAIBlock(string(original), block)
	if err != nil {
		return fmt.Errorf("fleet config %q: %w", path, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".fleet-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // best-effort temp cleanup
	if _, err := tmp.WriteString(candidate); err != nil {
		_ = tmp.Close() // already returning the write error
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	got, err := Load(tmpPath)
	if err != nil {
		return fmt.Errorf("the AI settings failed validation (file unchanged): %w", err)
	}
	if err := onlyAIChanged(original, []byte(candidate)); err != nil {
		return fmt.Errorf("saving AI settings would change other settings (file unchanged): %w", err)
	}
	want := a
	want.applyDefaults()
	if got.AI == nil || !AIEqual(*got.AI, want) {
		return errors.New("the written AI block does not read back as intended (file unchanged)")
	}
	return replaceUnchanged(path, tmpPath, original, true)
}

// AIEqual compares two applied-defaults AI blocks on every field that changes
// behaviour (the legacy backend spelling is ignored: provider supersedes it).
func AIEqual(x, y AIConfig) bool {
	return x.Provider == y.Provider && x.Model == y.Model && x.KeySource == y.KeySource &&
		x.APIKeyEnv == y.APIKeyEnv && x.APIKey == y.APIKey && x.BaseURL == y.BaseURL &&
		x.CLIPath == y.CLIPath && x.AllowProtectedEgress == y.AllowProtectedEgress &&
		x.TLSSkipVerify == y.TLSSkipVerify && x.UsesRefusalFallback() == y.UsesRefusalFallback()
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
		TLSSkipVerify: a.TLSSkipVerify, RefusalFallback: a.RefusalFallback,
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

// replaceAIBlock swaps the top-level `ai:` block in doc for block, or appends
// block when there is none. The old block runs from its `ai:` line to the next
// column-0 line (another key, a column-0 comment, a document marker); trailing
// blank lines stay where they were.
func replaceAIBlock(doc, block string) (string, error) {
	lines := strings.SplitAfter(doc, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := -1
	for i, l := range lines {
		if isTopLevelKey(l, "ai") {
			if start >= 0 {
				return "", errors.New("the file has more than one top-level ai: key")
			}
			start = i
		}
	}
	if start < 0 {
		out := doc
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		return out + block, nil
	}
	end := start + 1
	for end < len(lines) {
		l := lines[end]
		if strings.TrimSpace(l) == "" || l[0] == ' ' || l[0] == '\t' {
			end++
			continue
		}
		break
	}
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end-- // trailing blank lines belong to what follows
	}
	return strings.Join(lines[:start], "") + block + strings.Join(lines[end:], ""), nil
}

// isTopLevelKey reports whether line is the column-0 mapping key name.
func isTopLevelKey(line, name string) bool {
	if !strings.HasPrefix(line, name) {
		return false
	}
	rest := strings.TrimLeft(line[len(name):], " \t")
	return strings.HasPrefix(rest, ":")
}

// onlyAIChanged confirms that, decoded, every top-level key except ai is
// identical before and after.
func onlyAIChanged(before, after []byte) error {
	var a, b map[string]any
	if err := yaml.Unmarshal(before, &a); err != nil {
		return fmt.Errorf("decode current config: %w", err)
	}
	if err := yaml.Unmarshal(after, &b); err != nil {
		return fmt.Errorf("decode new config: %w", err)
	}
	delete(a, "ai")
	delete(b, "ai")
	if !reflect.DeepEqual(a, b) {
		return errors.New("a setting outside ai would change")
	}
	return nil
}

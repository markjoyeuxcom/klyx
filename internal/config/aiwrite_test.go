package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeFleet(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFleet(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Replacing an existing (legacy) ai block keeps every other byte, including
// comments and the block that follows it.
func TestSaveAI_ReplacesTheBlockAndKeepsTheRest(t *testing.T) {
	path := writeFleet(t, `# my fleet
clusters:
  - name: a # inline comment
ai:
  backend: hosted
  apiKey: sk-old

# trailing section comment
extra: keep-me
`)
	if err := SaveAI(path, AIConfig{Provider: "anthropic", KeySource: "keychain", Model: "claude-opus-5-5"}); err != nil {
		t.Fatal(err)
	}
	got := readFleet(t, path)
	for _, want := range []string{"# my fleet\n", "  - name: a # inline comment\n", "\n# trailing section comment\nextra: keep-me\n", "ai:\n  provider: anthropic\n  model: claude-opus-5-5\n  keySource: keychain\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sk-old") || strings.Contains(got, "backend:") {
		t.Fatalf("the legacy key or spelling survived a keychain save:\n%s", got)
	}
	cfg, err := Load(path)
	if err != nil || cfg.AI.Provider != "anthropic" || cfg.AI.KeySource != "keychain" {
		t.Fatalf("reload: %+v %v", cfg.AI, err)
	}
}

func TestSaveAI_AppendsWhenAbsentAndQuotesValues(t *testing.T) {
	path := writeFleet(t, "clusters:\n  - name: a\n")
	err := SaveAI(path, AIConfig{Provider: "openaiCompatible", Model: "llama3.1: 8b # not a comment", BaseURL: "http://192.168.1.10:11434", KeySource: "none"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AI.Model != "llama3.1: 8b # not a comment" || len(cfg.Clusters) != 1 {
		t.Fatalf("value not quoted safely: %+v", cfg.AI)
	}
}

// Only keySource legacy may keep a key in the file.
func TestSaveAI_KeyOnlyForLegacy(t *testing.T) {
	path := writeFleet(t, "clusters:\n  - name: a\n")
	if err := SaveAI(path, AIConfig{Provider: "anthropic", KeySource: "keychain", APIKey: "sk-should-not-land"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFleet(t, path), "sk-should-not-land") {
		t.Fatal("a keychain save wrote a key into fleet.yaml")
	}
	if err := SaveAI(path, AIConfig{Provider: "anthropic", KeySource: "legacy", APIKey: "sk-kept"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFleet(t, path), "apiKey: sk-kept") {
		t.Fatal("a legacy save must keep its key")
	}
}

func TestSaveAI_InvalidSettingsWriteNothing(t *testing.T) {
	body := "clusters:\n  - name: a\n"
	path := writeFleet(t, body)
	if err := SaveAI(path, AIConfig{Provider: "wat"}); err == nil {
		t.Fatal("an unknown provider must be refused")
	}
	if err := SaveAI(path, AIConfig{Provider: "openaiCompatible"}); err == nil {
		t.Fatal("a local server without a model must be refused")
	}
	if readFleet(t, path) != body {
		t.Fatal("a refused save changed the file")
	}
	if err := SaveAI(filepath.Join(t.TempDir(), "none.yaml"), AIConfig{Provider: "off"}); err == nil || !strings.Contains(err.Error(), "add a cluster first") {
		t.Fatalf("a missing file: %v", err)
	}
}

// An edit made on disk meanwhile is refused, not overwritten (spec §2.3).
func TestSaveAI_RefusesAnEditMadeMeanwhile(t *testing.T) {
	path := writeFleet(t, "clusters:\n  - name: a\n")
	edited := "clusters:\n  - name: a\n  - name: b\n"
	beforeReplace = func() { _ = os.WriteFile(path, []byte(edited), 0o644) }
	defer func() { beforeReplace = nil }()
	if err := SaveAI(path, AIConfig{Provider: "off"}); !errors.Is(err, ErrFleetChangedOnDisk) {
		t.Fatalf("want ErrFleetChangedOnDisk, got %v", err)
	}
	if readFleet(t, path) != edited {
		t.Fatal("the on-disk edit was overwritten")
	}
}

// SaveAI and AppendClusters share one lock: concurrent writers lose nothing.
func TestSaveAI_SerialisedWithAppendClusters(t *testing.T) {
	path := writeFleet(t, "clusters:\n  - name: seed\n")
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			errs <- AppendClusters(path, []string{"ctx-" + string(rune('a'+i))})
		}(i)
		go func(i int) {
			defer wg.Done()
			errs <- SaveAI(path, AIConfig{Provider: "anthropic", KeySource: "keychain", Model: "m" + string(rune('a'+i))})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a serialised write failed: %v", err)
		}
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clusters) != 7 || cfg.AI == nil || cfg.AI.Provider != "anthropic" {
		t.Fatalf("lost update: %d clusters, ai %+v", len(cfg.Clusters), cfg.AI)
	}
}

func TestReplaceAIBlock_Edges(t *testing.T) {
	block := "ai:\n  provider: off\n  allowProtectedEgress: false\n"
	cases := map[string]struct{ in, want string }{
		"flow style":       {"clusters: []\nai: {backend: hosted}\nz: 1\n", "clusters: []\n" + block + "z: 1\n"},
		"last, no eol":     {"clusters: []\nai:\n  backend: local", "clusters: []\n" + block},
		"comment in block": {"ai:\n  # note\n  backend: hosted\nclusters: []\n", block + "clusters: []\n"},
		"aiExtra key kept": {"aiExtra: 1\nai:\n  backend: off\n", "aiExtra: 1\n" + block},
	}
	for name, tc := range cases {
		got, err := replaceTopLevelBlock(tc.in, "ai", block)
		if err != nil || got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q (err %v)", name, got, tc.want, err)
		}
	}
	if _, err := replaceTopLevelBlock("ai: {}\nai: {}\n", "ai", block); err == nil {
		t.Error("two top-level ai keys must be refused")
	}
}

// The decoded-document guard: a candidate may differ from the original only
// under ai. The textual replacement should never trip it; it is the last line
// of defence if it ever does.
func TestOnlyAIChanged(t *testing.T) {
	before := []byte("clusters:\n  - name: a\nai:\n  backend: hosted\n")
	if err := onlyKeyChanged(before, []byte("clusters:\n  - name: a\nai:\n  provider: off\n"), "ai"); err != nil {
		t.Fatalf("an ai-only change was refused: %v", err)
	}
	for name, after := range map[string]string{
		"cluster changed": "clusters:\n  - name: b\nai:\n  provider: off\n",
		"key added":       "clusters:\n  - name: a\nextra: 1\nai:\n  provider: off\n",
		"key removed":     "ai:\n  provider: off\n",
	} {
		if err := onlyKeyChanged(before, []byte(after), "ai"); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// AI A3 §6: proposeActions is on by default, round-trips when turned off,
// and an unset value stays unset (the default) when written.
func TestSaveAI_ProposeActions(t *testing.T) {
	if !(&AIConfig{}).UsesProposeActions() || !(*AIConfig)(nil).UsesProposeActions() {
		t.Fatal("on by default")
	}
	path := writeFleet(t, "clusters:\n  - name: a\n")
	off := false
	if err := SaveAI(path, AIConfig{Provider: "anthropic", KeySource: "keychain", ProposeActions: &off}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFleet(t, path), "proposeActions: false") {
		t.Fatalf("written:\n%s", readFleet(t, path))
	}
	cfg, err := Load(path)
	if err != nil || cfg.AI.UsesProposeActions() {
		t.Fatalf("reload: %+v %v", cfg.AI, err)
	}
	if err := SaveAI(path, AIConfig{Provider: "anthropic", KeySource: "keychain"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFleet(t, path), "proposeActions") {
		t.Fatal("an unset value is the default and is not written")
	}
	if AIEqual(AIConfig{ProposeActions: &off}, AIConfig{}) {
		t.Fatal("the setting changes behaviour, so it counts in AIEqual")
	}
}

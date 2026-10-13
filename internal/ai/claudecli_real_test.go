//go:build !windows

package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClaudeContract_RealBinary repeats the 2026-10-13 review evidence
// against the installed reviewed binary (spec §1.5). It is opt-in
// (KLYX_CLAUDE_CONTRACT=1; KLYX_CLAUDE_BIN overrides the binary) because it
// runs the owner's Claude Code. It makes no model call and uses no login:
// HOME is a throwaway dir, and the helper runs get a throwaway
// CLAUDE_CONFIG_DIR, which also re-keys the keychain entry so no sign-in is
// found.
func TestClaudeContract_RealBinary(t *testing.T) {
	if os.Getenv("KLYX_CLAUDE_CONTRACT") != "1" {
		t.Skip("set KLYX_CLAUDE_CONTRACT=1 to check the installed Claude Code against its reviewed contract")
	}
	bin := os.Getenv("KLYX_CLAUDE_BIN")
	if bin == "" {
		home, _ := os.UserHomeDir()
		bin = filepath.Join(home, ".local", "share", "claude", "versions", reviewedClaudeContracts[0].Version)
	}
	home := t.TempDir()
	tmp := t.TempDir()
	p := newClaudeCLI(bin, DefaultAnthropicModel)
	p.hashes = &exeHashCache{m: map[string]exeHashEntry{}}
	p.store = &claudeAdmissionStore{}
	p.tmpDir = tmp
	p.environ = func() []string {
		return []string{"HOME=" + home, "PATH=/usr/bin:/bin", "USER=" + os.Getenv("USER"), "TMPDIR=" + tmp, "LANG=C"}
	}
	exe, err := p.executable()
	if err != nil {
		t.Fatalf("the installed binary is not the reviewed build: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := p.version(ctx, exe); err != nil {
		t.Fatal(err)
	}
	if err := p.flagProbe(ctx, exe); err != nil {
		t.Fatal(err)
	}

	// The preflight helpers run no user-settings command, with or without
	// the root switches, and with them auth status ignores user settings.
	cfg := filepath.Join(home, "cfg")
	sentinels := filepath.Join(home, "sentinels")
	os.MkdirAll(cfg, 0o700)
	os.MkdirAll(sentinels, 0o700)
	touch := func(name string) string { return "touch " + filepath.Join(sentinels, name) }
	settings, _ := json.Marshal(map[string]any{
		"apiKeyHelper":        touch("apiKeyHelper") + "; echo sk-ant-fake",
		"otelHeadersHelper":   touch("otel") + "; echo {}",
		"awsAuthRefresh":      touch("awsAuthRefresh"),
		"awsCredentialExport": touch("awsCredentialExport") + "; echo {}",
		"statusLine":          map[string]any{"type": "command", "command": touch("statusLine")},
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": touch("SessionStart")}}}},
			"SessionEnd":   []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": touch("SessionEnd")}}}},
		},
		"env": map[string]any{"KLYX_PROBE_ENV": "1"},
	})
	os.WriteFile(filepath.Join(cfg, "settings.json"), settings, 0o600)
	env := append(claudeEnv(p.environ()), "CLAUDE_CONFIG_DIR="+cfg)
	run := func(args ...string) []byte {
		dir, err := p.callDir()
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		r := runContained(ctx, runSpec{exe: exe.Path, args: args, env: env, dir: dir, stdoutMax: claudeHelperOutMax, stderrMax: claudeStderrMax, deadline: claudeDocDeadline})
		if r.startErr != nil || r.timedOut || r.stdoutOver {
			t.Fatalf("claude %v: %+v", args, r)
		}
		return r.stdout
	}
	authMethod := func(out []byte) string {
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("auth status: %v: %s", err, out)
		}
		s, _ := m["authMethod"].(string)
		return s
	}
	if got := authMethod(run("auth", "status", "--json")); got != "api_key_helper" {
		t.Fatalf("without the switches auth status reads user settings: authMethod %q", got)
	}
	if got := authMethod(run("--restricted", "--safe-mode", "auth", "status", "--json")); got != "none" {
		t.Fatalf("with the switches auth status must ignore user settings: authMethod %q", got)
	}
	doc := run("--restricted", "--safe-mode", "doctor")
	run("doctor")
	c := exe.Contract
	for _, want := range []string{"Running: native (" + c.Version + ")", "Platform: " + c.GOOS + "-" + c.GOARCH, "Managed settings (remote): ", "Organization policy: "} {
		if !strings.Contains(string(doc), want) {
			t.Fatalf("doctor no longer prints %q:\n%s", want, doc)
		}
	}
	if err := parseClaudeDoctor(doc, c); err == nil {
		t.Fatal("doctor without a sign-in must not pass the Pro/Max check")
	}
	if entries, _ := os.ReadDir(sentinels); len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("a preflight helper ran user-settings commands: %v - this version's contract fails", names)
	}
}

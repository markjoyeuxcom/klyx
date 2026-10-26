package ai

import (
	"os"
	"reflect"
	"testing"
)

// Settings S1 §3.3: the Claude Code CLI's environment through envread is the
// old claudeEnv(os.Environ()) exactly - its allowlist, LC_*, then the fixed
// lockdown values - and never "inherit all".
func TestClaudeEnvMatchesTheOldAllowlist(t *testing.T) {
	t.Setenv("LC_KLYXPARITY", "x")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-pass")
	t.Setenv("CLAUDE_CONFIG_DIR", "/must/not/pass")
	t.Setenv("AWS_PROFILE", "must-not-pass")
	if got, old := claudeEnv(claudeEnviron()), claudeEnv(os.Environ()); !reflect.DeepEqual(got, old) {
		t.Fatalf("new %v\nold %v", got, old)
	}
	for _, kv := range claudeEnviron() {
		if kv == "ANTHROPIC_API_KEY=must-not-pass" || kv == "AWS_PROFILE=must-not-pass" {
			t.Fatalf("the walk took %s", kv)
		}
	}
	if p := newClaudeCLI("", ""); p.envValue("LC_KLYXPARITY") != "x" {
		t.Fatal("the provider reads through the walk")
	}
}

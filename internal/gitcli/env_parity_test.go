package gitcli

import (
	"os"
	"strings"
	"testing"
)

// Settings S1 §3.3: ProcessEnv reads the same allowlist the old
// OwnerEnv(os.Getenv, os.Environ()) did - the six named values and LANG and
// LC_* - and nothing else.
func TestProcessEnvMatchesTheOldAllowlist(t *testing.T) {
	t.Setenv("LC_KLYXPARITY", "x")
	t.Setenv("LANGUAGE", "en")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/klyx-parity-agent")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/klyx-parity-xdg")
	t.Setenv("GIT_DIR", "/evil")
	old := Env{Home: os.Getenv("HOME"), Path: os.Getenv("PATH"), User: os.Getenv("USER"), Tmpdir: os.Getenv("TMPDIR"),
		SSHAuthSock: os.Getenv("SSH_AUTH_SOCK"), XDGConfigHome: os.Getenv("XDG_CONFIG_HOME")}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LANG=") || strings.HasPrefix(kv, "LC_") {
			old.Locale = append(old.Locale, kv)
		}
	}
	got := ProcessEnv()
	if strings.Join(got.isolated("/g", "", false), "\n") != strings.Join(old.isolated("/g", "", false), "\n") || len(got.Locale) != len(old.Locale) {
		t.Fatalf("new %+v\nold %+v", got, old)
	}
	if !contains(got.Locale, "LC_KLYXPARITY=x") {
		t.Fatalf("locale %v", got.Locale)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

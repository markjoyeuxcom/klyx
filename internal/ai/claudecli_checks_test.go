package ai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.296", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// authWith returns the synthesized Max sign-in with members changed: a nil
// value deletes the member.
func authWith(t *testing.T, change map[string]any) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(fixture(t, "auth-status-max.synth.json"), &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range change {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return append(b, '\n')
}

func TestParseClaudeAuth(t *testing.T) {
	const home = "/HOME"
	acct, err := parseClaudeAuth(fixture(t, "auth-status-max.synth.json"), home)
	if err != nil || len(acct.identity) != 64 {
		t.Fatalf("a Max sign-in is admitted, got (%+v, %v)", acct, err)
	}
	pro, err := parseClaudeAuth(authWith(t, map[string]any{"subscriptionType": "pro"}), home)
	if err != nil || pro.identity == acct.identity {
		t.Fatalf("a Pro sign-in is admitted with its own identity, got (%+v, %v)", pro, err)
	}
	other, _ := parseClaudeAuth(authWith(t, map[string]any{"email": "someone@example.test"}), home)
	if other.identity == acct.identity {
		t.Fatal("another account must have another identity")
	}
	// A newer build may add a member; one that names no policy is ignored
	// and changes nothing about the identity.
	newer, err := parseClaudeAuth(authWith(t, map[string]any{"billingTier": "standard"}), home)
	if err != nil || newer.identity != acct.identity {
		t.Fatalf("a new non-policy member is admitted, got (%+v, %v)", newer, err)
	}

	refused := []struct {
		name string
		out  []byte
		want string
	}{
		{"captured: API key helper", fixture(t, "auth-status-apikeyhelper.captured.json"), "API key"},
		{"captured: not signed in", fixture(t, "auth-status-locked-nologin.captured.json"), "missing"},
		{"Team", authWith(t, map[string]any{"subscriptionType": "team"}), "Team or Enterprise"},
		{"Enterprise", authWith(t, map[string]any{"subscriptionType": "enterprise"}), "Team or Enterprise"},
		{"unknown subscription", authWith(t, map[string]any{"subscriptionType": "ultra"}), "only Pro and Max"},
		{"third-party provider", authWith(t, map[string]any{"apiProvider": "bedrock"}), "first-party"},
		{"API key auth", authWith(t, map[string]any{"authMethod": "api_key"}), "claude.ai"},
		{"signed out", authWith(t, map[string]any{"loggedIn": false}), "not signed in"},
		{"policy-forced login", authWith(t, map[string]any{"forcedLoginMethod": "claudeai"}), "administrator"},
		{"policy-limited providers", authWith(t, map[string]any{"allowedProviders": []string{"firstParty"}}), "administrator"},
		{"a new field that names policy", authWith(t, map[string]any{"teamPolicy": "x"}), "may be administrator policy"},
		{"a new field that names a restriction", authWith(t, map[string]any{"loginRestrictions": []string{"x"}}), "may be administrator policy"},
		{"missing field", authWith(t, map[string]any{"orgId": nil}), "missing"},
		{"another config dir", authWith(t, map[string]any{"configDirectory": "/elsewhere"}), "configuration directory"},
		{"no account id", authWith(t, map[string]any{"orgId": ""}), "identified"},
		{"duplicate key", []byte(`{"loggedIn":true,"loggedIn":false}`), "duplicate"},
		{"loggedIn null", authWith(t, map[string]any{"loggedIn": json.RawMessage("null")}), "not signed in"},
		{"loggedIn as a string", authWith(t, map[string]any{"loggedIn": "true"}), "not signed in"},
		{"analyticsDisabled null", authWith(t, map[string]any{"analyticsDisabled": json.RawMessage("null")}), "analyticsDisabled"},
		{"analyticsDisabled as a string", authWith(t, map[string]any{"analyticsDisabled": "true"}), "analyticsDisabled"},
		{"subscription null", authWith(t, map[string]any{"subscriptionType": json.RawMessage("null")}), "only Pro and Max"},
		{"projectsDirectory null", authWith(t, map[string]any{"projectsDirectory": json.RawMessage("null")}), "projectsDirectory"},
		{"trailing data", append(fixture(t, "auth-status-max.synth.json"), []byte(`{"loggedIn":false}`)...), "after"},
		{"not JSON", []byte("Logged in as owner\n"), "could not be read"},
		{"empty", nil, "could not be read"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseClaudeAuth(tc.out, home)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal containing %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "owner@example.test") {
				t.Fatal("a refusal must not echo the account's email")
			}
		})
	}
}

func TestParseClaudeDoctor(t *testing.T) {
	c := claudeBuild{Version: "2.1.296", GOOS: "darwin", GOARCH: "arm64"}
	good := string(fixture(t, "doctor-promax.synth.txt"))
	if err := parseClaudeDoctor([]byte(good), c); err != nil {
		t.Fatalf("the Pro/Max doctor output is admitted: %v", err)
	}
	refused := []struct {
		name string
		out  string
		want string
	}{
		{"captured: no credentials", string(fixture(t, "doctor-locked-nologin.captured.txt")), "does not know"},
		{"remote policy loaded", strings.Replace(good, claudeRemotePolicyLine, "Managed settings (remote): loaded (3 keys)", 1), "does not know"},
		{"remote policy checking", strings.Replace(good, claudeRemotePolicyLine, "Managed settings (remote): checking…", 1), "does not know"},
		{"org policy applies", strings.Replace(good, claudeOrgPolicyLine, "Organization policy: applied", 1), "does not know"},
		{"remote line missing", strings.Replace(good, claudeRemotePolicyLine+"\n", "", 1), "did not confirm"},
		{"remote line twice", strings.Replace(good, claudeRemotePolicyLine, claudeRemotePolicyLine+"\n"+claudeRemotePolicyLine, 1), "more than once"},
		{"hyphen instead of em dash", strings.Replace(good, "—", "-", 1), "does not know"},
		{"a new local policy line", good + "Managed settings (local): /Library/Application Support/ClaudeCode\n", "does not know"},
		{"another version", strings.Replace(good, "Running: native (2.1.296)", "Running: native (2.1.297)", 1), "did not confirm"},
		{"another platform", strings.Replace(good, "Platform: darwin-arm64", "Platform: linux-x64", 1), "did not confirm"},
		{"truncated", good[:len(good)/3], "cannot confirm"},
		{"not UTF-8", "\xff\xfe" + good, "cannot read"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			err := parseClaudeDoctor([]byte(tc.out), c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal containing %q, got %v", tc.want, err)
			}
		})
	}
}

// The environment is built from an allowlist: nothing the parent has passes
// unless it is named.
func TestClaudeEnv(t *testing.T) {
	parent := []string{
		"HOME=/h", "PATH=/bin", "TMPDIR=/t", "LANG=en_GB.UTF-8", "LC_ALL=C", "USER=u", "LOGNAME=u",
		"KUBECONFIG=/k", "ANTHROPIC_API_KEY=sk-ant-x", "ANTHROPIC_BASE_URL=https://evil", "CLAUDE_CONFIG_DIR=/c",
		"CLAUDE_CODE_REMOTE_SETTINGS_PATH=/r", "CLAUDE_CODE_MANAGED_SETTINGS_PATH=/m", "CLAUDE_CODE_USE_BEDROCK=1",
		"NODE_OPTIONS=--require /x", "OTEL_EXPORTER_OTLP_ENDPOINT=https://o", "HTTPS_PROXY=http://p", "KLYX_X=1",
		"AWS_PROFILE=p", "AZURE_CLIENT_SECRET=s", "GOOGLE_APPLICATION_CREDENTIALS=/g", "CLAUDE_CODE_DISABLE_ATTACHMENTS=0",
	}
	got := map[string]string{}
	for _, kv := range claudeEnv(parent) {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Fatalf("%s set twice", k)
		}
		got[k] = v
	}
	for _, k := range []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "USER", "LOGNAME"} {
		if got[k] == "" {
			t.Errorf("%s should pass through", k)
		}
	}
	for _, kv := range claudeFixedEnv {
		k, v, _ := strings.Cut(kv, "=")
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != 7+len(claudeFixedEnv) {
		t.Fatalf("unexpected variables passed: %v", got)
	}
}

// The prompt never appears in argv: only a file path and the model.
func TestClaudeArgv(t *testing.T) {
	argv := claudeArgv("/tmp/x/system.txt", "claude-opus-5-5")
	joined := strings.Join(argv, " ")
	for _, want := range []string{"-p", "--tools", "--disallowedTools *", "--restricted", "--safe-mode", "--strict-mcp-config", `{"mcpServers":{}}`, "--disable-slash-commands", "--no-session-persistence", "--system-prompt-file /tmp/x/system.txt", "--model claude-opus-5-5", "--output-format stream-json", "--verbose"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q", want)
		}
	}
	if a, b := claudeArgvHash("a"), claudeArgvHash("b"); a == b || a != claudeArgvHash("a") {
		t.Fatal("the argv hash identifies the configuration")
	}
}

func TestNativeMagic(t *testing.T) {
	for _, h := range [][]byte{{0xcf, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe}, {0x7f, 'E', 'L', 'F'}, {'M', 'Z', 0, 0}} {
		if !nativeMagic(h) {
			t.Errorf("%x is native", h)
		}
	}
	for _, h := range [][]byte{[]byte("#!/b"), []byte("#!/usr/bin/env node"), {0, 0, 0, 0}, {0xcf}} {
		if nativeMagic(h) {
			t.Errorf("%q is not native", h)
		}
	}
}

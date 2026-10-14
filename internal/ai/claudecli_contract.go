package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// claudeBuild is the Claude Code build a call runs: the version the binary
// reports and the platform it runs on. Since 2026-10-14 (owner decision,
// spec §1.5) Klyx no longer pins exact reviewed builds - Claude Code updates
// itself every day or two, and every update broke the provider. Klyx accepts
// any build at or above claudeMinVersion on a supported platform once Test
// has checked that build on this machine (claudeBuildStore); a build that
// misbehaves loses that standing.
type claudeBuild struct {
	Version string
	GOOS    string
	GOARCH  string
}

// claudeMinVersion is the oldest build Klyx accepts: the first one whose
// flags, environment switches and output were read (2026-10-13).
const claudeMinVersion = "2.1.296"

// claudePlatforms are the platforms whose policy locations and doctor output
// Klyx has checked. Others are refused before anything runs.
var claudePlatforms = []string{"darwin/arm64"}

// The doctor lines that say no remote or organization policy applies to a
// personal Pro or Max sign-in, verbatim as 2.1.296 prints them (U+2014).
// Doctor is a diagnostic, not a documented machine interface: a build that
// rewords them is refused until Klyx learns the new wording.
const (
	claudeRemotePolicyLine = "Managed settings (remote): not fetched — requires an Enterprise or Team subscription"
	claudeOrgPolicyLine    = "Organization policy: not applicable to Pro and Max accounts"
)

func (c claudeBuild) platform() string    { return c.GOOS + "/" + c.GOARCH }

// doctorLines are the lines doctor must print exactly once each.
func (c claudeBuild) doctorLines() []string {
	return []string{
		"Running: native (" + c.Version + ")",
		"Platform: " + c.GOOS + "-" + c.GOARCH,
		claudeRemotePolicyLine,
		claudeOrgPolicyLine,
	}
}

// parseClaudeVersion reads `claude --version`: exactly "X.Y.Z (Claude
// Code)", at or above claudeMinVersion.
func parseClaudeVersion(out string) (string, error) {
	m := claudeVersionRe.FindStringSubmatch(strings.TrimRight(out, "\n"))
	if m == nil {
		return "", fmt.Errorf("claude --version printed %q, not a Claude Code version", label(strings.TrimSpace(out)))
	}
	if versionLess(m[1], claudeMinVersion) {
		return "", fmt.Errorf("Claude Code %s is older than %s, the oldest build Klyx runs; update Claude Code", m[1], claudeMinVersion)
	}
	return m[1], nil
}

var claudeVersionRe = regexp.MustCompile(`^(\d{1,4}\.\d{1,4}\.\d{1,6}) \(Claude Code\)$`)

// versionLess compares two X.Y.Z versions numerically.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// claudeLockdown are the switches that turn Claude Code into a plain
// completion backend: no tools, no MCP, no user, project or local settings,
// no CLAUDE.md, skills, plugins, hooks or slash commands. The preflight
// helpers get the root switches too, before their subcommand.
var claudeRootLockdown = []string{"--restricted", "--safe-mode"}

// claudeArgv is the production argv after the executable. The prompt never
// appears in it: the system text is a 0600 file and the evidence goes on
// stdin.
func claudeArgv(systemFile, model string) []string {
	return []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--tools", "", "--disallowedTools", "*",
		"--restricted", "--safe-mode",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--disable-slash-commands", "--no-session-persistence",
		"--system-prompt-file", systemFile,
		"--model", model,
	}
}

// claudeArgvHash identifies the lockdown configuration admission bound to:
// the argv with the per-call system file left out.
func claudeArgvHash(model string) string {
	h := sha256.Sum256([]byte(strings.Join(claudeArgv("<system>", model), "\x00")))
	return hex.EncodeToString(h[:])
}

// claudeFixedEnv is set on every process Klyx starts from the Claude Code
// binary. CLAUDE_CODE_DISABLE_ATTACHMENTS matters most: without it, @path
// text in a prompt is expanded into that file's contents even with every
// tool off, so cluster text naming @~/.kube/config would read the
// kubeconfig.
var claudeFixedEnv = []string{
	"CLAUDE_CODE_DISABLE_ATTACHMENTS=1",
	"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1",
	"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
	"CLAUDE_CODE_SKIP_PROMPT_HISTORY=1",
	"ENABLE_CLAUDEAI_MCP_SERVERS=false",
	"CLAUDE_AGENT_SDK_DISABLE_BUILTIN_AGENTS=1",
	"DISABLE_AUTOUPDATER=1",
	"DISABLE_TELEMETRY=1",
	"DISABLE_ERROR_REPORTING=1",
	"NO_COLOR=1",
	"TERM=dumb",
}

// claudeEnv is the whole environment of every Claude Code process: an
// allowlist built from scratch, never the parent's environment minus a
// denylist. Nothing else passes - not ANTHROPIC_*, other CLAUDE_CODE_*,
// CLAUDE_CONFIG_DIR (it also re-keys the keychain login), the remote or
// managed settings paths (they inject policy), NODE_OPTIONS, OTEL_*,
// proxies, KUBECONFIG or cloud credentials.
func claudeEnv(environ []string) []string {
	var env []string
	for _, kv := range environ {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case k == "HOME", k == "PATH", k == "TMPDIR", k == "LANG", k == "USER", k == "LOGNAME", strings.HasPrefix(k, "LC_"):
			env = append(env, kv)
		}
	}
	return append(env, claudeFixedEnv...)
}

// claudePolicyPaths are the managed-policy locations for a platform (spec
// §1.5 check 2). Any of them existing, or any that cannot be checked, refuses
// the call. Only supported platforms (claudePlatforms) reach this.
func claudePolicyPaths(goos, user string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/Library/Application Support/ClaudeCode",
			"/Library/Managed Preferences/com.anthropic.claudecode.plist",
			"/Library/Managed Preferences/" + user + "/com.anthropic.claudecode.plist",
		}
	case "linux":
		return []string{"/etc/claude-code"}
	}
	return nil
}

// claudeCachePaths are the policy caches that must be absent (check 3).
func claudeCachePaths(home string) []string {
	return []string{
		filepath.Join(home, ".claude", "remote-settings.json"),
		filepath.Join(home, ".claude", "policy-limits.json"),
	}
}

// ClaudeCLIModels are the models offered in the Settings picker for the
// Claude Code provider; free text is allowed. Each must be a full model ID:
// the run's init event must report exactly the model asked for.
var ClaudeCLIModels = []ModelInfo{
	{ID: "claude-opus-5-5", DisplayName: "Claude Opus 5.5"},
	{ID: "claude-sonnet-5-5", DisplayName: "Claude Sonnet 5.5"},
	{ID: "claude-fable-5-1", DisplayName: "Claude Fable 5.1"},
	{ID: "claude-haiku-5-5", DisplayName: "Claude Haiku 5.5"},
}

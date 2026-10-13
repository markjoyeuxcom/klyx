package ai

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// claudeAuthKeys are the members `claude auth status --json` prints for a
// claude.ai login on a reviewed version (2.1.296), and only those: an
// unknown member is a schema change and refuses (spec §1.5 check 4).
// apiKeySource, forcedLoginMethod and allowedProviders are absent by
// requirement - the last two come from admin policy.
var claudeAuthKeys = []string{
	"loggedIn", "authMethod", "apiProvider", "analyticsDisabled", "projectsDirectory",
	"configDirectory", "email", "orgId", "orgName", "subscriptionType",
}

// claudeAccount is what Klyx keeps of the login: its identity as a hash.
// The email and organization never leave this function.
type claudeAccount struct {
	identity string
}

// parseClaudeAuth checks `auth status` output against the pinned schema and
// returns the account identity. A Team or Enterprise account, API-key auth,
// a third-party provider, an unknown or missing member, or output that is
// not exactly one JSON object refuses.
func parseClaudeAuth(out []byte, home string) (claudeAccount, error) {
	m, err := strictObject(out)
	if err != nil {
		return claudeAccount{}, fmt.Errorf("Claude Code's sign-in status could not be read (%v)", err)
	}
	for _, k := range []string{"apiKeySource", "forcedLoginMethod", "allowedProviders"} {
		if _, ok := m[k]; ok {
			if k == "apiKeySource" {
				return claudeAccount{}, errors.New("Claude Code is using an API key, not a claude.ai sign-in; use Klyx's Claude API provider for an API key")
			}
			return claudeAccount{}, errors.New("Claude Code reports an administrator sign-in policy; Klyx cannot inspect it, so it refuses this CLI")
		}
	}
	want := map[string]bool{}
	for _, k := range claudeAuthKeys {
		want[k] = true
		if _, ok := m[k]; !ok {
			return claudeAccount{}, fmt.Errorf("Claude Code's sign-in status is missing %q; this output is outside the reviewed contract", k)
		}
	}
	var unknown []string
	for k := range m {
		if !want[k] {
			unknown = append(unknown, label(k))
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return claudeAccount{}, fmt.Errorf("Claude Code's sign-in status has fields outside the reviewed contract (%s)", strings.Join(unknown, ", "))
	}

	if v, ok := rawBool(m, "loggedIn"); !ok || !v {
		return claudeAccount{}, errors.New("Claude Code is not signed in; run `claude` once and sign in with a Pro or Max account")
	}
	if v, _ := rawString(m, "authMethod"); v != "claude.ai" {
		return claudeAccount{}, fmt.Errorf("Claude Code signs in with %q, not a claude.ai account; only a personal Pro or Max sign-in is admitted", label(v))
	}
	if v, _ := rawString(m, "apiProvider"); v != "firstParty" {
		return claudeAccount{}, fmt.Errorf("Claude Code is routed to %q, not to Anthropic directly; Klyx admits only the first-party API", label(v))
	}
	sub, _ := rawString(m, "subscriptionType")
	switch sub {
	case "pro", "max":
	case "team", "enterprise":
		return claudeAccount{}, errors.New("Claude Code is signed in to a Team or Enterprise account; its organisation can push policy Klyx cannot inspect, so Klyx refuses it. Sign in with a personal Pro or Max account, or use an API provider")
	default:
		return claudeAccount{}, fmt.Errorf("Claude Code reports subscription %q; only Pro and Max are admitted", label(sub))
	}
	if _, ok := rawBool(m, "analyticsDisabled"); !ok {
		return claudeAccount{}, errors.New("Claude Code's sign-in status has an unexpected analyticsDisabled value")
	}
	if _, ok := rawString(m, "projectsDirectory"); !ok {
		return claudeAccount{}, errors.New("Claude Code's sign-in status has an unexpected projectsDirectory value")
	}
	if v, _ := rawString(m, "configDirectory"); filepath.Clean(v) != filepath.Join(home, ".claude") {
		return claudeAccount{}, errors.New("Claude Code is reading a configuration directory other than ~/.claude")
	}
	if _, ok := rawString(m, "orgName"); !ok && !rawNull(m, "orgName") {
		return claudeAccount{}, errors.New("Claude Code's sign-in status has an unexpected orgName value")
	}
	email, _ := rawString(m, "email")
	org, _ := rawString(m, "orgId")
	if email == "" || org == "" {
		return claudeAccount{}, errors.New("Claude Code's account could not be identified (no account or organization ID)")
	}
	h := sha256.Sum256([]byte(strings.Join([]string{"claude.ai", "firstParty", sub, org, email}, "\x00")))
	return claudeAccount{identity: hex.EncodeToString(h[:])}, nil
}

// parseClaudeDoctor checks doctor's output against the reviewed version's
// pinned lines (spec §1.5 check 6). Each pinned line must appear exactly
// once; any other line whose label mentions managed settings or policy
// refuses, as does output that is not UTF-8. Doctor is a diagnostic, so this
// parse is never generalised beyond the reviewed version.
func parseClaudeDoctor(out []byte, c claudeContract) error {
	if !utf8.Valid(out) {
		return errors.New("claude doctor printed output Klyx cannot read")
	}
	pinned := c.doctorLines()
	count := map[string]int{}
	policy := map[string]bool{c.RemotePolicyLine: true, c.OrgPolicyLine: true}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, " \t\r")
		count[line]++
		lbl, _, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		l := strings.ToLower(lbl)
		if (strings.Contains(l, "managed") || strings.Contains(l, "polic")) && !policy[line] {
			return fmt.Errorf("claude doctor reports %q in a form outside the reviewed contract; Klyx cannot confirm that no policy applies", label(strings.ReplaceAll(lbl, " ", "_")))
		}
	}
	for _, want := range pinned {
		switch n := count[want]; {
		case n == 0:
			return fmt.Errorf("claude doctor did not confirm %q; Klyx cannot confirm that no policy applies", want)
		case n > 1:
			return fmt.Errorf("claude doctor printed %q more than once; Klyx cannot confirm that no policy applies", want)
		}
	}
	return nil
}

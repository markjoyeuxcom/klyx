package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// cliContractError is a Claude Code run whose output broke the reviewed
// contract. Revoke marks the cases §1.4 treats as the lockdown failing - a
// tool, hook or unknown event - which revoke admission until the next Test.
// The message names only event types and fields, bounded by label(), never
// vendor prose.
type cliContractError struct {
	msg    string
	base   error
	revoke bool
}

func (e *cliContractError) Error() string { return e.msg }
func (e *cliContractError) Unwrap() error { return e.base }

// toolEvent is a run that attempted a tool or returned an event outside the
// contract: the answer is discarded and admission revoked.
func toolEvent(what string) error {
	return &cliContractError{
		msg:    "Claude Code attempted a tool or returned an unknown event (" + what + "); Klyx discarded the answer and revoked the CLI's admission until the next Test in Settings > AI",
		base:   ErrUnexpectedOutput,
		revoke: true,
	}
}

// contractBreak is output that is malformed or contradicts itself: the answer
// is discarded and admission revoked, since the reviewed contract no longer
// describes this CLI.
func contractBreak(what string) error {
	return &cliContractError{
		msg:    "Claude Code's output broke its reviewed contract (" + what + "); Klyx discarded the answer and revoked the CLI's admission until the next Test in Settings > AI",
		base:   ErrUnexpectedOutput,
		revoke: true,
	}
}

func unconfirmed(what string) error {
	return fmt.Errorf("%w (Claude Code: %s)", ErrUnconfirmed, what)
}

// streamWant is what a run must report about itself.
type streamWant struct {
	Version        string   // the reviewed claude_code_version
	Model          string   // the model asked for
	Cwd            string   // the call dir, symlinks resolved
	BuiltinPlugins []string // the reviewed version's built-in plugins
}

// emptyInitLists are the init members that must be absent or empty: what the
// lockdown removes. plugins is checked separately (checkPlugins).
var emptyInitLists = []string{"tools", "mcp_servers", "slash_commands", "skills", "agents"}

// parseClaudeStream applies the per-call output contract (spec §1.5) to a
// run's stdout and returns the answer. It accepts only an explicitly
// confirmed, complete, non-refusal, text-only answer: one system/init first,
// then only assistant, rate_limit_event and system/api_retry events, then one
// result last, whose stop and terminal reasons both say the turn completed
// and whose text matches what was streamed. Everything else discards it.
func parseClaudeStream(out []byte, w streamWant) (string, error) {
	if len(out) == 0 {
		return "", unconfirmed("no output")
	}
	if !utf8.Valid(out) {
		return "", contractBreak("output is not UTF-8")
	}
	if out[len(out)-1] != '\n' {
		return "", contractBreak("data after the last complete line")
	}
	var (
		sessionID, msgID string
		sawInit          bool
		result           map[string]json.RawMessage
		streamed         strings.Builder
	)
	for i, line := range bytes.Split(out[:len(out)-1], []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			return "", contractBreak("a blank line in the stream")
		}
		if result != nil {
			return "", contractBreak("an event after the result")
		}
		m, err := strictObject(line)
		if err != nil {
			return "", contractBreak("a malformed line: " + err.Error())
		}
		typ, _ := rawString(m, "type")
		sub, _ := rawString(m, "subtype")
		if i == 0 && (typ != "system" || sub != "init") {
			return "", contractBreak("the first event is not system/init")
		}
		switch {
		case typ == "system" && sub == "init":
			if sawInit {
				return "", contractBreak("a second system/init")
			}
			sawInit = true
			if sessionID, err = checkInit(m, w); err != nil {
				return "", err
			}
		case typ == "assistant":
			id, err := checkAssistant(m, w, sessionID, &streamed)
			if err != nil {
				return "", err
			}
			if msgID == "" {
				msgID = id
			} else if id != msgID {
				return "", contractBreak("more than one model message")
			}
		case typ == "rate_limit_event", typ == "system" && sub == "api_retry":
			// Benign: subscription rate-limit state, and a retried request.
		case typ == "system" && sub == "model_refusal_no_fallback":
			return "", refused("", "", "")
		case typ == "system" && (sub == "model_fallback" || sub == "model_refusal_fallback" || sub == "model_consent_fallback"):
			return "", &cliContractError{msg: "Claude Code switched to another model (" + label(sub) + "); Klyx discarded the answer", base: ErrUnexpectedOutput}
		case typ == "result":
			result = m
		default:
			what := label(typ)
			if sub != "" {
				what += "/" + label(sub)
			}
			return "", toolEvent(what)
		}
	}
	if result == nil {
		return "", unconfirmed("no result event")
	}
	return checkResult(result, w, sessionID, streamed.String())
}

func checkInit(m map[string]json.RawMessage, w streamWant) (string, error) {
	for _, k := range emptyInitLists {
		if !rawNull(m, k) && string(bytes.TrimSpace(m[k])) != "[]" {
			return "", toolEvent("init lists " + k)
		}
	}
	if err := checkPlugins(m, w.BuiltinPlugins); err != nil {
		return "", err
	}
	if v, _ := rawString(m, "claude_code_version"); v != w.Version {
		return "", contractBreak("init reports another Claude Code version")
	}
	if v, _ := rawString(m, "cwd"); v != w.Cwd {
		return "", contractBreak("init reports another working directory")
	}
	if v, _ := rawString(m, "apiKeySource"); v != "none" {
		return "", contractBreak("init reports an API key source")
	}
	if v, _ := rawString(m, "model"); v != w.Model {
		return "", &cliContractError{msg: "Claude Code started another model than the one configured; Klyx discarded the answer", base: ErrUnexpectedOutput}
	}
	id, _ := rawString(m, "session_id")
	if id == "" {
		return "", contractBreak("init has no session")
	}
	return id, nil
}

// checkPlugins allows only the reviewed version's own built-in plugins in
// init: each exactly {name, path "builtin", source "<name>@builtin"}, at most
// once. Anything else - an installed or claude.ai-synced plugin, an
// unreviewed built-in, a plugin load error or warning - revokes admission.
func checkPlugins(m map[string]json.RawMessage, builtins []string) error {
	for _, k := range []string{"plugin_errors", "plugin_warnings"} {
		if !rawEmpty(m, k) {
			return toolEvent("init reports " + k)
		}
	}
	if rawNull(m, "plugins") {
		return nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(m["plugins"], &list); err != nil {
		return contractBreak("init plugins is not a list")
	}
	allowed := map[string]bool{}
	for _, b := range builtins {
		allowed[b] = true
	}
	seen := map[string]bool{}
	for _, raw := range list {
		p, err := strictObject(raw)
		if err != nil {
			return contractBreak("a malformed init plugin")
		}
		name, _ := rawString(p, "name")
		path, _ := rawString(p, "path")
		source, _ := rawString(p, "source")
		extra := false
		for k := range p {
			extra = extra || (k != "name" && k != "path" && k != "source" && k != "version")
		}
		if !allowed[name] || path != "builtin" || source != name+"@builtin" || extra || seen[name] {
			return toolEvent("init lists plugin " + label(name))
		}
		seen[name] = true
	}
	return nil
}

// checkAssistant checks one assistant event and appends its text blocks to
// streamed. Thinking blocks are allowlisted reasoning: discarded, never part
// of the answer.
func checkAssistant(m map[string]json.RawMessage, w streamWant, sessionID string, streamed *strings.Builder) (string, error) {
	if !rawNull(m, "parent_tool_use_id") {
		return "", toolEvent("a subagent message")
	}
	if !rawNull(m, "error") {
		return "", contractBreak("an assistant error message")
	}
	if v, _ := rawString(m, "session_id"); v != sessionID {
		return "", contractBreak("an assistant message from another session")
	}
	raw, ok := m["message"]
	if !ok {
		return "", contractBreak("an assistant event without a message")
	}
	msg, err := strictObject(raw)
	if err != nil {
		return "", contractBreak("a malformed assistant message: " + err.Error())
	}
	if v, _ := rawString(msg, "role"); v != "assistant" {
		return "", contractBreak("an assistant message with another role")
	}
	if v, _ := rawString(msg, "model"); v != w.Model {
		return "", &cliContractError{msg: "Claude Code answered with another model than the one configured; Klyx discarded the answer", base: ErrUnexpectedOutput}
	}
	id, _ := rawString(msg, "id")
	if id == "" {
		return "", contractBreak("an assistant message without an id")
	}
	if !rawNull(msg, "stop_reason") {
		stop, _ := rawString(msg, "stop_reason")
		if err := stopReasonError(stop); err != nil {
			return "", err
		}
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(msg["content"], &blocks); err != nil {
		return "", contractBreak("an assistant message without a content list")
	}
	for _, b := range blocks {
		bt, _ := rawString(b, "type")
		switch {
		case bt == "text":
			t, ok := rawString(b, "text")
			if !ok {
				return "", contractBreak("a text block without text")
			}
			streamed.WriteString(t)
		case bt == "thinking", bt == "redacted_thinking":
		default:
			// A tool_use, server_tool_use or tool_result block, or one the
			// contract does not know: either way, not a text answer.
			return "", toolEvent("a " + label(bt) + " block")
		}
	}
	return id, nil
}

// stopReasonError maps a stop reason to its outcome: nil only for end_turn.
func stopReasonError(stop string) error {
	switch stop {
	case "end_turn":
		return nil
	case "refusal":
		return refused("", "", "")
	case "max_tokens", "model_context_window_exceeded":
		return ErrTruncated
	case "tool_use", "pause_turn", "tool_deferred":
		return toolEvent("stop reason " + label(stop))
	case "":
		return unconfirmed("no stop reason")
	default:
		return unconfirmed("stop reason " + label(stop))
	}
}

func checkResult(m map[string]json.RawMessage, w streamWant, sessionID, streamed string) (string, error) {
	// Lockdown failures first: they revoke admission whatever else the
	// result says.
	if !rawEmpty(m, "permission_denials") {
		return "", toolEvent("permission denials")
	}
	// Claude Code 2.1.296 writes subagent_stats on every result, as running
	// totals that are all zero when no subagent ran (read from the shipped
	// binary, 2026-10-14). Only a non-zero counter is subagent use.
	if !rawNull(m, "subagent_stats") && !allZero(m["subagent_stats"]) {
		return "", toolEvent("subagent use")
	}
	if !rawNull(m, "deferred_tool_use") {
		return "", toolEvent("a deferred tool use")
	}
	if !rawNull(m, "api_error_status") || !rawNull(m, "api_error") || !rawNull(m, "api_error_code") {
		return "", fmt.Errorf("Claude Code reported an API error")
	}
	if sub, _ := rawString(m, "subtype"); sub != "success" {
		return "", fmt.Errorf("Claude Code did not finish the request (%s)", label(sub))
	}
	if rawNull(m, "stop_reason") {
		return "", unconfirmed("no stop reason")
	}
	stop, _ := rawString(m, "stop_reason")
	if err := stopReasonError(stop); err != nil {
		return "", err
	}
	if tr, _ := rawString(m, "terminal_reason"); tr != "completed" {
		if tr == "" {
			return "", unconfirmed("no terminal reason")
		}
		return "", unconfirmed("terminal reason " + label(tr))
	}
	if !rawEmpty(m, "safety_stops") {
		return "", refused("safety stop", "", "")
	}
	if v, ok := rawBool(m, "is_error"); !ok || v {
		return "", contractBreak("a success result that reports an error")
	}
	if string(bytes.TrimSpace(m["num_turns"])) != "1" {
		return "", contractBreak("a result with other than one turn")
	}
	if !rawNull(m, "errors") || !rawNull(m, "structured_output") {
		return "", contractBreak("a success result with errors or structured output")
	}
	if v, _ := rawString(m, "session_id"); v != sessionID {
		return "", contractBreak("a result from another session")
	}
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(m["modelUsage"], &usage); err != nil || len(usage) == 0 {
		return "", contractBreak("a result without model usage")
	}
	for model := range usage {
		if model != w.Model {
			return "", &cliContractError{msg: "Claude Code used another model than the one configured; Klyx discarded the answer", base: ErrUnexpectedOutput}
		}
	}
	text, ok := rawString(m, "result")
	if !ok {
		return "", contractBreak("a result without text")
	}
	if strings.TrimSpace(text) == "" && strings.TrimSpace(streamed) == "" {
		return "", ErrEmpty
	}
	if text != streamed {
		return "", contractBreak("the result text differs from the streamed answer")
	}
	return joinText([]string{text})
}

// allZero reports whether raw is null, zero, an empty string or array, or an
// object whose every member is itself allZero: counters that record nothing.
func allZero(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	switch string(t) {
	case "null", "0", `""`, "[]", "{}":
		return true
	}
	if len(t) == 0 || t[0] != '{' {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(t, &obj); err != nil {
		return false
	}
	for _, v := range obj {
		if !allZero(v) {
			return false
		}
	}
	return true
}

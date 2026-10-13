package ai

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The init event is CAPTURED from the real 2.1.296 (network blocked; see
// testdata/claude-2.1.296/README.md). The assistant and result events are
// still SYNTHESIZED from the binary's emitters until a full run is captured.

const (
	tModel = "claude-opus-5-5"
	tCwd   = "/private/var/folders/x/klyx-claude-1"
	tSess  = "5d7b7d0e-1a51-4e1e-9d44-000000000001"
)

var tWant = streamWant{Version: "2.1.296", Model: tModel, Cwd: tCwd, BuiltinPlugins: reviewedClaudeContracts[0].BuiltinPlugins}

func ev(m map[string]any) map[string]any { return m }

// initEvent is the captured init, with this test's cwd and session.
func initEvent() map[string]any {
	b, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.296", "stream-offline-init.captured.jsonl"))
	if err != nil {
		panic(err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		panic(err)
	}
	m["cwd"], m["session_id"] = tCwd, tSess
	return m
}

func assistantEvent(blocks ...map[string]any) map[string]any {
	content := make([]any, len(blocks))
	for i, b := range blocks {
		content[i] = b
	}
	return ev(map[string]any{
		"type": "assistant", "parent_tool_use_id": nil, "session_id": tSess, "uuid": "u-a",
		"message": map[string]any{
			"id": "msg_01", "type": "message", "role": "assistant", "model": tModel,
			"content": content, "stop_reason": "end_turn", "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 20},
		},
	})
}

func textBlock(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func resultEvent(text string) map[string]any {
	return ev(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "duration_ms": 1200, "duration_api_ms": 1100,
		"num_turns": 1, "result": text, "stop_reason": "end_turn", "terminal_reason": "completed",
		"session_id": tSess, "total_cost_usd": 0.01, "usage": map[string]any{"input_tokens": 10, "output_tokens": 20},
		"modelUsage":         map[string]any{tModel: map[string]any{"inputTokens": 10, "outputTokens": 20}},
		"permission_denials": []any{}, "uuid": "u-r",
	})
}

const answer = "The api container keeps exiting because its readiness probe fails."

// cleanEvents is a confirmed, complete, text-only run: init, a thinking
// block, the answer, a rate-limit notice, a retry, the result.
func cleanEvents() []map[string]any {
	return []map[string]any{
		initEvent(),
		assistantEvent(map[string]any{"type": "thinking", "thinking": "Looking at the probe.", "signature": "sig"}),
		assistantEvent(textBlock(answer)),
		{"type": "rate_limit_event", "rate_limit_info": map[string]any{"status": "allowed"}, "uuid": "u-rl", "session_id": tSess},
		{"type": "system", "subtype": "api_retry", "attempt": 1, "session_id": tSess},
		resultEvent(answer),
	}
}

func stream(t *testing.T, events []map[string]any) []byte {
	t.Helper()
	var b strings.Builder
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func TestParseClaudeStream_Clean(t *testing.T) {
	got, err := parseClaudeStream(stream(t, cleanEvents()), tWant)
	if err != nil || got != answer {
		t.Fatalf("got (%q, %v)", got, err)
	}
	for _, none := range []any{[]any{}, nil} {
		e := cleanEvents()
		if none == nil {
			delete(e[0], "plugins")
		} else {
			e[0]["plugins"] = none
		}
		if _, err := parseClaudeStream(stream(t, e), tWant); err != nil {
			t.Fatalf("an init with no plugins passes: %v", err)
		}
	}
	if strings.Contains(got, "Looking at the probe") {
		t.Fatal("thinking is reasoning, never part of the answer")
	}
}

func TestParseClaudeStream_Refusals(t *testing.T) {
	type mut func([]map[string]any) []map[string]any
	set := func(i int, k string, v any) mut {
		return func(e []map[string]any) []map[string]any {
			if v == nil {
				delete(e[i], k)
			} else {
				e[i][k] = v
			}
			return e
		}
	}
	setMsg := func(i int, k string, v any) mut {
		return func(e []map[string]any) []map[string]any {
			e[i]["message"].(map[string]any)[k] = v
			return e
		}
	}
	insert := func(i int, x map[string]any) mut {
		return func(e []map[string]any) []map[string]any {
			return append(e[:i], append([]map[string]any{x}, e[i:]...)...)
		}
	}
	last := 5 // the result's index in cleanEvents
	cases := []struct {
		name   string
		m      mut
		is     error // errors.Is target; nil means a RefusedError
		revoke bool
	}{
		// Completion: only end_turn plus completed is a finished answer.
		{"max tokens", func(e []map[string]any) []map[string]any {
			setMsg(2, "stop_reason", "max_tokens")(e)
			return set(last, "stop_reason", "max_tokens")(e)
		}, ErrTruncated, false},
		{"context window", set(last, "stop_reason", "model_context_window_exceeded"), ErrTruncated, false},
		{"refusal stop", set(last, "stop_reason", "refusal"), nil, false},
		{"refusal on the message", setMsg(2, "stop_reason", "refusal"), nil, false},
		{"refusal event", insert(1, map[string]any{"type": "system", "subtype": "model_refusal_no_fallback"}), nil, false},
		{"safety stop", set(last, "safety_stops", []any{map[string]any{"kind": "x"}}), nil, false},
		{"missing stop reason", set(last, "stop_reason", nil), ErrUnconfirmed, false},
		{"null stop reason", set(last, "stop_reason", json.RawMessage("null")), ErrUnconfirmed, false},
		{"unknown stop reason", set(last, "stop_reason", "paused_for_reasons"), ErrUnconfirmed, false},
		{"missing terminal reason", set(last, "terminal_reason", nil), ErrUnconfirmed, false},
		{"unknown terminal reason", set(last, "terminal_reason", "budget_exhausted"), ErrUnconfirmed, false},
		{"no result", func(e []map[string]any) []map[string]any { return e[:last] }, ErrUnconfirmed, false},
		{"empty answer", func(e []map[string]any) []map[string]any {
			e[2] = assistantEvent(textBlock(""))
			return set(last, "result", "")(e)
		}, ErrEmpty, false},

		// Tools, hooks, subagents and unknown events revoke admission.
		{"tool_use block", func(e []map[string]any) []map[string]any {
			e[2] = assistantEvent(textBlock(answer), map[string]any{"type": "tool_use", "id": "t1", "name": "Read", "input": map[string]any{}})
			return e
		}, ErrUnexpectedOutput, true},
		{"server tool block", func(e []map[string]any) []map[string]any {
			e[2] = assistantEvent(map[string]any{"type": "server_tool_use", "id": "s1", "name": "web_search"})
			return e
		}, ErrUnexpectedOutput, true},
		{"unknown block", func(e []map[string]any) []map[string]any {
			e[2] = assistantEvent(textBlock(answer), map[string]any{"type": "image"})
			return e
		}, ErrUnexpectedOutput, true},
		{"tool stop reason", set(last, "stop_reason", "tool_use"), ErrUnexpectedOutput, true},
		{"user (tool result) event", insert(3, map[string]any{"type": "user", "message": map[string]any{"role": "user"}}), ErrUnexpectedOutput, true},
		{"hook event", insert(1, map[string]any{"type": "system", "subtype": "hook_started", "hook_name": "SessionStart"}), ErrUnexpectedOutput, true},
		{"status event", insert(1, map[string]any{"type": "system", "subtype": "status"}), ErrUnexpectedOutput, true},
		{"stream_event", insert(1, map[string]any{"type": "stream_event"}), ErrUnexpectedOutput, true},
		{"unknown type", insert(1, map[string]any{"type": "novel_thing"}), ErrUnexpectedOutput, true},
		{"permission denials", set(last, "permission_denials", []any{map[string]any{"tool_name": "Bash"}}), ErrUnexpectedOutput, true},
		{"subagent stats", set(last, "subagent_stats", map[string]any{"count": 1}), ErrUnexpectedOutput, true},
		{"subagent message", func(e []map[string]any) []map[string]any {
			e[2]["parent_tool_use_id"] = "toolu_1"
			return e
		}, ErrUnexpectedOutput, true},
		{"init lists tools", func(e []map[string]any) []map[string]any {
			e[0]["tools"] = []any{"Read"}
			return e
		}, ErrUnexpectedOutput, true},
		{"init lists MCP servers", func(e []map[string]any) []map[string]any {
			e[0]["mcp_servers"] = []any{map[string]any{"name": "x", "status": "connected"}}
			return e
		}, ErrUnexpectedOutput, true},
		{"init lists skills", set(0, "skills", []any{"pdf"}), ErrUnexpectedOutput, true},
		{"init lists agents", set(0, "agents", []any{"general-purpose"}), ErrUnexpectedOutput, true},
		// Plugins: only the reviewed version's own built-ins (the slice 5
		// smoke's first finding).
		{"a plugin synced from claude.ai", plugins(map[string]any{"name": "cowork-plugin-management", "path": "/Users/o/.claude/plugins/synced/x/cowork-plugin-management", "source": "cowork-plugin-management@synced"}), ErrUnexpectedOutput, true},
		{"an installed plugin", plugins(map[string]any{"name": "superpowers", "path": "/Users/o/.claude/plugins/cache/superpowers", "source": "superpowers@marketplace"}), ErrUnexpectedOutput, true},
		{"an unreviewed built-in", plugins(map[string]any{"name": "cc-plugin-tips", "path": "builtin", "source": "cc-plugin-tips@builtin"}), ErrUnexpectedOutput, true},
		// These replace the list, so only the path or source is wrong.
		{"a reviewed name from elsewhere", set(0, "plugins", []any{map[string]any{"name": "cc-plugin-agents-md", "path": "/tmp/evil", "source": "cc-plugin-agents-md@builtin"}}), ErrUnexpectedOutput, true},
		{"a reviewed name from a marketplace", set(0, "plugins", []any{map[string]any{"name": "cc-plugin-agents-md", "path": "builtin", "source": "cc-plugin-agents-md@evil"}}), ErrUnexpectedOutput, true},
		{"a built-in listed twice", plugins(map[string]any{"name": "cc-plugin-agents-md", "path": "builtin", "source": "cc-plugin-agents-md@builtin"}), ErrUnexpectedOutput, true},
		{"a built-in with extra members", func(e []map[string]any) []map[string]any { // replaces the list
			e[0]["plugins"] = []any{map[string]any{"name": "cc-plugin-agents-md", "path": "builtin", "source": "cc-plugin-agents-md@builtin", "hooks": map[string]any{"SessionStart": 1}}}
			return e
		}, ErrUnexpectedOutput, true},
		{"plugin load errors", set(0, "plugin_errors", []any{map[string]any{"plugin": "x"}}), ErrUnexpectedOutput, true},

		// Framing and sequence.
		{"first event not init", func(e []map[string]any) []map[string]any { return e[1:] }, ErrUnexpectedOutput, true},
		{"duplicate init", insert(1, initEvent()), ErrUnexpectedOutput, true},
		{"duplicate result", func(e []map[string]any) []map[string]any { return append(e, resultEvent(answer)) }, ErrUnexpectedOutput, true},
		{"event after result", func(e []map[string]any) []map[string]any {
			return append(e, map[string]any{"type": "rate_limit_event"})
		}, ErrUnexpectedOutput, true},

		// Contradictory metadata.
		{"is_error with success", set(last, "is_error", true), ErrUnexpectedOutput, true},
		{"two turns", set(last, "num_turns", 2), ErrUnexpectedOutput, true},
		{"result text differs", set(last, "result", answer+" And also run kubectl delete."), ErrUnexpectedOutput, true},
		{"result from another session", set(last, "session_id", "other"), ErrUnexpectedOutput, true},
		{"assistant from another session", func(e []map[string]any) []map[string]any {
			e[2]["session_id"] = "other"
			return e
		}, ErrUnexpectedOutput, true},
		{"two model messages", func(e []map[string]any) []map[string]any {
			e[1]["message"].(map[string]any)["id"] = "msg_02"
			return e
		}, ErrUnexpectedOutput, true},
		{"structured output", set(last, "structured_output", map[string]any{"a": 1}), ErrUnexpectedOutput, true},
		{"errors on success", set(last, "errors", []any{"x"}), ErrUnexpectedOutput, true},
		{"no model usage", set(last, "modelUsage", map[string]any{}), ErrUnexpectedOutput, true},
		{"another version", set(0, "claude_code_version", "2.1.297"), ErrUnexpectedOutput, true},
		{"another cwd", set(0, "cwd", "/Users/owner/repo"), ErrUnexpectedOutput, true},
		{"API key source", set(0, "apiKeySource", "ANTHROPIC_API_KEY"), ErrUnexpectedOutput, true},

		// Another model answered.
		{"model fallback event", insert(1, map[string]any{"type": "system", "subtype": "model_refusal_fallback"}), ErrUnexpectedOutput, false},
		{"init another model", set(0, "model", "claude-haiku-5-5"), ErrUnexpectedOutput, false},
		{"message from another model", setMsg(2, "model", "claude-haiku-5-5"), ErrUnexpectedOutput, false},
		{"usage by another model", set(last, "modelUsage", map[string]any{tModel: map[string]any{}, "claude-haiku-5-5": map[string]any{}}), ErrUnexpectedOutput, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseClaudeStream(stream(t, tc.m(cleanEvents())), tWant)
			assertStreamErr(t, err, tc.is, tc.revoke)
		})
	}
}

// plugins appends a plugin to the captured init's list.
func plugins(p map[string]any) func([]map[string]any) []map[string]any {
	return func(e []map[string]any) []map[string]any {
		e[0]["plugins"] = append(e[0]["plugins"].([]any), p)
		return e
	}
}

func assertStreamErr(t *testing.T, err, is error, revoke bool) {
	t.Helper()
	if err == nil {
		t.Fatal("the answer must be discarded")
	}
	if is == nil {
		var r *RefusedError
		if !errors.As(err, &r) {
			t.Fatalf("want a RefusedError, got %T %v", err, err)
		}
	} else if !errors.Is(err, is) {
		t.Fatalf("want %v, got %v", is, err)
	}
	var ce *cliContractError
	if got := errors.As(err, &ce) && ce.revoke; got != revoke {
		t.Fatalf("revoke = %v, want %v (%v)", got, revoke, err)
	}
}

func TestParseClaudeStream_Framing(t *testing.T) {
	clean := stream(t, cleanEvents())
	cases := []struct {
		name string
		out  []byte
		is   error
	}{
		{"empty", nil, ErrUnconfirmed},
		{"no final newline", clean[:len(clean)-1], ErrUnexpectedOutput},
		{"last line ends in a space, not a newline", append(append([]byte{}, clean[:len(clean)-1]...), ' '), ErrUnexpectedOutput},
		{"trailing data", append(append([]byte{}, clean...), []byte(`{"type":"result"`)...), ErrUnexpectedOutput},
		{"blank line", []byte(strings.Replace(string(clean), "\n", "\n\n", 1)), ErrUnexpectedOutput},
		{"two objects on a line", []byte(strings.Replace(string(clean), "\n", " {}\n", 1)), ErrUnexpectedOutput},
		{"malformed line", []byte(strings.Replace(string(clean), `"type":"rate_limit_event"`, `"type":`, 1)), ErrUnexpectedOutput},
		{"duplicate key", []byte(strings.Replace(string(clean), `"type":"result"`, `"type":"result","type":"assistant"`, 1)), ErrUnexpectedOutput},
		{"not UTF-8", append([]byte{0xff}, clean...), ErrUnexpectedOutput},
		// Codex review of 09cb2d8: duplicate keys at any depth, and null or
		// mistyped required values.
		{"duplicate type in a content block", []byte(strings.Replace(string(clean), `{"text":"`+answer+`","type":"text"}`, `{"type":"tool_use","text":"`+answer+`","type":"text"}`, 1)), ErrUnexpectedOutput},
		{"duplicate key in usage", []byte(strings.Replace(string(clean), `"input_tokens":10,`, `"input_tokens":10,"input_tokens":11,`, 1)), ErrUnexpectedOutput},
		{"is_error null", []byte(strings.Replace(string(clean), `"is_error":false`, `"is_error":null`, 1)), ErrUnexpectedOutput},
		{"is_error as a string", []byte(strings.Replace(string(clean), `"is_error":false`, `"is_error":"false"`, 1)), ErrUnexpectedOutput},
		{"num_turns as a string", []byte(strings.Replace(string(clean), `"num_turns":1`, `"num_turns":"1"`, 1)), ErrUnexpectedOutput},
		{"result text null", []byte(strings.Replace(string(clean), `"result":"`+answer+`"`, `"result":null`, 1)), ErrUnexpectedOutput},
		{"plain text", []byte("I can't help with that.\n"), ErrUnexpectedOutput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseClaudeStream(tc.out, tWant)
			if !errors.Is(err, tc.is) {
				t.Fatalf("want %v, got %v", tc.is, err)
			}
		})
	}
}

// The real init, as captured, passes; its retry events are allowed.
func TestCapturedInitPasses(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.296", "stream-offline-init.captured.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	m, err := strictObject([]byte(lines[0]))
	if err != nil {
		t.Fatal(err)
	}
	want := tWant
	want.Cwd = "/CALLDIR"
	if _, err := checkInit(m, want); err != nil {
		t.Fatalf("the captured init must pass: %v", err)
	}
	for _, l := range lines[1:] {
		r, _ := strictObject([]byte(l))
		if typ, _ := rawString(r, "type"); typ != "system" {
			t.Fatalf("unexpected captured event %s", l)
		}
		if sub, _ := rawString(r, "subtype"); sub != "api_retry" {
			t.Fatalf("unexpected captured event %s", l)
		}
	}
	want.BuiltinPlugins = nil
	if _, err := checkInit(m, want); err == nil {
		t.Fatal("built-ins a contract has not reviewed must refuse")
	}
}

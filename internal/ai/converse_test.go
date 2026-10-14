package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// script is a fake vendor that answers successive requests in order and
// records each request body.
func script(t *testing.T, replies ...string) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		i := n
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if i >= len(replies) {
			w.WriteHeader(500)
			return
		}
		reply := replies[i]
		if strings.HasPrefix(reply, "HTTP") {
			code := 400
			if strings.HasPrefix(reply, "HTTP422") {
				code = 422
			} else if strings.HasPrefix(reply, "HTTP401") {
				code = 401
			} else if strings.HasPrefix(reply, "HTTP500") {
				code = 500
			}
			w.WriteHeader(code)
			_, _ = io.WriteString(w, reply[7:])
			return
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []map[string]any { mu.Lock(); defer mu.Unlock(); return append([]map[string]any(nil), bodies...) }
}

var convTools = []Tool{
	{Name: "get_resource", Description: "get one object", Schema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}, "additionalProperties": false}},
	{Name: "cluster_overview", Description: "overview", Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}},
}

// drive runs a two-step conversation: a question, the model's tool calls,
// their results, and the final answer. It returns both turns.
func drive(t *testing.T, c Converser) (Turn, Turn) {
	t.Helper()
	ctx := context.Background()
	msgs := []ConvMessage{{Role: "user", Text: "why is web failing?"}}
	first, err := c.Converse(ctx, ConvRequest{System: "SYS", Messages: msgs, Tools: convTools})
	if err != nil {
		t.Fatalf("first step: %v", err)
	}
	if first.Done || len(first.Calls) == 0 {
		t.Fatalf("the first step asks for tools: %+v", first)
	}
	var results []ToolResult
	for _, call := range first.Calls {
		results = append(results, ToolResult{CallID: call.ID, Name: call.Name, Content: `{"ok":true}`})
	}
	msgs = append(msgs, ConvMessage{Role: "assistant", Continuation: first.Continuation, Calls: first.Calls}, ConvMessage{Role: "tool", Results: results})
	second, err := c.Converse(ctx, ConvRequest{System: "SYS", Messages: msgs, Tools: convTools})
	if err != nil {
		t.Fatalf("second step: %v", err)
	}
	return first, second
}

func TestAnthropicConverseReplaysSignedThinkingAndToolUse(t *testing.T) {
	srv, bodies := script(t,
		anthropicReply("tool_use", `[{"type":"thinking","thinking":"check web","signature":"SIG-1"},{"type":"tool_use","id":"toolu_a","name":"get_resource","input":{"name":"web"}},{"type":"tool_use","id":"toolu_b","name":"cluster_overview","input":{}}]`),
		anthropicReply("end_turn", `[{"type":"text","text":"web is crash-looping."}]`))
	p := NewAnthropic("sk-ant-test-key-123456", srv.URL, "claude-opus-5-5", NewHTTPClient(false), false).(Converser)
	first, second := drive(t, p)
	if len(first.Calls) != 2 || first.Calls[0].ID != "toolu_a" || string(first.Calls[0].Args) != `{"name":"web"}` {
		t.Fatalf("parallel calls keep their ids and arguments: %+v", first.Calls)
	}
	if !second.Done || second.Text != "web is crash-looping." {
		t.Fatalf("the second step answers: %+v", second)
	}
	b := bodies()
	if _, ok := b[0]["thinking"]; ok {
		t.Fatal("no thinking field: current models default to adaptive")
	}
	raw, _ := json.Marshal(b[1]["messages"])
	for _, want := range []string{`"signature":"SIG-1"`, `"tool_use_id":"toolu_a"`, `"tool_use_id":"toolu_b"`, `"id":"toolu_a"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the second request replays %s: %s", want, raw)
		}
	}
	tools, _ := json.Marshal(b[0]["tools"])
	if !strings.Contains(string(tools), `"name":"get_resource"`) || !strings.Contains(string(tools), `"input_schema"`) {
		t.Fatalf("tools are offered with their schemas: %s", tools)
	}
}

func TestOpenAIConverseReplaysEncryptedReasoning(t *testing.T) {
	first := `{"model":"gpt-5","status":"completed","output":[{"type":"reasoning","id":"rs_1","encrypted_content":"ENC-1","summary":[]},{"type":"function_call","id":"fc_1","call_id":"call_a","name":"get_resource","arguments":"{\"name\":\"web\"}"}]}`
	second := `{"model":"gpt-5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"web is crash-looping."}]}]}`
	srv, bodies := script(t, first, second)
	p := NewOpenAI("sk-test-key-123456789012", srv.URL, "gpt-5", NewHTTPClient(false)).(Converser)
	f, s := drive(t, p)
	if f.Calls[0].ID != "call_a" || !s.Done || s.Text != "web is crash-looping." {
		t.Fatalf("calls pair by call_id and the answer ends the turn: %+v %+v", f, s)
	}
	b := bodies()
	if b[0]["store"] != false || !strings.Contains(mustJSON(b[0]["include"]), "reasoning.encrypted_content") {
		t.Fatalf("store:false and encrypted reasoning requested: %v", b[0])
	}
	in := mustJSON(b[1]["input"])
	for _, want := range []string{`"encrypted_content":"ENC-1"`, `"type":"function_call_output"`, `"call_id":"call_a"`} {
		if !strings.Contains(in, want) {
			t.Fatalf("the second input replays %s: %s", want, in)
		}
	}
}

func TestGeminiConverseReplaysThoughtSignatures(t *testing.T) {
	first := `{"modelVersion":"gemini-3-pro","candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"get_resource","args":{"name":"web"}},"thoughtSignature":"TS-1"}]}}]}`
	second := `{"modelVersion":"gemini-3-pro","candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"web is crash-looping."}]}}]}`
	srv, bodies := script(t, first, second)
	p := NewGemini("AIzaTestKey12345678901234567", srv.URL, "gemini-3-pro", NewHTTPClient(false)).(Converser)
	f, s := drive(t, p)
	if !strings.HasPrefix(f.Calls[0].ID, geminiLocalCallID) || !s.Done {
		t.Fatalf("a call without an id gets a local one; the answer ends the turn: %+v %+v", f, s)
	}
	contents := mustJSON(bodies()[1]["contents"])
	if !strings.Contains(contents, `"thoughtSignature":"TS-1"`) || !strings.Contains(contents, `"functionResponse"`) {
		t.Fatalf("the signature is replayed and the call answered: %s", contents)
	}
	if strings.Contains(contents, geminiLocalCallID) {
		t.Fatalf("a local call id is never sent back: %s", contents)
	}
}

func TestCompatConverseToolsAndFallbackIdentification(t *testing.T) {
	first := `{"model":"llama","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"get_resource","arguments":"{\"name\":\"web\"}"}}]}}]}`
	second := `{"model":"llama","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"web is crash-looping."}}]}`
	srv, bodies := script(t, first, second)
	p := NewOpenAICompatible(srv.URL, "", "llama", NewHTTPClient(false)).(Converser)
	f, s := drive(t, p)
	if f.Calls[0].ID != "c1" || !s.Done {
		t.Fatalf("tool calls and a stop: %+v %+v", f, s)
	}
	if msgs := mustJSON(bodies()[1]["messages"]); !strings.Contains(msgs, `"tool_call_id":"c1"`) || !strings.Contains(msgs, `"tool_calls"`) {
		t.Fatalf("the assistant message and the tool result are replayed: %s", msgs)
	}

	for _, c := range []struct {
		reply string
		want  bool
	}{
		{`HTTP400{"error":{"message":"tools are not supported by this model"}}`, true},
		{`HTTP422{"detail":"function calling unsupported"}`, true},
		{`HTTP401{"error":"invalid api key"}`, false},
		{`HTTP500{"error":"tool backend crashed, not supported"}`, false},
		{`HTTP400{"error":"context length exceeded"}`, false},
	} {
		srv, _ := script(t, c.reply)
		p := NewOpenAICompatible(srv.URL, "", "llama", NewHTTPClient(false)).(Converser)
		_, err := p.Converse(context.Background(), ConvRequest{System: "S", Messages: []ConvMessage{{Role: "user", Text: "q"}}, Tools: convTools})
		if got := errors.Is(err, ErrToolsUnsupported); got != c.want {
			t.Errorf("%s: tools-unsupported = %v, want %v (err %v)", c.reply, got, c.want, err)
		}
	}
}

func TestConverseRejectsBadCalls(t *testing.T) {
	cases := map[string]string{
		"an unoffered tool":       anthropicReply("tool_use", `[{"type":"tool_use","id":"t1","name":"delete_pod","input":{}}]`),
		"a duplicate id":          anthropicReply("tool_use", `[{"type":"tool_use","id":"t1","name":"cluster_overview","input":{}},{"type":"tool_use","id":"t1","name":"cluster_overview","input":{}}]`),
		"a server tool":           anthropicReply("end_turn", `[{"type":"server_tool_use","id":"s1","name":"web_search","input":{}}]`),
		"tool_use without a call": anthropicReply("tool_use", `[{"type":"text","text":"hmm"}]`),
	}
	for name, reply := range cases {
		srv, _ := script(t, reply)
		p := NewAnthropic("sk-ant-test-key-123456", srv.URL, "claude-opus-5-5", NewHTTPClient(false), false).(Converser)
		_, err := p.Converse(context.Background(), ConvRequest{System: "S", Messages: []ConvMessage{{Role: "user", Text: "q"}}, Tools: convTools})
		if !errors.Is(err, ErrUnexpectedOutput) {
			t.Errorf("%s: want ErrUnexpectedOutput, got %v", name, err)
		}
	}
	for stop, want := range map[string]error{"max_tokens": ErrTruncated, "pause_turn": ErrUnexpectedOutput} {
		srv, _ := script(t, anthropicReply(stop, `[{"type":"text","text":"x"}]`))
		p := NewAnthropic("sk-ant-test-key-123456", srv.URL, "claude-opus-5-5", NewHTTPClient(false), false).(Converser)
		if _, err := p.Converse(context.Background(), ConvRequest{System: "S", Messages: []ConvMessage{{Role: "user", Text: "q"}}, Tools: convTools}); !errors.Is(err, want) {
			t.Errorf("stop %s: want %v, got %v", stop, want, err)
		}
	}
	srv, _ := script(t, `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"refusal","stop_details":{"category":"cyber"},"content":[]}`)
	p := NewAnthropic("sk-ant-test-key-123456", srv.URL, "claude-opus-5-5", NewHTTPClient(false), false).(Converser)
	var rf *RefusedError
	if _, err := p.Converse(context.Background(), ConvRequest{System: "S", Messages: []ConvMessage{{Role: "user", Text: "q"}}}); !errors.As(err, &rf) {
		t.Errorf("a refusal is a refusal: %v", err)
	}
}

// A continuation from another provider is refused, never replayed.
func TestConverseRefusesAForeignContinuation(t *testing.T) {
	srv, bodies := script(t)
	p := NewOpenAI("sk-test-key-123456789012", srv.URL, "gpt-5", NewHTTPClient(false)).(Converser)
	msgs := []ConvMessage{{Role: "user", Text: "q"}, {Role: "assistant", Continuation: &Continuation{Provider: ProviderAnthropic, Raw: json.RawMessage(`{}`)}}}
	if _, err := p.Converse(context.Background(), ConvRequest{System: "S", Messages: msgs}); !errors.Is(err, errContinuation) {
		t.Fatalf("want errContinuation, got %v", err)
	}
	if len(bodies()) != 0 {
		t.Fatal("nothing is sent with a foreign continuation")
	}
}

func TestCheckTurnCapsContinuation(t *testing.T) {
	big := json.RawMessage(`"` + strings.Repeat("x", MaxContinuationBytes) + `"`)
	if _, err := checkTurn(Turn{Text: "ok", Continuation: &Continuation{Raw: big}}, nil); err == nil {
		t.Fatal("a continuation over the cap ends the turn")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- Codex review of 1390061 ----------------------------------------------------

func TestToolsUnsupportedIgnoresSchemaErrors(t *testing.T) {
	for _, c := range []struct {
		reply string
		want  bool
	}{
		{`HTTP400{"error":"Invalid schema for function get_resource: unsupported keyword pattern"}`, false},
		{`HTTP422{"detail":"tools[0].function.parameters: unsupported schema type"}`, false},
		{`HTTP400{"error":"this model does not support tools"}`, true},
		{`HTTP400{"error":"function calling is not supported"}`, true},
	} {
		srv, _ := script(t, c.reply)
		p := NewOpenAICompatible(srv.URL, "", "llama", NewHTTPClient(false)).(Converser)
		_, err := p.Converse(context.Background(), ConvRequest{System: "S", Messages: []ConvMessage{{Role: "user", Text: "q"}}, Tools: convTools})
		if got := errors.Is(err, ErrToolsUnsupported); got != c.want {
			t.Errorf("%s: tools-unsupported = %v, want %v", c.reply, got, c.want)
		}
	}
}

func TestGeminiPartsAreCheckedWhole(t *testing.T) {
	for name, parts := range map[string]string{
		"an unexpected payload beside a call":  `[{"functionCall":{"name":"cluster_overview","args":{}},"executableCode":{"code":"x"}}]`,
		"a thought flag that is not a boolean": `[{"text":"secret reasoning","thought":"true"}]`,
	} {
		srv, _ := script(t, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":`+parts+`}}]}`)
		p := NewGemini("AIzaTestKey12345678901234567", srv.URL, "gemini-3-pro", NewHTTPClient(false)).(Converser)
		if _, err := p.Converse(context.Background(), ConvRequest{System: "S", Messages: []ConvMessage{{Role: "user", Text: "q"}}, Tools: convTools}); !errors.Is(err, ErrUnexpectedOutput) {
			t.Errorf("%s: want ErrUnexpectedOutput, got %v", name, err)
		}
	}
}

func TestConverseNeverInventsArguments(t *testing.T) {
	openai := `{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"cluster_overview","arguments":""}]}`
	srv, _ := script(t, openai)
	p := NewOpenAI("sk-test-key-123456789012", srv.URL, "gpt-5", NewHTTPClient(false)).(Converser)
	if _, err := p.Converse(context.Background(), ConvRequest{System: "S", Messages: []ConvMessage{{Role: "user", Text: "q"}}, Tools: convTools}); !errors.Is(err, ErrUnexpectedOutput) {
		t.Errorf("OpenAI: an empty arguments string is malformed: %v", err)
	}
	compat := `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"cluster_overview","arguments":""}}]}}]}`
	srv, _ = script(t, compat)
	q := NewOpenAICompatible(srv.URL, "", "llama", NewHTTPClient(false)).(Converser)
	if _, err := q.Converse(context.Background(), ConvRequest{System: "S", Messages: []ConvMessage{{Role: "user", Text: "q"}}, Tools: convTools}); !errors.Is(err, ErrUnexpectedOutput) {
		t.Errorf("compat: an empty arguments string is malformed: %v", err)
	}
}

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
	"time"
)

// captured is one request a fake vendor server saw.
type captured struct {
	path    string
	headers http.Header
	body    map[string]any
}

// vendor starts a fake vendor endpoint that records every request and answers
// with reply (a JSON body) and status.
func vendor(t *testing.T, status int, reply string) (*httptest.Server, func() []captured) {
	t.Helper()
	var mu sync.Mutex
	var got []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		got = append(got, captured{path: r.URL.Path, headers: r.Header.Clone(), body: body})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []captured { mu.Lock(); defer mu.Unlock(); return append([]captured(nil), got...) }
}

var req = Request{System: "SYS", User: "EVIDENCE", Purpose: PurposeExplain}

func anthropicReply(stop, content string) string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"` + stop + `","stop_sequence":null,"stop_details":null,"usage":{"input_tokens":1,"output_tokens":1},"content":` + content + `}`
}

// Spec §0 defects 1-3: no sampling parameters, a budget with room for
// thinking, the key in x-api-key only, the refusal fallback for a model that
// takes it - and the SDK's environment defaults off, so an ambient
// ANTHROPIC_AUTH_TOKEN or ANTHROPIC_BASE_URL never joins the request.
func TestAnthropic_RequestShape(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-token-should-not-be-sent")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Ambient: leak")
	srv, got := vendor(t, 200, anthropicReply("end_turn", `[{"type":"thinking","thinking":"","signature":"s"},{"type":"text","text":"  The pod is crash-looping.  "}]`))
	p := NewAnthropic("sk-ant-test-key-123456", srv.URL, "claude-opus-5-5", NewHTTPClient(false), true)

	res, err := p.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.Text != "The pod is crash-looping." || res.ModelUsed != "claude-opus-5-5" {
		t.Fatalf("result = %+v", res)
	}
	c := got()[0]
	if c.path != "/v1/messages" {
		t.Fatalf("path = %s", c.path)
	}
	for _, banned := range []string{"temperature", "top_p", "top_k", "thinking"} {
		if _, ok := c.body[banned]; ok {
			t.Fatalf("request carries %q; current Claude models reject sampling parameters: %v", banned, c.body)
		}
	}
	if c.body["max_tokens"] != float64(apiMaxOutputTokens) {
		t.Fatalf("max_tokens = %v, want %d", c.body["max_tokens"], apiMaxOutputTokens)
	}
	if c.body["fallbacks"] != "default" || !strings.Contains(c.headers.Get("anthropic-beta"), "server-side-fallback-2026-07-01") {
		t.Fatalf("refusal fallback not requested: body=%v beta=%q", c.body["fallbacks"], c.headers.Get("anthropic-beta"))
	}
	if c.headers.Get("x-api-key") != "sk-ant-test-key-123456" {
		t.Fatalf("x-api-key = %q", c.headers.Get("x-api-key"))
	}
	if c.headers.Get("Authorization") != "" || c.headers.Get("X-Ambient") != "" {
		t.Fatalf("ambient ANTHROPIC_* configuration reached the request: auth=%q ambient=%q", c.headers.Get("Authorization"), c.headers.Get("X-Ambient"))
	}
}

func TestAnthropic_FallbackOnlyForModelsThatTakeIt(t *testing.T) {
	srv, got := vendor(t, 200, anthropicReply("end_turn", `[{"type":"text","text":"ok"}]`))
	p := NewAnthropic("sk-ant-test-key-123456", srv.URL, "claude-haiku-5-5", NewHTTPClient(false), true)
	if _, err := p.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, ok := got()[0].body["fallbacks"]; ok {
		t.Fatal("Haiku has no server-side fallback; the field must not be sent")
	}
}

// A refusal, a truncation, a tool call and an empty answer are errors, never
// an explanation (spec §0 defect 3, §1.2).
func TestAnthropic_OutcomeMapping(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  func(error) bool
	}{
		{"refusal", `{"id":"m","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"declined"},"usage":{},"content":[{"type":"text","text":"partial"}]}`,
			func(err error) bool { var r *RefusedError; return errors.As(err, &r) && r.Category == "cyber" }},
		{"max_tokens", anthropicReply("max_tokens", `[{"type":"text","text":"cut"}]`), func(err error) bool { return errors.Is(err, ErrTruncated) }},
		{"context window", anthropicReply("model_context_window_exceeded", `[{"type":"text","text":"cut"}]`), func(err error) bool { return errors.Is(err, ErrTruncated) }},
		{"tool_use", anthropicReply("tool_use", `[{"type":"tool_use","id":"t","name":"bash","input":{}}]`), func(err error) bool { return errors.Is(err, ErrUnexpectedOutput) }},
		{"tool block at end_turn", anthropicReply("end_turn", `[{"type":"text","text":"x"},{"type":"server_tool_use","id":"t","name":"web_search","input":{}}]`), func(err error) bool { return errors.Is(err, ErrUnexpectedOutput) }},
		{"empty", anthropicReply("end_turn", `[{"type":"text","text":"   "}]`), func(err error) bool { return errors.Is(err, ErrEmpty) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := vendor(t, 200, tc.reply)
			p := NewAnthropic("sk-ant-test-key-123456", srv.URL, "claude-opus-5-5", NewHTTPClient(false), false)
			_, err := p.Complete(context.Background(), req)
			if !tc.want(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// An HTTP error echoing the key comes back scrubbed (spec §2.1).
func TestAnthropic_ErrorIsScrubbed(t *testing.T) {
	srv, _ := vendor(t, 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key: sk-ant-test-key-123456"}}`)
	p := NewAnthropic("sk-ant-test-key-123456", srv.URL, "claude-opus-5-5", NewHTTPClient(false), false)
	_, err := p.Complete(context.Background(), req)
	if err == nil || strings.Contains(err.Error(), "sk-ant-test-key-123456") {
		t.Fatalf("error leaks the key or is nil: %v", err)
	}
}

// The call's deadline bounds the SDK's retries and backoff (spec §1.2).
func TestAnthropic_DeadlineBoundsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("retry-after", "30")
		w.WriteHeader(529)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropic("sk-ant-test-key-123456", srv.URL, "claude-opus-5-5", NewHTTPClient(false), false)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := p.Complete(ctx, req); err == nil {
		t.Fatal("want an error")
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("retries outlived the call deadline: %v", el)
	}
}

func TestOpenAI_RequestShapeAndOutcomes(t *testing.T) {
	ok := `{"model":"gpt-x-2026","status":"completed","output":[{"type":"reasoning","summary":[]},{"type":"message","content":[{"type":"output_text","text":"Root cause: image pull."}]}]}`
	srv, got := vendor(t, 200, ok)
	p := NewOpenAI("sk-openai-key-123456", srv.URL, "gpt-x", NewHTTPClient(false))
	res, err := p.Complete(context.Background(), req)
	if err != nil || res.Text != "Root cause: image pull." || res.ModelUsed != "gpt-x-2026" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	c := got()[0]
	if c.path != "/v1/responses" || c.headers.Get("Authorization") != "Bearer sk-openai-key-123456" {
		t.Fatalf("path=%s auth=%q", c.path, c.headers.Get("Authorization"))
	}
	if _, has := c.body["temperature"]; has {
		t.Fatal("no temperature: reasoning models reject it")
	}
	if c.body["store"] != false || c.body["instructions"] != "SYS" || c.body["input"] != "EVIDENCE" || c.body["max_output_tokens"] != float64(apiMaxOutputTokens) {
		t.Fatalf("body = %v", c.body)
	}

	cases := []struct {
		name  string
		reply string
		want  func(error) bool
	}{
		{"incomplete", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, func(err error) bool { return errors.Is(err, ErrTruncated) }},
		{"content filter", `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}`, func(err error) bool { var r *RefusedError; return errors.As(err, &r) }},
		{"refusal part", `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"no"}]}]}`, func(err error) bool { var r *RefusedError; return errors.As(err, &r) }},
		{"function call", `{"status":"completed","output":[{"type":"function_call","name":"x"}]}`, func(err error) bool { return errors.Is(err, ErrUnexpectedOutput) }},
		{"failed", `{"status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}`, func(err error) bool { return err != nil && strings.Contains(err.Error(), "boom") }},
		{"empty", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":""}]}]}`, func(err error) bool { return errors.Is(err, ErrEmpty) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := vendor(t, 200, tc.reply)
			_, err := NewOpenAI("sk-openai-key-123456", srv.URL, "gpt-x", NewHTTPClient(false)).Complete(context.Background(), req)
			if !tc.want(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestGemini_RequestShapeAndOutcomes(t *testing.T) {
	ok := `{"modelVersion":"gemini-x-001","candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"thinking...","thought":true},{"text":"The issuer is not ready."}]}}]}`
	srv, got := vendor(t, 200, ok)
	p := NewGemini("AIzaTestKey123456789012345", srv.URL, "models/gemini-x", NewHTTPClient(false))
	res, err := p.Complete(context.Background(), req)
	if err != nil || res.Text != "The issuer is not ready." || res.ModelUsed != "gemini-x-001" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	c := got()[0]
	if c.path != "/v1beta/models/gemini-x:generateContent" || c.headers.Get("x-goog-api-key") != "AIzaTestKey123456789012345" {
		t.Fatalf("path=%s key=%q", c.path, c.headers.Get("x-goog-api-key"))
	}
	if _, has := c.body["systemInstruction"]; !has {
		t.Fatalf("system instruction missing: %v", c.body)
	}

	cases := []struct {
		name  string
		reply string
		want  func(error) bool
	}{
		{"max tokens", `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"cut"}]}}]}`, func(err error) bool { return errors.Is(err, ErrTruncated) }},
		{"safety", `{"candidates":[{"finishReason":"SAFETY"}]}`, func(err error) bool { var r *RefusedError; return errors.As(err, &r) && r.Category == "SAFETY" }},
		{"prompt blocked", `{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}`, func(err error) bool { var r *RefusedError; return errors.As(err, &r) }},
		{"function call", `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"name":"x"}}]}}]}`, func(err error) bool { return errors.Is(err, ErrUnexpectedOutput) }},
		{"malformed call", `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`, func(err error) bool { return errors.Is(err, ErrUnexpectedOutput) }},
		{"no candidates", `{"candidates":[]}`, func(err error) bool { return errors.Is(err, ErrEmpty) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := vendor(t, 200, tc.reply)
			_, err := NewGemini("AIzaTestKey123456789012345", srv.URL, "gemini-x", NewHTTPClient(false)).Complete(context.Background(), req)
			if !tc.want(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestCompat_RequestShapeAndOutcomes(t *testing.T) {
	srv, got := vendor(t, 200, `{"model":"llama3.1","choices":[{"finish_reason":"stop","message":{"content":"Local answer."}}]}`)
	res, err := NewOpenAICompatible(srv.URL, "", "llama3.1", NewHTTPClient(false)).Complete(context.Background(), req)
	if err != nil || res.Text != "Local answer." {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	c := got()[0]
	if c.path != "/v1/chat/completions" || c.body["temperature"] != float64(0) || c.headers.Get("Authorization") != "" {
		t.Fatalf("path=%s body=%v auth=%q", c.path, c.body, c.headers.Get("Authorization"))
	}
	for name, reply := range map[string]string{
		"length":     `{"choices":[{"finish_reason":"length","message":{"content":"cut"}}]}`,
		"tool_calls": `{"choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{}]}}]}`,
	} {
		srv, _ := vendor(t, 200, reply)
		_, err := NewOpenAICompatible(srv.URL, "", "m", NewHTTPClient(false)).Complete(context.Background(), req)
		if err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

// The 4 MiB cap holds for a completion, an error body and a model list
// (spec §1.2: a timeout is not a byte limit).
func TestTransport_ResponseCap(t *testing.T) {
	huge := strings.Repeat("x", maxResponseBytes+10)
	for _, status := range []int{200, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"pad":"`+huge+`"}`)
		}))
		_, err := NewOpenAICompatible(srv.URL, "", "m", NewHTTPClient(false)).Complete(context.Background(), req)
		if err == nil || !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("status %d: want ErrResponseTooLarge, got %v", status, err)
		}
		_, err = NewOpenAI("sk-openai-key-123456", srv.URL, "m", NewHTTPClient(false)).(*openaiProvider).ListModels(context.Background())
		if err == nil || !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("model list, status %d: want ErrResponseTooLarge, got %v", status, err)
		}
		srv.Close()
	}
}

func TestTransport_RedirectPolicy(t *testing.T) {
	mk := func(raw string) *http.Request { r, _ := http.NewRequest(http.MethodPost, raw, nil); return r }
	if err := checkRedirect(mk("https://evil.example/v1"), []*http.Request{mk("https://api.example/v1")}); err == nil {
		t.Fatal("cross-host redirect must be refused")
	}
	if err := checkRedirect(mk("http://api.example/v1"), []*http.Request{mk("https://api.example/v1")}); err == nil {
		t.Fatal("HTTPS to HTTP redirect must be refused")
	}
	if err := checkRedirect(mk("https://api.example/v2"), []*http.Request{mk("https://api.example/v1")}); err != nil {
		t.Fatalf("same-host HTTPS redirect: %v", err)
	}
	five := []*http.Request{mk("https://a/1"), mk("https://a/2"), mk("https://a/3"), mk("https://a/4"), mk("https://a/5")}
	if err := checkRedirect(mk("https://a/6"), five); err == nil {
		t.Fatal("a sixth redirect must be refused")
	}

	// End to end: a cross-host redirect on a POST never reaches the other host.
	var hit bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	if _, err := NewOpenAICompatible(first.URL, "", "m", NewHTTPClient(false)).Complete(context.Background(), req); err == nil {
		t.Fatal("want an error")
	}
	if hit {
		t.Fatal("the request body reached the redirect target")
	}
}

func TestScrub(t *testing.T) {
	in := `invalid x-api-key: sk-ant-live-ABCDEF123456; Authorization: Bearer abcdefghijklmnop; key AIzaSyA1234567890abcdefghijk; mine=supersecret-value`
	out := Scrub(in, "supersecret-value")
	for _, leak := range []string{"sk-ant-live-ABCDEF123456", "abcdefghijklmnop", "AIzaSyA1234567890abcdefghijk", "supersecret-value"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q survived scrubbing: %s", leak, out)
		}
	}
	if !strings.Contains(out, "x-api-key: [redacted]") {
		t.Fatalf("header name should stay readable: %s", out)
	}
	if long := Scrub(strings.Repeat("a", 1000)); len(long) > scrubExcerptMax+len("…") {
		t.Fatalf("excerpt not bounded: %d", len(long))
	}
}

func TestIsLoopbackEndpoint(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://localhost:11434": true, "http://127.0.0.1:8080": true, "http://[::1]:1": true, "http://ollama.localhost": true,
		"http://192.168.1.10:11434": false, "https://api.anthropic.com": false, "localhost:11434": false, "": false,
	} {
		if got := IsLoopbackEndpoint(raw); got != want {
			t.Errorf("IsLoopbackEndpoint(%q) = %v, want %v", raw, got, want)
		}
	}
}

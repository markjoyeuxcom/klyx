package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// compatProvider talks to an OpenAI-compatible chat-completions server
// (Ollama, llama.cpp, vLLM): `POST /v1/chat/completions`. It keeps the raw
// client Klyx had before the 2026-10-13 providers: local servers accept
// temperature 0, and the key is optional.
type compatProvider struct {
	baseURL string
	key     string
	model   string
	http    *http.Client
}

// NewOpenAICompatible builds the provider for an OpenAI-compatible server.
func NewOpenAICompatible(baseURL, apiKey, model string, hc *http.Client) Provider {
	return &compatProvider{baseURL: strings.TrimRight(baseURL, "/"), key: apiKey, model: model, http: hc}
}

func (p *compatProvider) headers() map[string]string {
	h := map[string]string{}
	if p.key != "" {
		h["Authorization"] = "Bearer " + p.key
	}
	return h
}

func (p *compatProvider) Complete(ctx context.Context, req Request) (Result, error) {
	payload := map[string]any{
		"model":       p.model,
		"max_tokens":  compatMaxTokens(req.Purpose),
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.User},
		},
	}
	body, err := postJSON(ctx, p.http, p.baseURL+"/v1/chat/completions", p.headers(), payload, p.key)
	if err != nil {
		return Result{}, err
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
			Message      struct {
				Content      string            `json:"content"`
				Refusal      string            `json:"refusal"`
				ToolCalls    []json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage   `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("decode chat response: %w", err)
	}
	if len(out.Choices) == 0 {
		return Result{}, ErrEmpty
	}
	c := out.Choices[0]
	finish := ""
	if c.FinishReason != nil {
		finish = *c.FinishReason
	}
	// Tool output first: a call is never an answer, whatever the reason says
	// (tool_calls, or the legacy function_call, with or without content).
	if len(c.Message.ToolCalls) > 0 || hasJSONValue(c.Message.FunctionCall) || finish == "tool_calls" || finish == "function_call" {
		return Result{}, ErrUnexpectedOutput
	}
	if c.Message.Refusal != "" {
		return Result{}, refused("", c.Message.Refusal, p.key)
	}
	// Only an explicit "stop" confirms a finished answer; a missing, null or
	// unknown finish reason is not success (Codex review of 104f4c8).
	switch finish {
	case "stop":
	case "content_filter":
		return Result{}, refused("content_filter", "", p.key)
	case "length":
		return Result{}, ErrTruncated
	default:
		return Result{}, ErrUnconfirmed
	}
	text, err := joinText([]string{c.Message.Content})
	if err != nil {
		return Result{}, err
	}
	model := out.Model
	if model == "" {
		model = p.model
	}
	return Result{Text: text, ModelUsed: model}, nil
}

// hasJSONValue reports whether a raw JSON field is present and not null.
func hasJSONValue(raw json.RawMessage) bool {
	v := strings.TrimSpace(string(raw))
	return v != "" && v != "null"
}

// ListModels reads `GET /v1/models` (Ollama and llama.cpp serve it).
func (p *compatProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return listOpenAIModels(ctx, p.http, p.baseURL, p.headers(), p.key, false)
}

// postJSON POSTs payload as JSON and returns the capped 2xx body; any error
// is scrubbed of secrets.
func postJSON(ctx context.Context, hc *http.Client, url string, headers map[string]string, payload any, secrets ...string) ([]byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doRequest(hc, req, secrets...)
}

// getJSON GETs url and returns the capped 2xx body.
func getJSON(ctx context.Context, hc *http.Client, url string, headers map[string]string, secrets ...string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doRequest(hc, req, secrets...)
}

func doRequest(hc *http.Client, req *http.Request, secrets ...string) ([]byte, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, scrubErr(err, secrets...)
	}
	body, err := readBody(resp, secrets...)
	if err != nil {
		return nil, scrubErr(err, secrets...)
	}
	return body, nil
}

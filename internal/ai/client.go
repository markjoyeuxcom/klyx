// Package ai is a minimal, backend-agnostic LLM completion client. It is pure
// transport: it knows nothing about Klyx's fleet or the webview, and imports no
// Wails or client-go. It mirrors internal/metrics/transport.go - the caller owns
// the deadline via ctx and builds the *http.Client (timeout, TLS).
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxResponseBytes caps the model response read so a hostile or misconfigured
// endpoint cannot buffer an unbounded body into memory. Same guard as
// internal/metrics.
const maxResponseBytes = 4 << 20 // 4 MiB

// anthropicVersion is the required Messages API version header.
const anthropicVersion = "2023-06-01"

// Backend selects the wire protocol + auth for the model endpoint.
type Backend string

const (
	// BackendHosted talks the Anthropic Messages API (POST /v1/messages).
	BackendHosted Backend = "hosted"
	// BackendLocal talks an OpenAI-compatible endpoint (POST /v1/chat/completions),
	// e.g. an Ollama or llama.cpp server. Nothing leaves the machine.
	BackendLocal Backend = "local"
)

// Client is a single-completion LLM client.
type Client struct {
	backend Backend
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewClient builds a client. httpClient carries the timeout + TLS config; a nil
// httpClient falls back to http.DefaultClient (which has no timeout - callers
// should pass one).
func NewClient(backend Backend, baseURL, apiKey, model string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	// Never follow a redirect to another host: the egress policy is decided for
	// the CONFIGURED endpoint, and a 307/308 re-sends the POST body (cluster
	// context) wherever it points (review 2026-10-03 #14). A copy, so the
	// caller's client is not mutated.
	hc := *httpClient
	hc.CheckRedirect = refuseCrossHostRedirect
	return &Client{
		backend: backend,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &hc,
	}
}

// refuseCrossHostRedirect allows a redirect only within the original host.
func refuseCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		return fmt.Errorf("refusing redirect from %s to %s: the AI egress policy covers the configured endpoint only", via[0].URL.Host, req.URL.Host)
	}
	if len(via) >= 5 {
		return fmt.Errorf("too many redirects")
	}
	return nil
}

// IsLoopbackEndpoint reports whether baseURL points at THIS machine (localhost
// or a loopback IP). Only such an endpoint keeps data on the machine: "local"
// names the OpenAI-compatible wire protocol, not where the server runs - a team
// vLLM or a hosted OpenAI-compatible gateway configured as "local" still sends
// cluster context off-machine. No DNS lookup: a name other than localhost is
// treated as remote.
func IsLoopbackEndpoint(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Model returns the configured model id (for surfacing in the UI).
func (c *Client) Model() string { return c.model }

// Complete sends a single system+user prompt and returns the model's text reply.
// Deterministic (temperature 0). ctx owns the timeout.
func (c *Client) Complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if c.backend == BackendLocal {
		return c.completeOpenAI(ctx, system, user, maxTokens)
	}
	return c.completeAnthropic(ctx, system, user, maxTokens)
}

func (c *Client) completeAnthropic(ctx context.Context, system, user string, maxTokens int) (string, error) {
	payload := map[string]any{
		"model":       c.model,
		"max_tokens":  maxTokens,
		"temperature": 0,
		"system":      system,
		"messages":    []map[string]string{{"role": "user", "content": user}},
	}
	body, err := c.post(ctx, "/v1/messages", map[string]string{
		"x-api-key":         c.apiKey,
		"anthropic-version": anthropicVersion,
	}, payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode anthropic response: %w", err)
	}
	for _, b := range out.Content {
		if b.Type == "text" && b.Text != "" {
			return b.Text, nil
		}
	}
	return "", fmt.Errorf("anthropic response contained no text content")
}

func (c *Client) completeOpenAI(ctx context.Context, system, user string, maxTokens int) (string, error) {
	payload := map[string]any{
		"model":       c.model,
		"max_tokens":  maxTokens,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	headers := map[string]string{}
	if c.apiKey != "" {
		headers["Authorization"] = "Bearer " + c.apiKey
	}
	body, err := c.post(ctx, "/v1/chat/completions", headers, payload)
	if err != nil {
		return "", err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("chat response contained no message content")
	}
	return out.Choices[0].Message.Content, nil
}

// post marshals payload, POSTs it to baseURL+path with the given headers, and
// returns the (capped) response body. A non-2xx status is an error carrying a
// truncated body for context.
func (c *Client) post(ctx context.Context, path string, headers map[string]string, payload any) ([]byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ai endpoint returned HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// DefaultOpenAIOrigin is the OpenAI API's own origin.
const DefaultOpenAIOrigin = "https://api.openai.com"

// openaiProvider talks to the OpenAI Responses API (`POST /v1/responses`).
//
// It is a small raw-HTTP client rather than the official SDK because the SDK
// folds ambient OPENAI_* variables (organization, project, custom headers,
// base URL) into every request and its switch to disable that is internal-only
// - which would break the one-explicit-source rule (spec §2.1).
type openaiProvider struct {
	baseURL string
	key     string
	model   string
	http    *http.Client
}

// NewOpenAI builds the OpenAI provider.
func NewOpenAI(apiKey, baseURL, model string, hc *http.Client) Provider {
	if baseURL == "" {
		baseURL = DefaultOpenAIOrigin
	}
	return &openaiProvider{baseURL: strings.TrimRight(baseURL, "/"), key: apiKey, model: model, http: hc}
}

func (p *openaiProvider) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.key}
}

func (p *openaiProvider) Complete(ctx context.Context, req Request) (Result, error) {
	payload := map[string]any{
		"model":             p.model,
		"instructions":      req.System,
		"input":             req.User,
		"max_output_tokens": apiMaxOutputTokens,
		// No temperature: reasoning models reject it. Not stored server-side:
		// the prompt carries cluster context.
		"store": false,
	}
	body, err := postJSON(ctx, p.http, p.baseURL+"/v1/responses", p.headers(), payload, p.key)
	if err != nil {
		return Result{}, err
	}
	return openaiResult(body, p.model, p.key)
}

// openaiResult reads a Responses API body. Every vendor-controlled failure
// field is scrubbed with the active key before it leaves.
func openaiResult(body []byte, configured, key string) (Result, error) {
	var out struct {
		Model             string `json:"model"`
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("decode OpenAI response: %w", err)
	}
	switch out.Status {
	case "completed":
	case "incomplete":
		if out.IncompleteDetails != nil && out.IncompleteDetails.Reason == "content_filter" {
			return Result{}, refused("content_filter", "", key)
		}
		return Result{}, ErrTruncated
	default:
		if out.Error != nil && out.Error.Message != "" {
			return Result{}, fmt.Errorf("OpenAI response %s: %s", Scrub(out.Status, key), Scrub(out.Error.Message, key))
		}
		return Result{}, fmt.Errorf("OpenAI response status %q", Scrub(out.Status, key))
	}
	var parts []string
	for _, item := range out.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					parts = append(parts, c.Text)
				case "refusal":
					return Result{}, refused("", c.Refusal, key)
				default:
					return Result{}, ErrUnexpectedOutput
				}
			}
		case "reasoning":
			// Reasoning items are not the answer.
		default:
			// A function, web-search or other tool call Klyx never offered.
			return Result{}, ErrUnexpectedOutput
		}
	}
	text, err := joinText(parts)
	if err != nil {
		return Result{}, err
	}
	model := out.Model
	if model == "" {
		model = configured
	}
	return Result{Text: text, ModelUsed: model}, nil
}

// ListModels lists the account's text-generation models.
func (p *openaiProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return listOpenAIModels(ctx, p.http, p.baseURL, p.headers(), p.key, true)
}

// nonTextModel marks OpenAI model ids that are not text generators.
var nonTextModel = []string{"embedding", "whisper", "tts", "dall-e", "moderation", "image", "audio", "realtime", "transcribe", "sora", "davinci", "babbage", "search"}

// listOpenAIModels reads `GET /v1/models`; textOnly drops the obvious
// non-text families (the OpenAI list does not label capability).
func listOpenAIModels(ctx context.Context, hc *http.Client, baseURL string, headers map[string]string, key string, textOnly bool) ([]ModelInfo, error) {
	body, err := getJSON(ctx, hc, baseURL+"/v1/models", headers, key)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}
	var models []ModelInfo
	for _, m := range out.Data {
		if textOnly && containsAny(strings.ToLower(m.ID), nonTextModel) {
			continue
		}
		models = append(models, ModelInfo{ID: m.ID})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

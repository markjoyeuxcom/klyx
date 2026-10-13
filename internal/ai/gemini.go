package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// DefaultGeminiOrigin is the Gemini API's own origin.
const DefaultGeminiOrigin = "https://generativelanguage.googleapis.com"

// geminiProvider talks to the Gemini API's generateContent method
// (`POST /v1beta/models/{model}:generateContent`, key in x-goog-api-key).
//
// A small raw-HTTP client rather than google.golang.org/genai: the SDK falls
// back to GOOGLE_API_KEY / GEMINI_API_KEY, environment base URLs and a Vertex
// backend switch when a field is empty (spec §2.1 forbids any fallback), and
// it pulls grpc and opencensus into the binary for one REST call.
type geminiProvider struct {
	baseURL string
	key     string
	model   string
	http    *http.Client
}

// NewGemini builds the Gemini provider.
func NewGemini(apiKey, baseURL, model string, hc *http.Client) Provider {
	if baseURL == "" {
		baseURL = DefaultGeminiOrigin
	}
	return &geminiProvider{baseURL: strings.TrimRight(baseURL, "/"), key: apiKey, model: model, http: hc}
}

func (p *geminiProvider) headers() map[string]string {
	return map[string]string{"x-goog-api-key": p.key}
}

// modelPath turns "gemini-x" or "models/gemini-x" into an escaped path segment.
func geminiModelPath(model string) string {
	return url.PathEscape(strings.TrimPrefix(model, "models/"))
}

type geminiPart struct {
	Text string `json:"text,omitempty"`
}

func (p *geminiProvider) Complete(ctx context.Context, req Request) (Result, error) {
	payload := map[string]any{
		"systemInstruction": map[string]any{"parts": []geminiPart{{Text: req.System}}},
		"contents":          []map[string]any{{"role": "user", "parts": []geminiPart{{Text: req.User}}}},
		"generationConfig":  map[string]any{"maxOutputTokens": apiMaxOutputTokens},
	}
	body, err := postJSON(ctx, p.http, p.baseURL+"/v1beta/models/"+geminiModelPath(p.model)+":generateContent", p.headers(), payload, p.key)
	if err != nil {
		return Result{}, err
	}
	return geminiResult(body, p.model, p.key)
}

// geminiRefusalReasons are finish reasons that mean the vendor declined.
var geminiRefusalReasons = map[string]bool{
	"SAFETY": true, "RECITATION": true, "BLOCKLIST": true, "PROHIBITED_CONTENT": true, "SPII": true, "IMAGE_SAFETY": true, "LANGUAGE": true,
}

// geminiAllowedPartFields are the Part fields an answer may carry: text, the
// thought flag, and the thought signature that accompanies thinking. Any other
// payload - functionCall, executableCode, codeExecutionResult, inlineData,
// fileData or a field this build does not know - rejects the answer rather
// than being dropped in decoding (Codex review of 104f4c8).
var geminiAllowedPartFields = map[string]bool{"text": true, "thought": true, "thoughtSignature": true}

// geminiResult reads a generateContent body. Vendor-controlled failure text is
// scrubbed with the active key.
func geminiResult(body []byte, configured, key string) (Result, error) {
	var out struct {
		ModelVersion   string `json:"modelVersion"`
		PromptFeedback *struct {
			BlockReason        string `json:"blockReason"`
			BlockReasonMessage string `json:"blockReasonMessage"`
		} `json:"promptFeedback"`
		Candidates []struct {
			FinishReason  string `json:"finishReason"`
			FinishMessage string `json:"finishMessage"`
			Content       *struct {
				Parts []map[string]json.RawMessage `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("decode Gemini response: %w", err)
	}
	if out.PromptFeedback != nil && out.PromptFeedback.BlockReason != "" {
		return Result{}, refused(out.PromptFeedback.BlockReason, out.PromptFeedback.BlockReasonMessage, key)
	}
	if len(out.Candidates) == 0 {
		return Result{}, ErrEmpty
	}
	c := out.Candidates[0]
	switch {
	case c.FinishReason == "STOP":
	case c.FinishReason == "MAX_TOKENS":
		return Result{}, ErrTruncated
	case geminiRefusalReasons[c.FinishReason]:
		return Result{}, refused(c.FinishReason, c.FinishMessage, key)
	default:
		// MALFORMED_FUNCTION_CALL, UNEXPECTED_TOOL_CALL, OTHER, unspecified.
		return Result{}, ErrUnexpectedOutput
	}
	var parts []string
	if c.Content != nil {
		for _, part := range c.Content.Parts {
			for field := range part {
				if !geminiAllowedPartFields[field] {
					return Result{}, ErrUnexpectedOutput
				}
			}
			var thought bool
			if raw, ok := part["thought"]; ok {
				if err := json.Unmarshal(raw, &thought); err != nil {
					return Result{}, ErrUnexpectedOutput
				}
			}
			if thought {
				continue
			}
			var text string
			if raw, ok := part["text"]; ok {
				if err := json.Unmarshal(raw, &text); err != nil {
					return Result{}, ErrUnexpectedOutput
				}
			}
			parts = append(parts, text)
		}
	}
	text, err := joinText(parts)
	if err != nil {
		return Result{}, err
	}
	model := out.ModelVersion
	if model == "" {
		model = configured
	}
	return Result{Text: text, ModelUsed: model}, nil
}

// ListModels lists the models that support generateContent (paged).
func (p *geminiProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var models []ModelInfo
	token := ""
	for page := 0; page < 5; page++ {
		u := p.baseURL + "/v1beta/models?pageSize=200"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		body, err := getJSON(ctx, p.http, u, p.headers(), p.key)
		if err != nil {
			return nil, err
		}
		var out struct {
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("decode model list: %w", err)
		}
		for _, m := range out.Models {
			for _, method := range m.SupportedGenerationMethods {
				if method == "generateContent" {
					models = append(models, ModelInfo{ID: strings.TrimPrefix(m.Name, "models/"), DisplayName: m.DisplayName})
					break
				}
			}
		}
		if out.NextPageToken == "" {
			break
		}
		token = out.NextPageToken
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

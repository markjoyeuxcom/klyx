package ai

import (
	"context"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultAnthropicModel is the model a new Claude configuration starts with.
const DefaultAnthropicModel = "claude-opus-5-5"

// DefaultAnthropicOrigin is the Anthropic API's own origin.
const DefaultAnthropicOrigin = "https://api.anthropic.com"

// refusalFallbackModels accept the server-side refusal fallback's "default"
// routing. Other models (Haiku, older generations) get no fallback: sending
// the field to them is a request error.
var refusalFallbackModels = map[string]bool{
	"claude-opus-5-5":   true,
	"claude-opus-5":     true,
	"claude-sonnet-5-5": true,
	"claude-fable-5-1":  true,
	"claude-fable-5":    true,
}

// anthropicProvider talks to the Claude API through the official Go SDK
// (Messages API, beta surface for the refusal fallback).
//
// Spec §0 fixes: no sampling parameters (current Claude models reject a
// non-default temperature), no thinking field (it defaults to adaptive), a
// budget that leaves room for thinking, and stop_reason decoded - a refusal or
// a truncation is an error, never an explanation.
type anthropicProvider struct {
	client          anthropic.Client
	model           string
	key             string
	refusalFallback bool
}

// NewAnthropic builds the Claude provider. The SDK's environment defaults are
// switched off: the key, origin and headers are exactly the ones Klyx resolved
// (spec §2.1 - one explicit key source, no ANTHROPIC_* fallback).
func NewAnthropic(apiKey, baseURL, model string, hc *http.Client, refusalFallback bool) Provider {
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(hc),
		option.WithMaxRetries(sdkMaxRetries),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &anthropicProvider{
		client:          anthropic.NewClient(opts...),
		model:           model,
		key:             apiKey,
		refusalFallback: refusalFallback && refusalFallbackModels[model],
	}
}

func (p *anthropicProvider) Complete(ctx context.Context, req Request) (Result, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: apiMaxOutputTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: req.System}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(req.User))},
	}
	if p.refusalFallback {
		// A policy refusal is re-served by Anthropic's routed fallback model
		// inside the same call (same vendor, same egress contract); an explicit
		// final refusal still fails closed below.
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	msg, err := p.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return Result{}, scrubErr(err, p.key)
	}
	return anthropicResult(msg)
}

// anthropicResult reads a finished message: only end_turn/stop_sequence with
// text-only content is an answer.
func anthropicResult(msg *anthropic.BetaMessage) (Result, error) {
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return Result{}, &RefusedError{Category: string(msg.StopDetails.Category), Explanation: msg.StopDetails.Explanation}
	case anthropic.BetaStopReasonMaxTokens, anthropic.BetaStopReasonModelContextWindowExceeded:
		return Result{}, ErrTruncated
	case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonStopSequence:
	default:
		// tool_use, pause_turn or a reason this build does not know.
		return Result{}, ErrUnexpectedOutput
	}
	var parts []string
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "thinking", "redacted_thinking":
			// Reasoning blocks are not the answer.
		default:
			return Result{}, ErrUnexpectedOutput
		}
	}
	text, err := joinText(parts)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: text, ModelUsed: string(msg.Model)}, nil
}

// ListModels lists the account's models for the Settings picker.
func (p *anthropicProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var out []ModelInfo
	pager := p.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	for pager.Next() {
		m := pager.Current()
		out = append(out, ModelInfo{ID: m.ID, DisplayName: m.DisplayName})
		if len(out) >= 200 {
			break
		}
	}
	if err := pager.Err(); err != nil {
		return nil, scrubErr(err, p.key)
	}
	return out, nil
}

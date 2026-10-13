// Package ai is Klyx's model transport: one Provider per vendor, all behind
// the same narrow seam. It is pure transport - it knows nothing about Klyx's
// fleet or the webview, and imports no Wails or client-go. What crosses into a
// prompt, and whether it may leave the machine, is decided by the caller
// (internal/appbridge); this package decides only how a completion is asked
// for and how its outcome is read (spec 2026-10-13 §1).
package ai

import (
	"context"
	"errors"
	"strings"
)

// ProviderID names a configured provider. The values are the fleet.yaml
// `ai.provider` spellings.
type ProviderID string

const (
	ProviderOff              ProviderID = "off"
	ProviderAnthropic        ProviderID = "anthropic"
	ProviderOpenAI           ProviderID = "openai"
	ProviderGemini           ProviderID = "gemini"
	ProviderOpenAICompatible ProviderID = "openaiCompatible"
	ProviderClaudeCLI        ProviderID = "claudeCLI"
)

// Purpose tells a provider what a completion is for, so it can choose its own
// output budget; the caller never passes a token count.
type Purpose int

const (
	PurposeExplain Purpose = iota
	PurposeFix
	PurposeProbe
)

// Request is one single-turn completion. System holds Klyx's own instructions;
// User holds the evidence. Neither is ever logged.
type Request struct {
	System  string
	User    string
	Purpose Purpose
}

// Result is a confirmed, successful, non-empty completion. ModelUsed is the
// model the vendor reports having used (a refusal fallback can differ from the
// configured model), never just the configured one.
type Result struct {
	Text      string
	ModelUsed string
}

// Provider is one vendor's completion transport. Complete returns a Result
// only for a completion the vendor reports as finished; a refusal, a
// truncation, tool or other non-text output, or an empty answer is an error
// (spec §1.2): a partial or declined answer is never shown as an explanation.
type Provider interface {
	Complete(ctx context.Context, req Request) (Result, error)
}

// ModelInfo is one entry of a provider's model list.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
}

// ModelLister lists the models an account can use, for the Settings picker.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

var (
	// ErrTruncated: the vendor stopped before the answer was finished (an
	// output-token or context limit).
	ErrTruncated = errors.New("the model's answer was cut off before it finished")
	// ErrEmpty: the vendor reported success with no text.
	ErrEmpty = errors.New("the model returned no answer")
	// ErrUnexpectedOutput: the vendor returned a tool call or another output
	// kind Klyx never asked for.
	ErrUnexpectedOutput = errors.New("the model returned output other than text (a tool call or similar); Klyx discarded it")
)

// RefusedError: the vendor declined the request (a refusal stop reason, a
// safety block). Category is the vendor's own label when it gives one.
type RefusedError struct {
	Category    string
	Explanation string
}

func (e *RefusedError) Error() string {
	var b strings.Builder
	b.WriteString("the model declined to answer")
	if e.Category != "" {
		b.WriteString(" (" + e.Category + ")")
	}
	if e.Explanation != "" {
		b.WriteString(": " + e.Explanation)
	}
	return b.String()
}

// joinText concatenates a completion's text parts and refuses an empty result.
func joinText(parts []string) (string, error) {
	text := strings.TrimSpace(strings.Join(parts, ""))
	if text == "" {
		return "", ErrEmpty
	}
	return text, nil
}

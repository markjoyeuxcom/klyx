package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// The conversation seam (spec 2026-10-14 §6): multi-turn, with tools, beside
// the single-turn Complete. Explain and Suggest-fix stay on Complete. A Turn
// either ends with text or asks for tool calls; the provider-native
// continuation goes back on the next call exactly as received - signed
// thinking, encrypted reasoning, thought signatures - and is never shown or
// parsed for the answer.

// MaxContinuationBytes caps one response's continuation (spec §5.2). A larger
// one ends the turn; continuation is never sliced.
const MaxContinuationBytes = 256 * 1024

// Tool is one function the model may call.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any // a JSON-schema object
}

// ToolCall is one call the model asked for.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult answers one call.
type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// Continuation is a provider's own record of an assistant response, replayed
// verbatim. Provider names who wrote it: replay under another provider is
// refused.
type Continuation struct {
	Provider ProviderID
	Raw      json.RawMessage
}

// ConvMessage is one step of a conversation. A user step carries Text; an
// assistant step carries the Turn it was (its continuation); a tool step
// carries the results for that turn's calls.
type ConvMessage struct {
	Role         string // "user" | "assistant" | "tool"
	Text         string
	Continuation *Continuation
	Calls        []ToolCall
	Results      []ToolResult
}

// ConvRequest is one model call in a conversation.
type ConvRequest struct {
	System   string
	Messages []ConvMessage
	Tools    []Tool
}

// Turn is one response: final Text (Done), or Calls to run first. ModelUsed
// is what the vendor reports.
type Turn struct {
	Text         string
	Calls        []ToolCall
	Continuation *Continuation
	ModelUsed    string
	Done         bool
}

// Converser is a provider that can hold a conversation with tools.
type Converser interface {
	Converse(ctx context.Context, req ConvRequest) (Turn, error)
}

// ErrToolsUnsupported: an OpenAI-compatible server positively said it does
// not support tools. Only this switches a conversation to snapshot mode
// (spec §6); every other failure stays an error.
var ErrToolsUnsupported = errors.New("this OpenAI-compatible server does not support tools")

// errContinuation: a continuation from another provider, or one too large.
var errContinuation = errors.New("the conversation's continuation state does not belong to this provider")

// checkTurn applies the shared outcome rules to a parsed turn: call ids are
// present and unique, every call names an offered tool, and a turn is either
// text or calls.
func checkTurn(t Turn, offered []Tool) (Turn, error) {
	if t.Continuation != nil && len(t.Continuation.Raw) > MaxContinuationBytes {
		return Turn{}, fmt.Errorf("the model's response state is larger than %d KiB; Klyx stopped", MaxContinuationBytes>>10)
	}
	names := map[string]bool{}
	for _, tl := range offered {
		names[tl.Name] = true
	}
	seen := map[string]bool{}
	for _, c := range t.Calls {
		if c.ID == "" {
			return Turn{}, fmt.Errorf("%w (a tool call without an id)", ErrUnexpectedOutput)
		}
		if seen[c.ID] {
			return Turn{}, fmt.Errorf("%w (a duplicate tool call id)", ErrUnexpectedOutput)
		}
		seen[c.ID] = true
		if !names[c.Name] {
			return Turn{}, fmt.Errorf("%w (a call to a tool Klyx did not offer)", ErrUnexpectedOutput)
		}
		if !json.Valid(c.Args) {
			return Turn{}, fmt.Errorf("%w (tool arguments that are not JSON)", ErrUnexpectedOutput)
		}
	}
	if len(t.Calls) == 0 {
		if strings.TrimSpace(t.Text) == "" {
			return Turn{}, ErrEmpty
		}
		t.Done = true
	}
	return t, nil
}

func continuationFor(m ConvMessage, p ProviderID) (json.RawMessage, error) {
	if m.Continuation == nil || m.Continuation.Provider != p {
		return nil, errContinuation
	}
	return m.Continuation.Raw, nil
}

// ---- Anthropic ----------------------------------------------------------------

// Converse runs one conversation step on the Messages API with tools. The
// assistant message is kept as received (its raw
// JSON) and replayed through the SDK's ToParam, so thinking signatures and
// tool_use ids survive.
func (p *anthropicProvider) Converse(ctx context.Context, req ConvRequest) (Turn, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: apiMaxOutputTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: req.System}},
		// No thinking field: current models default to adaptive thinking
		// (spec 2026-10-13 §1.2), and thinking blocks are replayed below.
	}
	for _, t := range req.Tools {
		props := t.Schema["properties"]
		var required []string
		if r, ok := t.Schema["required"].([]string); ok {
			required = r
		}
		tp := anthropic.BetaToolParam{Name: t.Name, Description: anthropic.String(t.Description),
			InputSchema: anthropic.BetaToolInputSchemaParam{Properties: props, Required: required}}
		params.Tools = append(params.Tools, anthropic.BetaToolUnionParam{OfTool: &tp})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			params.Messages = append(params.Messages, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(m.Text)))
		case "assistant":
			raw, err := continuationFor(m, ProviderAnthropic)
			if err != nil {
				return Turn{}, err
			}
			var msg anthropic.BetaMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				return Turn{}, fmt.Errorf("replay assistant message: %w", err)
			}
			params.Messages = append(params.Messages, msg.ToParam())
		case "tool":
			var blocks []anthropic.BetaContentBlockParamUnion
			for _, r := range m.Results {
				blocks = append(blocks, anthropic.NewBetaToolResultBlock(r.CallID, r.Content, r.IsError))
			}
			params.Messages = append(params.Messages, anthropic.NewBetaUserMessage(blocks...))
		}
	}
	if p.refusalFallback {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	msg, err := p.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return Turn{}, scrubErr(err, p.key)
	}
	return anthropicTurn(msg, p.key, req.Tools)
}

func anthropicTurn(msg *anthropic.BetaMessage, key string, offered []Tool) (Turn, error) {
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return Turn{}, refused(string(msg.StopDetails.Category), msg.StopDetails.Explanation, key)
	case anthropic.BetaStopReasonMaxTokens, anthropic.BetaStopReasonModelContextWindowExceeded:
		return Turn{}, ErrTruncated
	case anthropic.BetaStopReasonEndTurn, anthropic.BetaStopReasonStopSequence, anthropic.BetaStopReasonToolUse:
	default:
		// pause_turn (a server tool Klyx never offers) or a reason this
		// build does not know.
		return Turn{}, ErrUnexpectedOutput
	}
	t := Turn{ModelUsed: string(msg.Model), Continuation: &Continuation{Provider: ProviderAnthropic, Raw: json.RawMessage(msg.RawJSON())}}
	var parts []string
	for _, b := range msg.Content {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "thinking", "redacted_thinking":
		case "tool_use":
			args, _ := json.Marshal(b.Input)
			if raw := b.JSON.Input.Raw(); raw != "" {
				args = json.RawMessage(raw)
			}
			t.Calls = append(t.Calls, ToolCall{ID: b.ID, Name: b.Name, Args: args})
		default:
			// server_tool_use, mcp_tool_use or a block this build does not
			// know: nothing Klyx offered.
			return Turn{}, ErrUnexpectedOutput
		}
	}
	if msg.StopReason == anthropic.BetaStopReasonToolUse && len(t.Calls) == 0 {
		return Turn{}, fmt.Errorf("%w (a tool_use stop without a call)", ErrUnexpectedOutput)
	}
	if len(t.Calls) == 0 {
		text, err := joinText(parts)
		if err != nil {
			return Turn{}, err
		}
		t.Text = text
	} else {
		t.Text = strings.Join(parts, "")
	}
	return checkTurn(t, offered)
}

// ---- OpenAI (Responses) --------------------------------------------------------

// Converse runs one step on the Responses API with function tools. With
// store:false the output items - reasoning with its encrypted content,
// function calls, messages - are replayed verbatim as the next input.
func (p *openaiProvider) Converse(ctx context.Context, req ConvRequest) (Turn, error) {
	input, err := openaiInput(req.Messages)
	if err != nil {
		return Turn{}, err
	}
	tools := make([]map[string]any, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Schema})
	}
	payload := map[string]any{
		"model":             p.model,
		"instructions":      req.System,
		"input":             input,
		"tools":             tools,
		"max_output_tokens": apiMaxOutputTokens,
		"store":             false,
		"include":           []string{"reasoning.encrypted_content"},
	}
	body, err := postJSON(ctx, p.http, p.baseURL+"/v1/responses", p.headers(), payload, p.key)
	if err != nil {
		return Turn{}, err
	}
	return openaiTurn(body, p.model, p.key, req.Tools)
}

func openaiInput(msgs []ConvMessage) ([]json.RawMessage, error) {
	var input []json.RawMessage
	add := func(v any) {
		b, _ := json.Marshal(v)
		input = append(input, b)
	}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			add(map[string]any{"role": "user", "content": m.Text})
		case "assistant":
			raw, err := continuationFor(m, ProviderOpenAI)
			if err != nil {
				return nil, err
			}
			var items []json.RawMessage
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("replay output items: %w", err)
			}
			input = append(input, items...)
		case "tool":
			for _, r := range m.Results {
				add(map[string]any{"type": "function_call_output", "call_id": r.CallID, "output": r.Content})
			}
		}
	}
	return input, nil
}

func openaiTurn(body []byte, configured, key string, offered []Tool) (Turn, error) {
	var out struct {
		Model             string `json:"model"`
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Turn{}, fmt.Errorf("decode OpenAI response: %w", err)
	}
	switch out.Status {
	case "completed":
	case "incomplete":
		if out.IncompleteDetails != nil && out.IncompleteDetails.Reason == "content_filter" {
			return Turn{}, refused("content_filter", "", key)
		}
		return Turn{}, ErrTruncated
	default:
		if out.Error != nil && out.Error.Message != "" {
			return Turn{}, fmt.Errorf("OpenAI response %s: %s", Scrub(out.Status, key), Scrub(out.Error.Message, key))
		}
		return Turn{}, fmt.Errorf("OpenAI response status %q", Scrub(out.Status, key))
	}
	raw, _ := json.Marshal(out.Output)
	model := out.Model
	if model == "" {
		model = configured
	}
	t := Turn{ModelUsed: model, Continuation: &Continuation{Provider: ProviderOpenAI, Raw: raw}}
	var parts []string
	for _, itemRaw := range out.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		}
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			return Turn{}, ErrUnexpectedOutput
		}
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					parts = append(parts, c.Text)
				case "refusal":
					return Turn{}, refused("", c.Refusal, key)
				default:
					return Turn{}, ErrUnexpectedOutput
				}
			}
		case "reasoning":
		case "function_call":
			args := json.RawMessage(item.Arguments)
			if strings.TrimSpace(item.Arguments) == "" {
				args = json.RawMessage("{}")
			}
			t.Calls = append(t.Calls, ToolCall{ID: item.CallID, Name: item.Name, Args: args})
		default:
			// A built-in tool call (web search, code interpreter) Klyx
			// never offered.
			return Turn{}, ErrUnexpectedOutput
		}
	}
	if len(t.Calls) == 0 {
		text, err := joinText(parts)
		if err != nil {
			return Turn{}, err
		}
		t.Text = text
	} else {
		t.Text = strings.Join(parts, "")
	}
	return checkTurn(t, offered)
}

// ---- Gemini ---------------------------------------------------------------------

// Converse runs one step on generateContent with function declarations. The
// model's content (parts with thoughtSignature) is replayed verbatim;
// function responses answer the calls in order.
func (p *geminiProvider) Converse(ctx context.Context, req ConvRequest) (Turn, error) {
	var contents []json.RawMessage
	add := func(v any) {
		b, _ := json.Marshal(v)
		contents = append(contents, b)
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			add(map[string]any{"role": "user", "parts": []map[string]any{{"text": m.Text}}})
		case "assistant":
			raw, err := continuationFor(m, ProviderGemini)
			if err != nil {
				return Turn{}, err
			}
			contents = append(contents, raw)
		case "tool":
			var parts []map[string]any
			for _, r := range m.Results {
				resp := map[string]any{"result": r.Content}
				if r.IsError {
					resp = map[string]any{"error": r.Content}
				}
				fr := map[string]any{"name": r.Name, "response": resp}
				if !strings.HasPrefix(r.CallID, geminiLocalCallID) {
					fr["id"] = r.CallID
				}
				parts = append(parts, map[string]any{"functionResponse": fr})
			}
			add(map[string]any{"role": "user", "parts": parts})
		}
	}
	var decls []map[string]any
	for _, t := range req.Tools {
		decls = append(decls, map[string]any{"name": t.Name, "description": t.Description, "parametersJsonSchema": t.Schema})
	}
	payload := map[string]any{
		"systemInstruction": map[string]any{"parts": []geminiPart{{Text: req.System}}},
		"contents":          contents,
		"tools":             []map[string]any{{"functionDeclarations": decls}},
		"generationConfig":  map[string]any{"maxOutputTokens": apiMaxOutputTokens},
	}
	body, err := postJSON(ctx, p.http, p.baseURL+"/v1beta/models/"+geminiModelPath(p.model)+":generateContent", p.headers(), payload, p.key)
	if err != nil {
		return Turn{}, err
	}
	return geminiTurn(body, p.model, p.key, req.Tools)
}

// geminiLocalCallID prefixes the ids Klyx gives calls that came without one;
// such an id is never sent back in a functionResponse.
const geminiLocalCallID = "klyx-call-"

func geminiTurn(body []byte, configured, key string, offered []Tool) (Turn, error) {
	var out struct {
		ModelVersion   string `json:"modelVersion"`
		PromptFeedback *struct {
			BlockReason        string `json:"blockReason"`
			BlockReasonMessage string `json:"blockReasonMessage"`
		} `json:"promptFeedback"`
		Candidates []struct {
			FinishReason  string          `json:"finishReason"`
			FinishMessage string          `json:"finishMessage"`
			Content       json.RawMessage `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Turn{}, fmt.Errorf("decode Gemini response: %w", err)
	}
	if out.PromptFeedback != nil && out.PromptFeedback.BlockReason != "" {
		return Turn{}, refused(out.PromptFeedback.BlockReason, out.PromptFeedback.BlockReasonMessage, key)
	}
	if len(out.Candidates) == 0 {
		return Turn{}, ErrEmpty
	}
	c := out.Candidates[0]
	switch {
	case c.FinishReason == "STOP":
	case c.FinishReason == "MAX_TOKENS":
		return Turn{}, ErrTruncated
	case geminiRefusalReasons[c.FinishReason]:
		return Turn{}, refused(c.FinishReason, c.FinishMessage, key)
	default:
		return Turn{}, ErrUnexpectedOutput
	}
	var content struct {
		Role  string                       `json:"role"`
		Parts []map[string]json.RawMessage `json:"parts"`
	}
	if len(c.Content) > 0 {
		if err := json.Unmarshal(c.Content, &content); err != nil {
			return Turn{}, ErrUnexpectedOutput
		}
	}
	model := out.ModelVersion
	if model == "" {
		model = configured
	}
	t := Turn{ModelUsed: model, Continuation: &Continuation{Provider: ProviderGemini, Raw: c.Content}}
	var parts []string
	for i, part := range content.Parts {
		if fc, ok := part["functionCall"]; ok {
			var call struct {
				ID   string          `json:"id"`
				Name string          `json:"name"`
				Args json.RawMessage `json:"args"`
			}
			if err := json.Unmarshal(fc, &call); err != nil {
				return Turn{}, ErrUnexpectedOutput
			}
			if call.ID == "" {
				// Older Gemini models give no call id; Klyx numbers the
				// calls in order, which is how functionResponse pairs.
				call.ID = fmt.Sprintf("%s%d", geminiLocalCallID, i)
			}
			if len(call.Args) == 0 {
				call.Args = json.RawMessage("{}")
			}
			t.Calls = append(t.Calls, ToolCall{ID: call.ID, Name: call.Name, Args: call.Args})
			continue
		}
		for field := range part {
			if !geminiAllowedPartFields[field] {
				return Turn{}, ErrUnexpectedOutput
			}
		}
		var thought bool
		if raw, ok := part["thought"]; ok {
			_ = json.Unmarshal(raw, &thought)
		}
		if thought {
			continue
		}
		var text string
		if raw, ok := part["text"]; ok {
			if err := json.Unmarshal(raw, &text); err != nil {
				return Turn{}, ErrUnexpectedOutput
			}
		}
		parts = append(parts, text)
	}
	if len(t.Calls) == 0 {
		text, err := joinText(parts)
		if err != nil {
			return Turn{}, err
		}
		t.Text = text
	} else {
		t.Text = strings.Join(parts, "")
	}
	return checkTurn(t, offered)
}

// ---- OpenAI-compatible (Chat Completions) ---------------------------------------

// Converse runs one step on /v1/chat/completions with tools. A server that
// positively refuses tools returns ErrToolsUnsupported; nothing else does.
func (p *compatProvider) Converse(ctx context.Context, req ConvRequest) (Turn, error) {
	msgs := []map[string]any{{"role": "system", "content": req.System}}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, map[string]any{"role": "user", "content": m.Text})
		case "assistant":
			raw, err := continuationFor(m, ProviderOpenAICompatible)
			if err != nil {
				return Turn{}, err
			}
			var msg map[string]any
			if err := json.Unmarshal(raw, &msg); err != nil {
				return Turn{}, fmt.Errorf("replay assistant message: %w", err)
			}
			msgs = append(msgs, msg)
		case "tool":
			for _, r := range m.Results {
				msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": r.CallID, "content": r.Content})
			}
		}
	}
	var tools []map[string]any
	for _, t := range req.Tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Schema}})
	}
	payload := map[string]any{"model": p.model, "max_tokens": compatMaxTokens(PurposeChat), "temperature": 0, "messages": msgs, "tools": tools}
	body, err := postJSON(ctx, p.http, p.baseURL+"/v1/chat/completions", p.headers(), payload, p.key)
	if err != nil {
		if toolsUnsupported(err) {
			return Turn{}, ErrToolsUnsupported
		}
		return Turn{}, err
	}
	return compatTurn(body, p.model, p.key, req.Tools)
}

// toolsUnsupported positively identifies a server refusing tools: HTTP 400
// or 422 whose body names tools, tool_choice or function as unsupported.
func toolsUnsupported(err error) bool {
	var se *HTTPStatusError
	if !errors.As(err, &se) || (se.Code != 400 && se.Code != 422) {
		return false
	}
	b := strings.ToLower(se.Body)
	names := strings.Contains(b, "tool") || strings.Contains(b, "function")
	says := strings.Contains(b, "not supported") || strings.Contains(b, "unsupported") || strings.Contains(b, "does not support")
	return names && says
}

func compatTurn(body []byte, configured, key string, offered []Tool) (Turn, error) {
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason *string         `json:"finish_reason"`
			Message      json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Turn{}, fmt.Errorf("decode chat response: %w", err)
	}
	if len(out.Choices) == 0 {
		return Turn{}, ErrEmpty
	}
	c := out.Choices[0]
	var msg struct {
		Content   string `json:"content"`
		Refusal   string `json:"refusal"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
		FunctionCall json.RawMessage `json:"function_call"`
	}
	if err := json.Unmarshal(c.Message, &msg); err != nil {
		return Turn{}, ErrUnexpectedOutput
	}
	if hasJSONValue(msg.FunctionCall) {
		return Turn{}, ErrUnexpectedOutput // the legacy single-call form Klyx does not offer
	}
	if msg.Refusal != "" {
		return Turn{}, refused("", msg.Refusal, key)
	}
	finish := ""
	if c.FinishReason != nil {
		finish = *c.FinishReason
	}
	model := out.Model
	if model == "" {
		model = configured
	}
	t := Turn{ModelUsed: model, Continuation: &Continuation{Provider: ProviderOpenAICompatible, Raw: c.Message}}
	for _, tc := range msg.ToolCalls {
		if tc.Type != "" && tc.Type != "function" {
			return Turn{}, ErrUnexpectedOutput
		}
		args := json.RawMessage(tc.Function.Arguments)
		if strings.TrimSpace(tc.Function.Arguments) == "" {
			args = json.RawMessage("{}")
		}
		t.Calls = append(t.Calls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	switch {
	case len(t.Calls) > 0:
		if finish != "tool_calls" && finish != "stop" {
			return Turn{}, ErrUnconfirmed
		}
		t.Text = msg.Content
	case finish == "stop":
		text, err := joinText([]string{msg.Content})
		if err != nil {
			return Turn{}, err
		}
		t.Text = text
	case finish == "content_filter":
		return Turn{}, refused("content_filter", "", key)
	case finish == "length":
		return Turn{}, ErrTruncated
	default:
		return Turn{}, ErrUnconfirmed
	}
	return checkTurn(t, offered)
}

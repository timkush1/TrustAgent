package llmapi

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// ChatResponse is a parsed response of any format (JSON, or reassembled from a stream by
// StreamAssembler), normalized to OpenAI-style choices. An Anthropic message is one choice with
// index 0 whose FinishReason is the stop_reason.
type ChatResponse struct {
	// Format is the API format the response was read as.
	Format  string
	ID      string
	Model   string
	Created int64
	Choices []ChatChoice
	Usage   *Usage
	// RawUsage is the upstream usage object as sent (OpenRouter adds cost, OpenAI token details);
	// synthesized streams repeat it instead of a reduced copy.
	RawUsage json.RawMessage
}

// ChatChoice is one choice of a response.
type ChatChoice struct {
	Index int
	// Text is the message content (chat), the text (completions) or the text blocks (Anthropic):
	// the gated text. Reasoning fields and tool-call arguments are never part of it (SPEC §2.10
	// gated response text).
	Text string
	// FinishReason is finish_reason (OpenAI) or stop_reason (Anthropic); empty until known.
	FinishReason string
	// ToolCalls reports that the message carries tool calls (which are not gated).
	ToolCalls bool
	// Refusal is the message refusal text, if any.
	Refusal string
}

// Usage is the token usage an upstream reported. For Anthropic, PromptTokens is input_tokens and
// CompletionTokens output_tokens.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// ErrNotChatResponse is returned for a body that is not a response of the expected format.
var ErrNotChatResponse = errors.New("llmapi: body is not a response of the API format")

// ParseResponse reads the parts of a JSON response the gate needs, for any API format.
func ParseResponse(format string, body []byte) (*ChatResponse, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return nil, ErrNotChatResponse
	}
	switch format {
	case FormatOpenAIChat, FormatOpenAICompletions:
		return parseOpenAIResponse(format, top)
	case FormatAnthropicMessages:
		return parseAnthropicResponse(top)
	}
	return nil, ErrUnknownFormat
}

// ParseChatResponse reads the parts of a chat completion the gate needs:
// ParseResponse(FormatOpenAIChat, body).
func ParseChatResponse(body []byte) (*ChatResponse, error) {
	return ParseResponse(FormatOpenAIChat, body)
}

func parseOpenAIResponse(format string, top map[string]json.RawMessage) (*ChatResponse, error) {
	var choices []json.RawMessage
	if err := json.Unmarshal(top["choices"], &choices); err != nil {
		return nil, ErrNotChatResponse
	}
	r := &ChatResponse{Format: format}
	decodeLoose(top["id"], &r.ID)
	decodeLoose(top["model"], &r.Model)
	decodeLoose(top["created"], &r.Created)
	if raw, ok := top["usage"]; ok && !isNull(raw) {
		var u Usage
		if json.Unmarshal(raw, &u) == nil {
			r.Usage = &u
			r.RawUsage = bytes.Clone(raw)
		}
	}
	for i, raw := range choices {
		var c map[string]json.RawMessage
		if json.Unmarshal(raw, &c) != nil {
			continue
		}
		choice := ChatChoice{Index: i}
		decodeLoose(c["index"], &choice.Index)
		decodeLoose(c["finish_reason"], &choice.FinishReason)
		if format == FormatOpenAICompletions {
			decodeLoose(c["text"], &choice.Text)
			r.Choices = append(r.Choices, choice)
			continue
		}
		var msg map[string]json.RawMessage
		decodeLoose(c["message"], &msg)
		choice.Text = strings.Join(ContentParts(msg["content"]), "\n")
		decodeLoose(msg["refusal"], &choice.Refusal)
		var calls []json.RawMessage
		decodeLoose(msg["tool_calls"], &calls)
		var fnCall map[string]json.RawMessage
		decodeLoose(msg["function_call"], &fnCall)
		choice.ToolCalls = len(calls) > 0 || fnCall != nil
		r.Choices = append(r.Choices, choice)
	}
	return r, nil
}

// Gated returns the choice the gate checks: choice 0 (with n>1 the other choices are not gated
// and are recorded as skipped:multi_choice).
func (r *ChatResponse) Gated() (ChatChoice, bool) {
	for _, c := range r.Choices {
		if c.Index == 0 {
			return c, true
		}
	}
	if len(r.Choices) > 0 {
		return r.Choices[0], true
	}
	return ChatChoice{}, false
}

// MultiChoice reports whether the response carries more than one choice: only choice 0 is gated.
func (r *ChatResponse) MultiChoice() bool { return len(r.Choices) > 1 }

// sortChoices orders choices by index.
func sortChoices(cs []ChatChoice) {
	slices.SortStableFunc(cs, func(a, b ChatChoice) int { return cmp.Compare(a.Index, b.Index) })
}

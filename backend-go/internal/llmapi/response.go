package llmapi

import (
	"encoding/json"
	"errors"
	"strings"
)

// ChatResponse is a parsed non-streaming /v1/chat/completions response.
type ChatResponse struct {
	ID      string
	Model   string
	Choices []ChatChoice
	Usage   *Usage
}

// ChatChoice is one choice of a chat response.
type ChatChoice struct {
	Index int
	// Text is the message content: the gated text. Reasoning fields and tool-call arguments are
	// never part of it (SPEC §2.10 gated response text).
	Text         string
	FinishReason string
	// ToolCalls reports that the message carries tool calls (which are not gated).
	ToolCalls bool
	// Refusal is the message refusal text, if any.
	Refusal string
}

// Usage is the token usage an upstream reported.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// ErrNotChatResponse is returned for a body that is not a chat completion object.
var ErrNotChatResponse = errors.New("llmapi: body is not a chat completion")

// ParseChatResponse reads the parts of a chat completion the gate needs.
func ParseChatResponse(body []byte) (*ChatResponse, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return nil, ErrNotChatResponse
	}
	var choices []json.RawMessage
	if err := json.Unmarshal(top["choices"], &choices); err != nil {
		return nil, ErrNotChatResponse
	}
	r := &ChatResponse{}
	decodeLoose(top["id"], &r.ID)
	decodeLoose(top["model"], &r.Model)
	if raw, ok := top["usage"]; ok && !isNull(raw) {
		var u Usage
		if json.Unmarshal(raw, &u) == nil {
			r.Usage = &u
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

// Gated returns the choice the gate checks: choice 0 (with n>1 the other choices are not gated).
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

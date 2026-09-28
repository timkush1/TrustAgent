package llmapi

import (
	"bytes"
	"encoding/json"
)

// Anthropic Messages content block types the gateway reads.
const (
	blockText       = "text"
	blockToolUse    = "tool_use"
	blockToolResult = "tool_result"
	// blockServerToolUse is a server-side tool call (web search and the like).
	blockServerToolUse = "server_tool_use"
)

// parseAnthropicMessages normalizes an Anthropic Messages request: the top-level system (a string
// or text blocks) becomes a leading system message, tool_use blocks become tool calls, and
// tool_result blocks become role "tool" messages placed before the rest of their user message,
// which keeps only its text parts. Images, documents, thinking and other block types carry no
// gated or evidence text and are ignored (SPEC §2.10 tolerant parsing).
func parseAnthropicMessages(system, messages json.RawMessage) []Message {
	var out []Message
	if parts := ContentParts(system); len(parts) > 0 {
		out = append(out, Message{Role: "system", Parts: parts, Origin: OriginAnthropicSystem})
	}
	var msgs []json.RawMessage
	decodeLoose(messages, &msgs)
	for _, raw := range msgs {
		var fields map[string]json.RawMessage
		if !unmarshalObject(raw, &fields) {
			continue
		}
		m := Message{}
		decodeLoose(fields["role"], &m.Role)
		content := bytes.TrimSpace(fields["content"])
		if len(content) == 0 || content[0] != '[' {
			m.Parts = ContentParts(content)
			out = append(out, m)
			continue
		}
		var blocks []json.RawMessage
		decodeLoose(content, &blocks)
		toolResults := 0
		for _, b := range blocks {
			var blk map[string]json.RawMessage
			if !unmarshalObject(b, &blk) {
				continue
			}
			var typ string
			decodeLoose(blk["type"], &typ)
			switch typ {
			case blockText:
				var text string
				if json.Unmarshal(blk["text"], &text) == nil {
					m.Parts = append(m.Parts, text)
				}
			case blockToolUse, blockServerToolUse:
				var tc ToolCall
				decodeLoose(blk["id"], &tc.ID)
				decodeLoose(blk["name"], &tc.Name)
				m.ToolCalls = append(m.ToolCalls, tc)
			case blockToolResult:
				tr := Message{Role: "tool", Origin: OriginAnthropicToolResult, Parts: ContentParts(blk["content"])}
				decodeLoose(blk["tool_use_id"], &tr.ToolCallID)
				out = append(out, tr)
				toolResults++
			}
		}
		// A turn that only returns tool results is fully represented by its tool messages (as in
		// the OpenAI shape, where the user's question stays the last user message).
		if toolResults == 0 || len(m.Parts) > 0 || len(m.ToolCalls) > 0 {
			out = append(out, m)
		}
	}
	return out
}

// parseAnthropicResponse reads an Anthropic Messages response: one choice (index 0) whose text is
// the concatenation of the text blocks. Thinking and tool_use blocks are not gated; a tool_use
// block sets ToolCalls.
func parseAnthropicResponse(top map[string]json.RawMessage) (*ChatResponse, error) {
	var blocks []json.RawMessage
	if err := json.Unmarshal(top["content"], &blocks); err != nil {
		return nil, ErrNotChatResponse
	}
	var typ string
	decodeLoose(top["type"], &typ)
	if typ != "" && typ != "message" {
		return nil, ErrNotChatResponse
	}
	r := &ChatResponse{Format: FormatAnthropicMessages}
	decodeLoose(top["id"], &r.ID)
	decodeLoose(top["model"], &r.Model)
	choice := ChatChoice{Index: 0}
	decodeLoose(top["stop_reason"], &choice.FinishReason)
	var text bytes.Buffer
	for _, b := range blocks {
		var blk map[string]json.RawMessage
		if !unmarshalObject(b, &blk) {
			continue
		}
		var bt string
		decodeLoose(blk["type"], &bt)
		switch bt {
		case blockText:
			var s string
			if json.Unmarshal(blk["text"], &s) == nil {
				text.WriteString(s)
			}
		case blockToolUse, blockServerToolUse:
			choice.ToolCalls = true
		}
	}
	choice.Text = text.String()
	r.Choices = []ChatChoice{choice}
	if raw, ok := top["usage"]; ok && !isNull(raw) {
		var u anthropicUsage
		if json.Unmarshal(raw, &u) == nil {
			r.Usage = &Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens}
			r.RawUsage = bytes.Clone(raw)
		}
	}
	return r, nil
}

// anthropicUsage is the usage object of Anthropic responses and stream events.
type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

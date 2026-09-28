// Package llmapi knows the LLM API wire formats the gateway reads and writes (SPEC §3.1): tolerant
// request parsing (B3), the gated response text, and the bodies TrustAgent itself generates.
//
// S0 implements openai_chat (/v1/chat/completions, non-streaming). openai_completions,
// anthropic_messages, the SSE reader and writer, and the block, fallback and synthesized-stream
// builders land in P2/P3.
package llmapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// API formats (ApiFormat).
const (
	FormatOpenAIChat        = "openai_chat"
	FormatOpenAICompletions = "openai_completions"
	FormatAnthropicMessages = "anthropic_messages"
)

// Body members the gateway owns: the trustagent extension and the legacy test_response. Both are
// removed before a body is forwarded upstream.
const (
	ExtensionField    = "trustagent"
	TestResponseField = "test_response"
)

// ErrUnparseable is returned for a body that is not a JSON object. Every other shape problem is
// tolerated: unknown or malformed members are ignored, and parsing never fails the request (B3).
var ErrUnparseable = errors.New("llmapi: request body is not a JSON object")

// Message is one chat message as the gateway reads it.
type Message struct {
	Role string
	// Parts are the texts of the content's text and input_text parts, in order. A string content
	// is a single part; other part types (images, audio, files) are ignored.
	Parts []string
	// Name is the message name (legacy role "function" carries the function name here).
	Name string
	// ToolCallID links a role "tool" message to the assistant tool call it answers.
	ToolCallID string
	// ToolCalls are an assistant message's tool calls, used to resolve tool result names.
	ToolCalls []ToolCall
}

// Text joins the text parts with a newline: the text of a message where one text is needed.
func (m *Message) Text() string { return strings.Join(m.Parts, "\n") }

// ToolCall is an assistant tool call reference.
type ToolCall struct {
	ID   string
	Name string
}

// Extension is the request body "trustagent" member (SPEC §2.12):
// {"route"?, "context"?: [{"id"?, "text", "source"?}], "rules"?: {...}}.
type Extension struct {
	// Present is true when the body carries a trustagent member (of any shape).
	Present bool
	Route   string
	Context []ContextItem
	// Rules are the string-valued members of trustagent.rules, honoured only with
	// overrides.client_rules.
	Rules map[string]string
}

// ContextItem is one trustagent.context entry.
type ContextItem struct {
	ID     string
	Text   string
	Source string
}

// ChatRequest is a parsed /v1/chat/completions request body.
type ChatRequest struct {
	Model    string
	Stream   bool
	N        int
	Messages []Message
	// Extension is the trustagent member.
	Extension Extension
	// TestResponse is the legacy test_response member: nil when absent or null.
	TestResponse *string
}

// ParseChatRequest parses an OpenAI chat request tolerantly. It fails only when body is not a JSON
// object; malformed members are ignored rather than rejected.
func ParseChatRequest(body []byte) (*ChatRequest, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return nil, ErrUnparseable
	}
	r := &ChatRequest{}
	decodeLoose(top["model"], &r.Model)
	decodeLoose(top["stream"], &r.Stream)
	decodeLoose(top["n"], &r.N)
	if raw, ok := top[TestResponseField]; ok && !isNull(raw) {
		s := ""
		decodeLoose(raw, &s)
		r.TestResponse = &s
	}
	var msgs []json.RawMessage
	decodeLoose(top["messages"], &msgs)
	for _, raw := range msgs {
		if m, ok := parseMessage(raw); ok {
			r.Messages = append(r.Messages, m)
		}
	}
	if raw, ok := top[ExtensionField]; ok {
		r.Extension = parseExtension(raw)
	}
	return r, nil
}

func parseMessage(raw json.RawMessage) (Message, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Message{}, false
	}
	var m Message
	decodeLoose(fields["role"], &m.Role)
	decodeLoose(fields["name"], &m.Name)
	decodeLoose(fields["tool_call_id"], &m.ToolCallID)
	m.Parts = ContentParts(fields["content"])
	var calls []json.RawMessage
	decodeLoose(fields["tool_calls"], &calls)
	for _, raw := range calls {
		var call map[string]json.RawMessage
		if json.Unmarshal(raw, &call) != nil {
			continue
		}
		var fn map[string]json.RawMessage
		decodeLoose(call["function"], &fn)
		var tc ToolCall
		decodeLoose(call["id"], &tc.ID)
		decodeLoose(fn["name"], &tc.Name)
		m.ToolCalls = append(m.ToolCalls, tc)
	}
	return m, true
}

// ContentParts reads a message content: a string is one part; an array contributes the text of
// its {type: text|input_text, text} parts; anything else has no text.
func ContentParts(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || isNull(raw) {
		return nil
	}
	var s string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &s) == nil {
			return []string{s}
		}
		return nil
	}
	var parts []json.RawMessage
	if raw[0] != '[' || json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	var out []string
	for _, p := range parts {
		var part struct {
			Type string          `json:"type"`
			Text json.RawMessage `json:"text"`
		}
		if json.Unmarshal(p, &part) != nil || (part.Type != "text" && part.Type != "input_text") {
			continue
		}
		var text string
		if json.Unmarshal(part.Text, &text) == nil {
			out = append(out, text)
		}
	}
	return out
}

func parseExtension(raw json.RawMessage) Extension {
	ext := Extension{Present: true}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ext
	}
	decodeLoose(fields["route"], &ext.Route)
	var items []json.RawMessage
	decodeLoose(fields["context"], &items)
	for _, it := range items {
		var f map[string]json.RawMessage
		if json.Unmarshal(it, &f) != nil {
			continue
		}
		var c ContextItem
		decodeLoose(f["id"], &c.ID)
		decodeLoose(f["text"], &c.Text)
		decodeLoose(f["source"], &c.Source)
		ext.Context = append(ext.Context, c)
	}
	var rules map[string]json.RawMessage
	decodeLoose(fields["rules"], &rules)
	for k, v := range rules {
		var s string
		if json.Unmarshal(v, &s) == nil {
			if ext.Rules == nil {
				ext.Rules = map[string]string{}
			}
			ext.Rules[k] = s
		}
	}
	return ext
}

// ForwardBody returns body without the members only the gateway reads (trustagent and
// test_response). Every other member keeps its bytes and its position; a body without those
// members is returned unchanged.
func ForwardBody(body []byte) ([]byte, error) {
	return StripMembers(body, ExtensionField, TestResponseField)
}

// decodeLoose decodes raw into v when it has the right shape. It is only used with scalar, slice
// and map targets, which a mismatched value leaves untouched.
func decodeLoose(raw json.RawMessage, v any) {
	if len(raw) == 0 {
		return
	}
	_ = json.Unmarshal(raw, v)
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// Package llmapi knows the LLM API wire formats the gateway reads and writes (SPEC §3.1, §2.12):
// openai_chat (/v1/chat/completions), openai_completions (/v1/completions) and anthropic_messages
// (/v1/messages).
//
//   - Requests: FormatForPath picks the format; ParseRequest reads any format tolerantly (B3) into
//     one normalized ChatRequest (messages with the developer role and the Anthropic top-level
//     system, tool results, the trustagent extension); ForwardBody strips the gateway's members
//     without re-encoding the rest.
//   - Responses: ParseResponse reads a JSON answer into a ChatResponse whose choice 0 holds the
//     gated text (content / text blocks only: reasoning and tool-call arguments are excluded).
//   - Streams: Reader reads server-sent events byte-preservingly, StreamAssembler rebuilds the
//     same ChatResponse from the deltas of each format, StreamWriter forwards events, holds the
//     terminal event back, injects the ": trustagent {...}" comment and sets HTTP trailers, and
//     Relay and Collect drive the passthrough and buffer streaming modes.
//   - Bodies TrustAgent writes itself: block bodies (message and error style, JSON and stream, in
//     OpenAI and Anthropic shapes), the fallback request, synthesized streams and errors in the
//     upstream API's own shape.
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

// Message origins: where a normalized message came from when it is not an OpenAI chat message.
const (
	// OriginAnthropicSystem is the Anthropic top-level system, read as a leading system message.
	OriginAnthropicSystem = "anthropic_system"
	// OriginAnthropicToolResult is an Anthropic tool_result block, read as a role "tool" message
	// whose ToolCallID is the block's tool_use_id.
	OriginAnthropicToolResult = "anthropic_tool_result"
	// OriginPrompt is a /v1/completions prompt, read as a user message.
	OriginPrompt = "completions_prompt"
)

// Message is one chat message as the gateway reads it. Every format is normalized to the OpenAI
// chat roles: system, developer, user, assistant, tool (and legacy function).
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
	// Origin is empty for an OpenAI chat message, else one of the Origin constants.
	Origin string
}

// Text joins the text parts with a newline: the text of a message where one text is needed.
func (m *Message) Text() string { return strings.Join(m.Parts, "\n") }

// IsSystem reports whether the message carries system instructions: role system (the Anthropic
// top-level system included) or developer.
func (m *Message) IsSystem() bool { return m.Role == "system" || m.Role == "developer" }

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

// ChatRequest is a parsed request body of any format, normalized to chat messages:
//   - openai_chat: the messages as sent;
//   - anthropic_messages: the top-level system first (role system, OriginAnthropicSystem), then
//     the messages, with each tool_result block lifted into a role "tool" message ahead of the
//     rest of its user message and each tool_use block read as a tool call;
//   - openai_completions: the first prompt as one user message (OriginPrompt).
type ChatRequest struct {
	// Format is the API format the body was parsed as.
	Format string
	Model  string
	Stream bool
	// IncludeUsage is stream_options.include_usage (OpenAI streams end with a usage chunk).
	IncludeUsage bool
	// N is the number of choices asked for (0 when absent); only choice 0 is gated.
	N        int
	Messages []Message
	// Prompts are the string prompts of a /v1/completions request (a string prompt is one; token
	// prompts are not read).
	Prompts []string
	// Extension is the trustagent member.
	Extension Extension
	// TestResponse is the legacy test_response member: nil when absent or null.
	TestResponse *string
}

// ParseRequest parses a request body of the given API format tolerantly. It fails only when body
// is not a JSON object (ErrUnparseable) or the format is unknown (ErrUnknownFormat); malformed
// members are ignored rather than rejected (B3).
func ParseRequest(format string, body []byte) (*ChatRequest, error) {
	top, err := parseTop(body)
	if err != nil {
		return nil, err
	}
	r := &ChatRequest{Format: format}
	switch format {
	case FormatOpenAIChat:
		r.Messages = parseMessages(top["messages"])
	case FormatOpenAICompletions:
		r.Prompts = parsePrompts(top["prompt"])
		if len(r.Prompts) > 0 {
			r.Messages = []Message{{Role: "user", Parts: []string{r.Prompts[0]}, Origin: OriginPrompt}}
		}
	case FormatAnthropicMessages:
		r.Messages = parseAnthropicMessages(top["system"], top["messages"])
	default:
		return nil, ErrUnknownFormat
	}
	parseCommon(top, r)
	return r, nil
}

// ParseChatRequest parses an OpenAI chat request tolerantly: ParseRequest(FormatOpenAIChat, body).
func ParseChatRequest(body []byte) (*ChatRequest, error) {
	return ParseRequest(FormatOpenAIChat, body)
}

// ErrUnknownFormat is returned for an API format this package does not know.
var ErrUnknownFormat = errors.New("llmapi: unknown API format")

func parseTop(body []byte) (map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return nil, ErrUnparseable
	}
	return top, nil
}

// parseCommon reads the members every format shares.
func parseCommon(top map[string]json.RawMessage, r *ChatRequest) {
	decodeLoose(top["model"], &r.Model)
	decodeLoose(top["stream"], &r.Stream)
	decodeLoose(top["n"], &r.N)
	var opts map[string]json.RawMessage
	decodeLoose(top["stream_options"], &opts)
	decodeLoose(opts["include_usage"], &r.IncludeUsage)
	if raw, ok := top[TestResponseField]; ok && !isNull(raw) {
		s := ""
		decodeLoose(raw, &s)
		r.TestResponse = &s
	}
	if raw, ok := top[ExtensionField]; ok {
		r.Extension = parseExtension(raw)
	}
}

func parseMessages(raw json.RawMessage) []Message {
	var msgs []json.RawMessage
	decodeLoose(raw, &msgs)
	var out []Message
	for _, raw := range msgs {
		if m, ok := parseMessage(raw); ok {
			out = append(out, m)
		}
	}
	return out
}

// parsePrompts reads a completions prompt: a string, or an array whose string elements count
// (token-id prompts carry no text).
func parsePrompts(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	var s string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &s) == nil {
			return []string{s}
		}
		return nil
	}
	var items []json.RawMessage
	decodeLoose(raw, &items)
	var out []string
	for _, it := range items {
		if json.Unmarshal(it, &s) == nil && bytes.HasPrefix(bytes.TrimSpace(it), []byte(`"`)) {
			out = append(out, s)
		}
	}
	return out
}

// LastUserText is the text of the last user message: the audit query (SPEC §3.1 low items).
func (r *ChatRequest) LastUserText() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == "user" {
			return r.Messages[i].Text()
		}
	}
	return ""
}

// SystemText joins the texts of the system and developer messages (the Anthropic top-level
// system included) with a blank line.
func (r *ChatRequest) SystemText() string {
	var parts []string
	for i := range r.Messages {
		if r.Messages[i].IsSystem() {
			parts = append(parts, r.Messages[i].Text())
		}
	}
	return strings.Join(parts, "\n\n")
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

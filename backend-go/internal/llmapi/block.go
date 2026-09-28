package llmapi

import (
	"encoding/json"
	"net/http"
)

// Block styles (RouteSettings.block.style).
const (
	BlockStyleMessage = "message"
	BlockStyleError   = "error"
)

// Block defaults (SPEC §2.7 RouteSettings.block).
const (
	DefaultBlockMessage = "I can't provide a verified answer to that right now."
	DefaultBlockStatus  = http.StatusUnprocessableEntity
)

// The error-style block body on OpenAI routes:
// {"error":{"message","type":"trustagent_policy_block","param":null,"code":"trustagent_blocked"}}.
const (
	BlockErrorType = "trustagent_policy_block"
	BlockErrorCode = "trustagent_blocked"
)

// Block is a route's block settings (RouteSettings.block); zero values take the defaults.
type Block struct {
	// Style is message (default: HTTP 200 with the message as the answer) or error.
	Style   string
	Message string
	// Status is the error-style status on OpenAI routes: 400, 403, 409, 422 or 451.
	Status int
	// AnthropicStopReason is refusal (default) or end_turn.
	AnthropicStopReason string
}

func (b Block) withDefaults() Block {
	if b.Style != BlockStyleError {
		b.Style = BlockStyleMessage
	}
	if b.Message == "" {
		b.Message = DefaultBlockMessage
	}
	b.Status = BlockStatus(b.Status)
	if b.AnthropicStopReason != StopReasonEndTurn {
		b.AnthropicStopReason = StopReasonRefusal
	}
	return b
}

// BlockStatus returns the error-style status to send: status itself when it is 400, 403, 409, 422
// or 451, else DefaultBlockStatus (a block is never a 429 or a 5xx, which clients retry).
func BlockStatus(status int) int {
	switch status {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity,
		http.StatusUnavailableForLegalReasons:
		return status
	}
	return DefaultBlockStatus
}

// BlockTarget is the exchange a block answers.
type BlockTarget struct {
	// TraceID names synthetic ids (<trace32>).
	TraceID string
	// Model is the request's model, used when the upstream response does not name one.
	Model string
	// Created is the unix time of objects the gateway builds.
	Created int64
	// Upstream is the upstream JSON answer: a message-style OpenAI block patches it, keeping id,
	// object, created, model, usage, system_fingerprint and every other member. Nil or
	// unparseable, a fresh object is built.
	Upstream []byte
	// Response is the upstream answer as parsed (for a stream, as assembled): its id, model,
	// created and usage are reported. When nil it is parsed from Upstream.
	Response *ChatResponse
	// IncludeUsage is the request's stream_options.include_usage (block streams).
	IncludeUsage bool
}

func (x *BlockTarget) response(format string) *ChatResponse {
	if x.Response != nil {
		return x.Response
	}
	if len(x.Upstream) > 0 {
		if r, err := ParseResponse(format, x.Upstream); err == nil {
			return r
		}
	}
	return &ChatResponse{Format: format}
}

// BlockJSON returns the status and JSON body that block a non-streaming answer (SPEC §2.12):
//
//   - message style, openai_chat: 200 with every choices[i].message replaced by
//     {"role":"assistant","content":<message>} (tool_calls, function_call and refusal go with it)
//     and finish_reason "content_filter";
//   - message style, openai_completions: 200 with choices[i].text = <message>, finish_reason
//     "content_filter" (M9);
//   - message style, anthropic_messages: 200 with a msg_trustagent_<trace32> message whose one
//     text block is the message and whose stop_reason is the route's anthropic_stop_reason;
//   - error style: the OpenAI trustagent_policy_block error with the route's status (400, 403,
//     409, 422 or 451), or on Anthropic routes a 400 invalid_request_error.
//
// A patched choice's logprobs are nulled: they would spell out the blocked answer.
func BlockJSON(format string, b Block, x BlockTarget) (int, []byte) {
	b = b.withDefaults()
	if b.Style == BlockStyleError {
		if IsAnthropic(format) {
			return http.StatusBadRequest, AnthropicError(AnthropicInvalidRequestError, b.Message)
		}
		return b.Status, marshal(openAIErrorBody{Error: openAIErrorDetail{Message: b.Message, Type: BlockErrorType, Code: BlockErrorCode}})
	}
	resp := x.response(format)
	model := firstNonEmpty(resp.Model, x.Model)
	switch format {
	case FormatAnthropicMessages:
		in := 0
		if resp.Usage != nil {
			in = resp.Usage.PromptTokens
		}
		stop := b.AnthropicStopReason
		return http.StatusOK, marshal(anthropicMessage{
			ID: SyntheticIDFor(format, x.TraceID), Type: "message", Role: "assistant", Model: model,
			Content: []anthropicText{{Type: blockText, Text: b.Message}}, StopReason: &stop,
			Usage: anthropicUsageTotal{InputTokens: in},
		})
	case FormatOpenAIChat, FormatOpenAICompletions:
		if body, ok := patchChoices(format, x.Upstream, b.Message); ok {
			return http.StatusOK, body
		}
		return http.StatusOK, openAIObject(format, x, model, b.Message, FinishReasonContentFilter)
	}
	return http.StatusInternalServerError, OpenAIError("unknown_format", "The API format is unknown.")
}

// BlockStream returns the events of a blocked streaming answer. It is always message style: once
// a stream's headers are committed an error status cannot be sent (SPEC §2.12 buffer streaming).
//
//   - OpenAI chat: data: {"id":<upstream id or chatcmpl-trustagent-<trace32>>,
//     "object":"chat.completion.chunk","created","model","choices":[{"index":0,
//     "delta":{"role":"assistant","content":<message>},"finish_reason":null}]}, then a chunk with
//     "delta":{} and "finish_reason":"content_filter", then "choices":[] with usage only under
//     stream_options.include_usage, then data: [DONE];
//   - OpenAI completions: the same chunks as text_completion objects with text;
//   - Anthropic: message_start, content_block_start, content_block_delta (text_delta: the
//     message), content_block_stop, message_delta (stop_reason, stop_sequence null, usage
//     output_tokens 0), message_stop.
func BlockStream(format string, b Block, x BlockTarget) []Event {
	b = b.withDefaults()
	resp := x.response(format)
	s := SynthStream{
		TraceID: x.TraceID, Created: resp.Created, Model: firstNonEmpty(resp.Model, x.Model), Text: b.Message,
		FinishReason: FinishReasonContentFilter, IncludeUsage: x.IncludeUsage, Usage: resp.Usage, RawUsage: resp.RawUsage,
	}
	if s.Created == 0 {
		s.Created = x.Created
	}
	if IsAnthropic(format) {
		// The Anthropic block id is always the gateway's own; no output token was delivered.
		s.FinishReason = b.AnthropicStopReason
		s.Usage, s.RawUsage = &Usage{}, nil
		if resp.Usage != nil {
			s.Usage.PromptTokens = resp.Usage.PromptTokens
		}
	} else {
		s.ID = resp.ID
	}
	return SynthesizeStream(format, s)
}

// openAIObject builds a non-streaming chat completion or text completion answering text.
func openAIObject(format string, x BlockTarget, model, text, finish string) []byte {
	type chatMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type chatChoice struct {
		Index        int         `json:"index"`
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	}
	type completionChoice struct {
		Index        int     `json:"index"`
		Text         string  `json:"text"`
		Logprobs     *string `json:"logprobs"`
		FinishReason string  `json:"finish_reason"`
	}
	type body struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []any  `json:"choices"`
	}
	v := body{ID: SyntheticIDFor(format, x.TraceID), Created: x.Created, Model: model}
	if format == FormatOpenAICompletions {
		v.Object = objectTextCompletion
		v.Choices = []any{completionChoice{Text: text, FinishReason: finish}}
	} else {
		v.Object = objectChatCompletion
		v.Choices = []any{chatChoice{Message: chatMessage{Role: "assistant", Content: text}, FinishReason: finish}}
	}
	return marshal(v)
}

// patchChoices rewrites the choices of an upstream OpenAI JSON answer to deliver text with
// finish_reason content_filter, leaving every other member's bytes alone. It reports false when
// upstream is not an object with a choices array. An empty choices array gets one choice.
func patchChoices(format string, upstream []byte, text string) ([]byte, bool) {
	if len(upstream) == 0 {
		return nil, false
	}
	obj, err := parseObject(upstream)
	if err != nil {
		return nil, false
	}
	raw, ok := obj.get("choices")
	var choices []json.RawMessage
	if !ok || json.Unmarshal(raw, &choices) != nil || choices == nil {
		return nil, false
	}
	if len(choices) == 0 {
		choices = []json.RawMessage{json.RawMessage(`{"index":0}`)}
	}
	message := marshal(struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{"assistant", text})
	finish := marshal(FinishReasonContentFilter)
	out := make([][]byte, 0, len(choices))
	for i, c := range choices {
		co, err := parseObject(c)
		if err != nil {
			co = &object{}
			co.set("index", marshal(i))
		}
		if format == FormatOpenAICompletions {
			co.set("text", marshal(text))
		} else {
			co.set("message", message)
		}
		if _, ok := co.get("logprobs"); ok {
			co.set("logprobs", []byte("null"))
		}
		co.set("finish_reason", finish)
		out = append(out, co.bytes())
	}
	obj.set("choices", joinArray(out))
	return obj.bytes(), true
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

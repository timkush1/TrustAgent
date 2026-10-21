package llmapi

import (
	"encoding/json"
)

// Finish reasons and Anthropic stop reasons the gateway writes.
const (
	FinishReasonStop          = "stop"
	FinishReasonContentFilter = "content_filter"
	StopReasonEndTurn         = "end_turn"
	StopReasonRefusal         = "refusal"
)

// Object types of the OpenAI bodies the gateway writes.
const (
	objectChatCompletion = "chat.completion"
	objectChatChunk      = "chat.completion.chunk"
	objectTextCompletion = "text_completion"
)

// SynthStream describes a stream the gateway writes itself: a block delivered as a stream, or a
// buffered answer (fallback or original text) re-emitted after the decision (SPEC §2.12).
type SynthStream struct {
	// ID is the stream's id; empty means the synthetic id of the format for TraceID.
	ID      string
	TraceID string
	// Created is the unix time of the OpenAI chunks.
	Created int64
	Model   string
	// Text is the whole answer, sent as one content delta.
	Text string
	// FinishReason is the OpenAI finish_reason or the Anthropic stop_reason; empty means "stop"
	// or "end_turn".
	FinishReason string
	// Usage is reported in the OpenAI usage chunk (when IncludeUsage) and in the Anthropic
	// message_start (input tokens) and message_delta (output tokens). RawUsage, when set, is the
	// OpenAI usage object sent verbatim instead.
	Usage    *Usage
	RawUsage json.RawMessage
	// IncludeUsage is the request's stream_options.include_usage: OpenAI streams then end with a
	// "choices":[] chunk carrying usage.
	IncludeUsage bool
}

// SynthesizeStream returns the events of a stream of format that delivers s.Text in one delta:
//
//   - openai_chat: a chunk with delta {role, content}, a chunk with an empty delta and the finish
//     reason, the usage chunk (with include_usage), then data: [DONE];
//   - openai_completions: the same with text instead of delta;
//   - anthropic_messages: message_start, content_block_start, content_block_delta (text_delta),
//     content_block_stop, message_delta (stop_reason), message_stop.
func SynthesizeStream(format string, s SynthStream) []Event {
	if s.ID == "" {
		s.ID = SyntheticIDFor(format, s.TraceID)
	}
	if IsAnthropic(format) {
		return synthAnthropic(s)
	}
	if s.FinishReason == "" {
		s.FinishReason = FinishReasonStop
	}
	return synthOpenAI(format, s)
}

// StreamFromResponse re-emits a delivered JSON answer (the fallback's, or the original under
// on_fail: original) as a synthesized stream of format for a buffered streaming client: the gated
// choice's text with finish_reason "stop" / stop_reason "end_turn" (SPEC §2.12 fallback step 6),
// the answer's id (else the synthetic one for traceID), created (else created), model and usage.
func StreamFromResponse(format string, resp *ChatResponse, traceID string, created int64, includeUsage bool) []Event {
	s := SynthStream{TraceID: traceID, Created: created, IncludeUsage: includeUsage}
	if resp != nil {
		c, _ := resp.Gated()
		s.ID, s.Model, s.Text, s.Usage, s.RawUsage = resp.ID, resp.Model, c.Text, resp.Usage, resp.RawUsage
		if resp.Created != 0 {
			s.Created = resp.Created
		}
	}
	if IsAnthropic(format) {
		s.RawUsage = nil
	}
	return SynthesizeStream(format, s)
}

// Chunk shapes, members in the order OpenAI sends them.
type chatChunk struct {
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []any           `json:"choices"`
	Usage   json.RawMessage `json:"usage,omitempty"`
}

type chatChunkChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type chatDelta struct {
	Role    string  `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
}

type completionChunkChoice struct {
	Index        int     `json:"index"`
	Text         string  `json:"text"`
	Logprobs     *string `json:"logprobs"`
	FinishReason *string `json:"finish_reason"`
}

func synthOpenAI(format string, s SynthStream) []Event {
	object := objectChatChunk
	if format == FormatOpenAICompletions {
		object = objectTextCompletion
	}
	chunk := func(choices []any, usage json.RawMessage) Event {
		return DataEvent("", marshal(chatChunk{ID: s.ID, Object: object, Created: s.Created, Model: s.Model, Choices: choices, Usage: usage}))
	}
	finish := s.FinishReason
	var events []Event
	if format == FormatOpenAICompletions {
		events = append(events,
			chunk([]any{completionChunkChoice{Index: 0, Text: s.Text}}, nil),
			chunk([]any{completionChunkChoice{Index: 0, FinishReason: &finish}}, nil))
	} else {
		text := s.Text
		events = append(events,
			chunk([]any{chatChunkChoice{Index: 0, Delta: chatDelta{Role: "assistant", Content: &text}}}, nil),
			chunk([]any{chatChunkChoice{Index: 0, FinishReason: &finish}}, nil))
	}
	if s.IncludeUsage {
		events = append(events, chunk([]any{}, openAIUsage(s.Usage, s.RawUsage)))
	}
	return append(events, DataEvent("", []byte(doneMarker)))
}

// openAIUsage is the usage object of a usage chunk: the upstream's own when known, else
// {prompt_tokens, completion_tokens, total_tokens} from u (zeros when unknown).
func openAIUsage(u *Usage, raw json.RawMessage) json.RawMessage {
	if len(raw) > 0 && json.Valid(raw) {
		return compactJSON(raw)
	}
	var v Usage
	if u != nil {
		v = *u
	}
	return marshal(struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	}{v.PromptTokens, v.CompletionTokens, v.PromptTokens + v.CompletionTokens})
}

// compactJSON removes insignificant whitespace (an SSE data line must not break).
func compactJSON(raw json.RawMessage) json.RawMessage {
	var v json.RawMessage
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	return marshal(v)
}

// Anthropic event shapes, members in the order the Messages API sends them.
type anthropicMessage struct {
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Role         string              `json:"role"`
	Model        string              `json:"model"`
	Content      []anthropicText     `json:"content"`
	StopReason   *string             `json:"stop_reason"`
	StopSequence *string             `json:"stop_sequence"`
	Usage        anthropicUsageTotal `json:"usage"`
}

type anthropicText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsageTotal struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicMessageStartEvent struct {
	Type    string           `json:"type"`
	Message anthropicMessage `json:"message"`
}

type anthropicBlockStartEvent struct {
	Type         string        `json:"type"`
	Index        int           `json:"index"`
	ContentBlock anthropicText `json:"content_block"`
}

type anthropicTextDeltaEvent struct {
	Type  string        `json:"type"`
	Index int           `json:"index"`
	Delta anthropicText `json:"delta"`
}

type anthropicBlockStopEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type anthropicMessageDeltaEvent struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason   string  `json:"stop_reason"`
		StopSequence *string `json:"stop_sequence"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicTypeOnly struct {
	Type string `json:"type"`
}

func synthAnthropic(s SynthStream) []Event {
	if s.FinishReason == "" {
		s.FinishReason = StopReasonEndTurn
	}
	var u Usage
	if s.Usage != nil {
		u = *s.Usage
	}
	ev := func(typ string, v any) Event { return DataEvent(typ, marshal(v)) }
	var md anthropicMessageDeltaEvent
	md.Type = anthropicMessageDelta
	md.Delta.StopReason = s.FinishReason
	md.Usage.OutputTokens = u.CompletionTokens
	return []Event{
		ev(anthropicMessageStart, anthropicMessageStartEvent{Type: anthropicMessageStart, Message: anthropicMessage{
			ID: s.ID, Type: "message", Role: "assistant", Model: s.Model, Content: []anthropicText{},
			Usage: anthropicUsageTotal{InputTokens: u.PromptTokens},
		}}),
		ev(anthropicContentBlockStart, anthropicBlockStartEvent{Type: anthropicContentBlockStart, ContentBlock: anthropicText{Type: blockText}}),
		ev(anthropicContentBlockDelta, anthropicTextDeltaEvent{Type: anthropicContentBlockDelta, Delta: anthropicText{Type: anthropicTextDelta, Text: s.Text}}),
		ev(anthropicContentBlockStop, anthropicBlockStopEvent{Type: anthropicContentBlockStop}),
		ev(anthropicMessageDelta, md),
		ev(anthropicMessageStop, anthropicTypeOnly{Type: anthropicMessageStop}),
	}
}

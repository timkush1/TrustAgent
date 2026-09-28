package llmapi

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
)

// EventKind classifies a stream event for whoever relays it.
type EventKind int

// Event kinds, from least to most significant (an event that is several reports the last).
const (
	// EventOther carries nothing the gate reads: comments, keepalives, pings, block stops and
	// unknown event types.
	EventOther EventKind = iota
	// EventDelta carried content, metadata or usage.
	EventDelta
	// EventFinish carried the finish reason of choice 0: the gated text is complete, so a client
	// that disconnects from now on still gets its answer gated and audited (SPEC §2.12).
	EventFinish
	// EventMalformed could not be parsed; the stream goes on.
	EventMalformed
	// EventError is an error the upstream reported inside the stream.
	EventError
	// EventTerminal is the end-of-stream marker, data: [DONE] (OpenAI) or message_stop
	// (Anthropic): the event a passthrough relay holds back until the decision.
	EventTerminal
)

var eventKindNames = [...]string{"other", "delta", "finish", "malformed", "error", "terminal"}

func (k EventKind) String() string {
	if k >= 0 && int(k) < len(eventKindNames) {
		return eventKindNames[k]
	}
	return "EventKind(" + strconv.Itoa(int(k)) + ")"
}

// StreamError is an error an upstream reported inside a stream: an OpenAI chunk with an error
// member, or an Anthropic error event.
type StreamError struct {
	Type    string
	Code    string
	Message string
}

func (e *StreamError) Error() string {
	msg := "llmapi: upstream stream error"
	for _, s := range []string{e.Type, e.Code} {
		if s != "" {
			msg += " " + s
		}
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Anthropic stream event types.
const (
	anthropicMessageStart      = "message_start"
	anthropicContentBlockStart = "content_block_start"
	anthropicContentBlockDelta = "content_block_delta"
	anthropicContentBlockStop  = "content_block_stop"
	anthropicMessageDelta      = "message_delta"
	anthropicMessageStop       = "message_stop"
	anthropicPing              = "ping"
	anthropicError             = "error"
	anthropicTextDelta         = "text_delta"
)

// doneMarker is the data of the OpenAI terminal event.
const doneMarker = "[DONE]"

// IsTerminal reports whether ev is the end-of-stream marker of format: data: [DONE] for the
// OpenAI formats, message_stop for Anthropic.
func IsTerminal(format string, ev Event) bool {
	if IsAnthropic(format) {
		return ev.Type() == anthropicMessageStop
	}
	return ev.HasData && string(bytes.TrimSpace(ev.Data)) == doneMarker
}

// StreamAssembler rebuilds a response from the events of one stream: the same ChatResponse
// ParseResponse returns for the equivalent JSON answer. Only content deltas (content, text,
// text_delta) make up the text; reasoning (reasoning, reasoning_content, thinking_delta) and
// tool-call arguments (tool_calls, input_json_delta) are left out, and tool calls only set
// ToolCalls (SPEC §2.10). Unparseable events are counted and skipped.
//
// Text is kept as the JSON string bodies the upstream sent and decoded as one string, so a
// character whose UTF-8 bytes or UTF-16 surrogate pair were split across two deltas comes out
// whole.
//
// A StreamAssembler is not safe for concurrent use.
type StreamAssembler struct {
	format     string
	id, model  string
	created    int64
	usage      *Usage
	rawUsage   json.RawMessage
	choices    map[int]*choiceAcc
	blocks     map[int]*blockAcc // Anthropic content blocks by index
	stopReason string            // Anthropic
	toolCalls  bool              // Anthropic
	finished   bool
	terminated bool
	err        *StreamError
	malformed  int
}

type choiceAcc struct {
	text, refusal jsonText
	finish        string
	toolCalls     bool
}

type blockAcc struct {
	typ  string
	text jsonText
}

// NewStreamAssembler returns an assembler for a stream of format.
func NewStreamAssembler(format string) (*StreamAssembler, error) {
	if Family(format) == "" {
		return nil, ErrUnknownFormat
	}
	return &StreamAssembler{format: format, choices: map[int]*choiceAcc{}, blocks: map[int]*blockAcc{}}, nil
}

// Format is the API format of the stream.
func (a *StreamAssembler) Format() string { return a.format }

// Finished reports whether the finish reason of choice 0 (the Anthropic stop_reason) was seen.
func (a *StreamAssembler) Finished() bool { return a.finished }

// Terminated reports whether the end-of-stream marker was seen.
func (a *StreamAssembler) Terminated() bool { return a.terminated }

// Err is the last error the upstream reported inside the stream, if any.
func (a *StreamAssembler) Err() *StreamError { return a.err }

// Malformed counts the events that could not be parsed.
func (a *StreamAssembler) Malformed() int { return a.malformed }

// Add reads one event and reports its kind.
func (a *StreamAssembler) Add(ev Event) EventKind {
	if IsAnthropic(a.format) {
		return a.addAnthropic(&ev)
	}
	return a.addOpenAI(&ev)
}

// Response returns what the stream said so far, choices ordered by index.
func (a *StreamAssembler) Response() *ChatResponse {
	r := &ChatResponse{Format: a.format, ID: a.id, Model: a.model, Created: a.created, RawUsage: bytes.Clone(a.rawUsage)}
	if a.usage != nil {
		u := *a.usage
		r.Usage = &u
	}
	if IsAnthropic(a.format) {
		var text bytes.Buffer
		idx := make([]int, 0, len(a.blocks))
		for i := range a.blocks {
			idx = append(idx, i)
		}
		slices.Sort(idx)
		for _, i := range idx {
			if b := a.blocks[i]; b.typ == blockText {
				text.WriteString(b.text.String())
			}
		}
		if len(a.blocks) > 0 || a.stopReason != "" || a.id != "" {
			r.Choices = []ChatChoice{{Index: 0, Text: text.String(), FinishReason: a.stopReason, ToolCalls: a.toolCalls}}
		}
		return r
	}
	for i, c := range a.choices {
		r.Choices = append(r.Choices, ChatChoice{
			Index: i, Text: c.text.String(), Refusal: c.refusal.String(), FinishReason: c.finish, ToolCalls: c.toolCalls,
		})
	}
	sortChoices(r.Choices)
	return r
}

func (a *StreamAssembler) addOpenAI(ev *Event) EventKind {
	if !ev.HasData {
		return EventOther
	}
	data := bytes.TrimSpace(ev.Data)
	if string(data) == doneMarker {
		a.terminated = true
		return EventTerminal
	}
	if len(data) == 0 {
		return EventOther
	}
	var chunk map[string]json.RawMessage
	if !unmarshalObject(data, &chunk) {
		a.malformed++
		return EventMalformed
	}
	kind := EventOther
	if a.id == "" {
		decodeLoose(chunk["id"], &a.id)
	}
	if a.model == "" {
		decodeLoose(chunk["model"], &a.model)
	}
	if a.created == 0 {
		decodeLoose(chunk["created"], &a.created)
	}
	if raw, ok := chunk["usage"]; ok && !isNull(raw) {
		var u Usage
		if json.Unmarshal(raw, &u) == nil {
			a.usage, a.rawUsage = &u, bytes.Clone(raw)
			kind = EventDelta
		}
	}
	var choices []json.RawMessage
	decodeLoose(chunk["choices"], &choices)
	for _, raw := range choices {
		var c map[string]json.RawMessage
		if !unmarshalObject(raw, &c) {
			continue
		}
		kind = max(kind, EventDelta)
		index := 0
		decodeLoose(c["index"], &index)
		acc := a.choice(index)
		if a.format == FormatOpenAICompletions {
			acc.text.add(c["text"])
		} else {
			var delta map[string]json.RawMessage
			decodeLoose(c["delta"], &delta)
			acc.text.add(delta["content"])
			acc.refusal.add(delta["refusal"])
			var calls []json.RawMessage
			decodeLoose(delta["tool_calls"], &calls)
			var fn map[string]json.RawMessage
			decodeLoose(delta["function_call"], &fn)
			if len(calls) > 0 || fn != nil {
				acc.toolCalls = true
			}
		}
		var finish string
		decodeLoose(c["finish_reason"], &finish)
		if finish != "" && acc.finish == "" {
			acc.finish = finish
			if index == 0 {
				a.finished = true
				kind = max(kind, EventFinish)
			}
		}
	}
	if raw, ok := chunk["error"]; ok && !isNull(raw) {
		a.err = openAIStreamError(raw)
		kind = EventError
	}
	return kind
}

func (a *StreamAssembler) choice(index int) *choiceAcc {
	c, ok := a.choices[index]
	if !ok {
		c = &choiceAcc{}
		a.choices[index] = c
	}
	return c
}

// openAIStreamError reads {"message","type","code"} (code may be a number) or a bare string.
func openAIStreamError(raw json.RawMessage) *StreamError {
	e := &StreamError{}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		e.Message = s
		return e
	}
	var f map[string]json.RawMessage
	decodeLoose(raw, &f)
	decodeLoose(f["message"], &e.Message)
	decodeLoose(f["type"], &e.Type)
	if code := bytes.TrimSpace(f["code"]); len(code) > 0 && !isNull(code) {
		if json.Unmarshal(code, &e.Code) != nil {
			e.Code = string(code)
		}
	}
	return e
}

func (a *StreamAssembler) addAnthropic(ev *Event) EventKind {
	var obj map[string]json.RawMessage
	parsed := ev.HasData && unmarshalObject(ev.Data, &obj)
	typ := ev.Name
	if typ == "" && parsed {
		decodeLoose(obj["type"], &typ)
	}
	switch {
	case typ == anthropicMessageStop:
		a.terminated = true
		return EventTerminal
	case typ == anthropicPing, !ev.HasData:
		return EventOther
	case !parsed:
		a.malformed++
		return EventMalformed
	}
	switch typ {
	case anthropicMessageStart:
		var msg map[string]json.RawMessage
		decodeLoose(obj["message"], &msg)
		decodeLoose(msg["id"], &a.id)
		decodeLoose(msg["model"], &a.model)
		a.addAnthropicUsage(msg["usage"])
		return EventDelta
	case anthropicContentBlockStart:
		var cb map[string]json.RawMessage
		decodeLoose(obj["content_block"], &cb)
		b := &blockAcc{}
		decodeLoose(cb["type"], &b.typ)
		a.blocks[blockIndex(obj)] = b
		switch b.typ {
		case blockText:
			b.text.add(cb["text"])
		case blockToolUse, blockServerToolUse:
			a.toolCalls = true
		}
		return EventDelta
	case anthropicContentBlockDelta:
		var delta map[string]json.RawMessage
		decodeLoose(obj["delta"], &delta)
		var dt string
		decodeLoose(delta["type"], &dt)
		if dt == anthropicTextDelta {
			i := blockIndex(obj)
			b, ok := a.blocks[i]
			if !ok { // a delta without its block start: tolerated as a text block
				b = &blockAcc{typ: blockText}
				a.blocks[i] = b
			}
			if b.typ == blockText {
				b.text.add(delta["text"])
			}
		}
		return EventDelta
	case anthropicMessageDelta:
		var delta map[string]json.RawMessage
		decodeLoose(obj["delta"], &delta)
		a.addAnthropicUsage(obj["usage"])
		var stop string
		decodeLoose(delta["stop_reason"], &stop)
		if stop != "" && a.stopReason == "" {
			a.stopReason = stop
			a.finished = true
			return EventFinish
		}
		return EventDelta
	case anthropicError:
		var e map[string]json.RawMessage
		decodeLoose(obj["error"], &e)
		se := &StreamError{}
		decodeLoose(e["type"], &se.Type)
		decodeLoose(e["message"], &se.Message)
		a.err = se
		return EventError
	}
	return EventOther // content_block_stop and event types added after this code was written
}

// addAnthropicUsage merges an Anthropic usage object: message_start carries input_tokens,
// message_delta the cumulative output_tokens (and, in newer API versions, input_tokens again).
func (a *StreamAssembler) addAnthropicUsage(raw json.RawMessage) {
	var u map[string]json.RawMessage
	decodeLoose(raw, &u)
	if u == nil {
		return
	}
	if a.usage == nil {
		a.usage = &Usage{}
	}
	var n int
	if json.Unmarshal(u["input_tokens"], &n) == nil && n > 0 {
		a.usage.PromptTokens = n
	}
	n = 0
	if json.Unmarshal(u["output_tokens"], &n) == nil && n > 0 {
		a.usage.CompletionTokens = n
	}
}

func blockIndex(obj map[string]json.RawMessage) int {
	i := 0
	decodeLoose(obj["index"], &i)
	return i
}

// jsonText accumulates the bodies of JSON strings (without their quotes and still escaped) and
// decodes their concatenation on demand. A character split across two strings, as raw UTF-8
// bytes or as the two halves of a 😀 surrogate pair, is joined before it is decoded.
type jsonText struct {
	raw     []byte
	decoded string
	dirty   bool
}

// add appends a JSON string value; other values (null, numbers, missing) are ignored. An array of
// content parts is read as its text parts.
func (t *jsonText) add(v json.RawMessage) {
	v = bytes.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		t.raw = append(t.raw, v[1:len(v)-1]...)
		t.dirty = true
		return
	}
	if len(v) > 0 && v[0] == '[' {
		for _, p := range ContentParts(v) {
			t.addString(p)
		}
	}
}

func (t *jsonText) addString(s string) {
	b, _ := json.Marshal(s) // a string always encodes
	t.raw = append(t.raw, b[1:len(b)-1]...)
	t.dirty = true
}

func (t *jsonText) String() string {
	if !t.dirty {
		return t.decoded
	}
	quoted := make([]byte, 0, len(t.raw)+2)
	quoted = append(quoted, '"')
	quoted = append(quoted, t.raw...)
	quoted = append(quoted, '"')
	if err := json.Unmarshal(quoted, &t.decoded); err != nil {
		// Unreachable for bodies of valid JSON strings; never lose the text over it.
		t.decoded = string(t.raw)
	}
	t.dirty = false
	return t.decoded
}

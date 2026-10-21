package mockupstream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type llmFormat int

const (
	formatOpenAIChat llmFormat = iota
	formatOpenAICompletions
	formatAnthropic
)

// llmPricePerMTok is the mock price of OpenRouter chat calls (input, output), so usage.cost is
// realistic for the judge's cost accounting.
var llmPricePerMTok = [2]float64{1, 5}

// llm serves an LLM endpoint in format f. openRouter adds OpenRouter's provider field, bearer
// check and usage.cost (with usage.include).
func (s *Server) llm(f llmFormat, openRouter bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		env := envOpenAI
		switch {
		case openRouter:
			env = envOpenRouter
		case f == formatAnthropic:
			env = envAnthropic
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]json.RawMessage
		if json.Unmarshal(body, &req) != nil {
			writeError(w, env, http.StatusBadRequest, "invalid_request_error", "the request body is not a JSON object", nil)
			return
		}
		_, text := s.llmAnswer(scenarioID(r), req)
		packSpec, countKey := s.upstreamFault(scenarioID(r), req)
		if s.writeFault(w, r, s.fault(r, countKey, packSpec), env) {
			return
		}
		if openRouter && bearer(r) == "" {
			writeError(w, env, http.StatusUnauthorized, "authentication_error", "No auth credentials found", nil)
			return
		}
		if !delay(r, s.cfg.Latency) {
			return
		}
		answer := s.cfg.DefaultAnswer
		if text != "" {
			answer = text
		}
		var model string
		_ = json.Unmarshal(req["model"], &model)
		if model == "" {
			model = "mock-model"
		}
		var stream bool
		_ = json.Unmarshal(req["stream"], &stream)
		sum := sha256.Sum256(body)
		c := completion{
			id: hex.EncodeToString(sum[:8]), model: model, answer: answer, created: time.Now().Unix(),
			in: len(body)/4 + 1, out: len(answer)/4 + 1, openRouter: openRouter,
			includeUsage: openRouter && usageInclude(req) || streamIncludeUsage(req),
			costUsage:    openRouter && usageInclude(req),
		}
		switch {
		case f == formatAnthropic && stream:
			s.anthropicStream(w, r, c)
		case f == formatAnthropic:
			writeJSON(w, http.StatusOK, c.anthropicMessage())
		case stream:
			s.openAIStream(w, r, c, f == formatOpenAICompletions)
		case f == formatOpenAICompletions:
			writeJSON(w, http.StatusOK, c.openAICompletion())
		default:
			writeJSON(w, http.StatusOK, c.openAIChat())
		}
	}
}

// completion is one mock LLM answer.
type completion struct {
	id, model, answer        string
	created                  int64
	in, out                  int
	openRouter, includeUsage bool
	costUsage                bool
}

func usageInclude(req map[string]json.RawMessage) bool {
	var u struct {
		Include bool `json:"include"`
	}
	return json.Unmarshal(req["usage"], &u) == nil && u.Include
}

func streamIncludeUsage(req map[string]json.RawMessage) bool {
	var o struct {
		IncludeUsage bool `json:"include_usage"`
	}
	return json.Unmarshal(req["stream_options"], &o) == nil && o.IncludeUsage
}

func (c completion) usage() map[string]any {
	u := map[string]any{"prompt_tokens": c.in, "completion_tokens": c.out, "total_tokens": c.in + c.out}
	if c.costUsage {
		u["cost"] = (float64(c.in)*llmPricePerMTok[0] + float64(c.out)*llmPricePerMTok[1]) / 1e6
	}
	return u
}

func (c completion) openAIChat() map[string]any {
	m := map[string]any{
		"id": "chatcmpl-mock-" + c.id, "object": "chat.completion", "created": c.created, "model": c.model,
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": c.answer, "refusal": nil},
			"logprobs": nil, "finish_reason": "stop",
		}},
		"usage": c.usage(), "system_fingerprint": "fp_mock",
	}
	if c.openRouter {
		m["provider"] = "Mock"
	}
	return m
}

func (c completion) openAICompletion() map[string]any {
	return map[string]any{
		"id": "cmpl-mock-" + c.id, "object": "text_completion", "created": c.created, "model": c.model,
		"choices": []any{map[string]any{"index": 0, "text": c.answer, "logprobs": nil, "finish_reason": "stop"}},
		"usage":   c.usage(),
	}
}

func (c completion) anthropicMessage() map[string]any {
	return map[string]any{
		"id": "msg_mock_" + c.id, "type": "message", "role": "assistant", "model": c.model,
		"content":     []any{map[string]any{"type": "text", "text": c.answer}},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": c.in, "output_tokens": c.out},
	}
}

// words splits text into chunks that keep their trailing spaces, so they concatenate back.
func words(text string) []string {
	var out []string
	for text != "" {
		i := strings.IndexByte(text, ' ')
		if i < 0 {
			out = append(out, text)
			break
		}
		out = append(out, text[:i+1])
		text = text[i+1:]
	}
	return out
}

// sse writes server-sent events with a per-event delay; it stops when the client leaves.
type sse struct {
	w     http.ResponseWriter
	r     *http.Request
	delay time.Duration
	first bool
}

func newSSE(w http.ResponseWriter, r *http.Request, d time.Duration) *sse {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	return &sse{w: w, r: r, delay: d, first: true}
}

// send writes one event (name may be empty) and flushes; false when the client is gone.
func (s *sse) send(name string, data any) bool {
	if !s.first && !sleepCtx(s.r, s.delay) {
		return false
	}
	s.first = false
	var payload string
	if str, ok := data.(string); ok {
		payload = str
	} else {
		b, err := json.Marshal(data)
		if err != nil {
			return false
		}
		payload = string(b)
	}
	if name != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", name); err != nil {
			return false
		}
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", payload); err != nil {
		return false
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return true
}

func (s *Server) openAIStream(w http.ResponseWriter, r *http.Request, c completion, completions bool) {
	st := newSSE(w, r, chunkDelay(r, s.cfg.ChunkDelay))
	chunk := func(choice map[string]any) map[string]any {
		if completions {
			return map[string]any{"id": "cmpl-mock-" + c.id, "object": "text_completion", "created": c.created, "model": c.model, "choices": []any{choice}}
		}
		m := map[string]any{"id": "chatcmpl-mock-" + c.id, "object": "chat.completion.chunk", "created": c.created, "model": c.model,
			"system_fingerprint": "fp_mock", "choices": []any{choice}}
		if c.openRouter {
			m["provider"] = "Mock"
		}
		return m
	}
	if !completions && !st.send("", chunk(map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil})) {
		return
	}
	for _, word := range words(c.answer) {
		choice := map[string]any{"index": 0, "delta": map[string]any{"content": word}, "finish_reason": nil}
		if completions {
			choice = map[string]any{"index": 0, "text": word, "logprobs": nil, "finish_reason": nil}
		}
		if !st.send("", chunk(choice)) {
			return
		}
	}
	final := map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}
	if completions {
		final = map[string]any{"index": 0, "text": "", "logprobs": nil, "finish_reason": "stop"}
	}
	if !st.send("", chunk(final)) {
		return
	}
	if c.includeUsage {
		u := chunk(nil)
		u["choices"] = []any{}
		u["usage"] = c.usage()
		if !st.send("", u) {
			return
		}
	}
	st.send("", "[DONE]")
}

func (s *Server) anthropicStream(w http.ResponseWriter, r *http.Request, c completion) {
	st := newSSE(w, r, chunkDelay(r, s.cfg.ChunkDelay))
	events := []struct {
		name string
		data any
	}{
		{"message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_mock_" + c.id, "type": "message", "role": "assistant", "model": c.model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": c.in, "output_tokens": 1},
		}}},
		{"content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}},
		{"ping", map[string]any{"type": "ping"}},
	}
	for _, e := range events {
		if !st.send(e.name, e.data) {
			return
		}
	}
	for _, word := range words(c.answer) {
		if !st.send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": word}}) {
			return
		}
	}
	tail := []struct {
		name string
		data any
	}{
		{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
		{"message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": c.out}}},
		{"message_stop", map[string]any{"type": "message_stop"}},
	}
	for _, e := range tail {
		if !st.send(e.name, e.data) {
			return
		}
	}
}

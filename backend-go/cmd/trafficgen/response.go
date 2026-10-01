package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
)

// annotationPrefix starts the SSE comment that carries the decision of a streamed answer
// (SPEC §2.12: `: trustagent {"trace_id","action","would_action","status","verdict","max_risk","reasons"}`).
const annotationPrefix = ": trustagent "

// annotation is the decision a stream carries in its trustagent comment.
type annotation struct {
	TraceID     string   `json:"trace_id"`
	Action      string   `json:"action"`
	WouldAction *string  `json:"would_action"`
	Status      string   `json:"status"`
	Verdict     *string  `json:"verdict"`
	MaxRisk     *float64 `json:"max_risk"`
	Reasons     []string `json:"reasons"`
}

// answer is what trafficgen read from a gateway response.
type answer struct {
	status  int
	header  http.Header
	trailer http.Header
	stream  bool
	// text is the assistant text the client received (ok is false when the body had none that
	// trafficgen could read, e.g. an error body).
	text string
	ok   bool
	// comment is the stream's trustagent comment, if any.
	comment *annotation
}

// isEventStream reports whether a response is a server-sent event stream.
func isEventStream(h http.Header) bool {
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && mt == "text/event-stream"
}

// readAnswer reads the assistant text of a gateway response body in the scenario's format.
func readAnswer(format string, status int, header, trailer http.Header, body []byte) *answer {
	a := &answer{status: status, header: header, trailer: trailer}
	if isEventStream(header) {
		a.stream = true
		a.text, a.ok, a.comment = readStream(format, body)
		return a
	}
	if status < 200 || status > 299 {
		return a
	}
	a.text, a.ok = readJSON(format, body)
	return a
}

// readJSON reads the text of choice 0 (OpenAI chat and completions) or the text blocks
// (Anthropic) of a JSON response.
func readJSON(format string, body []byte) (string, bool) {
	switch format {
	case "anthropic_messages":
		var m struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(body, &m) != nil || m.Content == nil {
			return "", false
		}
		var b strings.Builder
		for _, c := range m.Content {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		return b.String(), true
	case "openai_completions":
		var m struct {
			Choices []struct {
				Text string `json:"text"`
			} `json:"choices"`
		}
		if json.Unmarshal(body, &m) != nil || len(m.Choices) == 0 {
			return "", false
		}
		return m.Choices[0].Text, true
	}
	var m struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &m) != nil || len(m.Choices) == 0 {
		return "", false
	}
	return contentText(m.Choices[0].Message.Content), true
}

// contentText reads an OpenAI message content: a string, or text parts (joined as they come).
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// readStream reads an SSE body: the text of the content deltas (choice 0 for OpenAI, text_delta
// events for Anthropic) and the trustagent decision comment.
func readStream(format string, body []byte) (text string, ok bool, comment *annotation) {
	var b strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if rest, found := strings.CutPrefix(line, annotationPrefix); found {
			var a annotation
			if json.Unmarshal([]byte(rest), &a) == nil {
				comment = &a
			}
			continue
		}
		data, found := strings.CutPrefix(line, "data:")
		if !found {
			continue
		}
		data = strings.TrimPrefix(data, " ")
		if data == "[DONE]" {
			continue
		}
		if delta, isText := streamDelta(format, []byte(data)); isText {
			b.WriteString(delta)
			ok = true
		}
	}
	return b.String(), ok, comment
}

// streamDelta reads the text one SSE data payload adds.
func streamDelta(format string, data []byte) (string, bool) {
	switch format {
	case "anthropic_messages":
		var e struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if json.Unmarshal(data, &e) != nil || e.Type != "content_block_delta" || e.Delta.Type != "text_delta" {
			return "", false
		}
		return e.Delta.Text, true
	case "openai_completions":
		var c struct {
			Choices []struct {
				Index int    `json:"index"`
				Text  string `json:"text"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &c) != nil {
			return "", false
		}
		for _, ch := range c.Choices {
			if ch.Index == 0 {
				return ch.Text, true
			}
		}
		return "", false
	}
	var c struct {
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Content *string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &c) != nil {
		return "", false
	}
	for _, ch := range c.Choices {
		if ch.Index == 0 && ch.Delta.Content != nil {
			return *ch.Delta.Content, true
		}
	}
	return "", false
}

// decisionView is the decision a response exposes, as X-TrustAgent-* header values: the headers,
// overlaid by the HTTP trailers, overlaid by the stream's trustagent comment. Would-Action keeps
// the header rule (present only when it differs from Action).
type decisionView struct {
	values http.Header
}

func newDecisionView(a *answer) *decisionView {
	v := &decisionView{values: http.Header{}}
	for name, vals := range a.header {
		if len(vals) > 0 {
			v.values[name] = []string{vals[0]}
		}
	}
	for name, vals := range a.trailer {
		if len(vals) > 0 && vals[0] != "" {
			v.values[name] = []string{vals[0]}
		}
	}
	if c := a.comment; c != nil {
		set := func(name, value string) {
			if value == "" {
				v.values.Del(name)
				return
			}
			v.values.Set(name, value)
		}
		set(headerAction, c.Action)
		set(headerStatus, c.Status)
		if c.WouldAction != nil && *c.WouldAction != c.Action {
			set(headerWouldAction, *c.WouldAction)
		} else {
			v.values.Del(headerWouldAction)
		}
		set(headerVerdict, deref(c.Verdict))
		if c.MaxRisk != nil {
			set(headerRisk, decision.FormatRisk(*c.MaxRisk))
		} else {
			v.values.Del(headerRisk)
		}
		set(headerReasons, strings.Join(c.Reasons, ","))
	}
	return v
}

// get returns the value of a decision header and whether the response exposed it.
func (v *decisionView) get(name string) (string, bool) {
	vals := v.values.Values(name)
	if len(vals) == 0 {
		return "", false
	}
	return vals[0], true
}

// deferred reports an observe-mode response, whose gate runs after it (Status: deferred).
func (v *decisionView) deferred() bool {
	s, _ := v.get(headerStatus)
	return s == statusDeferred
}

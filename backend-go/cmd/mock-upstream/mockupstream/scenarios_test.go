package mockupstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/truthtable/contracts"
)

// contractScenarios reads the contract pack's scenarios.
func contractScenarios(t *testing.T) []*Scenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	all, err := parseScenarios(b)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

var formatPaths = map[string]string{
	"":                   "/v1/chat/completions",
	"openai_chat":        "/v1/chat/completions",
	"openai_completions": "/v1/completions",
	"anthropic_messages": "/v1/messages",
}

// forwarded is a scenario request as the gateway forwards it: without the trustagent extension
// (and test_response), with stream as the client asked.
func forwarded(t *testing.T, sc *Scenario, stream bool) map[string]json.RawMessage {
	t.Helper()
	req := map[string]json.RawMessage{}
	for k, v := range sc.Request {
		if k != "trustagent" && k != "test_response" {
			req[k] = v
		}
	}
	req["stream"] = json.RawMessage("false")
	if stream {
		req["stream"] = json.RawMessage("true")
	}
	return req
}

// answerText reads the assistant text of a JSON or SSE answer in any format.
func answerText(t *testing.T, format string, body []byte, sse bool) string {
	t.Helper()
	if !sse {
		var m struct {
			Choices []struct {
				Text    string `json:"text"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		switch {
		case format == "anthropic_messages" && len(m.Content) > 0:
			return m.Content[0].Text
		case format == "openai_completions" && len(m.Choices) > 0:
			return m.Choices[0].Text
		case len(m.Choices) > 0:
			return m.Choices[0].Message.Content
		}
		t.Fatalf("no answer in %s", body)
	}
	var b strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
			Choices []struct {
				Text  string `json:"text"`
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		switch {
		case ev.Type == "content_block_delta":
			b.WriteString(ev.Delta.Text)
		case len(ev.Choices) > 0:
			b.WriteString(ev.Choices[0].Text + ev.Choices[0].Delta.Content)
		}
	}
	return b.String()
}

// TestEveryScenarioAnswerIsServed: with the gateway's headers stripped (no scenario id), every
// scenario's request gets its scripted answer, as JSON and as a stream; no two scenarios share a
// conversation, so none shadows another.
func TestEveryScenarioAnswerIsServed(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	keys := map[string]string{}
	for _, sc := range contractScenarios(t) {
		if other, dup := keys[sc.conversation()]; dup {
			t.Errorf("%s and %s send the same conversation: mock-upstream cannot tell them apart", other, sc.ID)
		}
		keys[sc.conversation()] = sc.ID
		for _, stream := range []bool{false, true} {
			body, _ := json.Marshal(forwarded(t, sc, stream))
			resp, b := do(t, srv, call{path: formatPaths[sc.APIFormat], body: string(body)})
			if resp.StatusCode != 200 {
				t.Fatalf("%s: HTTP %d %s", sc.ID, resp.StatusCode, b)
			}
			if got := answerText(t, sc.APIFormat, b, stream); got != sc.Response {
				t.Errorf("%s (stream %v): answer %q, want %q", sc.ID, stream, got, sc.Response)
			}
		}
	}
}

// fallbackRequest builds the gateway's fallback request for a scenario the way SPEC §2.12 says
// (and internal/llmapi builds it): the fallback prompt with the CONTEXT block leads the messages
// as a system message (the Anthropic system, the completions prompt), and stream is off.
func fallbackRequest(t *testing.T, sc *Scenario) map[string]json.RawMessage {
	t.Helper()
	req := forwarded(t, sc, false)
	prompt := "Answer using only the information in CONTEXT.\n\nCONTEXT:\n<context>\n[doc] Some evidence.\n</context>"
	switch sc.APIFormat {
	case "anthropic_messages":
		var sys string
		_ = json.Unmarshal(req["system"], &sys)
		req["system"], _ = json.Marshal(prompt + "\n\n" + sys)
	case "openai_completions":
		var p string
		_ = json.Unmarshal(req["prompt"], &p)
		req["prompt"], _ = json.Marshal(prompt + "\n\n" + p)
	default:
		var msgs []json.RawMessage
		_ = json.Unmarshal(req["messages"], &msgs)
		sys, _ := json.Marshal(map[string]string{"role": "system", "content": prompt})
		req["messages"], _ = json.Marshal(append([]json.RawMessage{sys}, msgs...))
	}
	return req
}

// TestFallbackAnswersAndRegate: the fallback request of a scenario with a scripted fallback
// answers with fallback.response, and the decision re-gate of that answer with
// fallback.mock_answers.
func TestFallbackAnswersAndRegate(t *testing.T) {
	_, srv := newTestServer(t, Config{})
	n := 0
	for _, sc := range contractScenarios(t) {
		if sc.fallbackText() == "" {
			continue
		}
		n++
		body, _ := json.Marshal(fallbackRequest(t, sc))
		for _, header := range []map[string]string{nil, {"X-Mock-Scenario": sc.ID}} {
			_, b := do(t, srv, call{path: formatPaths[sc.APIFormat], body: string(body), header: header})
			if got := answerText(t, sc.APIFormat, b, false); got != sc.Fallback.Response {
				t.Errorf("%s (headers %v): fallback answer %q, want %q", sc.ID, header, got, sc.Fallback.Response)
			}
		}
		for text, want := range map[string]map[string]json.RawMessage{sc.Response: sc.MockAnswers, sc.Fallback.Response: sc.Fallback.MockAnswers} {
			questions := map[string]any{}
			for qid := range want {
				questions[qid] = map[string]any{"type": "noul", "instructions": "x", "criteria": map[string]string{"true": "a", "false": "b"}}
			}
			dec, _ := json.Marshal(map[string]any{"model": "typesafe/jev-1.13", "state": map[string]any{"assistant_response": text}, "questions": questions})
			_, b := do(t, srv, call{path: "/v1/systemone", body: string(dec), header: map[string]string{"Authorization": "Bearer k", "X-TrustAgent-Scenario": sc.ID}})
			var got struct {
				Answers map[string]json.RawMessage `json:"answers"`
			}
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("%s: %v: %s", sc.ID, err, b)
			}
			for qid, a := range want {
				if compact(t, got.Answers[qid]) != compact(t, a) {
					t.Errorf("%s: the gate of %q got %s = %s, want %s", sc.ID, text, qid, got.Answers[qid], a)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("the contract pack has no fallback scenario")
	}
}

func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	return b.String()
}

func TestFallbackPromptHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"P\n\nCONTEXT:\n<context>\n[a] b\n</context>\n\nWrite a note.": "Write a note.",
		"P" + fallbackNoEvidenceSuffix + "\n\nWrite a note.":           "Write a note.",
		"Write a note.": "Write a note.",
	} {
		if got := stripFallbackPrompt(in); got != want {
			t.Errorf("stripFallbackPrompt(%q) = %q", in, got)
		}
	}
	plain := map[string]json.RawMessage{"messages": json.RawMessage(`[{"role":"system","content":"You help."},{"role":"user","content":"Hi"}]`)}
	if isFallbackRequest(plain) {
		t.Error("an ordinary request is not a fallback request")
	}
	blocks := map[string]json.RawMessage{"system": json.RawMessage(`[{"type":"text","text":"P\n\nCONTEXT:\n<context>\n[a] b\n</context>"},{"type":"text","text":"You help."}]`)}
	if !isFallbackRequest(blocks) {
		t.Error("an Anthropic system whose first block is the fallback prompt is a fallback request")
	}
}

package mockupstream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Scenario is the part of a trustagent.scenarios/v1 scenario the mock serves.
type Scenario struct {
	ID          string                     `json:"id"`
	APIFormat   string                     `json:"api_format"`
	Request     map[string]json.RawMessage `json:"request"`
	Response    string                     `json:"response"`
	MockAnswers map[string]json.RawMessage `json:"mock_answers"`
	// Fallback is the scripted fallback answer (scenarios.v1 fallback): the LLM answer to the
	// gateway's fallback request, and the decision answers to its re-gate.
	Fallback *struct {
		Response    string                     `json:"response"`
		MockAnswers map[string]json.RawMessage `json:"mock_answers"`
	} `json:"fallback"`
}

func parseScenarios(b []byte) ([]*Scenario, error) {
	var pack struct {
		Scenarios []*Scenario `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, s := range pack.Scenarios {
		if s.ID == "" {
			return nil, fmt.Errorf("a scenario has no id")
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("scenario %q is defined twice", s.ID)
		}
		seen[s.ID] = true
	}
	return pack.Scenarios, nil
}

// prompt is the scenario's last user message (or completions prompt).
func (s *Scenario) prompt() string {
	return lastUserText(s.Request)
}

// conversation is the key of the scenario's conversation (see conversationKey).
func (s *Scenario) conversation() string {
	return conversationKey(s.Request)
}

// fallbackText returns the scenario's fallback answer, if it has one.
func (s *Scenario) fallbackText() string {
	if s.Fallback == nil {
		return ""
	}
	return s.Fallback.Response
}

// Markers of the gateway's fallback prompt (SPEC §2.12 Fallback, step 1): the prompt ends with the
// packed evidence as "\n\nCONTEXT:\n<context>\n...\n</context>", or, without evidence, with
// fallbackNoEvidenceSuffix.
const (
	fallbackContextMarker    = "CONTEXT:\n<context>\n"
	fallbackContextEnd       = "\n</context>"
	fallbackNoEvidenceSuffix = " If you are not certain, say so; do not include specific numbers, dates, URLs or quotations."
)

// isFallbackPrompt reports whether text is (or starts with) the gateway's fallback prompt.
func isFallbackPrompt(text string) bool {
	return strings.Contains(text, fallbackContextMarker) || strings.Contains(text, fallbackNoEvidenceSuffix)
}

// stripFallbackPrompt removes the fallback prompt the gateway put in front of text (a completions
// prompt), leaving the original.
func stripFallbackPrompt(text string) string {
	for _, end := range []string{fallbackContextEnd, fallbackNoEvidenceSuffix} {
		if i := strings.Index(text, end); i >= 0 && isFallbackPrompt(text) {
			return strings.TrimLeft(text[i+len(end):], "\n")
		}
	}
	return text
}

// isFallbackRequest reports whether an LLM request is the gateway's fallback request: the fallback
// prompt leads the OpenAI messages (as a system message), the Anthropic system, or the prompt.
func isFallbackRequest(req map[string]json.RawMessage) bool {
	if raw, ok := req["system"]; ok && isFallbackPrompt(contentText(raw)) {
		return true
	}
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(req["messages"], &msgs) == nil && len(msgs) > 0 {
		return msgs[0].Role == "system" && isFallbackPrompt(contentText(msgs[0].Content))
	}
	var p string
	return json.Unmarshal(req["prompt"], &p) == nil && isFallbackPrompt(p)
}

// conversationKey identifies a request's conversation independently of JSON formatting and of the
// members the gateway adds or removes (model, stream, test_response, trustagent): the Anthropic
// system, every message's role and text, and the completions prompt.
func conversationKey(req map[string]json.RawMessage) string {
	type turn struct {
		Role string `json:"r"`
		Text string `json:"t"`
	}
	var turns []turn
	if raw, ok := req["system"]; ok {
		turns = append(turns, turn{"system", contentText(raw)})
	}
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(req["messages"], &msgs) == nil {
		for _, m := range msgs {
			turns = append(turns, turn{m.Role, contentText(m.Content)})
		}
	}
	var p string
	if json.Unmarshal(req["prompt"], &p) == nil {
		turns = append(turns, turn{"prompt", p})
	}
	if len(turns) == 0 {
		return ""
	}
	b, _ := json.Marshal(turns)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// lastUserText extracts the last user turn of an OpenAI chat, Anthropic messages or completions
// request body.
func lastUserText(req map[string]json.RawMessage) string {
	if raw, ok := req["messages"]; ok {
		var msgs []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &msgs) == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == "user" {
					return strings.TrimSpace(contentText(msgs[i].Content))
				}
			}
		}
	}
	if raw, ok := req["prompt"]; ok {
		var p string
		if json.Unmarshal(raw, &p) == nil {
			return strings.TrimSpace(stripFallbackPrompt(p))
		}
		var ps []string
		if json.Unmarshal(raw, &ps) == nil && len(ps) > 0 {
			return strings.TrimSpace(stripFallbackPrompt(ps[len(ps)-1]))
		}
	}
	return ""
}

// contentText flattens a string content or an array of text parts/blocks.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

// llmAnswer finds the answer to an LLM request. The gateway strips X-TrustAgent-* before it
// calls the upstream, so a request usually carries no scenario id; it is matched on its
// conversation instead (the scenario's request as the gateway forwards it). The gateway's fallback
// request (its prompt carries the CONTEXT block) gets the fallback answer of the scenario with the
// same last user message. A request that matches no conversation falls back to the last user
// message, then to no scenario.
func (s *Server) llmAnswer(id string, body map[string]json.RawMessage) (*Scenario, string) {
	if id != "" {
		if sc := s.scenarios[id]; sc != nil {
			if isFallbackRequest(body) && sc.fallbackText() != "" {
				return sc, sc.fallbackText()
			}
			return sc, sc.Response
		}
	}
	if body == nil {
		return nil, ""
	}
	if isFallbackRequest(body) {
		if sc := s.byFallbackPrompt[lastUserText(body)]; sc != nil {
			return sc, sc.fallbackText()
		}
		return nil, ""
	}
	if sc := s.byConversation[conversationKey(body)]; sc != nil {
		return sc, sc.Response
	}
	if sc := s.byPrompt[lastUserText(body)]; sc != nil {
		return sc, sc.Response
	}
	return nil, ""
}

// scenarioAnswers returns the decision answers a scenario scripts for a decision request: its
// fallback answers when the state's assistant_response is its fallback answer (the attempt-1
// re-gate), else its mock answers.
func scenarioAnswers(sc *Scenario, state json.RawMessage) map[string]json.RawMessage {
	if sc == nil {
		return nil
	}
	if fb := sc.fallbackText(); fb != "" {
		var st struct {
			AssistantResponse string `json:"assistant_response"`
		}
		if json.Unmarshal(state, &st) == nil && st.AssistantResponse == fb {
			return sc.Fallback.MockAnswers
		}
	}
	return sc.MockAnswers
}

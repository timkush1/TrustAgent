package mockupstream

import (
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

// prompt is the scenario's last user message (or completions prompt), the key LLM requests
// match on when no scenario id reaches the upstream (the gateway strips X-TrustAgent-*).
func (s *Scenario) prompt() string {
	return lastUserText(s.Request)
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
			return strings.TrimSpace(p)
		}
		var ps []string
		if json.Unmarshal(raw, &ps) == nil && len(ps) > 0 {
			return strings.TrimSpace(ps[len(ps)-1])
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

// scenarioFor finds the scenario of a request: by id, else by the last user message.
func (s *Server) scenarioFor(id string, body map[string]json.RawMessage) *Scenario {
	if id != "" {
		if sc := s.scenarios[id]; sc != nil {
			return sc
		}
	}
	if body != nil {
		if p := lastUserText(body); p != "" {
			return s.byPrompt[p]
		}
	}
	return nil
}

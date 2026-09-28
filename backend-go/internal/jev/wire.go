package jev

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/truthtable/backend-go/internal/decision"
)

// maxSessionIDRunes bounds session_id (OpenRouter accepts at most 256 characters).
const maxSessionIDRunes = 256

// wireRequest is the decision request body. OpenRouter's endpoints take session_id and provider
// preferences; TypeSafe and compatible gateways take {model, state, questions} only.
type wireRequest struct {
	Model     string         `json:"model"`
	SessionID string         `json:"session_id,omitempty"`
	State     map[string]any `json:"state"`
	Questions map[string]any `json:"questions"`
	Provider  *wireProvider  `json:"provider,omitempty"`
}

// wireProvider is OpenRouter's provider routing preference; zdr asks for zero-data-retention
// endpoints only (TRUTHTABLE_JEV_ZDR).
type wireProvider struct {
	ZDR bool `json:"zdr"`
}

// encodeRequest builds the request body for ep. HTML escaping is off, so documents keep their
// bytes (and the body its size).
func encodeRequest(ep endpoint, req *Request, h *HTTPConfig) ([]byte, error) {
	w := wireRequest{Model: req.Model, State: req.State, Questions: req.Questions}
	if w.State == nil {
		w.State = map[string]any{}
	}
	if ep.openRouter {
		if h.SendSessionID {
			w.SessionID = truncateRunes(req.SessionID, maxSessionIDRunes)
		}
		if h.ZDR {
			w.Provider = &wireProvider{ZDR: true}
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(w); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// wireResponse is a decision response: {id, model, provider?, answers, usage}.
type wireResponse struct {
	ID       string                         `json:"id"`
	Model    string                         `json:"model"`
	Provider string                         `json:"provider"`
	Answers  map[string]*decision.RawAnswer `json:"answers"`
	Usage    *wireUsage                     `json:"usage"`
}

// wireUsage is the usage block. Jev bills input tokens only; cost is OpenRouter's reported cost.
// prompt_tokens is accepted from OpenAI-style gateways.
type wireUsage struct {
	InputTokens  *int     `json:"input_tokens"`
	OutputTokens *int     `json:"output_tokens"`
	PromptTokens *int     `json:"prompt_tokens"`
	Cost         *float64 `json:"cost"`
}

func (u *wireUsage) inputTokens() *int {
	if u == nil {
		return nil
	}
	if u.InputTokens != nil {
		return u.InputTokens
	}
	return u.PromptTokens
}

// decodeResponse parses a 2xx body. A body without an answers object is bad_response.
func decodeResponse(body []byte) (*wireResponse, *Error) {
	var w wireResponse
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, &Error{Kind: KindBadResponse, Status: 200, Message: "the response is not a decision JSON object", Err: err, Hint: Hint(KindBadResponse)}
	}
	if w.Answers == nil {
		msg := "the response has no answers object"
		// Some gateways answer 200 with an error envelope; its message says what went wrong.
		if env := parseEnvelope(body); env.message != "" {
			msg += ": " + env.message
		}
		return nil, &Error{Kind: KindBadResponse, Status: 200, Message: msg, Hint: Hint(KindBadResponse)}
	}
	if u := w.Usage; u != nil {
		if t := u.inputTokens(); t != nil && *t < 0 {
			return nil, &Error{Kind: KindBadResponse, Status: 200, Message: fmt.Sprintf("negative input_tokens %d", *t), Hint: Hint(KindBadResponse)}
		}
		if u.Cost != nil && (math.IsNaN(*u.Cost) || math.IsInf(*u.Cost, 0) || *u.Cost < 0) {
			u.Cost = nil
		}
	}
	return &w, nil
}

// Anomaly kinds of trustagent_gateway_jev_anomalies_total that the client detects.
const (
	AnomalySnapshotChanged  = "snapshot_changed"
	AnomalyMissingAnswer    = "missing_answer"
	AnomalyUnexpectedAnswer = "unexpected_answer"
	AnomalyTypeMismatch     = "type_mismatch"
	AnomalyProbabilitySum   = "probability_sum"
)

// checkAnswers keeps the answers to asked questions and reports anomalies: missing or unexpected
// answers, a type that differs from the question's, and probabilities summing outside 1 ± 0.02.
// Answers are not renormalized here; decision.Normalize owns that (SPEC §2.9).
func checkAnswers(questions map[string]any, answers map[string]*decision.RawAnswer) (map[string]*decision.RawAnswer, []string) {
	var anomalies []string
	out := make(map[string]*decision.RawAnswer, len(questions))
	for qid, a := range answers {
		q, asked := questions[qid]
		if !asked {
			anomalies = append(anomalies, AnomalyUnexpectedAnswer)
			continue
		}
		if a == nil {
			continue
		}
		if qt := questionType(q); qt != "" && a.Type != "" && a.Type != qt {
			anomalies = append(anomalies, AnomalyTypeMismatch)
		}
		if len(a.Probabilities) > 0 {
			var sum float64
			for _, p := range a.Probabilities {
				sum += p
			}
			if math.Abs(sum-1) > 0.02+decision.EPS {
				anomalies = append(anomalies, AnomalyProbabilitySum)
			}
		}
		out[qid] = a
	}
	for qid := range questions {
		if out[qid] == nil {
			anomalies = append(anomalies, AnomalyMissingAnswer)
		}
	}
	return out, anomalies
}

func questionType(q any) string {
	m, _ := q.(map[string]any)
	t, _ := m["type"].(string)
	return t
}

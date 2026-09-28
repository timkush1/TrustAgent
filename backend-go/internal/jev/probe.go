package jev

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/truthtable/contracts"
)

// ProbeQuestionID is the question id of the connectivity probe.
const ProbeQuestionID = "probe"

var loadProbe = sync.OnceValues(func() (probeDoc, error) {
	var p probeDoc
	b, err := contracts.FS.ReadFile("cascade/probe.v1.json")
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if len(p.Questions) == 0 {
		return p, fmt.Errorf("cascade/probe.v1.json has no questions")
	}
	return p, nil
})

type probeDoc struct {
	State     map[string]any `json:"state"`
	Questions map[string]any `json:"questions"`
}

// ProbeRequest is the connectivity probe (contracts/cascade/probe.v1.json): one noul question
// about a one-line state, about 290 input tokens (≈ $0.00001).
func ProbeRequest(model string) (*Request, error) {
	p, err := loadProbe()
	if err != nil {
		return nil, fmt.Errorf("jev: probe: %w", err)
	}
	return &Request{Model: model, State: p.State, Questions: p.Questions}, nil
}

// Probe sends the connectivity probe through c (startup probe, POST /api/decision-model/test,
// jevcheck). The answer itself carries no meaning and is never shown; a response with the probe
// answer proves the key, the endpoint and the model.
func Probe(ctx context.Context, c Client) (*Response, error) {
	req, err := ProbeRequest(c.Info().Model)
	if err != nil {
		return nil, err
	}
	resp, err := c.Decide(ctx, req, CallOptions{Class: ClassSync, Component: ComponentProbe})
	if err != nil {
		return nil, err
	}
	if resp.Answers[ProbeQuestionID] == nil {
		return resp, &Error{Kind: KindBadResponse, Message: "the probe response has no answer to the probe question", Hint: Hint(KindBadResponse)}
	}
	return resp, nil
}

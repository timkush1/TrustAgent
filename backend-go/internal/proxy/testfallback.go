package proxy

import (
	"encoding/json"
	"sync"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/gate"
)

// scriptedFallbacks maps each demo scenario that has a fallback to its fallback.response
// (contracts/demo/scenarios.v1.json), read once. A pack that cannot be read has none, and every
// fallback then calls its upstream.
var scriptedFallbacks = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		return out
	}
	var pack struct {
		Scenarios []struct {
			ID       string `json:"id"`
			Fallback *struct {
				Response string `json:"response"`
			} `json:"fallback"`
		} `json:"scenarios"`
	}
	if json.Unmarshal(b, &pack) != nil {
		return out
	}
	for _, s := range pack.Scenarios {
		if s.Fallback != nil && s.Fallback.Response != "" {
			out[s.ID] = s.Fallback.Response
		}
	}
	return out
})

// scriptedFallback is the test-mode fallback answer of f (D1): the zero-key demo has no LLM
// upstream, so a scripted exchange answers its fallback call from the scenario pack. It applies
// only when all three hold: the gateway runs in test mode, the attempt-0 answer was scripted
// (test_response), and X-TrustAgent-Scenario names a scenario that has a fallback. Otherwise the
// fallback calls its upstream (in the e2e stack, mock-upstream answers a request that carries the
// CONTEXT block with the same text).
func (h *Handler) scriptedFallback(f *flow) (string, bool) {
	t := f.x.Trace
	if !h.testMode || t.AnswerSource != gate.AnswerScripted || t.Scenario == "" {
		return "", false
	}
	text, ok := scriptedFallbacks()[t.Scenario]
	return text, ok
}

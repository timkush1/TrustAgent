package proxy

import (
	"encoding/json"
	"sync"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/gate"
)

// testPack is what test mode reads from the demo scenario pack (contracts/demo/scenarios.v1.json),
// once: each scenario's scripted fallback answer, and the scenarios whose answer_source is
// generated. A pack that cannot be read has neither: every fallback then calls its upstream, and
// every upstream answer is live.
type testPack struct {
	fallbacks map[string]string
	generated map[string]bool
}

var demoTestPack = sync.OnceValue(func() testPack {
	out := testPack{fallbacks: map[string]string{}, generated: map[string]bool{}}
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		return out
	}
	var pack struct {
		Scenarios []struct {
			ID           string `json:"id"`
			AnswerSource string `json:"answer_source"`
			Fallback     *struct {
				Response string `json:"response"`
			} `json:"fallback"`
		} `json:"scenarios"`
	}
	if json.Unmarshal(b, &pack) != nil {
		return out
	}
	for _, s := range pack.Scenarios {
		if s.Fallback != nil && s.Fallback.Response != "" {
			out.fallbacks[s.ID] = s.Fallback.Response
		}
		if s.AnswerSource == gate.AnswerGenerated {
			out.generated[s.ID] = true
		}
	}
	return out
})

// scriptedFallbacks maps each demo scenario that has a fallback to its fallback.response.
func scriptedFallbacks() map[string]string { return demoTestPack().fallbacks }

// generatedScenarios is the set of demo scenarios whose answer_source is generated: in test mode, an
// upstream answer to one of them is stamped generated, not live (D121).
func generatedScenarios() map[string]bool { return demoTestPack().generated }

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

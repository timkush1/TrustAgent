package jev

import (
	"encoding/json"
	"fmt"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
)

// The mock's answers to the Studio drafter's example samples (contracts/prompts/
// evaluator-drafter.v1.json). The mock judge drafts the first example on a stack without a real
// judge, and its test bench then asks the mock fast check about the draft's own samples. Hash
// answers there read as a broken check ("0 of 6 match"), so the mock knows these samples the way
// the mock judge knows the built-in scenarios: each answer follows the sample's should_fire.
// Questions the user edited, and any other text, still get hash answers.

// studioChoiceOptions names the option each choice-question sample takes, by its
// assistant_response: test_samples carry only should_fire. TestStudioSamplesAreScripted checks
// that every choice sample is listed and that the option agrees with should_fire.
var studioChoiceOptions = map[string]string{
	"Go to Settings > Export and choose CSV. Honestly, Globex does exports better if you need Gantt charts.": "recommends_competitor",
	"Yes, invite them from the Share menu. Unlike Initech, we don't charge for guests.":                     "mentions_competitor_unprompted",
	"Globex is cheaper for small teams, so I'd switch if price matters most to you.":                        "recommends_competitor",
	"Not yet, but you can import a CSV export from Globex under Settings > Import.":                          "answers_question_about_competitor",
	"Click Forgot password on the sign-in page and follow the link we email you.":                           "no_competitor_mentioned",
	"You're welcome! Anything else I can help with?":                                                        "no_competitor_mentioned",
}

// The scripted answers' strength: the chosen option's probability, and P(true) of a yes/no
// question for a sample that should fire and for one that should not.
const (
	studioChoiceTop = 0.9
	studioNoulFire  = 0.93
	studioNoulPass  = 0.05
)

// drafterExample is the part of a drafter example the mock reads.
type drafterExample struct {
	Output struct {
		Question struct {
			Type         string `json:"type"`
			Instructions string `json:"instructions"`
		} `json:"question"`
		Signal struct {
			RiskWeights map[string]float64 `json:"risk_weights"`
		} `json:"signal"`
		TestSamples []struct {
			AssistantResponse string `json:"assistant_response"`
			ShouldFire        bool   `json:"should_fire"`
		} `json:"test_samples"`
	} `json:"output"`
}

// studioKey keys a scripted answer by the question's instructions and the answer it is asked about.
func studioKey(instructions, response string) string { return instructions + "\x00" + response }

// parseStudioSamples builds the scripted answers of the drafter examples in b.
func parseStudioSamples(b []byte) (map[string]*decision.RawAnswer, error) {
	var prompt struct {
		Examples []drafterExample `json:"examples"`
	}
	if err := json.Unmarshal(b, &prompt); err != nil {
		return nil, err
	}
	out := map[string]*decision.RawAnswer{}
	for _, ex := range prompt.Examples {
		q := ex.Output.Question
		for _, s := range ex.Output.TestSamples {
			var a *decision.RawAnswer
			switch q.Type {
			case decision.QuestionNoul:
				p := studioNoulPass
				if s.ShouldFire {
					p = studioNoulFire
				}
				a = &decision.RawAnswer{Type: decision.QuestionNoul, Noul: &p}
			case decision.QuestionChoice:
				opt, ok := studioChoiceOptions[s.AssistantResponse]
				if !ok {
					return nil, fmt.Errorf("drafter sample %q has no scripted option", s.AssistantResponse)
				}
				if fires := ex.Output.Signal.RiskWeights[opt] > 0; fires != s.ShouldFire {
					return nil, fmt.Errorf("drafter sample %q: option %s does not match should_fire %v", s.AssistantResponse, opt, s.ShouldFire)
				}
				a = &decision.RawAnswer{Type: decision.QuestionChoice, Choice: opt}
			default:
				continue
			}
			out[studioKey(q.Instructions, s.AssistantResponse)] = a
		}
	}
	return out, nil
}

// loadStudioSamples reads the embedded drafter prompt; a mock without it scripts no samples.
func loadStudioSamples() (map[string]*decision.RawAnswer, error) {
	b, err := contracts.FS.ReadFile("prompts/evaluator-drafter.v1.json")
	if err != nil {
		return nil, err
	}
	return parseStudioSamples(b)
}

// studioAnswer is the scripted answer to question about state when they are one of the drafter
// examples' samples, else nil. A choice answer spreads the rest of the probability over the
// question's other options.
func (m *mockClient) studioAnswer(question any, state map[string]any) *decision.RawAnswer {
	q, _ := question.(map[string]any)
	instructions, _ := q["instructions"].(string)
	response, _ := state[decision.FieldAssistantResponse].(string)
	if instructions == "" || response == "" {
		return nil
	}
	a := m.studio[studioKey(instructions, response)]
	if a == nil || a.Type != q["type"] {
		return nil
	}
	out := cloneAnswer(a)
	if out.Type == decision.QuestionChoice {
		criteria, _ := q["criteria"].(map[string]any)
		if _, ok := criteria[out.Choice]; !ok {
			return nil
		}
		keys := make([]string, 0, len(criteria))
		for k := range criteria {
			keys = append(keys, k)
		}
		out.Probabilities = spread(keys, out.Choice, (studioChoiceTop-0.55)/0.4)
	}
	return out
}

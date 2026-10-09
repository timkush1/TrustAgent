package jev

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
)

// Every Studio drafter example's own samples are scripted, even in strict mode and without a
// scenario: a question about a sample that should fire is answered with a risk above the draft's
// warn_at, and one about a sample that should not, below it. That is what the test bench of the
// mock judge's draft shows on the demo, instead of hash answers that read as a broken check.
func TestStudioSamplesAreScripted(t *testing.T) {
	raw, err := contracts.FS.ReadFile("prompts/evaluator-drafter.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var prompt struct {
		Examples []struct {
			Output map[string]any `json:"output"`
		} `json:"examples"`
	}
	if err := json.Unmarshal(raw, &prompt); err != nil {
		t.Fatal(err)
	}
	cl := mockClientFor(t, MockConfig{Mode: MockScenario, Strict: true})
	checked := 0
	for i, ex := range prompt.Examples {
		doc := map[string]any{}
		for k, v := range ex.Output {
			doc[k] = v
		}
		samples, _ := doc["test_samples"].([]any)
		delete(doc, "test_samples")
		delete(doc, "rationale")
		delete(doc, "split_suggestions")
		doc["id"], doc["version"] = "proof_example", float64(i+1)
		b, _ := json.Marshal(doc)
		spec, err := decision.ParseSpec(b)
		if err != nil {
			t.Fatalf("example %d: %v", i, err)
		}
		warnAt := spec.Outcomes.WarnAt
		qid := spec.QuestionID()
		for _, s := range samples {
			sample := s.(map[string]any)
			state := map[string]any{decision.FieldAssistantResponse: sample["assistant_response"]}
			resp, err := cl.Decide(context.Background(), &Request{Model: DefaultMockModel, State: state,
				Questions: map[string]any{qid: spec.QuestionBody()}}, CallOptions{})
			if err != nil {
				t.Fatalf("example %d, %q: %v", i, sample["assistant_response"], err)
			}
			n := decision.Normalize(resp.Answers[qid], spec)
			fires := n.Error == "" && n.RiskRaw >= warnAt
			if fires != sample["should_fire"].(bool) || n.Error != "" {
				t.Errorf("example %d, %q: risk %.2f (%s), should_fire %v", i, sample["assistant_response"], n.RiskRaw, n.Error, sample["should_fire"])
			}
			checked++
		}
	}
	if checked < 6 {
		t.Fatalf("only %d samples checked", checked)
	}
}

// A sample text under an edited question, or a question about any other answer, is not scripted:
// strict mode refuses it and the default mode answers it by hash.
func TestStudioAnswersOnlyTheExamplesOwnQuestions(t *testing.T) {
	q := map[string]any{"type": "noul", "instructions": "Does `assistant_response` ask for a password?",
		"criteria": map[string]any{"true": "…", "false": "…"}}
	state := map[string]any{decision.FieldAssistantResponse: "I can reset it for you. What's your current password?"}
	strict := mockClientFor(t, MockConfig{Mode: MockScenario, Strict: true})
	if _, err := strict.Decide(context.Background(), &Request{State: state, Questions: map[string]any{"proof_x__v1": q}}, CallOptions{}); ErrorKind(err) != KindMockFixtureMissing {
		t.Errorf("an edited question was scripted: %v", err)
	}
	m, err := newMock(MockConfig{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if a := m.studioAnswer(q, state); a != nil {
		t.Errorf("edited question answered %+v", a)
	}
	// A choice option the question no longer offers is not scripted either.
	for key, a := range m.studio {
		if a.Type != decision.QuestionChoice {
			continue
		}
		instructions, response, _ := strings.Cut(key, "\x00")
		q := map[string]any{"type": "choice", "instructions": instructions, "criteria": map[string]any{"other": map[string]any{"what": "…"}}}
		if got := m.studioAnswer(q, map[string]any{decision.FieldAssistantResponse: response}); got != nil {
			t.Errorf("answered with an option the question lacks: %+v", got)
		}
		break
	}
}

// A drafter example whose choice sample has no scripted option, or one that contradicts its
// should_fire, fails loudly rather than answering wrongly.
func TestParseStudioSamplesRefusesUnscriptedChoices(t *testing.T) {
	for name, doc := range map[string]string{
		"unlisted": `{"examples":[{"output":{"question":{"type":"choice","instructions":"i"},"signal":{"risk_weights":{"a":1}},
			"test_samples":[{"assistant_response":"not in the table","should_fire":true}]}}]}`,
		"contradicts": `{"examples":[{"output":{"question":{"type":"choice","instructions":"i"},"signal":{"risk_weights":{"recommends_competitor":1}},
			"test_samples":[{"assistant_response":"You're welcome! Anything else I can help with?","should_fire":true}]}}]}`,
	} {
		if _, err := parseStudioSamples([]byte(doc)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// A drafted check about a list of names (the competitor example) does not fire at random on a
// Studio test over past traffic: an answer that names none of them takes the "no_…" option (or a
// low yes/no), and one that names one is left to the hash for a choice and fires for a yes/no.
// Rules that are prose or a scope are not name lists, and a strict mock still refuses.
func TestNamesAnswerFollowsTheListedNames(t *testing.T) {
	choice := map[string]any{"type": "choice", "instructions": "How does `assistant_response` treat the companies in `rules.competitors`?",
		"criteria": map[string]any{"no_competitor_mentioned": map[string]any{}, "recommends_competitor": map[string]any{}, "mentions_competitor_unprompted": map[string]any{}}}
	noul := map[string]any{"type": "noul", "instructions": "Does `assistant_response` name a company in `rules.competitors`?"}
	rules := map[string]any{"competitors": "Globex, Initech"}
	state := func(response string, rules any) map[string]any {
		return map[string]any{decision.FieldAssistantResponse: response, decision.FieldRules: rules}
	}
	refund := "Refunds are available within 30 days of purchase."
	if a := namesAnswer(choice, state(refund, rules)); a == nil || a.Choice != "no_competitor_mentioned" || a.Probabilities["no_competitor_mentioned"] != studioChoiceTop {
		t.Errorf("a refund answer: %+v", a)
	}
	if a := namesAnswer(choice, state("Honestly, globex does exports better.", rules)); a != nil {
		t.Errorf("an answer naming Globex (any case) was not left to the hash: %+v", a)
	}
	if a := namesAnswer(noul, state(refund, rules)); a == nil || *a.Noul != studioNoulPass {
		t.Errorf("yes/no on a refund answer: %+v", a)
	}
	if a := namesAnswer(noul, state("Initech is cheaper.", rules)); a == nil || *a.Noul != studioNoulFire {
		t.Errorf("yes/no naming Initech: %+v", a)
	}
	// Not a name list, no rules, or a word inside another: no scripted answer, or no match.
	for _, r := range []any{map[string]any{"competitors": "Friendly, clear, and professional"}, map[string]any{"competitors": "Any subject"}, decision.NoneProvided, nil} {
		if a := namesAnswer(choice, state(refund, r)); a != nil {
			t.Errorf("rules %v answered %+v", r, a)
		}
	}
	if a := namesAnswer(noul, state("Our globexample plan.", rules)); a == nil || *a.Noul != studioNoulPass {
		t.Errorf("a name inside another word fired: %+v", a)
	}

	// Through Decide: the scenario mock answers it, a strict one still refuses an unscripted question.
	m := mockClientFor(t, MockConfig{Mode: MockScenario})
	resp, err := m.Decide(context.Background(), &Request{State: state(refund, rules), Questions: map[string]any{"competitor_mentions__v1": choice}}, CallOptions{})
	if err != nil || resp.Answers["competitor_mentions__v1"].Choice != "no_competitor_mentioned" {
		t.Fatalf("Decide: %+v %v", resp, err)
	}
	strict := mockClientFor(t, MockConfig{Mode: MockScenario, Strict: true})
	if _, err := strict.Decide(context.Background(), &Request{State: state(refund, rules), Questions: map[string]any{"competitor_mentions__v1": choice}}, CallOptions{}); ErrorKind(err) != KindMockFixtureMissing {
		t.Errorf("a strict mock answered an unscripted question: %v", err)
	}
}

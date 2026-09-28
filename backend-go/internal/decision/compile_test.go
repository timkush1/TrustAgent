package decision

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const testModel = "mock/jev-heuristic-v1"

var refundItem = EvidenceItem{
	ID:     "refund-policy.md",
	Source: "request_system",
	Text:   "Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable.",
}

func testRoute(refs ...string) *CompileRoute {
	r := &CompileRoute{
		OnNoEvidence:    OnNoEvidenceIgnore,
		EvidenceSources: []string{"request_system", "request_context", "tool_results", "extension"},
		Model:           testModel,
	}
	for _, ref := range refs {
		r.Evaluators = append(r.Evaluators, RouteEvaluator{Ref: ref})
	}
	return r
}

func refundInputs() *CompileInputs {
	return &CompileInputs{
		UserRequest:        "Can I still get a refund on my annual plan? I bought it 45 days ago.",
		AssistantResponse:  "You can get a full refund within 60 days of purchase.",
		SystemInstructions: "You are the Acme support assistant.",
		Evidence:           []EvidenceItem{refundItem},
	}
}

func TestCompileRefundRequest(t *testing.T) {
	specs := loadTemplates(t)
	c, err := Compile(testRoute("groundedness@1", "contradiction@1"), specs, refundInputs(), DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Requests) != 1 || len(c.RequestSHA256) != 1 || len(c.EstTokens) != 1 {
		t.Fatalf("compiled %d requests", len(c.Requests))
	}
	req := c.Requests[0]
	if req.Model != testModel || len(req.Questions) != 2 || req.Questions["groundedness__v1"] == nil {
		t.Errorf("request = %+v", req)
	}
	wantState := map[string]any{
		"assistant_response": "You can get a full refund within 60 days of purchase.",
		"evidence":           []any{map[string]any{"id": refundItem.ID, "source": refundItem.Source, "text": refundItem.Text}},
	}
	if !reflect.DeepEqual(req.State, wantState) {
		t.Errorf("state = %#v", req.State)
	}
	if !reflect.DeepEqual(c.QIDRequest, map[string]int{"groundedness__v1": 0, "contradiction__v1": 0}) ||
		!reflect.DeepEqual(c.PackedIDs, []string{"refund-policy.md"}) || c.Truncated || len(c.Excluded) != 0 {
		t.Errorf("compile = %+v", c)
	}
	sum, _ := SHA256Hex(req)
	if c.RequestSHA256[0] != sum {
		t.Errorf("request_sha256 = %s, want %s", c.RequestSHA256[0], sum)
	}
	again, err := Compile(testRoute("groundedness@1", "contradiction@1"), specs, refundInputs(), DefaultBudget())
	if err != nil || again.RequestSHA256[0] != sum || again.QuestionSetSHA256 != c.QuestionSetSHA256 {
		t.Error("compile is not deterministic")
	}
}

func TestCompileErrors(t *testing.T) {
	specs := loadTemplates(t)
	badQuestion, err := ParseSpec([]byte(`{"id":"counts","version":1,"state_fields":["assistant_response"],
		"question":{"type":"noul","instructions":["x", 3],"criteria":{"true":"a","false":"b"}},
		"evidence":{"requires":"none"},"signal":{},"outcomes":{"warn_at":0.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	badKey, err := ParseSpec([]byte(`{"id":"keys","version":1,"state_fields":["assistant_response"],
		"question":{"type":"noul","instructions":{"caf` + string(rune(0xe9)) + `":"x"},"criteria":{"true":"a","false":"b"}},
		"evidence":{"requires":"none"},"signal":{},"outcomes":{"warn_at":0.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	withSpecs := func(extra map[string]*Spec) map[string]*Spec {
		out := map[string]*Spec{}
		for k, v := range specs {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	noModel := testRoute("refusal@1")
	noModel.Model = ""
	badMode := testRoute("refusal@1")
	badMode.OnNoEvidence = "maybe"
	noFactual := testRoute("groundedness@1")
	noFactual.OnNoEvidence = OnNoEvidenceFlag
	noEvidence := refundInputs()
	noEvidence.Evidence = nil
	badStage := testRoute("refusal@1")
	badStage.Evaluators[0].Stage = "canary"
	smallBudget := DefaultBudget()
	smallBudget.MaxQuestions = 0

	cases := []struct {
		name  string
		route *CompileRoute
		specs map[string]*Spec
		in    *CompileInputs
		b     Budget
	}{
		{"unknown evaluator", testRoute("nope@1"), specs, refundInputs(), DefaultBudget()},
		{"attached twice", testRoute("refusal@1", "refusal@1"), specs, refundInputs(), DefaultBudget()},
		{"unknown stage", badStage, specs, refundInputs(), DefaultBudget()},
		{"no model", noModel, specs, refundInputs(), DefaultBudget()},
		{"bad on_no_evidence", badMode, specs, refundInputs(), DefaultBudget()},
		{"factual_claims spec missing", noFactual, map[string]*Spec{"groundedness@1": specs["groundedness@1"]}, noEvidence, DefaultBudget()},
		{"number in question", testRoute("counts@1"), withSpecs(map[string]*Spec{"counts@1": badQuestion}), refundInputs(), DefaultBudget()},
		{"non-ASCII key in question", testRoute("keys@1"), withSpecs(map[string]*Spec{"keys@1": badKey}), refundInputs(), DefaultBudget()},
		{"invalid budget", testRoute("refusal@1"), specs, refundInputs(), smallBudget},
	}
	for _, c := range cases {
		if _, err := Compile(c.route, c.specs, c.in, c.b); err == nil {
			t.Errorf("%s: Compile succeeded, want an error", c.name)
		}
	}
}

func TestCompileRequestTooLarge(t *testing.T) {
	specs := loadTemplates(t)
	in := refundInputs()
	in.AssistantResponse = strings.Repeat("All plans are refundable forever. ", 3500) // ~30k tokens at cpt 4
	b := DefaultBudget()
	b.MaxResponseTokens = 40000
	_, err := Compile(testRoute("refusal@1"), specs, in, b)
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("err = %v, want ErrRequestTooLarge", err)
	}
}

func TestCheckTextTree(t *testing.T) {
	ok := map[string]any{"a": []any{"x", nil, map[string]any{"b": "y"}}}
	if err := checkTextTree(ok, "state"); err != nil {
		t.Error(err)
	}
	for _, bad := range []any{true, 1.0, map[string]any{"": "x"}, []any{"x", false}} {
		if err := checkTextTree(bad, "state"); err == nil {
			t.Errorf("checkTextTree(%v) succeeded, want an error", bad)
		}
	}
}

func TestOverlapTokens(t *testing.T) {
	got := overlapTokens("Annual plans: REFUNDED within 30 days (see §2), e.g. purchase-date 2026x.")
	want := map[string]bool{"annual": true, "plans": true, "refunded": true, "within": true, "days": true, "purchase": true, "date": true, "2026x": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overlapTokens = %v", got)
	}
}

func TestDecideEvaluators(t *testing.T) {
	specs := loadTemplates(t)
	r := testRoute("groundedness@1", "contradiction@1")
	r.OnNoEvidence = OnNoEvidenceFlag
	in := refundInputs()
	in.Evidence = nil
	c, err := Compile(r, specs, in, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	evs, err := c.DecideEvaluators(specs, map[string]Thresholds{"groundedness@1": {Space: SpaceCalibrated, CriticalAt: Null()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[2].Ref != FactualClaimsRef || !evs[2].Internal || evs[2].Thresholds.Space != SpaceRaw ||
		evs[0].Thresholds.Space != SpaceCalibrated || !evs[0].Thresholds.CriticalAt.Set || evs[1].Stage != StageActive {
		t.Errorf("DecideEvaluators = %+v", evs)
	}
	if _, err := c.DecideEvaluators(map[string]*Spec{}, nil); err == nil {
		t.Error("DecideEvaluators without specs succeeded")
	}
}

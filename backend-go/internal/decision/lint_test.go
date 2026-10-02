package decision

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/truthtable/contracts"
)

// noulSpec is a lint-clean custom spec (lint.test.ts noul).
const noulSpec = `{
  "spec_version": "evaluator/v1", "id": "custom_check", "version": 1, "name": "Custom check", "category": "custom",
  "state_fields": ["user_request", "assistant_response"],
  "question": {"type": "noul", "instructions": "Does ` + "`assistant_response`" + ` promise a delivery date?",
    "criteria": {"true": "` + "`assistant_response`" + ` promises a date", "false": "No date is promised"}},
  "evidence": {"requires": "none"},
  "signal": {"invert": false},
  "outcomes": {"warn_at": 0.6, "critical_at": null, "block_eligible": false, "requires_corroboration": [], "implies": []},
  "escalation": {"escalate_from": null, "numeric_or_date": false, "on_error": "ignore"}
}`

// withSpec returns noulSpec with members replaced (a nil value deletes the member), at the top
// level or one level down with "parent.child" keys.
func withSpec(t *testing.T, set map[string]any) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(noulSpec), &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range set {
		target, key := m, k
		if parent, child, ok := strings.Cut(k, "."); ok {
			target, key = m[parent].(map[string]any), child
		}
		if v == nil {
			delete(target, key)
		} else {
			target[key] = v
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustParse(t *testing.T, doc []byte) *Spec {
	t.Helper()
	s, err := ParseSpec(doc)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func codesOf(lints []Lint) [2][]string {
	e, w := LintCodes(lints)
	return [2][]string{e, w}
}

func pathsOf(lints []Lint, code string) []string {
	var out []string
	for _, l := range lints {
		if l.Code == code {
			out = append(out, l.Path)
		}
	}
	return out
}

func builtinDocs(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := fs.ReadDir(contracts.FS, "evaluators")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := fs.ReadFile(contracts.FS, "evaluators/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = b
	}
	if len(out) != 11 {
		t.Fatalf("%d built-in templates, want 11", len(out))
	}
	return out
}

func TestBuiltinsAreClean(t *testing.T) {
	docs := builtinDocs(t)
	var ids []string
	for _, b := range docs {
		ids = append(ids, mustParse(t, b).ID)
	}
	for name, b := range docs {
		v := ValidateSpec(b, LintOptions{Registry: IDSet(ids...)})
		if len(v.Errors) != 0 || len(v.Lints) != 0 {
			t.Errorf("%s: errors %v lints %v", name, v.Errors, v.Lints)
		}
		h, err := v.Spec.Hashes()
		if err != nil || v.Hashes == nil || *v.Hashes != h {
			t.Errorf("%s: hashes %v, want %v (%v)", name, v.Hashes, h, err)
		}
	}
}

func TestValidateSpecSchema(t *testing.T) {
	v := ValidateSpec([]byte(noulSpec), LintOptions{})
	if !v.Valid() || len(v.Lints) != 0 || v.Hashes == nil || v.Spec.Ref() != "custom_check@1" {
		t.Fatalf("clean spec: %+v", v)
	}

	cases := []struct {
		name string
		doc  []byte
		want []FieldError
	}{
		{"not json", []byte(`{"id":`), nil},
		{"not an object", []byte(`[]`), []FieldError{{Path: "", Message: "got array, want object"}}},
		{"missing id", withSpec(t, map[string]any{"id": nil}), []FieldError{{Path: "", Message: "missing property 'id'"}}},
		{"unknown question type", withSpec(t, map[string]any{"question.type": "ranking"}),
			[]FieldError{{Path: "question.type", Message: `must be one of "noul", "choice", "score"`}}},
		{"bad option key", withSpec(t, map[string]any{
			"question": map[string]any{"type": "choice", "instructions": "Which?", "criteria": map[string]any{"rude_reply": "Rude", "X": "Polite"}},
			"signal":   map[string]any{"risk_weights": map[string]any{"rude_reply": 1}},
		}), nil},
		{"bad param name", withSpec(t, map[string]any{"params": map[string]any{"Tone": map[string]any{"default": "x"}}}),
			[]FieldError{{Path: "params.Tone", Message: "invalid key: 'Tone' does not match pattern '^[a-z][a-z0-9_]{1,31}$'"}}},
		{"reserved option key", withSpec(t, map[string]any{
			"question": map[string]any{"type": "choice", "instructions": "Which?", "criteria": map[string]any{"rude_reply": "Rude", "other": "Anything else"}},
			"signal":   map[string]any{"risk_weights": map[string]any{"rude_reply": 1}},
		}), []FieldError{{Path: "question.criteria.other", Message: "invalid key: 'other' is reserved"}}},
		{"option of no known shape", withSpec(t, map[string]any{
			"question": map[string]any{"type": "choice", "instructions": "Which?", "criteria": map[string]any{"rude_reply": 5, "polite_reply": "Polite"}},
			"signal":   map[string]any{"risk_weights": map[string]any{"rude_reply": 1}},
		}), []FieldError{{Path: "question.criteria.rude_reply", Message: "must be null or object or string or array"}}},
		{"evidence required but not a state field", withSpec(t, map[string]any{"evidence.requires": "required"}),
			[]FieldError{{Path: "state_fields", Message: `must include "evidence"`}}},
		{"risk levels on a yes/no question", withSpec(t, map[string]any{"signal": map[string]any{"risk_levels": []int{1}}}),
			[]FieldError{{Path: "signal", Message: "is not allowed together with the rest of the spec"}}},
		{"string where a list goes", withSpec(t, map[string]any{"state_fields": "assistant_response"}),
			[]FieldError{{Path: "state_fields", Message: "got string, want array"}}},
	}
	for _, c := range cases {
		v := ValidateSpec(c.doc, LintOptions{})
		if len(v.Errors) == 0 || v.Valid() || v.Hashes != nil {
			t.Errorf("%s: want schema errors and no hashes, got %+v", c.name, v)
			continue
		}
		if c.want != nil && !reflect.DeepEqual(v.Errors, c.want) {
			t.Errorf("%s: errors %+v, want %+v", c.name, v.Errors, c.want)
		}
	}
	// Only the choice alternative fits a choice question, so its findings are the only ones.
	v = ValidateSpec(cases[4].doc, LintOptions{})
	for _, e := range v.Errors {
		if !strings.HasPrefix(e.Path, "question.criteria") {
			t.Errorf("bad option key: unexpected finding %+v", e)
		}
	}
	if len(v.Errors) == 0 || v.Errors[0].Path != "question.criteria.X" || !strings.HasPrefix(v.Errors[0].Message, "invalid key: 'X'") {
		t.Errorf("bad option key: %+v", v.Errors)
	}
	// A document that is not JSON has one finding and nothing else.
	if v := ValidateSpec([]byte(`{"id":`), LintOptions{}); len(v.Errors) != 1 || !strings.HasPrefix(v.Errors[0].Message, "not valid JSON") || v.Spec != nil {
		t.Errorf("not json: %+v", v)
	}
}

func TestValidateSpecLintsDespiteSchemaErrors(t *testing.T) {
	// The schema refuses the unknown member, but the spec parses, so the hard rules still run.
	doc := withSpec(t, map[string]any{"colour": "red", "state_fields": []string{"user_request"}})
	v := ValidateSpec(doc, LintOptions{})
	if len(v.Errors) != 1 || !strings.Contains(v.Errors[0].Message, "colour") {
		t.Fatalf("errors %+v", v.Errors)
	}
	if got := codesOf(v.Lints); !reflect.DeepEqual(got[0], []string{CodeUnresolvedStateReference}) {
		t.Errorf("lints %+v", v.Lints)
	}
	if v.Spec == nil || v.Hashes != nil {
		t.Errorf("spec %v hashes %v", v.Spec, v.Hashes)
	}
	// A hard-rule error alone also withholds the hashes.
	v = ValidateSpec(withSpec(t, map[string]any{"outcomes.critical_at": 0.5}), LintOptions{})
	if len(v.Errors) != 0 || v.Valid() || v.Hashes != nil || len(v.HardErrors()) != 1 {
		t.Errorf("threshold order: %+v", v)
	}
}

// TestValidateSpecBoundsWork pins the bounds that keep validation cheap on hostile documents: the
// nesting limit (refused before the schema runs), the cap on schema findings, and the 255 options
// that near_duplicate_options compares at most.
func TestValidateSpecBoundsWork(t *testing.T) {
	nested := func(depth int) []byte {
		inner := strings.Repeat("[", depth) + `"Is it rude?"` + strings.Repeat("]", depth)
		return withSpec(t, map[string]any{"question.instructions": json.RawMessage(inner)})
	}
	// The document is level 1 and question.instructions level 3, so 30 nested arrays reach level
	// 32 and the 31st goes over it.
	if v := ValidateSpec(nested(30), LintOptions{}); len(v.Errors) != 0 || v.Spec == nil {
		t.Errorf("30 nested arrays are within the limit: %+v", v.Errors)
	}
	v := ValidateSpec(nested(10000-3), LintOptions{})
	want := []FieldError{{Path: "question.instructions" + strings.Repeat("[0]", 30), Message: "is nested more than 32 levels deep"}}
	if !reflect.DeepEqual(v.Errors, want) || len(v.Lints) != 0 || v.Spec != nil || v.Valid() {
		t.Errorf("deep nesting: %+v", v)
	}
	if v := ValidateSpec(nested(31), LintOptions{Draft: true}); !reflect.DeepEqual(v.Errors, want) {
		t.Errorf("deep draft: %+v", v.Errors)
	}

	// 2,000 options with invalid keys, all described the same way.
	criteria := map[string]any{}
	for i := range 2000 {
		criteria[fmt.Sprintf("Same_reply_%d", i)] = "The same description"
	}
	doc := withSpec(t, map[string]any{
		"question": map[string]any{"type": "choice", "instructions": "Which fits `assistant_response`?", "criteria": criteria},
		"signal":   map[string]any{"risk_weights": map[string]any{"Same_reply_0": 1}},
	})
	v = ValidateSpec(doc, LintOptions{})
	if len(v.Errors) != maxSchemaFindings {
		t.Errorf("%d schema findings, want the cap %d", len(v.Errors), maxSchemaFindings)
	}
	if got := len(pathsOf(v.Lints, LintNearDuplicateOptions)); got != 255*254/2 {
		t.Errorf("%d near-duplicate findings, want the %d pairs among the first 255 options", got, 255*254/2)
	}

	// A refused key in a text tree: the schema location names no member, so the key index finds
	// the object (the first in key order that has the key).
	long := strings.Repeat("k", 65)
	v = ValidateSpec(withSpec(t, map[string]any{"question.instructions": map[string]any{"intro": "Is `assistant_response` rude?", long: "x"}}), LintOptions{})
	if len(v.Errors) == 0 || v.Errors[0].Path != "question.instructions."+long || !strings.HasPrefix(v.Errors[0].Message, "invalid key: '"+long+"'") {
		t.Errorf("text tree key: %+v", v.Errors)
	}
}

func draftDoc(t *testing.T, samples int) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(builtinDocs(t)["groundedness.v1.json"], &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "version", "builtin", "parent", "changelog"} {
		delete(m, k)
	}
	m["rationale"] = "A yes/no question is enough: the answer either states unsupported facts or it does not."
	m["split_suggestions"] = []string{}
	var list []any
	for i := range samples {
		list = append(list, map[string]any{"assistant_response": "The refund window is 30 days.", "evidence": []string{"Refunds within 30 days."}, "should_fire": i%2 == 0})
	}
	m["test_samples"] = list
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestValidateSpecDraft(t *testing.T) {
	v := ValidateSpec(draftDoc(t, 6), LintOptions{Draft: true})
	if len(v.Errors) != 0 || len(v.Lints) != 0 || !v.Valid() {
		t.Fatalf("draft: %+v", v)
	}
	if v.Spec == nil || v.Spec.ID != DraftID || v.Spec.Version != 1 || v.Hashes != nil {
		t.Errorf("draft spec %v hashes %v", v.Spec, v.Hashes)
	}
	if _, ok := v.Spec.raw["test_samples"]; ok {
		t.Error("the draft-only members must not reach the spec")
	}
	// The draft schema needs exactly six samples; the evaluator schema refuses a draft outright.
	v = ValidateSpec(draftDoc(t, 5), LintOptions{Draft: true})
	if len(v.Errors) != 1 || v.Errors[0].Path != "test_samples" {
		t.Errorf("five samples: %+v", v.Errors)
	}
	if v := ValidateSpec(draftDoc(t, 6), LintOptions{}); len(v.Errors) == 0 {
		t.Error("a draft is not an evaluator.v1 spec")
	}
	if v := ValidateSpec([]byte(`"text"`), LintOptions{Draft: true}); len(v.Errors) == 0 || v.Spec != nil {
		t.Errorf("non-object draft: %+v", v)
	}
}

// The tests below port frontend-react/src/lib/decision/lint.test.ts.

func TestLintRiskWeightsAndLevels(t *testing.T) {
	choice := withSpec(t, map[string]any{
		"question": map[string]any{"type": "choice", "instructions": "Which fits `assistant_response`?", "criteria": map[string]any{"rude_reply": "Rude", "polite_reply": "Polite"}},
		"signal":   map[string]any{"risk_weights": map[string]any{"rude_reply": 1, "angry_reply": 1}},
	})
	lints := LintSpec(mustParse(t, choice), LintOptions{})
	if got := codesOf(lints); !reflect.DeepEqual(got[0], []string{CodeUnknownRiskWeightOption}) {
		t.Errorf("choice: %+v", lints)
	}
	if got := pathsOf(lints, CodeUnknownRiskWeightOption); !reflect.DeepEqual(got, []string{"signal.risk_weights.angry_reply"}) {
		t.Errorf("choice paths %v", got)
	}
	score := withSpec(t, map[string]any{
		"question": map[string]any{"type": "score", "instructions": "How rude is `assistant_response`?", "criteria": []any{"Polite", "Curt", "3"}},
		"signal":   map[string]any{"risk_levels": []int{1, 3}},
	})
	lints = LintSpec(mustParse(t, score), LintOptions{})
	want := [2][]string{{CodeRiskLevelOutOfRange}, {LintNumericOnlyScoreLevels, LintScoreUsedForPolicy}}
	if got := codesOf(lints); !reflect.DeepEqual(got, want) {
		t.Errorf("score: %+v", lints)
	}
	if lints[0].Message != "level 3 does not exist: the question has 3 levels" || lints[1].Path != "question.criteria[2]" {
		t.Errorf("score findings %+v", lints)
	}
}

func TestLintThresholdOrderAfterOverrides(t *testing.T) {
	s := mustParse(t, withSpec(t, map[string]any{"outcomes.critical_at": 0.9, "escalation.escalate_from": 0.4}))
	warn := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		name string
		th   *Thresholds
		want []string
	}{
		{"spec", nil, []string{}},
		{"warn above critical", &Thresholds{WarnAt: warn(0.95)}, []string{CodeThresholdOrder}},
		{"escalate not below warn", &Thresholds{EscalateFrom: Some(0.6)}, []string{CodeThresholdOrder}},
		{"explicit null critical", &Thresholds{WarnAt: warn(0.95), CriticalAt: Null()}, []string{}},
		{"within EPS", &Thresholds{WarnAt: warn(0.9 + 1e-10)}, []string{}},
	} {
		if got := codesOf(LintSpec(s, LintOptions{Thresholds: c.th})); !reflect.DeepEqual(got[0], c.want) {
			t.Errorf("%s: errors %v, want %v", c.name, got[0], c.want)
		}
	}
	lints := LintSpec(s, LintOptions{Thresholds: &Thresholds{WarnAt: warn(0.95), EscalateFrom: Some(1e-7)}})
	if len(lints) != 1 || lints[0].Message != "critical_at 0.9 must be at least warn_at 0.95" || lints[0].Path != "outcomes.critical_at" {
		t.Errorf("messages %+v", lints)
	}
	lints = LintSpec(s, LintOptions{Thresholds: &Thresholds{WarnAt: warn(1e-7), CriticalAt: Null()}})
	if len(lints) != 1 || lints[0].Message != "escalate_from 0.4 must be below warn_at 1e-7" {
		t.Errorf("JavaScript number format %+v", lints)
	}
}

func TestLintReferences(t *testing.T) {
	spec := withSpec(t, map[string]any{
		"outcomes.requires_corroboration": []string{"groundedness", "det:numbers_not_in_evidence", "det:vibes"},
		"outcomes.implies":                []string{"ghost"},
	})
	s := mustParse(t, spec)
	if got := codesOf(LintSpec(s, LintOptions{})); !reflect.DeepEqual(got[0], []string{CodeUnknownDetector}) {
		t.Errorf("no registry: %v", got)
	}
	if got := codesOf(LintSpec(s, LintOptions{Registry: IDSet("groundedness")})); !reflect.DeepEqual(got[0], []string{CodeUnknownDetector, CodeUnknownEvaluatorReference}) {
		t.Errorf("registry: %v", got)
	}
	onRoute := mustParse(t, withSpec(t, map[string]any{"outcomes.requires_corroboration": []string{"groundedness"}}))
	lints := LintSpec(onRoute, LintOptions{Registry: IDSet("groundedness"), Attached: IDSet()})
	if got := pathsOf(lints, LintReferenceNotAttached); !reflect.DeepEqual(got, []string{"outcomes.requires_corroboration"}) || lints[0].Severity != SeverityWarning {
		t.Errorf("not attached: %+v", lints)
	}
	if lints := LintSpec(onRoute, LintOptions{Registry: IDSet("groundedness"), Attached: IDSet("groundedness")}); len(lints) != 0 {
		t.Errorf("attached: %+v", lints)
	}
}

func TestLintNonDescriptiveKeysInDocumentOrder(t *testing.T) {
	// The keys are out of byte order on purpose: Go must report them in document order, as lint.ts does.
	doc := []byte(strings.Replace(noulSpec, `"type": "noul", "instructions": "Does `+"`assistant_response`"+` promise a delivery date?",
    "criteria": {"true": "`+"`assistant_response`"+` promises a date", "false": "No date is promised"}}`,
		`"type": "choice", "instructions": "Which fits `+"`assistant_response`"+`?", "criteria": {"option_a": "Rude", "choice_2": "Polite", "no_reply": "Empty"}}`, 1))
	doc = []byte(strings.Replace(string(doc), `"signal": {"invert": false}`, `"signal": {"risk_weights": {"option_a": 1}}`, 1))
	lints := LintSpec(mustParse(t, doc), LintOptions{})
	if got := pathsOf(lints, LintNonDescriptiveKey); !reflect.DeepEqual(got, []string{"question.criteria.option_a", "question.criteria.choice_2"}) {
		t.Errorf("paths %v (%+v)", got, lints)
	}
}

func TestLintTextTrees(t *testing.T) {
	tree := withSpec(t, map[string]any{"question.instructions": []any{"Does `assistant_response` promise a date?", map[string]any{"note": "Answer how many dates."}}})
	lints := LintSpec(mustParse(t, tree), LintOptions{})
	if got := pathsOf(lints, LintCountingOrArithmetic); !reflect.DeepEqual(got, []string{"question.instructions[1].note"}) {
		t.Errorf("text tree: %+v", lints)
	}
	unresolved := withSpec(t, map[string]any{"question.criteria": map[string]any{"true": []any{"`rules.tone` is ignored"}, "false": "ok"}})
	lints = LintSpec(mustParse(t, unresolved), LintOptions{})
	if got := pathsOf(lints, CodeUnresolvedStateReference); !reflect.DeepEqual(got, []string{"question.criteria.true[0]"}) {
		t.Errorf("unresolved: %+v", lints)
	}
	// rules.<param> resolves only with rules declared and the param declared.
	withRules := withSpec(t, map[string]any{
		"state_fields":          []string{"assistant_response", "rules"},
		"params":                map[string]any{"tone": map[string]any{"default": "friendly"}},
		"question.instructions": "Is `assistant_response` off `rules.tone`, or off `rules.voice`?",
	})
	lints = LintSpec(mustParse(t, withRules), LintOptions{})
	if len(lints) != 1 || lints[0].Message != "`rules.voice` is neither a declared state field nor rules.<declared param>" {
		t.Errorf("rules params: %+v", lints)
	}
}

func TestLintJavaScriptParity(t *testing.T) {
	// JavaScript's "." stops at line terminators, so a double negative split across lines is not one.
	for text, want := range map[string]bool{
		"Is it not true that it has no date?":  true,
		"Is it not true\nthat it has no date?": false,
		"Is it not true\u2028that it has no?":  false,
		"Nothing here is not obvious":          false,
	} {
		if got := doubleNegative.MatchString(text); got != want {
			t.Errorf("double negative %q: %v", text, got)
		}
	}
	// ESCAPE_WHAT/i folds ASCII only: U+017F (long s) does not match "s".
	for text, want := range map[string]bool{"A GREETING": true, "No Subject at all": true, "too \u017fhort": false, "no\nfacts": false} {
		if got := escapeWhat.MatchString(asciiLower(text)); got != want {
			t.Errorf("escape what %q: %v", text, got)
		}
	}
	// toLowerCase: KELVIN SIGN becomes k; I WITH DOT ABOVE becomes i plus a combining dot.
	if got := lowerTokens("\u212Aelvin \u0130stanbul Rate-2"); !reflect.DeepEqual(slices.Sorted(maps.Keys(got)), []string{"2", "i", "kelvin", "rate", "stanbul"}) {
		t.Errorf("tokens %v", got)
	}
	// Object.entries lists array-index keys first, ascending, then the others in insertion order;
	// a repeated key keeps its first position and its last value.
	v, err := decodeOrdered([]byte(`{"b":1,"10":2,"a":3,"2":4,"b":5,"01":6}`))
	if err != nil {
		t.Fatal(err)
	}
	o := v.(*jsObject)
	if got := o.entries(); !reflect.DeepEqual(got, []string{"2", "10", "b", "a", "01"}) {
		t.Errorf("entries %v", got)
	}
	if b, _ := o.get("b"); b.(json.Number) != "5" {
		t.Errorf("repeated key value %v", b)
	}
	for k, want := range map[string]bool{"0": true, "4294967294": true, "4294967295": false, "-1": false, "1.0": false, "": false} {
		if isArrayIndex(k) != want {
			t.Errorf("isArrayIndex(%q) = %v", k, !want)
		}
	}
	if utf16Len("ab\U0001F600") != 4 {
		t.Error("utf16Len counts surrogate pairs as two")
	}
}

func TestLintChoiceOptions(t *testing.T) {
	choice := func(criteria map[string]any) *Spec {
		weights := map[string]any{}
		for k := range criteria {
			weights[k] = 1
		}
		return mustParse(t, withSpec(t, map[string]any{
			"question": map[string]any{"type": "choice", "instructions": "Which fits `assistant_response`?", "criteria": criteria},
			"signal":   map[string]any{"risk_weights": weights},
		}))
	}
	// An option whose what mentions a greeting is an escape, like a no_ key.
	if lints := LintSpec(choice(map[string]any{"rude_reply": "Rude", "polite_reply": "Polite", "small_talk": map[string]any{"what": "Only a GREETING"}}), LintOptions{}); len(lints) != 0 {
		t.Errorf("escape by what: %+v", lints)
	}
	if got := codesOf(LintSpec(choice(map[string]any{"rude_reply": "Rude", "polite_reply": "Polite", "neutral_reply": "Neutral"}), LintOptions{})); !reflect.DeepEqual(got[1], []string{LintMissingEscapeOption}) {
		t.Errorf("missing escape: %v", got)
	}
	// not_for separates near-duplicates.
	dup := map[string]any{
		"promises_refund":         map[string]any{"what": "The reply promises a full refund to the customer"},
		"promises_partial_refund": map[string]any{"what": "The reply promises a partial refund to the customer"},
		"no_refund_promise":       "No refund is promised",
	}
	if got := codesOf(LintSpec(choice(dup), LintOptions{})); !reflect.DeepEqual(got[1], []string{LintNearDuplicateOptions}) {
		t.Errorf("near duplicate: %v", got)
	}
	dup["promises_partial_refund"] = map[string]any{"what": "The reply promises a partial refund to the customer", "not_for": "full refunds"}
	if got := codesOf(LintSpec(choice(dup), LintOptions{})); len(got[1]) != 0 {
		t.Errorf("not_for: %v", got)
	}
}

func TestLintLongInstructionsCountsCodePoints(t *testing.T) {
	// The limit counts code points: 26 ASCII characters plus 574 emoji are 600 code points (but
	// 1,174 UTF-16 units), which is not over it; one more emoji is.
	prefix := "Does `assistant_response` "
	if len(prefix) != 26 {
		t.Fatal(len(prefix))
	}
	for k, want := range map[int]bool{574: false, 575: true} {
		s := mustParse(t, withSpec(t, map[string]any{"question.instructions": prefix + strings.Repeat("\U0001F600", k)}))
		got := slices.Contains(pathsOf(LintSpec(s, LintOptions{}), LintLongInstructions), "question.instructions")
		if got != want {
			t.Errorf("%d emoji: long_instructions %v", k, got)
		}
	}
}

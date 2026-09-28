package policy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/decision"
)

func env(t *testing.T) Env {
	return Env{Seeds: DefaultSeeds(), Specs: registry(t)}
}

func enforcingEnv(t *testing.T) Env {
	e := env(t)
	e.Enforcing = true
	return e
}

func mustCheck(t *testing.T, doc string, e Env) *Policy {
	t.Helper()
	res := Check([]byte(doc), e)
	if res.Policy == nil {
		t.Fatalf("Check failed: %v", res.Errors)
	}
	return res.Policy
}

func findError(errs []FieldError, path string) *FieldError {
	for i := range errs {
		if errs[i].Path == path {
			return &errs[i]
		}
	}
	return nil
}

// TestFieldErrorPositions pins the path, line and column of errors from each validation layer:
// the structural walk (unknown fields, types), the value checks, and cross-field rules.
func TestFieldErrorPositions(t *testing.T) {
	cases := []struct {
		name, doc, path string
		line, col       int
		msg             string
	}{
		{"unknown top-level field", "version: 1\npolicies: []\n", "policies", 2, 1, `unknown field "policies"`},
		{"unknown nested field", "version: 1\ndefaults:\n  evidence:\n    sourcez: []\n", "defaults.evidence.sourcez", 4, 5, "unknown field"},
		{"unknown route field", "version: 1\nroutes:\n  - name: a\n    match: {}\n    enforce: true\n", "routes[0].enforce", 5, 5, "unknown field"},
		{"unknown evaluator field", "version: 1\ndefaults:\n  evaluators:\n    - {ref: refusal@1, weight: 2}\n", "defaults.evaluators[0].weight", 4, 24, "unknown field"},
		{"string for int", "version: 1\ndefaults:\n  deadline_ms: soon\n", "defaults.deadline_ms", 3, 16, "must be an integer"},
		{"number for string", "version: 1\ndefaults:\n  block: {message: 42}\n", "defaults.block.message", 3, 20, "must be a string"},
		{"null", "version: 1\ndefaults:\n  mode: null\n", "defaults.mode", 3, 9, "must not be null"},
		{"list for object", "version: 1\ndefaults:\n  evidence: [a]\n", "defaults.evidence", 3, 13, "must be a mapping"},
		{"repeated key", "version: 1\ndefaults:\n  mode: flag\n  mode: observe\n", "defaults.mode", 4, 3, "repeated"},
		{"bad enum", "version: 1\ndefaults:\n  mode: strict\n", "defaults.mode", 3, 9, `unknown value "strict"`},
		{"bad version", "version: 2\n", "version", 1, 10, "version must be 1"},
		{"route value", "version: 1\nroutes:\n  - name: a\n    match: {}\n    deadline_ms: 50\n", "routes[0].deadline_ms", 5, 18, "outside [100, 5000]"},
		{"threshold order", "version: 1\ndefaults:\n  evaluators:\n    - ref: groundedness@1\n      thresholds: {warn_at: 0.99}\n", "defaults.evaluators[0].thresholds", 5, 19, "must not exceed critical_at"},
		{"undeclared param", "version: 1\ndefaults:\n  evaluators:\n    - ref: refusal@1\n      params: {tone: x}\n", "defaults.evaluators[0].params.tone", 5, 22, "declares no such param"},
		{"route name", "version: 1\nroutes:\n  - name: Bad_Name\n    match: {}\n", "routes[0].name", 3, 11, "must match"},
		{"missing match", "version: 1\nroutes:\n  - name: a\n", "routes[0].match", 3, 5, "match: required"},
		{"match path", "version: 1\nroutes:\n  - name: a\n    match: {paths: [v1/chat]}\n", "routes[0].match.paths[0]", 4, 21, "paths start with /"},
		{"cross-field hedge", "version: 1\ndefaults: {deadline_ms: 300}\nroutes:\n  - name: a\n    match: {}\n    hedge_after_ms: 400\n", "routes[0].hedge_after_ms", 6, 21, "deadline_ms 300"},
		{"evidence_sources", "version: 1\ndefaults:\n  evidence: {sources: [request_system]}\n  evaluators:\n    - {ref: groundedness@1, evidence_sources: [extension]}\n", "defaults.evaluators[0].evidence_sources[0]", 5, 48, "never widen"},
		{"syntax", "version: 1\ndefaults: {mode: flag\n", "", 1, 1, "did not find expected"},
		{"not a mapping", "- a\n- b\n", "", 1, 1, "must be a YAML mapping"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := Check([]byte(c.doc), env(t))
			if res.Policy != nil {
				t.Fatal("Check succeeded")
			}
			fe := findError(res.Errors, c.path)
			if fe == nil {
				t.Fatalf("no error at %q in %+v", c.path, res.Errors)
			}
			if fe.Line != c.line || fe.Col != c.col || !strings.Contains(fe.Message, c.msg) {
				t.Errorf("error %+v, want line %d col %d containing %q", *fe, c.line, c.col, c.msg)
			}
		})
	}
}

// TestInheritedErrorsReportedOnce: a bad defaults value is reported at the defaults, not again
// for each route that inherits it; a cross-field rule that fails only in a route is reported for
// that route.
func TestInheritedErrorsReportedOnce(t *testing.T) {
	doc := `version: 1
defaults:
  deadline_ms: 50
  evidence: {sources: [request_system, extension]}
  evaluators:
    - {ref: groundedness@1, evidence_sources: [extension]}
routes:
  - {name: a, match: {}}
  - {name: b, match: {}, evidence: {sources: [request_system]}}
`
	res := Check([]byte(doc), env(t))
	var deadline, sources []FieldError
	for _, e := range res.Errors {
		if strings.Contains(e.Path, "deadline_ms") {
			deadline = append(deadline, e)
		}
		if strings.Contains(e.Path, "evidence_sources") {
			sources = append(sources, e)
		}
	}
	if len(deadline) != 1 || deadline[0].Path != "defaults.deadline_ms" || deadline[0].Line != 3 {
		t.Errorf("deadline errors %+v", deadline)
	}
	if len(sources) != 1 || !strings.Contains(sources[0].Message, `(in route "b")`) || sources[0].Path != "defaults.evaluators[0].evidence_sources[0]" {
		t.Errorf("evidence_sources errors %+v", sources)
	}
}

func TestWarnings(t *testing.T) {
	doc := `version: 1
overrides: {client_rules: true}
defaults:
  fail: closed
routes:
  - name: pay
    match: {}
    mode: block
    streaming: buffer
    evaluators: [{ref: groundedness@1}]
  - name: sampled
    match: {}
    mode: flag
    sample_rate: 0.5
`
	res := Check([]byte(doc), enforcingEnv(t))
	if res.Policy == nil {
		t.Fatalf("errors %v", res.Errors)
	}
	want := map[string]string{
		"overrides.client_rules":      `enforcing route "pay"`,
		"defaults.fail":               "only matters in block and fallback",
		"routes[0].evaluators[0].ref": "contradiction, which this route does not attach",
		"routes[1].sample_rate":       "observe routes only",
	}
	for path, msg := range want {
		fe := findError(res.Warnings, path)
		if fe == nil || !strings.Contains(fe.Message, msg) {
			t.Errorf("warning at %s: %+v (want %q) in %+v", path, fe, msg, res.Warnings)
		}
	}
	// pay inherits request_context from the built-in sources: it trusts user-message context.
	var trust *FieldError
	for i, w := range res.Warnings {
		if strings.Contains(w.Message, "user-message context") {
			trust = &res.Warnings[i]
		}
	}
	if trust == nil || trust.Path != "routes[0].evidence.sources" || !strings.Contains(trust.Message, `(in route "pay")`) {
		t.Errorf("trust warning %+v", trust)
	}
	if len(res.Policy.Warnings) != len(res.Warnings) {
		t.Error("the policy keeps its warnings")
	}
}

func TestExamplePolicyNeedsEnforcing(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "policies", "example.policies.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res := Check(b, env(t))
	if res.Policy != nil || findError(res.Errors, "routes[1].mode") == nil {
		t.Fatalf("without enforcing, the fallback route must be refused: %+v", res.Errors)
	}
	p := mustCheck(t, string(b), enforcingEnv(t))
	pay, ok := p.Route("payments-faq")
	if !ok || pay.Mode != decision.ModeFallback || pay.Fail != decision.FailClosed || pay.HedgeAfterMs != 400 || pay.Fallback.Model != "gpt-4o" {
		t.Errorf("payments-faq %+v", pay)
	}
	if _, ok := p.Route("nope"); ok {
		t.Error("unknown route")
	}
	if d, _ := p.Route(DefaultRouteName); d != p.Default {
		t.Error("the default route")
	}
	if got := len(p.AllRoutes()); got != 3 {
		t.Errorf("%d routes", got)
	}
	// Client overrides may ask for block only on an enforcing build.
	sb, _ := p.Route("support-bot")
	p.Overrides.ClientMode = ClientModeStricterOnly
	if m, ok := p.Mode(sb, "block"); !ok || m != decision.ModeBlock {
		t.Errorf("stricter block on an enforcing build: %s %v", m, ok)
	}
}

// TestDocIsAsWritten: the stored document is the file as written (no defaults), its hash is
// sha256(JCS(doc)), and the JSON form of the same policy has the same hash.
func TestDocIsAsWritten(t *testing.T) {
	src := "# comment\nversion: 1\ndefaults:\n  sample_rate: 1.0\n  mode: flag\nroutes:\n  - {name: a, match: {headers: {X-TrustAgent-Route: a}}, evaluators: [{ref: groundedness@1, thresholds: {critical_at: null}}]}\n"
	p := mustCheck(t, src, env(t))
	var doc map[string]any
	if err := json.Unmarshal(p.Doc, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["overrides"]; ok || len(doc["defaults"].(map[string]any)) != 2 {
		t.Errorf("doc %s: defaults must not be filled in", p.Doc)
	}
	want, err := decision.SHA256Hex(doc)
	if err != nil || p.SHA256 != want || p.YAML != src {
		t.Errorf("sha %s want %s (%v)", p.SHA256, want, err)
	}
	res := CheckDoc(p.Doc, env(t))
	if res.Policy == nil {
		t.Fatalf("doc form: %v", res.Errors)
	}
	if res.Policy.SHA256 != p.SHA256 {
		t.Errorf("doc form hash %s, yaml form %s", res.Policy.SHA256, p.SHA256)
	}
	if !strings.Contains(res.Policy.YAML, "critical_at: null") || strings.Contains(res.Policy.YAML, "{") {
		t.Errorf("generated YAML is block style and keeps nulls:\n%s", res.Policy.YAML)
	}
	// Strings that look like other types stay strings.
	tricky := `{"version":1,"defaults":{"block":{"message":"true"},"fallback":{"model":"1.5"}}}`
	res = CheckDoc(json.RawMessage(tricky), env(t))
	if res.Policy == nil || res.Policy.Default.Block.Message != "true" || res.Policy.Default.Fallback.Model != "1.5" {
		t.Fatalf("tricky strings: %v\n%s", res.Errors, res.Policy.YAML)
	}
	// Errors of the doc form carry paths only.
	res = CheckDoc(json.RawMessage(`{"version":1,"defaults":{"mode":"strict"}}`), env(t))
	if fe := findError(res.Errors, "defaults.mode"); fe == nil || fe.Line != 0 || fe.Col != 0 {
		t.Errorf("doc errors %+v", res.Errors)
	}
	if res := CheckDoc(json.RawMessage(`not json`), env(t)); len(res.Errors) != 1 || res.Errors[0].Path != "doc" {
		t.Errorf("invalid JSON: %+v", res.Errors)
	}
}

func schemaErr(t *testing.T, doc []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return err
	}
	return contractstest.Schema(t, "policy.v1.schema.json").Validate(inst)
}

// TestAgreesWithSchema: documents the loader accepts are valid policy/v1, and documents the
// schema refuses are refused by the loader too.
func TestAgreesWithSchema(t *testing.T) {
	var valid []string
	for _, f := range []string{filepath.Join("..", "..", "..", "config", "policies.yaml"), filepath.Join("..", "..", "..", "contracts", "policies", "example.policies.yaml")} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		valid = append(valid, string(b))
	}
	valid = append(valid,
		"version: 1\n",
		"version: 1\ndefaults:\n  evidence: {tag_patterns: [{pattern: '<kb>(.*?)</kb>', flags: s}], untrusted_tools: [web_*]}\n",
		"version: 1\nroutes:\n  - {name: r, match: {models: [gpt-*], key_labels: [k]}, hedge_after_ms: 0, decision: {model: typesafe/jev-1.13}}\n",
	)
	for _, doc := range valid {
		p := mustCheck(t, doc, enforcingEnv(t))
		if err := schemaErr(t, p.Doc); err != nil {
			t.Errorf("accepted document is not policy/v1: %v\n%s", err, doc)
		}
	}
	invalid := []string{
		`{"version":1,"defaults":{"hedge_after_ms":50}}`,
		`{"version":1,"defaults":{"block":{"status":500}}}`,
		`{"version":1,"defaults":{"buffer_keepalive_s":61}}`,
		`{"version":1,"defaults":{"evidence":{"knowledge":{"top_k":0}}}}`,
		`{"version":1,"defaults":{"detectors":{"flag_on":["secret_pattern","secret_pattern"]}}}`,
		`{"version":1,"defaults":{"decision":{"model":""}}}`,
		`{"version":1,"defaults":{"fallback":{"upstream":"https://evil.example"}}}`,
		`{"version":1,"defaults":{"evaluators":[{"ref":"refusal@1","params":{"Bad":"x"}}]}}`,
		`{"version":1,"routes":[{"name":"a","match":{"headers":{"bad header":"x"}}}]}`,
		`{"version":1,"routes":[{"name":"a","match":{"paths":["/a","/a"]}}]}`,
		`{"version":1,"routes":[{"name":"default","match":{}}]}`,
		`{"version":1,"defaults":{"evidence":{"untrusted_tools":[""]}}}`,
	}
	for _, doc := range invalid {
		if schemaErr(t, []byte(doc)) == nil {
			t.Errorf("the schema accepts %s: pick another example", doc)
		}
		if res := CheckDoc(json.RawMessage(doc), enforcingEnv(t)); res.Policy != nil {
			t.Errorf("loader accepts a schema-invalid document: %s", doc)
		}
	}
}

func TestValidationErrorFormatting(t *testing.T) {
	_, err := Parse([]byte("version: 1\ndefaults: {mode: strict}\n"), DefaultSeeds(), registry(t))
	if err == nil || !strings.HasPrefix(err.Error(), "policy: defaults.mode (line 2, col 18): unknown value") {
		t.Fatalf("error %v", err)
	}
	if errs := FieldErrors(err); len(errs) != 1 {
		t.Errorf("field errors %+v", errs)
	}
	if FieldErrors(os.ErrNotExist) != nil {
		t.Error("not a validation error")
	}
	if s := (FieldError{Message: "m"}).String(); s != "m" {
		t.Errorf("bare error %q", s)
	}
}

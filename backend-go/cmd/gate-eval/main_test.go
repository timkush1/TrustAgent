package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/llmapi"
	"github.com/truthtable/backend-go/internal/policy"
)

// repoPolicy is the repository policy file, relative to this package.
const repoPolicy = "../../../config/policies.yaml"

const refundDoc = "Annual plans can be refunded within 30 days of purchase. Monthly plans are non-refundable."

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func runCLI(t *testing.T, stdin string, getenv func(string) string, args ...string) (code int, lines []map[string]any, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(context.Background(), args, strings.NewReader(stdin), &out, &errb, getenv)
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("output line is not JSON: %v\n%s", err, sc.Text())
		}
		lines = append(lines, m)
	}
	return code, lines, errb.String()
}

func dig(m map[string]any, path ...string) any {
	var v any = m
	for _, p := range path {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[p]
	}
	return v
}

func TestParseExample(t *testing.T) {
	ex, err := parseExample([]byte(`{"id":"a","query":"Q?","response":"R.","context":["one","two"],"labels":{"groundedness@1":true},"domain":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ex.UserRequest != "Q?" || ex.Response != "R." || len(ex.Evidence) != 2 ||
		ex.Evidence[1] != (decision.EvidenceItem{ID: "doc-2", Source: "request_context", Text: "two"}) || !ex.trusted() {
		t.Errorf("example = %+v", ex)
	}
	ex, err = parseExample([]byte(`{"id":"b","user_request":"Q","answer":"R","evidence":[{"id":"kb","source":"tool_results","text":"T","untrusted":true}],"rules":{"k":"v"},"max_marker":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if ex.Evidence[0] != (decision.EvidenceItem{ID: "kb", Source: "tool_results", Text: "T", Untrusted: true}) || ex.trusted() || ex.line.MaxMarker != 2 {
		t.Errorf("example = %+v", ex)
	}
	for doc, want := range map[string]string{
		`{"id":"c","response":"R","evidense":["x"]}`:                           "unknown field",
		`{"response":"R"}`:                                                     "id is required",
		`{"id":"c"}`:                                                           "response is required",
		`{"id":"c","response":"R","answer":"R"}`:                               "either response or answer",
		`{"id":"c","response":"R","context":["a"],"evidence":["b"]}`:           "either context or evidence",
		`{"id":"c","response":"R","evidence":[{"source":"request_context"}]}`:  "text is required",
		`{"id":"c","response":"R","evidence":[{"text":"a","source":"web"}]}`:   "unknown source",
		`{"id":"c","response":"R","evidence":["a",{"id":"doc-1","text":"b"}]}`: "duplicate id",
		`{"id":"c","response":"R","evidence":"a"}`:                             "must be a list",
		`{"id":"c","response":"R"} {"id":"d"}`:                                 "more than one JSON value",
		`{"id":"c","response":"R","max_marker":-1}`:                            "max_marker",
	} {
		if _, err := parseExample([]byte(doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("parseExample(%s) = %v, want an error about %q", doc, err, want)
		}
	}
}

func TestSpend(t *testing.T) {
	s := &spend{cap: 1}
	if !s.reserve(0.4) || !s.reserve(0.5) {
		t.Fatal("reservations within the cap refused")
	}
	if s.reserve(0.2) || !s.stopped() {
		t.Fatal("a reservation over the cap was accepted")
	}
	if s.reserve(0.01) {
		t.Fatal("the budget accepted a reservation after it was exhausted")
	}
	s.settle(0.4, 0.3)
	s.settle(0.5, 0.25)
	if got := s.total(); got != 0.55 {
		t.Errorf("total = %v", got)
	}
	unlimited := &spend{}
	for range 100 {
		if !unlimited.reserve(1e6) {
			t.Fatal("an uncapped budget refused a reservation")
		}
	}
}

// scenarioExample builds a gate-eval example from a demo scenario the way the proxy sees it: the
// request's evidence is extracted with the route's settings.
func scenarioExample(t *testing.T, id string) string {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []struct {
			ID       string          `json:"id"`
			Request  json.RawMessage `json:"request"`
			Response string          `json:"response"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	for _, s := range pack.Scenarios {
		if s.ID != id {
			continue
		}
		req, err := llmapi.ParseChatRequest(s.Request)
		if err != nil {
			t.Fatal(err)
		}
		settings := evidence.DefaultSettings()
		res, err := evidence.Extract(req, &settings)
		if err != nil {
			t.Fatal(err)
		}
		items := make([]map[string]any, len(res.Items))
		for i, it := range res.Items {
			items[i] = map[string]any{"id": it.ID, "source": it.Source, "text": it.Text}
		}
		line, _ := json.Marshal(map[string]any{
			"id": id, "user_request": res.UserRequest, "system_instructions": res.SystemInstructions,
			"response": s.Response, "evidence": items, "scenario": id, "labels": map[string]bool{"groundedness@1": id != "grounded-pass"},
		})
		return string(line)
	}
	t.Fatalf("no scenario %q", id)
	return ""
}

func TestGateEvalMockMatchesTheDemoScenarios(t *testing.T) {
	input := strings.Join([]string{
		scenarioExample(t, "grounded-pass"),
		scenarioExample(t, "refund-contradiction-flag"),
		scenarioExample(t, "no-evidence-not-assessed"),
		`{"id":"broken","response":"R","evidense":[]}`,
	}, "\n") + "\n"
	dir := t.TempDir()
	out := filepath.Join(dir, "results.jsonl")
	code, _, stderr := runCLI(t, input, env("TRUTHTABLE_JEV_MOCK_MODE", "scenario"),
		"--backend", "mock", "--policy", repoPolicy, "--route", "support-bot", "--out", out, "--concurrency", "3")
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 4 {
		t.Fatalf("%d output lines, want 4", len(lines))
	}
	for i, id := range []string{"grounded-pass", "refund-contradiction-flag", "no-evidence-not-assessed", "broken"} {
		if lines[i]["id"] != id || lines[i]["line"] != float64(i+1) {
			t.Errorf("line %d is %v (line %v), want %s in input order", i, lines[i]["id"], lines[i]["line"], id)
		}
	}
	// The expectations of contracts/demo/scenarios.v1.json on the support-bot route.
	for i, want := range []map[string]any{
		{"action": "pass", "would_action": "pass", "status": "ok", "verdict": "supported", "max_risk": 0.06},
		{"action": "flag", "would_action": "block", "status": "ok", "verdict": "contradicted", "max_risk": 0.98},
		{"action": "pass", "would_action": "pass", "status": "not_assessed", "verdict": "not_assessed", "max_risk": nil},
	} {
		d, _ := lines[i]["decision"].(map[string]any)
		for k, v := range want {
			if d[k] != v {
				t.Errorf("%s: decision.%s = %v, want %v", lines[i]["id"], k, d[k], v)
			}
		}
		if lines[i]["simulated"] != true || lines[i]["error"] != nil || lines[i]["mode"] != "flag" || lines[i]["route"] != "support-bot" {
			t.Errorf("%s: %v", lines[i]["id"], lines[i])
		}
	}
	flagged := lines[1]
	if dig(flagged, "decision", "evaluators", "contradiction@1", "level") != "critical" ||
		dig(flagged, "decision", "evaluators", "groundedness@1", "risk_raw") == nil ||
		dig(flagged, "decision_model", "backend") != "mock" || dig(flagged, "decision_model", "cost_source") != "simulated" ||
		dig(flagged, "decision_model", "snapshot") != "mock/jev-heuristic-v1" || flagged["cost_usd"].(float64) <= 0 {
		t.Errorf("flagged line: %v", flagged)
	}
	if dig(flagged, "labels", "groundedness@1") != true {
		t.Errorf("labels were not passed through: %v", flagged["labels"])
	}
	if fields := dig(flagged, "compile", "state_fields"); fmt.Sprint(fields) != "[assistant_response evidence]" {
		t.Errorf("state_fields = %v", fields)
	}
	if lines[2]["decision_model"] != nil || lines[2]["cost_usd"] != 0.0 || dig(lines[2], "compile", "requests") != 0.0 {
		t.Errorf("no evidence must send nothing: %v", lines[2])
	}
	if e, _ := lines[3]["error"].(string); !strings.Contains(e, "unknown field") || lines[3]["decision"] != nil {
		t.Errorf("broken line: %v", lines[3])
	}
	for _, want := range []string{"4 examples: 3 evaluated, 1 errors, 0 over budget", "backend mock (mock/jev-heuristic-v1)", "(simulated)", "Simulated: mock decision model"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("summary lacks %q:\n%s", want, stderr)
		}
	}
}

// fakeClient is a decision-model backend with a fixed answer, a fixed reported cost per call and an
// optional failure.
type fakeClient struct {
	calls    atomic.Int64
	costUSD  *float64
	fail     error
	noul     float64
	delay    func() time.Duration
	mu       sync.Mutex
	maxSpent float64
	spent    float64
	cap      float64
}

func (f *fakeClient) Info() jev.Info {
	return jev.Info{Backend: jev.BackendOpenRouterDecisions, Model: "typesafe/jev-1.13"}
}

func (f *fakeClient) Decide(ctx context.Context, req *jev.Request, _ jev.CallOptions) (*jev.Response, error) {
	f.calls.Add(1)
	if f.delay != nil {
		time.Sleep(f.delay())
	}
	if f.fail != nil {
		return nil, f.fail
	}
	answers := map[string]*decision.RawAnswer{}
	for qid := range req.Questions {
		v := f.noul
		answers[qid] = &decision.RawAnswer{Type: decision.QuestionNoul, Noul: &v}
	}
	if f.costUSD != nil {
		f.mu.Lock()
		f.spent += *f.costUSD
		f.maxSpent = max(f.maxSpent, f.spent)
		f.mu.Unlock()
	}
	return &jev.Response{Snapshot: "typesafe/jev-1.13-20260917", Answers: answers, CostUSD: f.costUSD, CostSource: jev.CostReported,
		Backend: jev.BackendOpenRouterDecisions, LatencyMs: 5, Attempts: 1}, nil
}

func testEvaluator(t *testing.T, client jev.Client, capUSD float64) *evaluator {
	t.Helper()
	reg, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.LoadFile(repoPolicy, policy.DefaultSeeds(), reg)
	if err != nil {
		t.Fatal(err)
	}
	route := *pol.Routes[0] // support-bot: groundedness@1 and contradiction@1
	b := decision.DefaultBudget()
	b.MaxResponseTokens, b.EvidenceMaxTokens = route.Decision.MaxResponseTokens, route.Evidence.MaxTokens
	return &evaluator{
		client: client, route: &route, mode: route.Mode, model: "typesafe/jev-1.13", specs: reg.Specs(), budget: b,
		pricePerMTok: 10, callTimeout: time.Second, spend: &spend{cap: capUSD}, now: time.Now,
	}
}

func examples(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, `{"id":"ex-%d","query":"How long is the refund window?","response":"Annual plans can be refunded within 30 days.","context":[%q]}`+"\n", i, refundDoc)
	}
	return b.String()
}

func TestCostCapIsHard(t *testing.T) {
	cost := 0.004
	client := &fakeClient{costUSD: &cost, noul: 0.1}
	ev := testEvaluator(t, client, 0.05)
	// Price the reservation: est tokens of one example at $10/Mtok, doubled.
	ex, err := parseExample([]byte(strings.SplitN(examples(1), "\n", 2)[0]))
	if err != nil {
		t.Fatal(err)
	}
	c, err := decision.Compile(ev.compileRoute(), ev.specs, &decision.CompileInputs{
		UserRequest: ex.UserRequest, AssistantResponse: ex.Response, Evidence: ex.Evidence,
	}, ev.budget)
	if err != nil || len(c.Requests) != 1 {
		t.Fatalf("compile: %v %+v", err, c)
	}
	reservation := float64(c.EstTokens[0]) * 10 / 1e6 * reserveFactor
	if reservation >= 0.05 || reservation < 0.004 {
		t.Fatalf("reservation %v does not suit this test", reservation)
	}

	var out bytes.Buffer
	s, err := evaluateAll(context.Background(), ev, strings.NewReader(examples(40)), &out, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.overBudget == 0 || s.evaluated == 0 || s.examples != 40 {
		t.Fatalf("stats = %+v", s)
	}
	if spent := ev.spend.total(); spent > 0.05 || client.maxSpent > 0.05 {
		t.Errorf("spent %v (vendor saw %v), over the $0.05 cap", spent, client.maxSpent)
	}
	// Sequential run: the evaluated examples come first, every later one is skipped, and no call
	// starts after the first refusal.
	var skipped bool
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var r result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Skipped != nil {
			skipped = true
			if *r.Skipped != skippedOverBudget || r.Decision != nil || r.CostUSD != 0 {
				t.Errorf("skipped line %+v", r)
			}
			continue
		}
		if skipped {
			t.Errorf("%s was evaluated after the cap stopped the run", r.ID)
		}
	}
	if int(client.calls.Load()) != s.evaluated {
		t.Errorf("%d calls for %d evaluated examples", client.calls.Load(), s.evaluated)
	}
	if summary := s.String(ev); !strings.Contains(summary, "Stopped at the cost cap") || !strings.Contains(summary, "of a $0.05 cap") {
		t.Errorf("summary: %s", summary)
	}
}

func TestCostCapWithConcurrency(t *testing.T) {
	cost := 0.004
	r := rand.New(rand.NewPCG(1, 2))
	var mu sync.Mutex
	client := &fakeClient{costUSD: &cost, noul: 0.5, delay: func() time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return time.Duration(r.IntN(3)) * time.Millisecond
	}}
	ev := testEvaluator(t, client, 0.03)
	var out bytes.Buffer
	s, err := evaluateAll(context.Background(), ev, strings.NewReader(examples(30)), &out, 6)
	if err != nil {
		t.Fatal(err)
	}
	if client.maxSpent > 0.03 || ev.spend.total() > 0.03 || s.overBudget == 0 {
		t.Errorf("spent %v with 6 workers, cap 0.03; stats %+v", client.maxSpent, s)
	}
	// Output stays in input order.
	sc := bufio.NewScanner(&out)
	for i := 0; sc.Scan(); i++ {
		var r result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.ID != fmt.Sprintf("ex-%d", i) {
			t.Fatalf("line %d is %s", i, r.ID)
		}
	}
}

func TestFailedCallsAreChargedAtTheEstimate(t *testing.T) {
	client := &fakeClient{fail: &jev.Error{Kind: jev.KindCredits, Status: 402}}
	ev := testEvaluator(t, client, 1)
	ex, err := parseExample([]byte(strings.TrimSpace(examples(1))))
	if err != nil {
		t.Fatal(err)
	}
	r := ev.evaluate(context.Background(), 1, ex)
	if r.Error != nil || r.Decision == nil || r.Decision.Status != decision.StatusError {
		t.Fatalf("result = %+v", r)
	}
	if g, _ := r.Decision.Evaluators.Get("groundedness@1"); g.Reason == nil || *g.Reason != "error:credits" {
		t.Errorf("groundedness = %+v", g)
	}
	if r.DecisionModel.CostSource != jev.CostComputed || r.CostUSD <= 0 || ev.spend.total() != r.CostUSD {
		t.Errorf("usage %+v, cost %v, spent %v", r.DecisionModel, r.CostUSD, ev.spend.total())
	}

	timeout := &fakeClient{fail: context.DeadlineExceeded}
	r = testEvaluator(t, timeout, 1).evaluate(context.Background(), 1, ex)
	if r.Decision.Status != decision.StatusTimeout {
		t.Errorf("timeout status = %s", r.Decision.Status)
	}
}

func TestPaidBackendNeedsACap(t *testing.T) {
	code, _, stderr := runCLI(t, "", env(), "--backend", "off")
	if code != exitFailed || !strings.Contains(stderr, "decision model is off") {
		t.Errorf("off backend: exit %d, %s", code, stderr)
	}
	// This build resolves the HTTP backends to off until P2; with a key they are paid and the cap
	// is required.
	code, _, stderr = runCLI(t, "", env("OPENROUTER_API_KEY", "sk-or-v1-test"), "--backend", "openrouter-decisions")
	if code == exitOK {
		t.Errorf("a paid backend without --max-cost-usd ran: %s", stderr)
	}
	for _, args := range [][]string{
		{"--concurrency", "0"}, {"--max-cost-usd", "-1"}, {"extra"},
		{"--backend", "mock", "--policy", repoPolicy, "--route", "nope"},
		{"--backend", "mock", "--policy", repoPolicy, "--evaluators", "nope@1"},
		{"--backend", "mock", "--policy", repoPolicy, "--evaluators", "groundedness@1,groundedness@1"},
		{"--backend", "mock", "--policy", repoPolicy, "--mode", "enforce"},
	} {
		if code, _, _ := runCLI(t, "", env(), args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestPaidBackendCapIsEnforceable(t *testing.T) {
	// A fake paid backend stands in for the HTTP backends this build resolves to off.
	orig := newJevClient
	t.Cleanup(func() { newJevClient = orig })
	newJevClient = func(jev.Config) (jev.Client, error) { return &fakeClient{noul: 0.1}, nil }

	line := fmt.Sprintf(`{"id":"x","query":"Refund window?","response":"Refunds within 30 days.","context":[%q]}`, refundDoc) + "\n"
	base := []string{"--policy", repoPolicy, "--backend", "openrouter-decisions"}
	for _, c := range []struct {
		name string
		env  func(string) string
		args []string
		want string
	}{
		{"no cap", env(), nil, "set --max-cost-usd"},
		{"NaN cap", env(), []string{"--max-cost-usd", "NaN"}, "must be finite"},
		{"infinite cap", env(), []string{"--max-cost-usd", "+Inf"}, "must be finite"},
		{"NaN price", env("TRUTHTABLE_JEV_PRICE_PER_MTOK", "NaN"), []string{"--max-cost-usd", "1"}, "must be finite"},
		{"infinite price", env(), []string{"--max-cost-usd", "1", "--price-per-mtok", "Inf"}, "must be finite"},
		{"zero price", env(), []string{"--max-cost-usd", "1", "--price-per-mtok", "0"}, "--price-per-mtok must be above 0"},
	} {
		code, _, stderr := runCLI(t, line, c.env, append(slices.Clone(base), c.args...)...)
		if code != exitUsage || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, want %d with %q:\n%s", c.name, code, exitUsage, c.want, stderr)
		}
	}
	code, lines, stderr := runCLI(t, line, env(), append(slices.Clone(base), "--max-cost-usd", "1", "--evaluators", "groundedness@1")...)
	if code != exitOK || len(lines) != 1 || lines[0]["simulated"] != false || dig(lines[0], "decision", "status") != "ok" ||
		dig(lines[0], "decision_model", "cost_source") != "computed" {
		t.Errorf("capped paid run: exit %d, %v\n%s", code, lines, stderr)
	}
}

func TestOutputMustNotBeTheInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "examples.jsonl")
	if err := os.WriteFile(path, []byte(examples(2)), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "", env(), "--backend", "mock", "--policy", repoPolicy, "--in", path, "--out", path)
	if code != exitUsage || !strings.Contains(stderr, "is the --in file") {
		t.Errorf("exit %d: %s", code, stderr)
	}
	if b, _ := os.ReadFile(path); string(b) != examples(2) {
		t.Errorf("the examples file was changed:\n%s", b)
	}
}

func TestEvaluatorsOverrideAndModeOverride(t *testing.T) {
	line := fmt.Sprintf(`{"id":"x","query":"Refund window?","response":"Refunds within 60 days.","context":[%q]}`, refundDoc)
	code, lines, stderr := runCLI(t, line+"\n", env("TRUTHTABLE_JEV_MOCK_MODE", "hash"),
		"--backend", "mock", "--policy", repoPolicy, "--evaluators", "contradiction@1", "--mode", "block")
	if code != exitOK || len(lines) != 1 {
		t.Fatalf("exit %d, %d lines\n%s", code, len(lines), stderr)
	}
	evs, _ := dig(lines[0], "decision", "evaluators").(map[string]any)
	if _, ok := evs["contradiction@1"]; !ok || len(evs) != 1 || lines[0]["mode"] != "block" || lines[0]["route"] != "default" {
		t.Errorf("line = %v", lines[0])
	}
	// The detectors ran: 60 is not in the evidence.
	fired := false
	for _, d := range lines[0]["detectors"].([]any) {
		if dm := d.(map[string]any); dm["id"] == decision.DetNumbers && dm["fired"] == true {
			fired = true
		}
	}
	if !fired {
		t.Errorf("numbers detector did not fire: %v", lines[0]["detectors"])
	}
}

func TestInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ev := testEvaluator(t, &fakeClient{noul: 0.1}, 0)
	var out bytes.Buffer
	if _, err := evaluateAll(ctx, ev, strings.NewReader(examples(3)), &out, 1); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("err = %v", err)
	}
}

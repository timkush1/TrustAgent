package jev

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		want   string
		reason string
	}{
		{"demo forces mock", Config{Backend: BackendOpenRouterDecisions, DemoMode: true}, BackendMock, "demo mode"},
		{"explicit mock", Config{Backend: BackendMock}, BackendMock, "=mock"},
		{"explicit off", Config{Backend: BackendOff, OpenRouterKey: "k"}, BackendOff, "=off"},
		{"auto without keys", Config{}, BackendOff, "no decision-model credentials"},
		{"auto openrouter", Config{Backend: BackendAuto, OpenRouterKey: "k"}, BackendOff, "openrouter-decisions backend is not available"},
		{"auto typesafe", Config{TypeSafeKey: "k"}, BackendOff, "typesafe backend"},
		{"auto compatible", Config{BaseURL: "https://x", JevAPIKey: "k", OpenRouterKey: "k"}, BackendOff, "compatible backend"},
		{"explicit http backend", Config{Backend: BackendOpenRouterSystemOne}, BackendOff, "lands in P2"},
		{"unknown", Config{Backend: "bogus-backend"}, BackendOff, "unknown TRUTHTABLE_JEV_BACKEND"},
	}
	for _, c := range cases {
		cl, err := New(c.cfg)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		info := cl.Info()
		if info.Backend != c.want || !strings.Contains(info.Reason, c.reason) {
			t.Errorf("%s: %+v, want backend %s with reason containing %q", c.name, info, c.want, c.reason)
		}
	}
}

func TestOffClient(t *testing.T) {
	cl, _ := New(Config{Backend: BackendOff, Model: "typesafe/jev-1.13"})
	if cl.Info().Model != "typesafe/jev-1.13" {
		t.Errorf("model %q", cl.Info().Model)
	}
	_, err := cl.Decide(context.Background(), &Request{}, CallOptions{})
	if ErrorKind(err) != KindOff {
		t.Errorf("err %v, want kind off", err)
	}
}

func mockClientFor(t *testing.T, mc MockConfig) Client {
	t.Helper()
	cl, err := New(Config{Backend: BackendMock, Mock: mc})
	if err != nil {
		t.Fatal(err)
	}
	return cl
}

var s0Questions = map[string]any{
	"groundedness__v1":  map[string]any{"type": "noul", "instructions": "…", "criteria": map[string]any{"true": "…", "false": "…"}},
	"contradiction__v1": map[string]any{"type": "noul", "instructions": "…", "criteria": map[string]any{"true": "…", "false": "…"}},
}

func s0Request() *Request {
	return &Request{Model: DefaultMockModel, State: map[string]any{"assistant_response": "You can get a full refund within 60 days."}, Questions: s0Questions}
}

func TestMockScenarioAnswers(t *testing.T) {
	cl := mockClientFor(t, MockConfig{Mode: MockScenario, Strict: true})
	if cl.Info().Model != DefaultMockModel {
		t.Errorf("default model %q", cl.Info().Model)
	}
	resp, err := cl.Decide(context.Background(), s0Request(), CallOptions{Scenario: "refund-contradiction-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if g := resp.Answers["groundedness__v1"]; g == nil || *g.Noul != 0.96 {
		t.Errorf("groundedness answer %+v", g)
	}
	if c := resp.Answers["contradiction__v1"]; c == nil || *c.Noul != 0.98 {
		t.Errorf("contradiction answer %+v", c)
	}
	if resp.Snapshot != DefaultMockModel || resp.Backend != BackendMock || resp.CostSource != CostSimulated || resp.Attempts != 1 {
		t.Errorf("response accounting %+v", resp)
	}
	if resp.InputTokens == nil || *resp.InputTokens <= decision.Overhead || resp.CostUSD == nil || *resp.CostUSD <= 0 {
		t.Errorf("tokens %v cost %v", resp.InputTokens, resp.CostUSD)
	}
	// Answers are copies: changing one does not change the fixtures.
	*resp.Answers["groundedness__v1"].Noul = 0
	again, _ := cl.Decide(context.Background(), s0Request(), CallOptions{Scenario: "refund-contradiction-flag"})
	if *again.Answers["groundedness__v1"].Noul != 0.96 {
		t.Error("the fixture was modified through a returned answer")
	}
}

func TestMockStrictMisses(t *testing.T) {
	cl := mockClientFor(t, MockConfig{Mode: MockScenario, Strict: true})
	for _, scenario := range []string{"", "no-such-scenario", "no-evidence-not-assessed"} {
		_, err := cl.Decide(context.Background(), s0Request(), CallOptions{Scenario: scenario})
		var e *Error
		if !errors.As(err, &e) || e.Kind != KindMockFixtureMissing || e.Message == "" {
			t.Errorf("scenario %q: err %v, want mock_fixture_missing", scenario, err)
		}
	}
}

func TestMockHashFallbackIsDeterministic(t *testing.T) {
	cl := mockClientFor(t, MockConfig{Mode: MockScenario, Seed: 42})
	a, err := cl.Decide(context.Background(), s0Request(), CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := cl.Decide(context.Background(), s0Request(), CallOptions{})
	for qid := range s0Questions {
		x, y := a.Answers[qid], b.Answers[qid]
		if x == nil || x.Noul == nil || *x.Noul < 0 || *x.Noul > 1 || *x.Noul != *y.Noul {
			t.Errorf("%s: answers %+v / %+v", qid, x, y)
		}
	}
	other := mockClientFor(t, MockConfig{Mode: MockHash, Seed: 7})
	c, _ := other.Decide(context.Background(), s0Request(), CallOptions{Scenario: "refund-contradiction-flag"})
	if *c.Answers["groundedness__v1"].Noul == 0.96 && *c.Answers["contradiction__v1"].Noul == 0.98 {
		t.Error("hash mode must ignore scripted answers")
	}
}

func TestMockHashChoiceAndScore(t *testing.T) {
	cl := mockClientFor(t, MockConfig{Mode: MockHash})
	req := &Request{Model: "typesafe/jev-1.13", State: map[string]any{"x": "y"}, Questions: map[string]any{
		"refusal__v1": map[string]any{"type": "choice", "instructions": "…", "criteria": map[string]any{
			"answers_request": map[string]any{"what": "…"}, "declines_or_deflects": map[string]any{"what": "…"}, "nothing_to_answer": nil}},
		"quality__v1": map[string]any{"type": "score", "instructions": "…", "criteria": []any{"bad", "ok", "good"}},
	}}
	resp, err := cl.Decide(context.Background(), req, CallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Snapshot != DefaultMockModel {
		t.Errorf("snapshot %q: a non-mock model still reports the mock snapshot", resp.Snapshot)
	}
	specs := map[string]*decision.Spec{}
	for _, raw := range []string{
		`{"spec_version":"evaluator/v1","id":"refusal","version":1,"name":"R","category":"quality","state_fields":["assistant_response"],
		  "question":{"type":"choice","instructions":"i","criteria":{"answers_request":{"what":"a"},"declines_or_deflects":{"what":"b"},"nothing_to_answer":null}},
		  "evidence":{"requires":"none"},"signal":{"risk_weights":{"declines_or_deflects":1}},"outcomes":{"warn_at":0.7}}`,
		`{"spec_version":"evaluator/v1","id":"quality","version":1,"name":"Q","category":"quality","state_fields":["assistant_response"],
		  "question":{"type":"score","instructions":"i","criteria":["bad","ok","good"]},
		  "evidence":{"requires":"none"},"signal":{"risk_levels":[0]},"outcomes":{"warn_at":0.7}}`,
	} {
		s, err := decision.ParseSpec([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		specs[s.QuestionID()] = s
	}
	for qid, spec := range specs {
		if n := decision.Normalize(resp.Answers[qid], spec); n.Error != "" {
			t.Errorf("%s: the hash answer %+v does not normalize: %s", qid, resp.Answers[qid], n.Error)
		}
	}
}

func TestMockLatencyHonoursDeadline(t *testing.T) {
	cl := mockClientFor(t, MockConfig{Mode: MockHash, Latency: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := cl.Decide(ctx, s0Request(), CallOptions{})
	if ErrorKind(err) != KindTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v, want a timeout", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Error("the mock did not stop at the deadline")
	}
}

func TestMockFixturesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(path, []byte(`{"scenarios":[{"id":"x","mock_answers":{"groundedness__v1":{"type":"noul","noul":0.5}}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cl := mockClientFor(t, MockConfig{FixturesPath: path, Strict: true})
	req := &Request{Model: "mock/x", State: map[string]any{}, Questions: map[string]any{"groundedness__v1": s0Questions["groundedness__v1"]}}
	resp, err := cl.Decide(context.Background(), req, CallOptions{Scenario: "x"})
	if err != nil || *resp.Answers["groundedness__v1"].Noul != 0.5 || resp.Snapshot != "mock/x" {
		t.Errorf("resp %+v err %v", resp, err)
	}
	for name, body := range map[string]string{"dup": `{"scenarios":[{"id":"a"},{"id":"a"}]}`, "noid": `{"scenarios":[{}]}`, "json": `{`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(Config{Backend: BackendMock, Mock: MockConfig{FixturesPath: path}}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := New(Config{Backend: BackendMock, Mock: MockConfig{Mode: "psychic"}}); err == nil {
		t.Error("unknown mock mode must fail")
	}
	cl, _ = New(Config{Backend: BackendMock, Mock: MockConfig{Mode: MockHeuristic}})
	if !strings.Contains(cl.Info().Reason, "heuristic") {
		t.Errorf("reason %q should say heuristic answers as hash", cl.Info().Reason)
	}
}

func TestErrorKind(t *testing.T) {
	if ErrorKind(&Error{Kind: KindAuth, Status: 401}) != KindAuth {
		t.Error("auth")
	}
	if ErrorKind(context.DeadlineExceeded) != KindTimeout {
		t.Error("deadline")
	}
	if ErrorKind(errors.New("x")) != KindBadResponse {
		t.Error("other")
	}
	if msg := (&Error{Kind: KindOff, Message: "m", Err: errors.New("e")}).Error(); msg != "jev off: m: e" {
		t.Errorf("message %q", msg)
	}
}

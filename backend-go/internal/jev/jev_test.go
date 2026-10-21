package jev

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
)

func TestResolve(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		want     string
		reason   string
		endpoint string
		model    string
		cred     string
		misconf  bool
		warning  string
	}{
		{name: "demo forces mock", cfg: Config{Backend: BackendOpenRouterDecisions, DemoMode: true, OpenRouterKey: "k"}, want: BackendMock, reason: "demo mode", model: DefaultMockModel},
		{name: "explicit mock", cfg: Config{Backend: BackendMock}, want: BackendMock, reason: "=mock", model: DefaultMockModel},
		{name: "explicit off", cfg: Config{Backend: BackendOff, OpenRouterKey: "k"}, want: BackendOff, reason: "=off"},
		{name: "auto without keys", cfg: Config{}, want: BackendOff, reason: "no decision-model credentials"},
		{name: "auto openrouter", cfg: Config{Backend: BackendAuto, OpenRouterKey: "k"}, want: BackendOpenRouterDecisions,
			reason: "auto: OPENROUTER_API_KEY", endpoint: "https://openrouter.ai/api/alpha/decisions", model: DefaultOpenRouterModel, cred: "env"},
		{name: "auto openrouter beats typesafe", cfg: Config{OpenRouterKey: "k", TypeSafeKey: "t"}, want: BackendOpenRouterDecisions, reason: "OPENROUTER_API_KEY"},
		{name: "auto typesafe", cfg: Config{TypeSafeKey: "k"}, want: BackendTypeSafe,
			reason: "TYPESAFE_API_KEY", endpoint: "https://api.typesafe.ai/v1/systemone", model: DefaultTypeSafeModel},
		{name: "auto compatible beats openrouter", cfg: Config{BaseURL: "https://ai-gateway.vercel.sh/typesafe", JevAPIKey: "k", OpenRouterKey: "k", Model: "typesafe-ai/jev"},
			want: BackendCompatible, reason: "compatible", endpoint: "https://ai-gateway.vercel.sh/typesafe/v1/systemone", model: "typesafe-ai/jev"},
		{name: "auto ignores JEV_API_KEY without a base URL", cfg: Config{JevAPIKey: "k"}, want: BackendOff, reason: "no decision-model credentials"},
		{name: "auto ignores the base URL for openrouter", cfg: Config{BaseURL: "https://x.example/api", OpenRouterKey: "k"}, want: BackendOpenRouterDecisions,
			endpoint: "https://openrouter.ai/api/alpha/decisions", warning: "TRUTHTABLE_JEV_BASE_URL is ignored"},
		{name: "OpenRouter base URL moves the default endpoint", cfg: Config{OpenRouterKey: "k", OpenRouterBaseURL: "http://127.0.0.1:9/"}, want: BackendOpenRouterDecisions,
			endpoint: "http://127.0.0.1:9/api/alpha/decisions"},
		{name: "explicit systemone", cfg: Config{Backend: BackendOpenRouterSystemOne, OpenRouterKey: "k"}, want: BackendOpenRouterSystemOne,
			reason: "TRUTHTABLE_JEV_BACKEND=openrouter-systemone with the key from OPENROUTER_API_KEY", endpoint: "https://openrouter.ai/api/v1/systemone"},
		{name: "explicit backend prefers JEV_API_KEY", cfg: Config{Backend: BackendOpenRouterDecisions, OpenRouterKey: "k", JevAPIKey: "j"}, want: BackendOpenRouterDecisions,
			reason: "TRUTHTABLE_JEV_API_KEY"},
		{name: "explicit backend honours base URL and path", cfg: Config{Backend: BackendTypeSafe, TypeSafeKey: "k", BaseURL: "https://eu.typesafe.example/", Path: "v2/systemone"},
			want: BackendTypeSafe, endpoint: "https://eu.typesafe.example/v2/systemone"},
		{name: "explicit backend without a key", cfg: Config{Backend: BackendOpenRouterSystemOne}, want: BackendOff,
			reason: "set OPENROUTER_API_KEY or TRUTHTABLE_JEV_API_KEY", misconf: true},
		{name: "compatible needs a model", cfg: Config{Backend: BackendCompatible, JevAPIKey: "k", BaseURL: "https://gw.example"}, want: BackendOff,
			reason: "needs TRUTHTABLE_JEV_MODEL", misconf: true},
		{name: "compatible needs a base URL", cfg: Config{Backend: BackendCompatible, JevAPIKey: "k", Model: "m"}, want: BackendOff,
			reason: "needs TRUTHTABLE_JEV_BASE_URL", misconf: true},
		{name: "http is refused off localhost", cfg: Config{BaseURL: "http://gw.example.com", JevAPIKey: "k", Model: "m"}, want: BackendOff,
			reason: "must use https", misconf: true},
		{name: "http is fine for a compose service", cfg: Config{BaseURL: "http://mock-upstream:8090", JevAPIKey: "k", Model: "m"}, want: BackendCompatible,
			endpoint: "http://mock-upstream:8090/v1/systemone"},
		{name: "credentials in the URL are refused", cfg: Config{BaseURL: "https://user:pw@gw.example", JevAPIKey: "k", Model: "m"}, want: BackendOff,
			reason: "must not carry credentials", misconf: true},
		{name: "latest model warns", cfg: Config{OpenRouterKey: "k", Model: "~typesafe/jev-latest"}, want: BackendOpenRouterDecisions,
			model: "~typesafe/jev-latest", warning: "recalibrate"},
		{name: "openrouter base without /api warns", cfg: Config{Backend: BackendOpenRouterDecisions, OpenRouterKey: "k", BaseURL: "https://openrouter.ai"},
			want: BackendOpenRouterDecisions, endpoint: "https://openrouter.ai/alpha/decisions", warning: "OpenRouter URLs end in /api"},
		{name: "binding", cfg: Config{Binding: &Binding{Backend: BackendOpenRouterDecisions, Name: "openrouter-main", Key: staticKey("k")}},
			want: BackendOpenRouterDecisions, reason: `dashboard credential "openrouter-main"`, cred: "dashboard"},
		{name: "env key beats a binding", cfg: Config{TypeSafeKey: "t", Binding: &Binding{Backend: BackendOpenRouterDecisions, Key: staticKey("k")}},
			want: BackendTypeSafe, cred: "env"},
		{name: "explicit backend uses a matching binding", cfg: Config{Backend: BackendTypeSafe, Binding: &Binding{Backend: BackendTypeSafe, Name: "ts", Key: staticKey("k")}},
			want: BackendTypeSafe, reason: `TRUTHTABLE_JEV_BACKEND=typesafe with the dashboard credential "ts"`, cred: "dashboard"},
		{name: "unknown", cfg: Config{Backend: "bogus-backend"}, want: BackendOff, reason: "unknown TRUTHTABLE_JEV_BACKEND", misconf: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, err := New(c.cfg)
			if err != nil {
				t.Fatal(err)
			}
			info := cl.Info()
			if resolved := Resolve(c.cfg); resolved.Backend != info.Backend || resolved.Reason != info.Reason {
				t.Errorf("Resolve %+v disagrees with New %+v", resolved, info)
			}
			if info.Backend != c.want || !strings.Contains(info.Reason, c.reason) {
				t.Errorf("%+v, want backend %s with reason containing %q", info, c.want, c.reason)
			}
			if c.endpoint != "" && info.Endpoint != c.endpoint {
				t.Errorf("endpoint %q, want %q", info.Endpoint, c.endpoint)
			}
			if c.model != "" && info.Model != c.model {
				t.Errorf("model %q, want %q", info.Model, c.model)
			}
			if c.cred != "" && info.CredentialSource != c.cred {
				t.Errorf("credential source %q, want %q", info.CredentialSource, c.cred)
			}
			if info.Misconfigured != c.misconf {
				t.Errorf("misconfigured %v, want %v", info.Misconfigured, c.misconf)
			}
			if c.warning != "" && !strings.Contains(strings.Join(info.Warnings, "\n"), c.warning) {
				t.Errorf("warnings %q, want one containing %q", info.Warnings, c.warning)
			}
			if st, ok := cl.(StatusReporter); !ok {
				t.Error("every backend reports its status")
			} else if st.Status().Backend != info.Backend {
				t.Errorf("status backend %q", st.Status().Backend)
			}
		})
	}
}

func staticKey(k string) KeyFunc {
	return func(context.Context, string) (string, error) { return k, nil }
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
		t.Fatalf("tokens %v cost %v", resp.InputTokens, resp.CostUSD)
	}
	// The simulated cost is $0.042 per million input tokens, without float noise in its JSON.
	want := strconv.Itoa(*resp.InputTokens*42) + "e-9"
	if got, _ := json.Marshal(*resp.CostUSD); !sameNumber(t, string(got), want) || len(got) > 12 {
		t.Errorf("cost %s for %d tokens, want %s", got, *resp.InputTokens, want)
	}
	// Answers are copies: changing one does not change the fixtures.
	*resp.Answers["groundedness__v1"].Noul = 0
	again, _ := cl.Decide(context.Background(), s0Request(), CallOptions{Scenario: "refund-contradiction-flag"})
	if *again.Answers["groundedness__v1"].Noul != 0.96 {
		t.Error("the fixture was modified through a returned answer")
	}
}

// TestMockFallbackAnswers: the re-gate of a scenario's scripted fallback answer (its state's
// assistant_response is fallback.response) is answered from fallback.mock_answers, any other
// state of the scenario from mock_answers.
func TestMockFallbackAnswers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	pack := `{"scenarios":[{"id":"fb","mock_answers":{"groundedness__v1":{"type":"noul","noul":0.97}},
		"fallback":{"response":"Refunds take 5 to 10 business days.","mock_answers":{"groundedness__v1":{"type":"noul","noul":0.04}}}}]}`
	if err := os.WriteFile(path, []byte(pack), 0o600); err != nil {
		t.Fatal(err)
	}
	cl := mockClientFor(t, MockConfig{FixturesPath: path, Strict: true})
	q := map[string]any{"groundedness__v1": s0Questions["groundedness__v1"]}
	for text, want := range map[string]float64{"Refunds take 2 business days.": 0.97, "Refunds take 5 to 10 business days.": 0.04} {
		req := &Request{Model: DefaultMockModel, State: map[string]any{"assistant_response": text}, Questions: q}
		resp, err := cl.Decide(context.Background(), req, CallOptions{Scenario: "fb"})
		if err != nil || *resp.Answers["groundedness__v1"].Noul != want {
			t.Errorf("%q: resp %+v err %v, want %v", text, resp, err, want)
		}
	}

	// Every fallback in the contract pack scripts its re-gate.
	cl = mockClientFor(t, MockConfig{Mode: MockScenario, Strict: true})
	m := cl.(*mockClient)
	if len(m.fallbacks) == 0 {
		t.Fatal("the contract pack has no fallback scenario")
	}
	for id, fb := range m.fallbacks {
		req := &Request{Model: DefaultMockModel, State: map[string]any{"assistant_response": fb.Response}, Questions: s0Questions}
		resp, err := cl.Decide(context.Background(), req, CallOptions{Scenario: id})
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		for qid, a := range fb.MockAnswers {
			if got := resp.Answers[qid]; got == nil || got.Noul == nil || *got.Noul != *a.Noul {
				t.Errorf("%s: %s answered %+v, want the fallback answer %+v", id, qid, got, a)
			}
		}
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
	if msg := (&Error{Kind: KindAuth, Status: 401, Message: "m"}).Error(); msg != "jev auth (HTTP 401): m" {
		t.Errorf("message %q", msg)
	}
}

func TestNewRequest(t *testing.T) {
	c := &decision.Request{Model: "typesafe/jev-1.13", State: map[string]any{"a": "b"}, Questions: s0Questions}
	r := NewRequest(c, "0192f7c2-trace")
	if r.Model != c.Model || r.SessionID != "0192f7c2-trace" || r.State["a"] != "b" || len(r.Questions) != 2 {
		t.Errorf("request %+v", r)
	}
	// session_id is transport only: the request hash covers {model, state, questions}.
	h1, _ := decision.SHA256Hex(*c)
	h2, _ := decision.SHA256Hex(decision.Request{Model: r.Model, State: r.State, Questions: r.Questions})
	if h1 != h2 {
		t.Error("request hash changed")
	}
}

func TestSkipReason(t *testing.T) {
	for kind, want := range map[string]string{
		KindBreakerOpen: "breaker_open", KindRateLimited: "rate_limited", KindOverload: "overload", KindBudget: "budget",
		KindOff: "decision_model_off", KindCredentialsUnavailable: "credentials_unavailable",
	} {
		if got, ok := SkipReason(&Error{Kind: kind}); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", kind, got, ok, want)
		}
	}
	for _, kind := range []string{KindAuth, KindTimeout, KindRetryable, KindBadContentType} {
		if got, ok := SkipReason(&Error{Kind: kind}); ok {
			t.Errorf("%s is a request error, not a skip (%q)", kind, got)
		}
	}
}

func TestMockStatusAndCostEvents(t *testing.T) {
	var events []CostEvent
	cl, err := New(Config{Backend: BackendMock, Mock: MockConfig{Mode: MockHash}, Costs: CostFunc(func(e CostEvent) { events = append(events, e) })})
	if err != nil {
		t.Fatal(err)
	}
	if st := cl.(StatusReporter).Status(); st.State != "mock" || st.Snapshot != DefaultMockModel || st.Breaker != BreakerClosed {
		t.Errorf("status %+v", st)
	}
	if _, err := cl.Decide(context.Background(), s0Request(), CallOptions{TraceID: "t1", Route: "r", Component: ComponentProbe}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events %+v", events)
	}
	e := events[0]
	if e.TraceID != "t1" || e.Route != "r" || e.Component != ComponentProbe || e.Provider != BackendMock || e.CostSource != CostSimulated ||
		e.Status != AttemptOK || e.CostUSD <= 0 || e.InputTokens == nil || e.Questions != 2 || e.Kind != CostKindDecision {
		t.Errorf("event %+v", e)
	}
	if st := cl.(StatusReporter).Status(); st.Calls != 1 || st.Attempts != 1 || st.CostUSD != e.CostUSD {
		t.Errorf("status counters %+v", st)
	}
}

// sameNumber reports whether two JSON numbers denote the same float64.
func sameNumber(t *testing.T, a, b string) bool {
	t.Helper()
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil {
		t.Fatalf("not numbers: %q %q", a, b)
	}
	return x == y
}

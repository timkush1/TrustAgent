package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/contracts"
)

// newFaultEnv is the e2e stack in process (docker-compose.e2e.yml): the compatible decision
// backend and the LLM upstreams on mock-upstream, trace writes inline, so the stored rows can be
// read as soon as the response returns.
func newFaultEnv(t *testing.T, tunes ...func(*config.Config)) (*testEnv, *mockupstream.Server) {
	t.Helper()
	return newFaultEnvWith(t, judgedEngine(t), tunes...)
}

// newFaultEnvWith is newFaultEnv with eng as the audit engine (nil: none configured), adjusted by
// tunes after the e2e settings.
func newFaultEnvWith(t *testing.T, eng *enginetest.Engine, tunes ...func(*config.Config)) (*testEnv, *mockupstream.Server) {
	t.Helper()
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(mu)
	t.Cleanup(upstream.Close)
	env := newScenarioEnvWith(t, func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.BaseURL, cfg.Jev.Path = jev.BackendCompatible, upstream.URL, "/v1/systemone"
		cfg.Jev.APIKey, cfg.Jev.Model = "e2e-mock-upstream-key", "typesafe/jev-1.13"
		cfg.UpstreamURL, cfg.AnthropicUpstreamURL = upstream.URL, upstream.URL
		for _, tune := range tunes {
			tune(cfg)
		}
	}, eng)
	return env, mu
}

// sendWithScenarioHeader sends demo scenario s with its scripted answer and the
// X-TrustAgent-Scenario value header, and returns the response headers.
func sendWithScenarioHeader(t *testing.T, env *testEnv, s *demoScenario, header string) http.Header {
	t.Helper()
	code, h, body := postScenario(t, env, s, header)
	if code != http.StatusOK {
		t.Fatalf("%s: HTTP %d: %s", s.ID, code, body)
	}
	return h
}

// postScenario sends demo scenario s with its scripted answer (test_response, so no upstream is
// called) and the X-TrustAgent-Scenario value header, and returns the status, headers and body.
func postScenario(t *testing.T, env *testEnv, s *demoScenario, header string) (int, http.Header, []byte) {
	t.Helper()
	var req map[string]json.RawMessage
	if err := json.Unmarshal(s.Request, &req); err != nil {
		t.Fatal(err)
	}
	req["test_response"], _ = json.Marshal(s.Response)
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest(http.MethodPost, env.http.URL+"/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-TrustAgent-Key", testKey)
	r.Header.Set("X-TrustAgent-Scenario", header)
	for k, v := range s.Headers {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header, b
}

// storedTraceRow is the trace row the memory store holds for id.
func storedTraceRow(t *testing.T, env *testEnv, id string) store.TraceRecord {
	t.Helper()
	m, ok := env.store.(*memStore)
	if !ok {
		t.Fatalf("the store is %T, want the memory store (trace writes inline)", env.store)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.traces[id]
	if row == nil {
		t.Fatalf("trace %s is not stored", id)
	}
	return *row
}

// attemptZeroModel is the decision_model of a trace's attempt-0 decision.
func attemptZeroModel(t *testing.T, d store.TraceDetail) struct {
	Attempts int  `json:"attempts"`
	Hedged   bool `json:"hedged"`
} {
	t.Helper()
	var out struct {
		Attempts int  `json:"attempts"`
		Hedged   bool `json:"hedged"`
	}
	for _, raw := range d.Decisions {
		var dec struct {
			Attempt       int             `json:"attempt"`
			DecisionModel json.RawMessage `json:"decision_model"`
		}
		if err := json.Unmarshal(raw, &dec); err != nil {
			t.Fatal(err)
		}
		if dec.Attempt == 0 {
			if err := json.Unmarshal(dec.DecisionModel, &out); err != nil {
				t.Fatalf("decision_model %s: %v", dec.DecisionModel, err)
			}
			return out
		}
	}
	t.Fatalf("trace %s has no attempt-0 decision", d.TraceID)
	return out
}

// TestFaultChannelReachesMockUpstream is the forward-only fault value end to end (D119), as the
// load test's scenario f sends it: X-TrustAgent-Scenario fault:<spec> reaches mock-upstream's
// decision endpoint on every attempt of that request (with X-Mock-Key, the trace id, shared by an
// attempt and its retry or hedge), so the counted spec faults exactly the first attempt: 503@1 is
// retried, and hang@1 on a hedging route is answered by the hedge. The trace stores no scenario.
func TestFaultChannelReachesMockUpstream(t *testing.T) {
	scenarios := loadDemoScenarios(t)
	for _, c := range []struct {
		name, scenario, fault string
		attempts              int
		hedged                bool
	}{
		{"503 retried", "grounded-pass", "fault:503@1", 2, false},
		{"hang hedged", "payments-fallback", "fault:hang@1", 2, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, mu := newFaultEnv(t)
			h := sendWithScenarioHeader(t, env, scenarios[c.scenario], c.fault)
			traceID := h.Get("X-TrustAgent-Trace-Id")
			if got := h.Get("X-TrustAgent-Status"); got != "ok" {
				t.Errorf("status %q, want ok once the %s attempt recovers", got, c.fault)
			}
			faulted := 0
			for _, r := range mu.Requests() {
				// Keep-warm requests name no scenario and no call.
				if r.Path != "/v1/systemone" || r.Method != http.MethodPost ||
					r.Header.Get("X-TrustAgent-Scenario") == "" && r.Header.Get("X-Mock-Key") == "" {
					continue
				}
				if got := r.Header.Get("X-TrustAgent-Scenario"); got != c.fault {
					t.Errorf("a decision request names %q, want %q", got, c.fault)
					continue
				}
				faulted++
				if got := r.Header.Get("X-Mock-Key"); got != traceID {
					t.Errorf("X-Mock-Key %q, want the trace id %s", got, traceID)
				}
			}
			if faulted < c.attempts {
				t.Errorf("%d decision requests carried the fault value, want at least %d", faulted, c.attempts)
			}
			d := env.settledTrace(traceID)
			if dm := attemptZeroModel(t, d); dm.Attempts != c.attempts || dm.Hedged != c.hedged {
				t.Errorf("decision_model attempts %d hedged %v, want %d and %v", dm.Attempts, dm.Hedged, c.attempts, c.hedged)
			}
			if row := storedTraceRow(t, env, traceID); row.Scenario != nil {
				t.Errorf("the trace stored scenario %q: a fault value is forward-only", *row.Scenario)
			}
		})
	}
}

// loadFaultScenarios reads the vendor-fault pack (contracts/demo/faults.v1.json, D119).
func loadFaultScenarios(t testing.TB) map[string]*demoScenario {
	t.Helper()
	out := map[string]*demoScenario{}
	for _, s := range faultPack(t) {
		out[s.ID] = s.demoScenario
	}
	return out
}

// faultScenario is a faults-pack scenario: a demo scenario with the vendor faults mock-upstream
// answers it with.
type faultScenario struct {
	*demoScenario
	MockFaults struct {
		Decision string `json:"decision"`
		Upstream string `json:"upstream"`
	}
}

// faultPack is the vendor-fault pack's scenarios in file order.
func faultPack(t testing.TB) []faultScenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/faults.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []json.RawMessage `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	out := make([]faultScenario, len(pack.Scenarios))
	for i, raw := range pack.Scenarios {
		out[i].demoScenario = &demoScenario{}
		var faults struct {
			MockFaults json.RawMessage `json:"mock_faults"`
		}
		if err := json.Unmarshal(raw, out[i].demoScenario); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &faults); err != nil || json.Unmarshal(faults.MockFaults, &out[i].MockFaults) != nil {
			t.Fatalf("%s: mock_faults %s: %v", out[i].ID, faults.MockFaults, err)
		}
	}
	return out
}

// TestFaultScenariosAgainstMockUpstream is make e2e-faults in process: the e2e stack (decision
// backend and LLM upstream on mock-upstream) gets the whole vendor-fault pack once, in file order,
// through trafficgen --expect --pack faults --upstream all, so each row's status, action, degraded
// and decision_model are checked on the response and the settled trace (D119).
//
// The pack's order is part of what it proves. The client's breaker opens after 5 consecutive
// vendor faults, and the pack never sends more than 4 decision-model faults in a row; the 402 of F8
// reads as an account out of credits on the TypeSafe-shaped endpoint (no limit_source), which opens
// the breaker for the auth cooldown, so F8 is the last scenario. The run ends with that cooldown
// open (status no_credits), and an answer sent after it is skipped without a decision request.
func TestFaultScenariosAgainstMockUpstream(t *testing.T) {
	pack := faultPack(t)
	if len(pack) != 12 {
		t.Fatalf("the faults pack holds %d scenarios, want F1-F10 and U1-U2", len(pack))
	}
	if last := pack[len(pack)-1]; last.ID != "fault-402-credits" {
		t.Fatalf("the last scenario is %s: F8 (fault-402-credits) opens the auth cooldown, so it comes last", last.ID)
	}
	env, mu := newFaultEnv(t)
	cmd := exec.Command(buildTrafficgen(t), "--once", "--expect", "--pack", "faults", "--scenarios", "faults", "--upstream", "all",
		"--base-url", env.http.URL, "--audit-timeout", "30s")
	cmd.Env = append(os.Environ(), "TRAFFICGEN_API_KEY="+testKey)
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil {
		t.Fatalf("trafficgen --pack faults: %v\n%s", err, out)
	}
	t.Logf("trafficgen --pack faults:\n%s", out)
	lines := okLine.FindAllStringSubmatch(out, -1)
	if len(lines) != len(pack) {
		t.Fatalf("%d scenarios passed, want all %d in order:\n%s", len(lines), len(pack), out)
	}
	for i, s := range pack {
		if lines[i][1] != s.ID {
			t.Errorf("line %d is %s, want %s (file order)", i, lines[i][1], s.ID)
		}
	}

	// What reached mock-upstream: each decision fault on the requests that name its scenario (the
	// forwarded id, with X-Mock-Key the trace id), and no decision request for the two upstream
	// faults, whose answers were never gated.
	decisions := map[string]int{}
	for _, r := range mu.Requests() {
		if r.Path == "/v1/systemone" && r.Method == http.MethodPost {
			if id := r.Header.Get("X-TrustAgent-Scenario"); id != "" {
				decisions[id]++
			}
		}
	}
	for _, s := range pack {
		switch {
		case s.MockFaults.Decision != "" && decisions[s.ID] == 0:
			t.Errorf("%s: no decision request named the scenario, so mock_faults.decision %s never applied", s.ID, s.MockFaults.Decision)
		case s.MockFaults.Upstream != "" && decisions[s.ID] != 0:
			t.Errorf("%s: %d decision requests for an answer the upstream fault leaves ungated", s.ID, decisions[s.ID])
		}
	}

	// F8 left the auth cooldown open: the status says so, and the next answer is skipped with no
	// decision request (which is why F8 comes last).
	code, body := env.get("/api/status", testKey)
	var st struct {
		DecisionModel struct {
			State   string `json:"state"`
			Breaker string `json:"breaker"`
		} `json:"decision_model"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &st) != nil {
		t.Fatalf("GET /api/status: %d %s", code, body)
	}
	if st.DecisionModel.State != "no_credits" || st.DecisionModel.Breaker != "open" {
		t.Errorf("after F8 the decision model is %s with the breaker %s, want no_credits and open", st.DecisionModel.State, st.DecisionModel.Breaker)
	}
	before := len(mu.Requests())
	h := sendWithScenarioHeader(t, env, loadDemoScenarios(t)["grounded-pass"], "grounded-pass")
	if got := h.Get("X-TrustAgent-Status"); got != "skipped:breaker_open" {
		t.Errorf("an answer after F8 has status %q, want skipped:breaker_open", got)
	}
	for _, r := range mu.Requests()[before:] {
		if r.Path == "/v1/systemone" {
			t.Errorf("a decision request reached mock-upstream while the auth cooldown is open: %+v", r)
		}
	}
}

// The stateful vendor faults (D119 S1-S6) run in process only: each needs the client's state
// (breaker, cooldowns, budget, rate limit) or the engine's, which a pack run on a shared stack
// cannot set up or reset. Each test starts a fresh e2e stack (newFaultEnv).

// groundedSupport and groundedPayments are a grounded answer on support-bot (flag) and on
// payments-faq (fallback, fail closed), each answered from its scenario's mock_answers when the
// decision backend is asked (a faults-pack scenario with only an upstream fault, sent with
// test_response, meets no fault).
func groundedSupport(t *testing.T) *demoScenario { return loadDemoScenarios(t)["grounded-pass"] }

func groundedPayments(t *testing.T) *demoScenario {
	return loadFaultScenarios(t)["upstream-html-fail-closed"]
}

// paymentsBlock is payments-faq's block message (config/policies.yaml).
const paymentsBlock = "I can't confirm that from our policy documents. A person will follow up."

// decisionCalls counts the decision requests mock-upstream received for gated answers (they
// carry X-Mock-Key, the trace id; keep-warm and probe requests do not).
func decisionCalls(mu *mockupstream.Server) int {
	n := 0
	for _, r := range mu.Requests() {
		if r.Path == "/v1/systemone" && r.Method == http.MethodPost && r.Header.Get("X-Mock-Key") != "" {
			n++
		}
	}
	return n
}

// gate is what one answer's response says about its gate.
type gateOutcome struct {
	code                     int
	status, action, degraded string
	audit                    string
	body                     string
	traceID                  string
}

func (g gateOutcome) String() string {
	return fmt.Sprintf("HTTP %d status %q action %q degraded %q audit %q", g.code, g.status, g.action, g.degraded, g.audit)
}

// gateOf sends s (with its scripted answer) under the scenario header value and reads its gate.
func gateOf(t *testing.T, env *testEnv, s *demoScenario, header string) gateOutcome {
	t.Helper()
	code, h, body := postScenario(t, env, s, header)
	return gateOutcome{code: code, status: h.Get("X-TrustAgent-Status"), action: h.Get("X-TrustAgent-Action"),
		degraded: h.Get("X-TrustAgent-Degraded"), audit: h.Get("X-TrustAgent-Audit"), body: string(body),
		traceID: h.Get("X-TrustAgent-Trace-Id")}
}

// expectGate checks a gate outcome: status, action and degraded ("" = absent).
func expectGate(t *testing.T, step string, g gateOutcome, status, action, degraded string) {
	t.Helper()
	if g.code != http.StatusOK || g.status != status || g.action != action || g.degraded != degraded {
		t.Errorf("%s: %s, want HTTP 200 status %q action %q degraded %q", step, g, status, action, degraded)
	}
	if action == "block" && !strings.Contains(g.body, paymentsBlock) {
		t.Errorf("%s: a fail-closed block delivers the route's block message: %s", step, g.body)
	}
}

// decisionState is GET /api/status decision_model state and breaker.
func decisionState(t *testing.T, env *testEnv) (state, breaker string) {
	t.Helper()
	st := env.statusOf(testKey)
	return st.DecisionModel.State, st.DecisionModel.Breaker
}

// statusStream opens the WS v2 stream and reads past its hello.
func faultStream(t *testing.T, env *testEnv) *gws.Conn {
	t.Helper()
	conn, _, err := dialWS(t, env, "protocol=2", http.Header{"X-TrustAgent-Key": {testKey}})
	if err != nil {
		t.Fatal(err)
	}
	if f := readFrame(t, conn); f.Type != "hello" {
		t.Fatalf("first frame %s", f.raw)
	}
	return conn
}

// nextStatus has the server broadcast the component states that changed (the status watch's
// tick, run now instead of within 5 s) and returns the next status event of component.
func nextStatus(t *testing.T, env *testEnv, conn *gws.Conn, component string) string {
	t.Helper()
	env.srv.watchStatus()
	for {
		f := readFrame(t, conn)
		if f.Type != "status" {
			continue // trace lifecycle events and heartbeats
		}
		var d struct{ Component, State string }
		if err := json.Unmarshal(f.Data, &d); err != nil {
			t.Fatal(err)
		}
		if d.Component == component {
			return d.State
		}
	}
}

// breakerGauge is trustagent_gateway_jev_breaker_state for the compatible backend (0 closed,
// 1 half-open, 2 open).
func breakerGauge(t *testing.T) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "trustagent_gateway_jev_breaker_state" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "backend" && l.GetValue() == jev.BackendCompatible {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	t.Fatal("no breaker gauge for the compatible backend")
	return -1
}

// TestBreakerOpensSkipsAndRecovers (S1): five consecutive vendor faults open the breaker; while it
// is open an answer is skipped without a decision request; after the cooldown one trial call is
// let through, and its success closes the breaker. /api/status, the WS status events and the
// breaker gauge follow each step.
func TestBreakerOpensSkipsAndRecovers(t *testing.T) {
	const cooldown = 400 * time.Millisecond
	env, mu := newFaultEnv(t, func(cfg *config.Config) { cfg.Jev.BreakerFailures, cfg.Jev.BreakerCooldown = 5, cooldown })
	conn := faultStream(t, env)
	s := groundedSupport(t)

	// bad_json is a vendor fault that is not retried: one fault per answer.
	for i := range 5 {
		expectGate(t, fmt.Sprintf("fault %d", i+1), gateOf(t, env, s, "fault:bad_json"), "error:bad_response", "pass", "error")
	}
	if state, breaker := decisionState(t, env); state != "breaker_open" || breaker != "open" {
		t.Errorf("after 5 faults: state %s breaker %s, want breaker_open and open", state, breaker)
	}
	if got := nextStatus(t, env, conn, "decision_model"); got != "breaker_open" {
		t.Errorf("WS decision_model status %s, want breaker_open", got)
	}
	if g := breakerGauge(t); g != 2 {
		t.Errorf("breaker gauge %v, want 2 (open)", g)
	}

	before := decisionCalls(mu)
	expectGate(t, "while open", gateOf(t, env, s, s.ID), "skipped:breaker_open", "pass", "breaker")
	if n := decisionCalls(mu) - before; n != 0 {
		t.Errorf("%d decision requests while the breaker is open", n)
	}

	time.Sleep(cooldown + 100*time.Millisecond)
	if _, breaker := decisionState(t, env); breaker != "half_open" {
		t.Errorf("after the cooldown the breaker is %s, want half_open", breaker)
	}
	expectGate(t, "the half-open trial", gateOf(t, env, s, s.ID), "ok", "pass", "")
	if n := decisionCalls(mu) - before; n != 1 {
		t.Errorf("%d decision requests for the trial, want 1", n)
	}
	if state, breaker := decisionState(t, env); state != "ready" || breaker != "closed" {
		t.Errorf("after the trial: state %s breaker %s, want ready and closed", state, breaker)
	}
	if got := nextStatus(t, env, conn, "decision_model"); got != "ready" {
		t.Errorf("WS decision_model status %s, want ready", got)
	}
	if g := breakerGauge(t); g != 0 {
		t.Errorf("breaker gauge %v, want 0 (closed)", g)
	}
}

// TestAuthCreditsAndHTMLCooldowns (S2): a vendor that refuses the key (401) or finds no credits
// (402) opens the breaker for the auth cooldown, and the status names the cause; an admin's
// Settings → Test lifts that pause at once, and its probe closes the breaker. A 200 text/html
// page (a wrong base URL) trips the breaker for the fault cooldown, which the test does not lift
// (a misconfigured endpoint stays paused) until the cooldown lets a trial through.
func TestAuthCreditsAndHTMLCooldowns(t *testing.T) {
	const admin = "admin-faults-secret"
	for _, c := range []struct {
		fault, status, state string
		liftedByTest         bool
	}{
		{"402_credits", "error:credits", "no_credits", true},
		{"401", "error:auth", "unauthorized", true},
		{"html200", "error:bad_content_type", "breaker_open", false},
	} {
		t.Run(c.fault, func(t *testing.T) {
			const cooldown = 300 * time.Millisecond
			env, mu := newFaultEnv(t, func(cfg *config.Config) {
				cfg.Jev.BreakerCooldown, cfg.Jev.AuthCooldown = cooldown, 10*time.Minute
				cfg.Auth.AdminKeys = []config.APIKey{{Label: "admin", Key: admin}}
			})
			s := groundedSupport(t)
			expectGate(t, "the fault", gateOf(t, env, s, "fault:"+c.fault), c.status, "pass", "error")
			if state, breaker := decisionState(t, env); state != c.state || breaker != "open" {
				t.Errorf("after %s: state %s breaker %s, want %s and open", c.fault, state, breaker, c.state)
			}
			before := decisionCalls(mu)
			expectGate(t, "paused", gateOf(t, env, s, s.ID), "skipped:breaker_open", "pass", "breaker")
			expectGate(t, "paused, fail closed", gateOf(t, env, groundedPayments(t), "upstream-html-fail-closed"),
				"skipped:breaker_open", "block", "breaker")
			if n := decisionCalls(mu) - before; n != 0 {
				t.Errorf("%d decision requests during the cooldown", n)
			}

			code, body := env.do(http.MethodPost, "/api/decision-model/test", admin, nil)
			var res struct {
				OK    bool `json:"ok"`
				Error *struct {
					Kind string `json:"kind"`
				} `json:"error"`
			}
			if code != http.StatusOK || json.Unmarshal(body, &res) != nil {
				t.Fatalf("POST /api/decision-model/test: %d %s", code, body)
			}
			if c.liftedByTest {
				if !res.OK {
					t.Errorf("the test after %s did not lift the pause: %s", c.fault, body)
				}
				if state, breaker := decisionState(t, env); state != "ready" || breaker != "closed" {
					t.Errorf("after the test: state %s breaker %s, want ready and closed", state, breaker)
				}
			} else {
				if res.OK || res.Error == nil || res.Error.Kind != jev.KindBreakerOpen {
					t.Errorf("the test lifted a breaker vendor faults opened: %s", body)
				}
				time.Sleep(cooldown + 100*time.Millisecond)
			}
			expectGate(t, "recovered", gateOf(t, env, s, s.ID), "ok", "pass", "")
			if state, breaker := decisionState(t, env); state != "ready" || breaker != "closed" {
				t.Errorf("recovered: state %s breaker %s, want ready and closed", state, breaker)
			}
		})
	}
}

// ledgerOf is the env's cost ledger (the decision budget and the LLM cap).
func ledgerOf(t *testing.T, env *testEnv) *costs.Ledger {
	t.Helper()
	l, ok := env.srv.costs.decision.(*costs.Ledger)
	if !ok {
		t.Fatalf("the cost ledger is %T", env.srv.costs.decision)
	}
	return l
}

// spendDecision books usd of enforcing decision spend today, as answered calls would.
func spendDecision(t *testing.T, l *costs.Ledger, usd float64) {
	t.Helper()
	r, err := l.Reserve(jev.SpendRequest{USD: usd, Enforcing: true})
	if err != nil {
		t.Fatalf("spending %v: %v", usd, err)
	}
	r.Settle(usd)
}

// TestBudgetSkipsAndBlocks (S3): the daily decision cap with an enforcing reserve (D82). Past the
// observe line only enforcing routes are still checked (reserve_only): a flag route is skipped
// (budget) and passes, a fail-closed fallback route is still checked. At the cap every check is
// skipped and the fail-closed route blocks. /api/status and the WS budget events follow, and a
// skipped check sends no decision request.
func TestBudgetSkipsAndBlocks(t *testing.T) {
	env, mu := newFaultEnv(t, func(cfg *config.Config) {
		cfg.SpendCapDecisionUSDDay, cfg.Jev.SpendEnforcingReserve = 1, 0.25
	})
	l := ledgerOf(t, env)
	conn := faultStream(t, env)
	support, payments := groundedSupport(t), groundedPayments(t)

	expectGate(t, "fresh", gateOf(t, env, support, support.ID), "ok", "pass", "")
	if b := env.statusOf(testKey).Budgets; b.DecisionReserveOnly || b.DecisionExhausted {
		t.Errorf("fresh budgets %+v", b)
	}

	spendDecision(t, l, 0.8) // past the observe line (0.75)
	if b := env.statusOf(testKey).Budgets; !b.DecisionReserveOnly || b.DecisionExhausted || b.DecisionUSDDay != 1 {
		t.Errorf("past the line: budgets %+v, want reserve_only", b)
	}
	if got := nextStatus(t, env, conn, "budget"); got != "reserve_only" {
		t.Errorf("WS budget %s, want reserve_only", got)
	}
	before := decisionCalls(mu)
	expectGate(t, "reserve only, flag route", gateOf(t, env, support, support.ID), "skipped:budget", "pass", "budget")
	if n := decisionCalls(mu) - before; n != 0 {
		t.Errorf("%d decision requests for a check the budget skipped", n)
	}
	expectGate(t, "reserve only, enforcing route", gateOf(t, env, payments, "upstream-html-fail-closed"), "ok", "pass", "")
	if n := decisionCalls(mu) - before; n != 1 {
		t.Errorf("%d decision requests for the enforcing check, want 1", n)
	}

	sp := l.Spend()
	spendDecision(t, l, sp.CapUSD-sp.SpentUSD-sp.ReservedUSD)
	st := env.statusOf(testKey)
	if !st.Budgets.DecisionExhausted || st.DecisionModel.State != "budget_exhausted" {
		t.Errorf("at the cap: budgets %+v, decision model %s, want exhausted", st.Budgets, st.DecisionModel.State)
	}
	if got := nextStatus(t, env, conn, "budget"); got != "exhausted" {
		t.Errorf("WS budget %s, want exhausted", got)
	}
	before = decisionCalls(mu)
	expectGate(t, "exhausted, flag route", gateOf(t, env, support, support.ID), "skipped:budget", "pass", "budget")
	expectGate(t, "exhausted, fail closed", gateOf(t, env, payments, "upstream-html-fail-closed"), "skipped:budget", "block", "budget")
	if n := decisionCalls(mu) - before; n != 0 {
		t.Errorf("%d decision requests past the cap", n)
	}
}

// TestLLMCapPausesAudits (S3): once the day's LLM cap is spent, deep audits are dropped
// (llm_budget) instead of queued, while the fast check goes on; the status says so.
func TestLLMCapPausesAudits(t *testing.T) {
	env, _ := newFaultEnv(t, func(cfg *config.Config) { cfg.SpendCapLLMUSDDay = 0.5 })
	s := groundedSupport(t)
	g := gateOf(t, env, s, s.ID)
	if g.status != "ok" || g.audit != "queued" {
		t.Fatalf("under the cap: %s, want ok and a queued audit", g)
	}
	// The audit settles its hold at the mock judge's $0; then the day's LLM spend reaches the cap.
	if d := env.settledTrace(g.traceID); d.Audit.Status != store.AuditCompleted {
		t.Fatalf("the audit under the cap is %s, want completed", d.Audit.Status)
	}
	settle, err := ledgerOf(t, env).LLM().Reserve(0.5, "deep_audit")
	if err != nil {
		t.Fatal(err)
	}
	settle(0.5)
	st := env.statusOf(testKey)
	if !st.Budgets.LLMExhausted {
		t.Errorf("budgets %+v, want the LLM cap exhausted", st.Budgets)
	}
	g = gateOf(t, env, s, s.ID)
	if g.status != "ok" || g.action != "pass" || g.audit != "dropped" {
		t.Errorf("at the LLM cap: %s, want the check ok and the audit dropped", g)
	}
	if d := env.settledTrace(g.traceID); d.Audit.Status != store.TraceAuditDropped {
		t.Errorf("the trace's audit is %s, want dropped", d.Audit.Status)
	}
}

// TestRateLimitSkips (S4): the gateway's own limit on decision calls (TRUTHTABLE_GATE_JEV_RPM;
// 6 a minute is a burst of one) skips a check it refuses, with no decision request: a flag route
// passes, a fail-closed route blocks. The status shows the limit and its use. No startup probe or
// keep-warm call takes the one token first.
func TestRateLimitSkips(t *testing.T) {
	env, mu := newFaultEnv(t, func(cfg *config.Config) {
		cfg.Jev.RPM, cfg.Jev.StartupProbe, cfg.Jev.KeepAlive = 6, false, 0
	})
	support := groundedSupport(t)
	limited := func() float64 {
		families, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatal(err)
		}
		sum := 0.0
		for _, f := range families {
			if f.GetName() == "trustagent_gateway_jev_rate_limited_total" {
				for _, m := range f.GetMetric() {
					sum += m.GetCounter().GetValue()
				}
			}
		}
		return sum
	}
	before := limited()
	expectGate(t, "the burst", gateOf(t, env, support, support.ID), "ok", "pass", "")
	calls := decisionCalls(mu)
	expectGate(t, "past the limit", gateOf(t, env, support, support.ID), "skipped:rate_limited", "pass", "rate_limited")
	expectGate(t, "past the limit, fail closed", gateOf(t, env, groundedPayments(t), "upstream-html-fail-closed"),
		"skipped:rate_limited", "block", "rate_limited")
	if n := decisionCalls(mu) - calls; n != 0 {
		t.Errorf("%d decision requests past the limit", n)
	}
	if got := limited() - before; got != 2 {
		t.Errorf("%v rate-limited calls counted, want 2", got)
	}
	if dm := env.statusOf(testKey).DecisionModel; dm.RPMLimit != 6 || dm.RPMUsed != 1 {
		t.Errorf("status rpm %d of %d, want 1 of 6", dm.RPMUsed, dm.RPMLimit)
	}
}

// TestEngineDownLeavesTheDecision (S5): the deep audit is asynchronous, so an engine that is down
// changes nothing the client gets: the audit is queued and then fails (worker.Pool records the
// engine's Unavailable as a failed audit). With no engine configured at all
// (TRUTHTABLE_GRPC_ADDRESS empty, the load stack) the deep audit is disabled instead.
func TestEngineDownLeavesTheDecision(t *testing.T) {
	down := enginetest.Start(t, &enginetest.Engine{
		EvaluateFunc: func(context.Context, *pb.AuditRequest) (*pb.AuditResult, error) {
			return nil, status.Error(codes.Unavailable, "engine down")
		},
		HealthFunc: func(context.Context) (*pb.HealthResponse, error) {
			return nil, status.Error(codes.Unavailable, "engine down")
		},
	})
	for _, c := range []struct {
		name          string
		engine        *enginetest.Engine
		audit, stored string
	}{
		{"engine down", down, "queued", store.AuditFailed},
		{"no engine", nil, "disabled", store.TraceAuditDisabled},
	} {
		t.Run(c.name, func(t *testing.T) {
			up, _ := newFaultEnv(t)
			env, _ := newFaultEnvWith(t, c.engine)
			s := groundedSupport(t)
			want := gateOf(t, up, s, s.ID)
			got := gateOf(t, env, s, s.ID)
			if got.code != want.code || got.status != want.status || got.action != want.action || got.degraded != want.degraded {
				t.Errorf("%s changed the decision: %s, with the engine up %s", c.name, got, want)
			}
			if got.audit != c.audit {
				t.Errorf("X-TrustAgent-Audit %q, want %q", got.audit, c.audit)
			}
			d := env.settledTrace(got.traceID)
			if d.Audit.Status != c.stored || d.GateStatus != "ok" || d.Action == nil || *d.Action != "pass" {
				t.Errorf("trace: gate %s action %v audit %s, want ok, pass and %s", d.GateStatus, d.Action, d.Audit.Status, c.stored)
			}
		})
	}
}

// TestOffAndMissingCredentials (S6): with the decision model off, or configured with no
// credential (which turns it off as misconfigured, naming the missing key), nothing is asked: a
// flag route passes unchecked and a fail-closed route blocks, and the status says which. (A
// dashboard-bound credential that cannot be read at call time skips as credentials_unavailable,
// the same degradation; internal/gate and the decision client tests cover that path.)
func TestOffAndMissingCredentials(t *testing.T) {
	for _, c := range []struct {
		name, status, degraded, state, reason string
		tune                                  func(*config.Config)
	}{
		{"off", "skipped:decision_model_off", "off", "off", "", func(cfg *config.Config) { cfg.Jev.Backend = jev.BackendOff }},
		{"no credential", "skipped:decision_model_off", "off", "misconfigured", "TRUTHTABLE_JEV_API_KEY",
			func(cfg *config.Config) { cfg.Jev.APIKey = "" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, mu := newFaultEnv(t, c.tune)
			expectGate(t, "flag route", gateOf(t, env, groundedSupport(t), "grounded-pass"), c.status, "pass", c.degraded)
			expectGate(t, "fail closed", gateOf(t, env, groundedPayments(t), "upstream-html-fail-closed"), c.status, "block", c.degraded)
			if n := decisionCalls(mu); n != 0 {
				t.Errorf("%d decision requests", n)
			}
			dm := env.statusOf(testKey).DecisionModel
			if dm.State != c.state || !strings.Contains(dm.BackendReason, c.reason) {
				t.Errorf("decision model state %s (%s), want %s naming %q", dm.State, dm.BackendReason, c.state, c.reason)
			}
		})
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/contracts"
)

// newFaultEnv is the e2e stack in process (docker-compose.e2e.yml): the compatible decision
// backend and the LLM upstreams on mock-upstream, trace writes inline, so the stored rows can be
// read as soon as the response returns.
func newFaultEnv(t *testing.T) (*testEnv, *mockupstream.Server) {
	t.Helper()
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(mu)
	t.Cleanup(upstream.Close)
	env := newScenarioEnv(t, func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.BaseURL, cfg.Jev.Path = jev.BackendCompatible, upstream.URL, "/v1/systemone"
		cfg.Jev.APIKey, cfg.Jev.Model = "e2e-mock-upstream-key", "typesafe/jev-1.13"
		cfg.UpstreamURL, cfg.AnthropicUpstreamURL = upstream.URL, upstream.URL
	})
	return env, mu
}

// sendWithScenarioHeader sends demo scenario s with its scripted answer and the
// X-TrustAgent-Scenario value header, and returns the response headers.
func sendWithScenarioHeader(t *testing.T, env *testEnv, s *demoScenario, header string) http.Header {
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
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: HTTP %d", s.ID, resp.StatusCode)
	}
	return resp.Header
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
		"--phase", "p5", "--base-url", env.http.URL, "--audit-timeout", "30s")
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

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/store"
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

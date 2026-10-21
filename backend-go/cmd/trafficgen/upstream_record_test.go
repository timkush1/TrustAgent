package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/truthtable/backend-go/internal/upstream"
)

// vendorCall is one request the fake OpenRouter received.
type vendorCall struct {
	path, auth, model string
	maxTokens         int
	trustAgent        bool
}

// fakeVendor stands in for OpenRouter's chat endpoint and records every call.
func fakeVendor(t *testing.T) (*httptest.Server, func() []vendorCall) {
	var mu sync.Mutex
	var calls []vendorCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Model     string          `json:"model"`
			MaxTokens int             `json:"max_tokens"`
			TestResp  json.RawMessage `json:"test_response"`
		}
		_ = json.Unmarshal(b, &req)
		c := vendorCall{path: r.URL.Path, auth: r.Header.Get("Authorization"), model: req.Model, maxTokens: req.MaxTokens}
		for name := range r.Header {
			c.trustAgent = c.trustAgent || strings.HasPrefix(strings.ToLower(name), "x-trustagent-")
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen-x","object":"chat.completion","model":"` + req.Model + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []vendorCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]vendorCall(nil), calls...)
	}
}

// TestGeneratedAnswersThroughTheUpstream (D208): in a recording run only the generated scenarios
// reach the upstream, each with the vendor-prefixed model id and max_tokens at most 300, and the
// gateway's upstream client sends the configured key as the bearer (the record overlay sets
// TRUTHTABLE_UPSTREAM_API_KEY to the OpenRouter key); scripted scenarios make no upstream call.
func TestGeneratedAnswersThroughTheUpstream(t *testing.T) {
	vendor, calls := fakeVendor(t)
	const vendorKey = "sk-or-v1-e2e-record-upstream-key"
	client, err := upstream.New(upstream.Config{BaseURL: vendor.URL + "/api", APIKey: vendorKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stub, srv := newStub(t)
	stub.status = recordableStatus()
	stub.forward = func(r *http.Request, body []byte) error {
		resp, cancel, err := client.Do(context.Background(), http.MethodPost, r.URL.Path, "", r.Header, body)
		if err != nil {
			return err
		}
		defer cancel()
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	drift := filepath.Join(t.TempDir(), "drift.json")
	code, out := runTG(t, "--record", "--scenarios", "demo", "--exclude", "knowledge", "--upstream", upstreamGenerated,
		"--model-prefix", "openai/", "--max-tokens", "300", "--base-url", srv.URL, "--audit-timeout", "10s", "--drift-out", drift)
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	all, err := loadScenarios("")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectScenarios(all, "demo", "knowledge")
	if err != nil {
		t.Fatal(err)
	}
	generated := 0
	for _, sc := range selected {
		if sc.AnswerSource == answerGenerated {
			generated++
		}
	}
	got := calls()
	if generated == 0 || len(got) != generated {
		t.Fatalf("%d upstream calls for %d generated scenarios (of %d sent): scripted scenarios must make none", len(got), generated, len(selected))
	}
	for _, c := range got {
		if c.path != "/api/v1/chat/completions" || c.model != "openai/gpt-4o-mini" || c.maxTokens != 300 ||
			c.auth != "Bearer "+vendorKey || c.trustAgent {
			t.Errorf("upstream call %+v: want openai/gpt-4o-mini, max_tokens 300, the configured bearer and no X-TrustAgent header", c)
		}
	}
	// What trafficgen sent: scripted scenarios keep their model and test_response.
	requests, bodies, _ := stub.snapshot()
	for i, r := range requests {
		sc := stub.scenarios[r.Header.Get("X-TrustAgent-Scenario")]
		var sent struct {
			Model     string          `json:"model"`
			MaxTokens *int            `json:"max_tokens"`
			TestResp  json.RawMessage `json:"test_response"`
		}
		_ = json.Unmarshal(bodies[i], &sent)
		if sc.AnswerSource == answerGenerated {
			if sent.TestResp != nil || sent.Model != "openai/gpt-4o-mini" || sent.MaxTokens == nil || *sent.MaxTokens != 300 {
				t.Errorf("%s: sent model %q max_tokens %v test_response %t", sc.ID, sent.Model, sent.MaxTokens, sent.TestResp != nil)
			}
			continue
		}
		if sent.TestResp == nil || strings.HasPrefix(sent.Model, "openai/") || (sent.MaxTokens != nil && *sent.MaxTokens == 300 && !strings.Contains(string(sc.Request), `"max_tokens": 300`)) {
			t.Errorf("%s (scripted): sent model %q max_tokens %v test_response %t", sc.ID, sent.Model, sent.MaxTokens, sent.TestResp != nil)
		}
	}

	// drift.json lists every scenario; the generated pair is unchecked.
	b, err := os.ReadFile(drift)
	if err != nil {
		t.Fatal(err)
	}
	var rep driftReport
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Scenarios) != len(selected) || rep.Counts[outcomeUnchecked] != generated || rep.Counts[outcomeFailed] != 0 {
		t.Errorf("drift.json: %d scenarios, counts %v", len(rep.Scenarios), rep.Counts)
	}
	if !strings.Contains(out, "scenario") || !strings.Contains(out, "drifted fields") || !strings.Contains(out, "unchecked") {
		t.Errorf("no drift table:\n%s", out)
	}
}

// TestRewriteGenerated: the prefix goes on once, and max_tokens is set or lowered, never raised.
func TestRewriteGenerated(t *testing.T) {
	o := options{modelPrefix: "openai/", maxTokens: 300}
	for in, want := range map[string]string{
		`{"model":"gpt-4o-mini"}`:                         `{"max_tokens":300,"model":"openai/gpt-4o-mini"}`,
		`{"model":"openai/gpt-4o-mini","max_tokens":120}`: `{"max_tokens":120,"model":"openai/gpt-4o-mini"}`,
		`{"model":"gpt-4o-mini","max_tokens":2000}`:       `{"max_tokens":300,"model":"openai/gpt-4o-mini"}`,
	} {
		got, err := o.rewriteGenerated([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("rewrite %s = %s (%v), want %s", in, got, err, want)
		}
	}
	if _, err := o.rewriteGenerated([]byte(`{"messages":[]}`)); err == nil {
		t.Error("a body without a model was rewritten")
	}
	none := options{}
	if got, _ := none.rewriteGenerated([]byte(`{"model":"gpt-4o-mini"}`)); string(got) != `{"model":"gpt-4o-mini"}` {
		t.Errorf("no flags changed the body: %s", got)
	}
}

// TestReportOnly: --expect --report-only reports a mismatch as drift without failing, and writes
// drift.json with the mismatch.
func TestReportOnly(t *testing.T) {
	stub, srv := newStub(t)
	stub.mutate = func(id string, h http.Header) {
		if id == "grounded-pass" {
			h.Set(headerAction, "flag")
		}
	}
	drift := filepath.Join(t.TempDir(), "drift.json")
	code, out := runTG(t, "--once", "--expect", "--report-only", "--scenarios", "s0", "--base-url", srv.URL, "--audit-timeout", "10s", "--drift-out", drift)
	if code != exitOK || !strings.Contains(out, "drift grounded-pass") || !strings.Contains(out, "1 drift from their expectations") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	b, _ := os.ReadFile(drift)
	var rep driftReport
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range rep.Scenarios {
		if e.Scenario == "grounded-pass" {
			found = e.Outcome == outcomeDrift && len(e.Mismatches) > 0 && e.Mismatches[0].Field == headerAction && e.TraceID != nil
		}
	}
	if !found || rep.Counts[outcomeDrift] != 1 {
		t.Errorf("drift.json = %s", b)
	}
	// Without --report-only the same mismatch fails.
	if code, _ := runTG(t, "--once", "--expect", "--scenarios", "s0", "--base-url", srv.URL, "--audit-timeout", "10s"); code != exitMismatch {
		t.Errorf("--expect without --report-only: exit %d", code)
	}
}

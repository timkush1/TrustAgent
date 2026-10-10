package main

// The demo scenario pack end to end, in process: the real gateway (policy config/policies.yaml,
// evidence, detectors, gate, handler, trace store, deep-audit worker), the real trafficgen binary
// with --expect, and a fake engine that answers like the engine's mock judge. Two stacks:
//
//   - the zero-key demo (make demo): the mock decision backend and scripted answers
//     (test_response), --scenarios demo;
//   - the e2e stack (docker-compose.e2e.yml): the compatible decision backend against
//     mock-upstream's /v1/systemone and the LLM upstream on mock-upstream too, so every answer
//     takes the upstream path (trafficgen --upstream all), --scenarios e2e.
//
// trafficgen's default phase reaches every phase tag the sets carry, so both sets are sent whole:
// streams, /v1/messages and /v1/completions, and the block and fallback routes. A phase skip would
// fail --expect (D18), and runScenarios also pins how many scenarios each set sends. Both runs also
// check the answer source each trace stored (D121): scripted in the demo stack; in the e2e stack
// generated for the generated pair, whose stand-in answers mock-upstream serves, and live for the
// rest.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/contracts"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/cmd/mock-upstream/mockupstream"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
	"github.com/truthtable/backend-go/internal/jev"
)

// scoredVerdict is the verdict scoring v2 (truthtable/scoring/aggregate.py) gives judge claims,
// each with the LLM default distribution: P(verdict) = confidence, the rest split evenly. An
// UNSUPPORTED claim is baseless (policy hallucination) when evidence came with the request, and
// unverified (policy unverifiable) when all of it came from the knowledge base.
func scoredVerdict(verdicts []string, confidences []float64, hallucinationPolicy bool) pb.AuditVerdict {
	const eps = 1e-9
	index := map[string]int{"SUPPORTED": 0, "PARTIALLY_SUPPORTED": 1, "CONTRADICTED": 2, "UNSUPPORTED": 3}
	var sum, maxC, maxU float64
	n := 0
	for i, v := range verdicts {
		k, decided := index[v]
		if !decided {
			continue
		}
		var p [4]float64
		for j := range p {
			p[j] = (1 - confidences[i]) / 3
		}
		p[k] = confidences[i]
		c, u := p[2], p[3]+0.5*p[1]
		n++
		sum += u
		maxC, maxU = math.Max(maxC, c), math.Max(maxU, u)
	}
	switch {
	case len(verdicts) == 0:
		return pb.AuditVerdict_AUDIT_VERDICT_NO_CLAIMS
	case n == 0:
		return pb.AuditVerdict_AUDIT_VERDICT_NOT_ASSESSED
	case maxC >= 0.35-eps, hallucinationPolicy && (maxU >= 0.60-eps || sum/float64(n) >= 0.30-eps):
		return pb.AuditVerdict_AUDIT_VERDICT_HALLUCINATED
	case !hallucinationPolicy && 1-sum/float64(n) < 0.5-eps:
		return pb.AuditVerdict_AUDIT_VERDICT_UNVERIFIABLE
	}
	return pb.AuditVerdict_AUDIT_VERDICT_GROUNDED
}

// judgedEngine is a fake audit engine that answers like the engine with the mock judge
// (LLM_PROVIDER=mock): without evidence the preflight answers NOT_ASSESSED (no_evidence) and no
// judge is called; with evidence, the scenario's judge claims (only for the scenario's own answer,
// as the mock judge decomposes nothing else) with the verdict scoring v2 gives them.
func judgedEngine(t testing.TB) *enginetest.Engine {
	scenarios := loadDemoScenarios(t)
	return enginetest.Start(t, &enginetest.Engine{EvaluateFunc: func(_ context.Context, req *pb.AuditRequest) (*pb.AuditResult, error) {
		res := &pb.AuditResult{AuditId: req.GetAuditId(), TraceId: req.GetTraceId(), RequestId: req.GetRequestId(), ScoringVersion: "v2",
			ProcessingTimeMs: 3, StepTimings: map[string]int64{"total_ms": 3}, Status: pb.AuditStatus_AUDIT_STATUS_COMPLETED}
		if len(req.GetContext()) == 0 {
			res.Verdict, res.NotAssessedReason = pb.AuditVerdict_AUDIT_VERDICT_NOT_ASSESSED, "no_evidence"
			return res, nil
		}
		s, ok := scenarios[req.GetOptions().GetMockScenario()]
		if !ok || s.Judge == nil || req.GetResponse() != s.Response {
			res.Status, res.ErrorKind, res.Error = pb.AuditStatus_AUDIT_STATUS_FAILED, "mock_fixture_missing", "no mock judge fixture for this answer"
			return res, nil
		}
		knowledgeOnly := !slices.ContainsFunc(req.GetContext(), func(d *pb.ContextDocument) bool { return d.GetSource() != "knowledge" })
		var verdicts []string
		var confidences []float64
		supported := 0
		for i, c := range s.Judge.Claims {
			st := claimStatuses[c.Verdict]
			if st == pb.VerificationStatus_VERIFICATION_STATUS_SUPPORTED {
				supported++
			}
			verdicts, confidences = append(verdicts, c.Verdict), append(confidences, float64(c.Confidence))
			res.Claims = append(res.Claims, &pb.ClaimVerification{
				Index: int32(i), Claim: c.Claim, Status: st, Confidence: c.Confidence, Evidence: c.Evidence, //nolint:gosec // a handful of claims
				Reasoning: c.Reasoning, Quote: c.Quote, DecidedBy: "llm", SpanStart: -1, SpanEnd: -1,
			})
		}
		res.Verdict = scoredVerdict(verdicts, confidences, !knowledgeOnly)
		res.HallucinationDetected = res.Verdict == pb.AuditVerdict_AUDIT_VERDICT_HALLUCINATED
		if n := len(s.Judge.Claims); n > 0 {
			score := float32(supported) / float32(n)
			claims := int32(n) //nolint:gosec // a handful of claims
			res.Scores = &pb.AuditScores{TrustScore: &score, ClaimsTotal: claims, ClaimsDecided: claims}
			res.FaithfulnessScore = score
		}
		return res, nil
	}})
}

// newScenarioEnv starts the gateway as the tests' default stack, adjusted by tune, with the
// judged engine behind it. Trace writes run inline unless tune sets TraceFlush to async; then the
// batched writer fronts the store as main wires it, and its flush runs after the server's drain.
func newScenarioEnv(t *testing.T, tune func(*config.Config)) *testEnv {
	t.Helper()
	cfg := testConfig(t, envOpts{})
	cfg.TraceFlush = traceFlushSync
	if tune != nil {
		tune(cfg)
	}
	eng := judgedEngine(t)
	client, err := grpc.NewAuditClient(eng.Target, 5*time.Second, "", eng.Dialer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	st, flushTraces := wrapTraceWriter(cfg, newMemStore(), slog.New(slog.DiscardHandler))
	cfg.DatabaseURL = "postgres://configured-for-status"
	srv, err := newServer(serverDeps{cfg: cfg, engine: client, store: st, log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv.start()
	hs := httptest.NewServer(srv.router)
	t.Cleanup(func() {
		hs.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.shutdown(ctx)
		if err := flushTraces(ctx); err != nil {
			t.Errorf("trace writes at shutdown: %v", err)
		}
	})
	return &testEnv{t: t, srv: srv, http: hs, store: st, engine: eng}
}

// setSizes is how many scenarios each set sends, and how many of them need phase p3: this build
// serves every one of them.
var setSizes = map[string]struct{ all, p3 int }{"demo": {27, 7}, "e2e": {26, 6}}

// runScenarios runs trafficgen --once --expect with args against env, and checks that it sends
// every scenario of set, skips none and that each passes.
func runScenarios(t *testing.T, env *testEnv, set string, args ...string) string {
	t.Helper()
	cmd := exec.Command(buildTrafficgen(t), append([]string{"--once", "--expect", "--scenarios", set, "--base-url", env.http.URL,
		"--audit-timeout", "30s"}, args...)...)
	cmd.Env = append(os.Environ(), "TRAFFICGEN_API_KEY="+testKey)
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil {
		t.Fatalf("trafficgen --scenarios %s %v: %v\n%s", set, args, err, out)
	}
	if strings.Contains(out, "skipping") {
		t.Errorf("trafficgen skipped scenarios of set %s:\n%s", set, out)
	}
	sent, p3 := 0, 0
	for _, s := range loadDemoScenarios(t) {
		if !slices.Contains(s.Sets, set) {
			continue
		}
		sent++
		if slices.Contains(s.Sets, "p3") {
			p3++
		}
		if !strings.Contains(out, "ok   "+s.ID+" ") {
			t.Errorf("%s did not pass:\n%s", s.ID, out)
		}
	}
	if want := setSizes[set]; sent != want.all || p3 != want.p3 {
		t.Errorf("set %s sent %d scenarios (%d of them p3), want %d (%d p3)", set, sent, p3, want.all, want.p3)
	}
	if n := strings.Count(out, "ok   "); n != sent {
		t.Errorf("%d ok lines for the %d scenarios of set %s:\n%s", n, sent, set, out)
	}
	t.Logf("trafficgen --scenarios %s %v:\n%s", set, args, out)
	return out
}

// runScenarioIDs runs trafficgen --once --expect --phase p3 on the scenarios ids against env and
// checks that each passes.
func runScenarioIDs(t *testing.T, env *testEnv, ids []string, args ...string) {
	t.Helper()
	cmd := exec.Command(buildTrafficgen(t), append([]string{"--once", "--expect", "--phase", "p3", "--scenarios", strings.Join(ids, ","),
		"--base-url", env.http.URL, "--audit-timeout", "30s"}, args...)...)
	cmd.Env = append(os.Environ(), "TRAFFICGEN_API_KEY="+testKey)
	b, err := cmd.CombinedOutput()
	out := string(b)
	if err != nil {
		t.Fatalf("trafficgen --scenarios %v %v: %v\n%s", ids, args, err, out)
	}
	for _, id := range ids {
		if !strings.Contains(out, "ok   "+id+" ") {
			t.Errorf("%s did not pass:\n%s", id, out)
		}
	}
}

// p3FormatScenarios are the P3 scenarios on the other gated formats (/v1/messages and
// /v1/completions, JSON answers).
var p3FormatScenarios = []string{"anthropic-system-grounded", "completions-extension-grounded"}

// TestP3FormatScenariosEndToEnd runs the P3 format scenarios in both stacks, scripted answers with
// the mock decision backend and upstream answers from mock-upstream, and checks that each format
// reaches its own upstream path.
func TestP3FormatScenariosEndToEnd(t *testing.T) {
	runScenarioIDs(t, newScenarioEnv(t, nil), p3FormatScenarios)

	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(mu)
	t.Cleanup(upstream.Close)
	env := newScenarioEnv(t, func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.BaseURL, cfg.Jev.Path = jev.BackendCompatible, upstream.URL, "/v1/systemone"
		cfg.Jev.APIKey, cfg.Jev.Model = "e2e-mock-upstream-key", "typesafe/jev-1.13"
		cfg.UpstreamURL, cfg.AnthropicUpstreamURL = upstream.URL, upstream.URL // as docker-compose.e2e.yml
	})
	runScenarioIDs(t, env, p3FormatScenarios, "--upstream", "all")
	paths := map[string]int{}
	for _, r := range mu.Requests() {
		paths[r.Path]++
	}
	if paths["/v1/messages"] != 1 || paths["/v1/completions"] != 1 {
		t.Errorf("mock-upstream answered %v: each format reaches its own path once", paths)
	}
}

// TestV1ErrorShapesOnTheServer: the server's own /v1 wiring (ingest auth, the v1 limiter, the
// upstream call and the NoRoute branch of server.go) answers in the error shape of the route's
// API, as an SDK pointed at the gateway expects: Anthropic on /v1/messages and its sub-paths,
// OpenAI elsewhere.
func TestV1ErrorShapesOnTheServer(t *testing.T) {
	env := newScenarioEnv(t, func(cfg *config.Config) { cfg.RateLimitPerMinute = 1 })
	send := func(method, path, key string) (*http.Response, string) {
		t.Helper()
		r, _ := http.NewRequest(method, env.http.URL+path, strings.NewReader(`{"model":"claude-haiku-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("X-TrustAgent-Key", key)
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	// anthropic checks an Anthropic-shaped error; traced, that it carries the trace and request
	// ids, as every /v1 response does (SPEC §2.12), the NoRoute branch's 404 included.
	anthropic := func(resp *http.Response, body string, status int, errType string, traced bool) {
		t.Helper()
		var e struct {
			Type  string                         `json:"type"`
			Error struct{ Type, Message string } `json:"error"`
		}
		if resp.StatusCode != status || json.Unmarshal([]byte(body), &e) != nil || e.Type != "error" || e.Error.Type != errType ||
			e.Error.Message == "" || traced && (resp.Header.Get("X-TrustAgent-Trace-Id") == "" || resp.Header.Get("X-Request-ID") == "") {
			t.Errorf("%s %s: want %d %s in the Anthropic shape, got %d %s", resp.Request.Method, resp.Request.URL.Path, status, errType,
				resp.StatusCode, body)
		}
	}

	resp, body := send(http.MethodPost, "/v1/messages", "")
	anthropic(resp, body, http.StatusUnauthorized, "authentication_error", true)
	if resp, body := send(http.MethodPost, "/v1/chat/completions", ""); resp.StatusCode != http.StatusUnauthorized ||
		!strings.Contains(body, `"type":"trustagent_error"`) || !strings.Contains(body, `"code":"unauthorized"`) {
		t.Errorf("chat completions without a key: %d %s", resp.StatusCode, body)
	}
	resp, body = send(http.MethodGet, "/v1/messages/batches", testKey)
	anthropic(resp, body, http.StatusNotFound, "invalid_request_error", true)
	// The test stack's upstream refuses connections: a 502 in the route's shape, then the limit.
	resp, body = send(http.MethodPost, "/v1/messages", testKey)
	anthropic(resp, body, http.StatusBadGateway, "api_error", true)
	if got := resp.Header.Get("X-TrustAgent-Status"); got != "skipped:upstream_error" {
		t.Errorf("status %q", got)
	}
	// The limiter's window is a fixed minute: a request that lands in a new one passes again.
	for range 3 {
		if resp, body = send(http.MethodPost, "/v1/messages", testKey); resp.StatusCode != http.StatusBadGateway {
			break
		}
	}
	anthropic(resp, body, http.StatusTooManyRequests, "rate_limit_error", true)
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a 429 carries Retry-After")
	}
}

// TestDemoScenariosEndToEnd is the zero-key demo's scenario check (make demo-scenarios) in
// process: the mock decision backend answers from mock_answers, the answers are scripted, and the
// repo's calibration directory holds the mock calibration payments-faq adopts (as the demo stack's
// TRUTHTABLE_CALIBRATION_DIR does), so payments-fallback falls back instead of being capped to a flag.
// It runs with trace writes inline (TRUTHTABLE_TRACE_FLUSH=sync, the e2e stack) and batched
// (async, the default).
func TestDemoScenariosEndToEnd(t *testing.T) {
	for _, flush := range traceFlushModes {
		t.Run("TRACE_FLUSH="+flush, func(t *testing.T) {
			env := newScenarioEnv(t, func(cfg *config.Config) {
				cfg.CalibrationDir = config.RepoPath("config/calibration")
				cfg.TraceFlush = flush
			})
			ids := filepath.Join(t.TempDir(), "trace-ids.txt")
			out := runScenarios(t, env, "demo", "--trace-ids-out", ids)
			checkAnswerSources(t, env, out, ids, func(string) string { return "scripted" })
		})
	}
}

// TestRecordRefusesTheTestStacksFromTheirStatus: trafficgen --record reads the gateway's own GET
// /api/status (D121), so its reading of that body is checked here against the real handler, not a
// stub: both test stacks are refused before anything is sent, the demo stack for its mock
// decision model and the e2e stack for its decision model on mock-upstream, a local endpoint the
// gateway forwards each scenario id to (D119). No trace is started and no --trace-ids-out file is
// written.
func TestRecordRefusesTheTestStacksFromTheirStatus(t *testing.T) {
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(mu)
	t.Cleanup(upstream.Close)
	for _, c := range []struct {
		name string
		tune func(*config.Config)
		want string
	}{
		{"demo", nil, "the decision model is mock"},
		{"e2e", func(cfg *config.Config) {
			cfg.Jev.Backend, cfg.Jev.BaseURL, cfg.Jev.Path = jev.BackendCompatible, upstream.URL, "/v1/systemone"
			cfg.Jev.APIKey, cfg.Jev.Model = "e2e-mock-upstream-key", "typesafe/jev-1.13"
			cfg.UpstreamURL, cfg.AnthropicUpstreamURL = upstream.URL, upstream.URL
		}, "answers from the local endpoint 127.0.0.1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newScenarioEnv(t, c.tune)
			ids := filepath.Join(t.TempDir(), "trace-ids.txt")
			cmd := exec.Command(buildTrafficgen(t), "--record", "--scenarios", "s0", "--base-url", env.http.URL, "--trace-ids-out", ids)
			cmd.Env = append(os.Environ(), "TRAFFICGEN_API_KEY="+testKey)
			b, err := cmd.CombinedOutput()
			out := string(b)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(out, "trafficgen: --record refused: ") ||
				!strings.Contains(out, c.want) {
				t.Fatalf("want a refusal naming %q (exit 2): %v\n%s", c.want, err, out)
			}
			m := env.store.(*memStore)
			m.mu.Lock()
			stored := len(m.traces)
			m.mu.Unlock()
			if stored != 0 {
				t.Errorf("%d traces stored after a refusal", stored)
			}
			if _, err := os.Stat(ids); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("--trace-ids-out was written after a refusal: %v", err)
			}
		})
	}
}

// okLine is trafficgen's line for a scenario that passed: its id and its trace id.
var okLine = regexp.MustCompile(`(?m)^ok   (\S+) \(trace (\S+), `)

// checkAnswerSources checks the answer_source the gateway stored for every scenario of a trafficgen
// run (out), read back through GET /api/traces/:id, against want(scenario id); and that
// --trace-ids-out (ids) listed exactly the run's traces, in send order.
func checkAnswerSources(t *testing.T, env *testEnv, out, ids string, want func(id string) string) {
	t.Helper()
	b, err := os.ReadFile(ids)
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	for _, m := range okLine.FindAllStringSubmatch(out, -1) {
		sent = append(sent, m[2])
		code, body := env.get("/api/traces/"+m[2], testKey)
		var d struct {
			AnswerSource *string `json:"answer_source"`
		}
		if code != http.StatusOK || json.Unmarshal(body, &d) != nil || d.AnswerSource == nil {
			t.Errorf("%s: GET /api/traces/%s: %d %s", m[1], m[2], code, body)
			continue
		}
		if *d.AnswerSource != want(m[1]) {
			t.Errorf("%s: answer_source %q, want %q", m[1], *d.AnswerSource, want(m[1]))
		}
	}
	if len(sent) == 0 || string(b) != strings.Join(sent, "\n")+"\n" {
		t.Errorf("--trace-ids-out holds %q, want the %d traces of the run in send order", b, len(sent))
	}
}

// generatedScenarios are the ids of the pack's scenarios whose answer_source is generated.
func generatedScenarios(t *testing.T) map[string]bool {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []struct {
			ID           string `json:"id"`
			AnswerSource string `json:"answer_source"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, s := range pack.Scenarios {
		if s.AnswerSource == "generated" {
			out[s.ID] = true
		}
	}
	if len(out) != 2 {
		t.Fatalf("%d generated scenarios, want the pair", len(out))
	}
	return out
}

// traceFlushModes are the TRUTHTABLE_TRACE_FLUSH values the scenario sets run under.
var traceFlushModes = []string{traceFlushSync, "async"}

// TestE2EScenariosAgainstMockUpstream is the e2e stack (docker-compose.e2e.yml, make
// e2e-scenarios) in process: the compatible decision backend calls mock-upstream's TypeSafe-shaped
// /v1/systemone over HTTP (X-TrustAgent-Scenario reaches it in test mode, so it answers from
// mock_answers), and every answer comes from mock-upstream through the upstream path. It runs
// with trace writes inline and batched.
func TestE2EScenariosAgainstMockUpstream(t *testing.T) {
	for _, flush := range traceFlushModes {
		t.Run("TRACE_FLUSH="+flush, func(t *testing.T) { e2eScenariosAgainstMockUpstream(t, flush) })
	}
}

func e2eScenariosAgainstMockUpstream(t *testing.T, flush string) {
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(mu)
	t.Cleanup(upstream.Close)
	env := newScenarioEnv(t, func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.BaseURL, cfg.Jev.Path = jev.BackendCompatible, upstream.URL, "/v1/systemone"
		cfg.Jev.APIKey, cfg.Jev.Model = "e2e-mock-upstream-key", "typesafe/jev-1.13"
		cfg.UpstreamURL, cfg.AnthropicUpstreamURL = upstream.URL, upstream.URL // as docker-compose.e2e.yml
		cfg.TraceFlush = flush
	})
	ids := filepath.Join(t.TempDir(), "trace-ids.txt")
	out := runScenarios(t, env, "e2e", "--upstream", "all", "--trace-ids-out", ids)
	generated := generatedScenarios(t)
	checkAnswerSources(t, env, out, ids, func(id string) string {
		if generated[id] {
			return "generated"
		}
		return "live"
	})

	// What reached the vendors: every answer through the upstream path, with no test_response, no
	// X-TrustAgent-* header and no trustagent extension; the decisions over HTTP with the scenario.
	llm, decisions := 0, 0
	for _, r := range mu.Requests() {
		switch r.Path {
		case "/v1/chat/completions", "/v1/completions", "/v1/messages":
			llm++
			var body map[string]json.RawMessage
			if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
				t.Fatal(err)
			}
			if body["test_response"] != nil || body["trustagent"] != nil {
				t.Errorf("the upstream got a TrustAgent member: %s", r.Body)
			}
			for name := range r.Header {
				if strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Trustagent-") {
					t.Errorf("the upstream got header %s", name)
				}
			}
		case "/v1/systemone":
			// Keep-warm requests carry no scenario; gated answers do (mock-upstream records the
			// Authorization header redacted).
			if r.Method != http.MethodPost || r.Header.Get("X-TrustAgent-Scenario") == "" {
				continue
			}
			decisions++
			if r.Header.Get("Authorization") == "" {
				t.Errorf("decision request without a credential: %v", r.Header)
			}
		}
	}
	if llm != setSizes["e2e"].all || decisions < 10 {
		t.Errorf("mock-upstream answered %d LLM and %d decision requests", llm, decisions)
	}
}

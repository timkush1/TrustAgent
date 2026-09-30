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
// Scenarios tagged with a later gateway phase (p3) are skipped by trafficgen's default --phase;
// cmd/trafficgen's reference tests cover them, and TestP3FormatScenariosEndToEnd runs the ones on
// the other gated formats (/v1/messages, /v1/completions) with --phase p3.

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

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
// judged engine behind it.
func newScenarioEnv(t *testing.T, tune func(*config.Config)) *testEnv {
	t.Helper()
	cfg := testConfig(t, envOpts{})
	if tune != nil {
		tune(cfg)
	}
	eng := judgedEngine(t)
	client, err := grpc.NewAuditClient(eng.Target, 5*time.Second, "", eng.Dialer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	st := newMemStore()
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
	})
	return &testEnv{t: t, srv: srv, http: hs, store: st, engine: eng}
}

// runScenarios runs trafficgen --once --expect with args against env, and checks that every
// scenario of set it sends passes and that it skips exactly the phase-tagged ones.
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
	sent := 0
	for _, s := range loadDemoScenarios(t) {
		if !slices.Contains(s.Sets, set) {
			continue
		}
		if slices.Contains(s.Sets, "p3") {
			if !strings.Contains(out, "skipping "+s.ID+":") {
				t.Errorf("%s needs P3, but trafficgen did not skip it:\n%s", s.ID, out)
			}
			continue
		}
		sent++
		if !strings.Contains(out, "ok   "+s.ID+" ") {
			t.Errorf("%s did not pass:\n%s", s.ID, out)
		}
	}
	if sent < 15 {
		t.Errorf("only %d scenarios of set %s ran on this build", sent, set)
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
// /v1/completions, JSON answers): the handler serves them before the turn-on moves trafficgen's
// default phase to p3.
var p3FormatScenarios = []string{"anthropic-system-grounded", "completions-extension-grounded"}

// TestP3FormatScenariosEndToEnd runs the P3 format scenarios in both stacks: scripted answers
// with the mock decision backend, and upstream answers from mock-upstream.
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
		cfg.UpstreamURL = upstream.URL
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

// TestDemoScenariosEndToEnd is the zero-key demo's scenario check (make demo-scenarios) in
// process: the mock decision backend answers from mock_answers, the answers are scripted.
func TestDemoScenariosEndToEnd(t *testing.T) {
	runScenarios(t, newScenarioEnv(t, nil), "demo")
}

// TestE2EScenariosAgainstMockUpstream is the e2e stack (docker-compose.e2e.yml, make
// e2e-scenarios) in process: the compatible decision backend calls mock-upstream's TypeSafe-shaped
// /v1/systemone over HTTP (X-TrustAgent-Scenario reaches it in test mode, so it answers from
// mock_answers), and every answer comes from mock-upstream through the upstream path.
func TestE2EScenariosAgainstMockUpstream(t *testing.T) {
	mu, err := mockupstream.New(mockupstream.Config{})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(mu)
	t.Cleanup(upstream.Close)
	env := newScenarioEnv(t, func(cfg *config.Config) {
		cfg.Jev.Backend, cfg.Jev.BaseURL, cfg.Jev.Path = jev.BackendCompatible, upstream.URL, "/v1/systemone"
		cfg.Jev.APIKey, cfg.Jev.Model = "e2e-mock-upstream-key", "typesafe/jev-1.13"
		cfg.UpstreamURL = upstream.URL
	})
	runScenarios(t, env, "e2e", "--upstream", "all")

	// What reached the vendors: every answer through the upstream path, with no test_response, no
	// X-TrustAgent-* header and no trustagent extension; the decisions over HTTP with the scenario.
	llm, decisions := 0, 0
	for _, r := range mu.Requests() {
		switch r.Path {
		case "/v1/chat/completions":
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
	if llm < 15 || decisions < 10 {
		t.Errorf("mock-upstream answered %d LLM and %d decision requests", llm, decisions)
	}
}

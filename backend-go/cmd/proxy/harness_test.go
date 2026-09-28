package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/contracts"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// testKey is the TRUTHTABLE_API_KEYS entry (label trafficgen) the tests authenticate with.
const testKey = "tg-test-secret"

// demoScenario is a contracts/demo/scenarios.v1.json scenario as the tests use it.
type demoScenario struct {
	ID       string            `json:"id"`
	Sets     []string          `json:"sets"`
	Route    string            `json:"route"`
	Headers  map[string]string `json:"headers"`
	Request  json.RawMessage   `json:"request"`
	Response string            `json:"response"`
	Judge    *struct {
		Claims []struct {
			Claim      string   `json:"claim"`
			Quote      string   `json:"quote"`
			Verdict    string   `json:"verdict"`
			Confidence float32  `json:"confidence"`
			Evidence   []string `json:"evidence"`
			Reasoning  string   `json:"reasoning"`
		} `json:"claims"`
	} `json:"judge"`
	Expect struct {
		Action       string  `json:"action"`
		Status       string  `json:"status"`
		Audit        string  `json:"audit"`
		AuditVerdict *string `json:"audit_verdict"`
		Outcome      string  `json:"outcome"`
	} `json:"expect"`
}

func loadDemoScenarios(t testing.TB) map[string]*demoScenario {
	t.Helper()
	b, err := contracts.FS.ReadFile("demo/scenarios.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scenarios []*demoScenario `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		t.Fatal(err)
	}
	out := map[string]*demoScenario{}
	for _, s := range pack.Scenarios {
		out[s.ID] = s
	}
	return out
}

var claimStatuses = map[string]pb.VerificationStatus{
	"SUPPORTED": pb.VerificationStatus_VERIFICATION_STATUS_SUPPORTED, "CONTRADICTED": pb.VerificationStatus_VERIFICATION_STATUS_CONTRADICTED,
	"UNSUPPORTED": pb.VerificationStatus_VERIFICATION_STATUS_UNSUPPORTED, "PARTIALLY_SUPPORTED": pb.VerificationStatus_VERIFICATION_STATUS_PARTIALLY_SUPPORTED,
}

// scenarioEngine is a fake audit engine that answers like the engine's mock judge
// (LLM_PROVIDER=mock): the claims of the scenario named by AuditOptions.mock_scenario, and a failed
// mock_fixture_missing audit for any other request.
func scenarioEngine(t testing.TB, down bool) *enginetest.Engine {
	scenarios := loadDemoScenarios(t)
	var health func(context.Context) (*pb.HealthResponse, error)
	if down {
		health = func(context.Context) (*pb.HealthResponse, error) { return nil, errors.New("engine down") }
	}
	return enginetest.Start(t, &enginetest.Engine{HealthFunc: health, EvaluateFunc: func(_ context.Context, req *pb.AuditRequest) (*pb.AuditResult, error) {
		res := &pb.AuditResult{AuditId: req.GetAuditId(), TraceId: req.GetTraceId(), RequestId: req.GetRequestId(), ScoringVersion: "v2",
			ProcessingTimeMs: 3, StepTimings: map[string]int64{"total_ms": 3}}
		s, ok := scenarios[req.GetOptions().GetMockScenario()]
		if !ok || s.Judge == nil {
			res.Status, res.ErrorKind, res.Error = pb.AuditStatus_AUDIT_STATUS_FAILED, "mock_fixture_missing", "no mock judge fixture"
			return res, nil
		}
		res.Status, res.Verdict, res.Grade = pb.AuditStatus_AUDIT_STATUS_COMPLETED, pb.AuditVerdict_AUDIT_VERDICT_GROUNDED, pb.TrustGrade_TRUST_GRADE_A
		supported := 0
		for i, c := range s.Judge.Claims {
			st := claimStatuses[c.Verdict]
			if st == pb.VerificationStatus_VERIFICATION_STATUS_CONTRADICTED {
				res.Verdict, res.Grade, res.HallucinationDetected = pb.AuditVerdict_AUDIT_VERDICT_HALLUCINATED, pb.TrustGrade_TRUST_GRADE_F, true
			}
			if st == pb.VerificationStatus_VERIFICATION_STATUS_SUPPORTED {
				supported++
			}
			res.Claims = append(res.Claims, &pb.ClaimVerification{
				Index: int32(i), Claim: c.Claim, Status: st, Confidence: c.Confidence, Evidence: c.Evidence, //nolint:gosec // a handful of claims
				Reasoning: c.Reasoning, Quote: c.Quote, DecidedBy: "llm", SpanStart: -1, SpanEnd: -1,
			})
		}
		score := float32(supported) / float32(len(s.Judge.Claims))
		n := int32(len(s.Judge.Claims)) //nolint:gosec // a handful of claims
		res.Scores = &pb.AuditScores{TrustScore: &score, ClaimsTotal: n, ClaimsDecided: n}
		res.FaithfulnessScore = score
		return res, nil
	}})
}

// envOpts configure a test server.
type envOpts struct {
	publicRead bool
	demo       bool
	readonly   bool
	noStore    bool
	store      store.Store // default: a memStore
	// engineDown makes the fake engine fail its health checks.
	engineDown bool
	// rateLimit is TRUTHTABLE_RATE_LIMIT_PER_MINUTE (default: high enough never to trip).
	rateLimit int
	// trustedProxies is TRUTHTABLE_TRUSTED_PROXIES.
	trustedProxies []string
}

type testEnv struct {
	t      *testing.T
	srv    *server
	http   *httptest.Server
	store  store.Store
	engine *enginetest.Engine
}

func testConfig(t *testing.T, o envOpts) *config.Config {
	t.Helper()
	cfg := config.Load()
	cfg.Warnings = nil
	cfg.TestMode, cfg.DemoMode, cfg.PublicRead, cfg.Readonly = true, o.demo, o.publicRead, o.readonly
	cfg.APIKeys = []config.APIKey{{Label: "trafficgen", Key: testKey}}
	cfg.IngestAuth = "keys"
	cfg.PolicyFile = config.RepoPath("config/policies.yaml")
	cfg.GateDefaultMode, cfg.GateFailMode = "observe", "open"
	cfg.Jev.Backend, cfg.Jev.MockMode, cfg.Jev.MockLatency, cfg.Jev.MockFixtures = "mock", "scenario", 0, ""
	cfg.UpstreamURL = "http://127.0.0.1:1" // test_response answers; nothing reaches the upstream
	cfg.RateLimitPerMinute, cfg.UploadLimitPerMin = 100000, 100000
	if o.rateLimit > 0 {
		cfg.RateLimitPerMinute = o.rateLimit
	}
	cfg.TrustedProxies = o.trustedProxies
	cfg.DatabaseURL, cfg.RedisURL, cfg.InternalToken = "", "", ""
	cfg.EngineHealthInterval = time.Hour
	cfg.WorkerCount = 2
	return cfg
}

func newTestEnv(t *testing.T, o envOpts) *testEnv {
	t.Helper()
	cfg := testConfig(t, o)
	eng := scenarioEngine(t, o.engineDown)
	client, err := grpc.NewAuditClient(eng.Target, 5*time.Second, "", eng.Dialer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	d := serverDeps{cfg: cfg, engine: client, log: slog.New(slog.DiscardHandler)}
	if !o.noStore {
		d.store = o.store
		if d.store == nil {
			d.store = newMemStore()
		}
		cfg.DatabaseURL = "postgres://configured-for-status"
	}
	srv, err := newServer(d)
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
	// The pinger checks once at start; wait for it so /health reports the engine.
	deadline := time.Now().Add(5 * time.Second)
	for srv.pinger.Health().State == "unknown" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return &testEnv{t: t, srv: srv, http: hs, store: d.store, engine: eng}
}

// send posts a scenario the way trafficgen does and returns the response.
func (e *testEnv) send(s *demoScenario) *http.Response {
	e.t.Helper()
	var req map[string]json.RawMessage
	if err := json.Unmarshal(s.Request, &req); err != nil {
		e.t.Fatal(err)
	}
	req["test_response"], _ = json.Marshal(s.Response)
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest(http.MethodPost, e.http.URL+"/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-TrustAgent-Key", testKey)
	r.Header.Set("X-TrustAgent-Scenario", s.ID)
	for k, v := range s.Headers {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("%s: HTTP %d", s.ID, resp.StatusCode)
	}
	return resp
}

// get sends a GET with key (none when empty) and returns the status and body.
func (e *testEnv) get(path, key string) (int, []byte) {
	e.t.Helper()
	return e.do(http.MethodGet, path, key, nil)
}

func (e *testEnv) do(method, path, key string, body []byte) (int, []byte) {
	e.t.Helper()
	r, _ := http.NewRequest(method, e.http.URL+path, bytes.NewReader(body))
	if key != "" {
		r.Header.Set("X-TrustAgent-Key", key)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// settledTrace polls a trace until its gate decided and its audit left queued/running.
func (e *testEnv) settledTrace(traceID string) store.TraceDetail {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, body := e.get("/api/traces/"+traceID, testKey)
		var d store.TraceDetail
		if code == http.StatusOK {
			if err := json.Unmarshal(body, &d); err != nil {
				e.t.Fatal(err)
			}
			if d.GateStatus != store.GateStatusPending && d.Audit.Status != store.AuditQueued && d.Audit.Status != store.AuditRunning {
				return d
			}
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("trace %s did not settle: HTTP %d %s", traceID, code, body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

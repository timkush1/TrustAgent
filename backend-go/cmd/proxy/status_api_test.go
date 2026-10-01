package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/jev"
)

func TestJudgeStateFromEngineHealth(t *testing.T) {
	health := func(r *pb.HealthResponse) grpc.EngineHealth {
		if r == nil {
			return grpc.EngineHealth{State: grpc.StateDown}
		}
		return grpc.EngineHealth{State: grpc.StateUp, Response: r}
	}
	judge := func(ok bool) map[string]bool { return map[string]bool{"judge": ok} }
	for _, tc := range []struct {
		name     string
		h        grpc.EngineHealth
		want     string
		verifier string
	}{
		{"no engine", grpc.EngineHealth{State: stateDisabled}, stateDisabled, "off"},
		{"engine not answering", health(nil), stateUnknown, "llm"},
		{"judge not named, dependency healthy (S0 engine)", health(&pb.HealthResponse{Healthy: true, Dependencies: judge(true)}), stateUp, "llm"},
		{"judge not named, dependency failing", health(&pb.HealthResponse{Dependencies: judge(false)}), stateDown, "off"},
		{"judge not named, no dependency", health(&pb.HealthResponse{Healthy: true}), stateUnknown, "llm"},
		{"engine without a judge", health(&pb.HealthResponse{JudgeProvider: "none", Dependencies: judge(true)}), stateDisabled, "off"},
		{"named judge", health(&pb.HealthResponse{JudgeProvider: "mock", Dependencies: judge(true)}), stateUp, "llm"},
		{"fallback judge", health(&pb.HealthResponse{JudgeProvider: "ollama", JudgeFallbackActive: true, Dependencies: judge(true)}), stateDegraded, "llm"},
	} {
		if got := judgeState(tc.h); got != tc.want {
			t.Errorf("%s: judge %q, want %q", tc.name, got, tc.want)
		}
		if got := verifierStatus(tc.h).Effective; got != tc.verifier {
			t.Errorf("%s: verifier %q, want %q", tc.name, got, tc.verifier)
		}
	}
}

// fakeStatusJev is a decision-model client that reports a fixed live status.
type fakeStatusJev struct {
	jev.Client
	st jev.Status
}

func (f fakeStatusJev) Status() jev.Status { return f.st }

func TestDecisionModelStatusIsLive(t *testing.T) {
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	st := jev.Status{
		Info: jev.Info{Backend: jev.BackendOpenRouterDecisions, Reason: "OPENROUTER_API_KEY is set", Model: "typesafe/jev-1.13",
			Endpoint: "https://openrouter.ai/api/alpha/decisions", CredentialSource: "env"},
		Snapshot: "typesafe/jev-1.13-20260917", State: "unauthorized", Breaker: jev.BreakerOpen,
		LastError: &jev.StatusError{Kind: jev.KindAuth, Message: "User not found.", Hint: "check the key", Status: 401, At: at},
		RPMLimit:  1000, RPMUsed: 7, Calls: 40, CostUSD: 0.001, Calls24h: 12, Cost24hUSD: 0.0003,
		Spend: &jev.SpendStatus{SpentUSD: 1.6, ReservedUSD: 0, CapUSD: 2, SoftCapUSD: 1.5},
	}
	cfg := &config.Config{SpendCapDecisionUSDDay: 2, SpendCapLLMUSDDay: 5}
	s := &statusReporter{cfg: cfg, jev: st.Info, jevClient: fakeStatusJev{st: st}, now: time.Now}
	d := s.decisionModel()
	if d.Backend != jev.BackendOpenRouterDecisions || d.State != "unauthorized" || d.Breaker != "open" ||
		*d.Snapshot != "typesafe/jev-1.13-20260917" || *d.EndpointHost != "openrouter.ai" || d.Credential.Source != "env" {
		t.Errorf("decision model %+v", d)
	}
	if d.LastError == nil || d.LastError.Kind != jev.KindAuth || d.LastError.Hint != "check the key" || !d.LastError.At.Equal(at) {
		t.Errorf("last error %+v", d.LastError)
	}
	if d.RPMLimit != 1000 || d.RPMUsed != 7 || d.Calls24h != 12 || d.Cost24hUSD != 0.0003 {
		t.Errorf("counters %+v", d)
	}
	b := s.budgets()
	if b.DecisionSpentTodayUSD != 1.6 || b.DecisionExhausted || !b.DecisionReserveOnly || b.DecisionUSDDay != 2 || b.LLMUSDDay != 5 {
		t.Errorf("budgets %+v", b)
	}

	// Without a status-reporting client, the resolved backend alone.
	off := &statusReporter{cfg: cfg, jev: jev.Info{Backend: jev.BackendOff, Reason: "no key"}, now: time.Now}
	if d := off.decisionModel(); d.State != "off" || d.Breaker != "closed" || d.EndpointHost != nil || d.Credential.Source != "none" {
		t.Errorf("off %+v", d)
	}
}

func TestStatusUsesEngineInfo(t *testing.T) {
	h := grpc.EngineHealth{State: grpc.StateUp,
		Response: &pb.HealthResponse{Healthy: true, JudgeProvider: "ollama", JudgeModel: "llama3.2:1b", JudgeFallbackActive: true,
			KnowledgeEnabled: true, Dependencies: map[string]bool{"judge": true, "qdrant": true}},
		Info: &pb.EngineInfo{MaxAudits: 16, Warnings: []string{"LLM_MODEL is ignored under auto"},
			Judge:              &pb.JudgeInfo{EffectiveProvider: "ollama", CredentialSource: "none", FallbackActive: true, FallbackReason: "no ANTHROPIC_API_KEY"},
			VerifierConfigured: "auto", VerifierEffective: "llm", VerifierReason: "2.0 verifies with the LLM judge",
			Knowledge: &pb.KnowledgeInfo{Enabled: true, EmbeddingModel: "all-MiniLM-L6-v2"}},
	}
	j := judgeStatus(h)
	if j.CredentialSource != "none" || j.FallbackReason == nil || *j.FallbackReason != "no ANTHROPIC_API_KEY" {
		t.Errorf("judge %+v", j)
	}
	if v := verifierStatus(h); v.Configured != "auto" || v.Reason != "2.0 verifies with the LLM judge" {
		t.Errorf("verifier %+v", v)
	}
	if k := knowledgeStatus(h); k.EmbeddingModel != "all-MiniLM-L6-v2" {
		t.Errorf("knowledge %+v", k)
	}
	s := &statusReporter{cfg: &config.Config{}, engine: fixedHealth{h}, jev: jev.Info{Backend: jev.BackendMock}, now: time.Now,
		auth: &apiAuth{keys: authz.New(nil, "keys")}}
	st := s.system(context.Background())
	if st.Engine.MaxAudits == nil || *st.Engine.MaxAudits != 16 || len(st.Warnings) != 1 || st.Warnings[0] != "Engine: LLM_MODEL is ignored under auto" {
		t.Errorf("engine %+v warnings %v", st.Engine, st.Warnings)
	}
}

type fixedHealth struct{ h grpc.EngineHealth }

func (f fixedHealth) Health() grpc.EngineHealth { return f.h }

// statusCalibration reads decision_model's calibration fields from GET /api/status.
func statusCalibration(t *testing.T, env *testEnv) (state string, targets int, snapshot string) {
	t.Helper()
	code, body := env.get("/api/status", testKey)
	if code != http.StatusOK {
		t.Fatalf("status %d %s", code, body)
	}
	var st struct {
		DecisionModel struct {
			CalibrationState  string  `json:"calibration_state"`
			CalibratedTargets int     `json:"calibrated_targets"`
			Snapshot          *string `json:"snapshot"`
		} `json:"decision_model"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st.DecisionModel.Snapshot != nil {
		snapshot = *st.DecisionModel.Snapshot
	}
	return st.DecisionModel.CalibrationState, st.DecisionModel.CalibratedTargets, snapshot
}

func TestStatusShowsTheDemoCalibration(t *testing.T) {
	env := newTestEnv(t, envOpts{demo: true})
	if state, n, snap := statusCalibration(t, env); state != "mock" || n != 2 || snap != jev.DefaultMockModel {
		t.Errorf("decision model calibration %s, %d targets, snapshot %s", state, n, snap)
	}
	// The evaluator API reads the same lookup (calibration_progress), for the current snapshot.
	code, body := env.get("/api/evaluators/groundedness", testKey)
	var ev struct {
		CalibrationProgress struct {
			State    string  `json:"state"`
			Snapshot *string `json:"snapshot"`
		} `json:"calibration_progress"`
	}
	if err := json.Unmarshal(body, &ev); err != nil || code != http.StatusOK {
		t.Fatalf("evaluator %d %s (%v)", code, body, err)
	}
	if p := ev.CalibrationProgress; p.State != "mock" || p.Snapshot == nil || *p.Snapshot != jev.DefaultMockModel {
		t.Errorf("calibration progress %+v", p)
	}
	// decision_model_changed reports the new snapshot's calibration from it too.
	if got := env.srv.calibrationState(jev.DefaultMockModel); got != "mock" {
		t.Errorf("snapshot calibration %s", got)
	}
	if got := env.srv.calibrationState("typesafe/jev-1.13-20260917"); got != "none" {
		t.Errorf("uncalibrated snapshot %s", got)
	}

	// The gateway's gate reads it: a gated answer's evidence checks carry the mock calibration's
	// state (support-bot keeps raw thresholds, so nothing is calibrated and nothing changes).
	sc := loadDemoScenarios(t)["refund-contradiction-flag"]
	traceID := env.send(sc).Header.Get("X-TrustAgent-Trace-Id")
	tr := env.settledTrace(traceID)
	if len(tr.Decisions) != 1 {
		t.Fatalf("decisions %d", len(tr.Decisions))
	}
	var d gate.Decision
	if err := json.Unmarshal(tr.Decisions[0], &d); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"groundedness@1", "contradiction@1"} {
		if e, _ := d.Evaluators.Get(ref); e == nil || e.CalibrationState != "mock" || e.Calibrated {
			t.Errorf("%s: %+v", ref, e)
		}
	}
	if d.Calibration != "none" || d.Action != "flag" {
		t.Errorf("decision calibration %s action %s", d.Calibration, d.Action)
	}
}

func TestCalibrationReloadBroadcastsTheChange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TRUTHTABLE_CALIBRATION_DIR", dir)
	old := calibrationReload
	calibrationReload = 20 * time.Millisecond
	t.Cleanup(func() { calibrationReload = old })
	env := newTestEnv(t, envOpts{demo: true})
	if state, n, _ := statusCalibration(t, env); state != "none" || n != 0 {
		t.Fatalf("empty directory: %s %d", state, n)
	}
	conn, _, err := dialWS(t, env, "protocol=2", http.Header{"X-TrustAgent-Key": {testKey}})
	if err != nil {
		t.Fatal(err)
	}
	if hello := readFrame(t, conn); hello.Type != "hello" {
		t.Fatalf("hello %s", hello.raw)
	}

	b, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "calibration", "mock__jev-heuristic-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mock__jev-heuristic-v1.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		f := readFrame(t, conn) // fails after 10 s without a frame
		if f.Type != "calibration_updated" {
			continue
		}
		var data struct {
			Snapshot string `json:"snapshot"`
			Imported int    `json:"imported"`
		}
		if err := json.Unmarshal(f.Data, &data); err != nil || data.Snapshot != jev.DefaultMockModel || data.Imported != 2 || f.TraceID != "" {
			t.Errorf("calibration_updated %s (%v)", f.raw, err)
		}
		break
	}
	if state, n, _ := statusCalibration(t, env); state != "mock" || n != 2 {
		t.Errorf("after the reload: %s %d", state, n)
	}
}

func TestCalibrationSettings(t *testing.T) {
	for _, tc := range []struct {
		cfg  config.Config
		want bool
	}{
		{config.Config{DemoMode: true}, true},
		{config.Config{TestMode: true, Jev: config.JevConfig{Backend: "mock"}}, true},
		{config.Config{TestMode: true, Jev: config.JevConfig{Backend: "compatible"}}, false},
		{config.Config{Jev: config.JevConfig{Backend: "mock"}}, false},
		{config.Config{}, false},
	} {
		if got := allowMockCalibration(&tc.cfg); got != tc.want {
			t.Errorf("demo %v test %v backend %q: allow mock %v", tc.cfg.DemoMode, tc.cfg.TestMode, tc.cfg.Jev.Backend, got)
		}
	}
	if dir, family := calibrationSettings(&config.Config{}); dir != "" || family {
		t.Errorf("a configuration built by hand: %q %v", dir, family)
	}
	dir := t.TempDir()
	t.Setenv("TRUTHTABLE_CALIBRATION_DIR", dir)
	t.Setenv("TRUTHTABLE_CALIBRATION_FAMILY_FALLBACK", "true")
	if got, family := calibrationSettings(config.Load()); got != dir || !family {
		t.Errorf("loaded: %q %v", got, family)
	}
}

func TestBudgetStatesMatchTheCap(t *testing.T) {
	for _, tc := range []struct {
		name                string
		sp                  jev.SpendStatus
		exhausted, reserved bool
	}{
		{"under the soft cap", jev.SpendStatus{SpentUSD: 1, CapUSD: 2, SoftCapUSD: 1.5}, false, false},
		// jev reports Exhausted from the soft cap on: only observe and flag calls stop there.
		{"between the soft cap and the cap", jev.SpendStatus{SpentUSD: 1.6, CapUSD: 2, SoftCapUSD: 1.5, Exhausted: true}, false, true},
		{"held past the soft cap", jev.SpendStatus{SpentUSD: 1.4, ReservedUSD: 0.2, CapUSD: 2, SoftCapUSD: 1.5}, false, true},
		{"at the cap", jev.SpendStatus{SpentUSD: 2, CapUSD: 2, SoftCapUSD: 1.5, Exhausted: true}, true, false},
		{"no reserve", jev.SpendStatus{SpentUSD: 1.9, CapUSD: 2, SoftCapUSD: 2}, false, false},
		{"no cap", jev.SpendStatus{SpentUSD: 9}, false, false},
	} {
		st := jev.Status{Info: jev.Info{Backend: jev.BackendOpenRouterDecisions}, Spend: &tc.sp}
		s := &statusReporter{cfg: &config.Config{SpendCapDecisionUSDDay: tc.sp.CapUSD}, jev: st.Info, jevClient: fakeStatusJev{st: st}, now: time.Now}
		if b := s.budgets(); b.DecisionExhausted != tc.exhausted || b.DecisionReserveOnly != tc.reserved {
			t.Errorf("%s: exhausted %v reserve_only %v, want %v %v", tc.name, b.DecisionExhausted, b.DecisionReserveOnly, tc.exhausted, tc.reserved)
		}
	}
}

func TestKnowledgeStatusWhileTheEngineIsSilent(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    grpc.EngineHealth
		want string
	}{
		{"no engine", grpc.EngineHealth{State: stateDisabled}, stateDisabled},
		{"not checked yet", grpc.EngineHealth{State: grpc.StateUnknown}, stateUnknown},
		{"down", grpc.EngineHealth{State: grpc.StateDown}, stateUnknown},
		{"without the add-on", grpc.EngineHealth{State: grpc.StateUp, Response: &pb.HealthResponse{Healthy: true}}, stateDisabled},
		{"with the add-on", grpc.EngineHealth{State: grpc.StateUp, Response: &pb.HealthResponse{Healthy: true, KnowledgeEnabled: true}}, stateUp},
	} {
		if k := knowledgeStatus(tc.h); k.State != tc.want {
			t.Errorf("%s: knowledge %s, want %s", tc.name, k.State, tc.want)
		}
	}
}

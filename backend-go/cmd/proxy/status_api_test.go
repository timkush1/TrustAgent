package main

import (
	"context"
	"testing"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
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
		RPMLimit:  1000, RPMUsed: 7, Calls: 12, CostUSD: 0.0003,
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
		policy: &policy.Policy{Source: policy.SourceBuiltin}, auth: &apiAuth{keys: authz.New(nil, "keys")}}
	st := s.system(context.Background())
	if st.Engine.MaxAudits == nil || *st.Engine.MaxAudits != 16 || len(st.Warnings) != 1 || st.Warnings[0] != "Engine: LLM_MODEL is ignored under auto" {
		t.Errorf("engine %+v warnings %v", st.Engine, st.Warnings)
	}
}

type fixedHealth struct{ h grpc.EngineHealth }

func (f fixedHealth) Health() grpc.EngineHealth { return f.h }

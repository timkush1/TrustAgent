package main

import (
	"testing"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/grpc"
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

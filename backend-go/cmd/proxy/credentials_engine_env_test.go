package main

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
	"github.com/truthtable/backend-go/internal/runtimecfg"
)

// TestCredentialWriteAsksAnEngineThatHasNotReportedYet (D80): the engine was still starting at the
// gateway's first health check, and the next one is an hour away. A write that binds the judge
// asks the engine once first, so a judge its env sets (LLM_PROVIDER) is skipped as env-managed
// instead of being bound, and announced as bound, behind the engine's back.
func TestCredentialWriteAsksAnEngineThatHasNotReportedYet(t *testing.T) {
	var up atomic.Bool
	var infos atomic.Int32
	eng := enginetest.Start(t, &enginetest.Engine{
		HealthFunc: func(context.Context) (*pb.HealthResponse, error) {
			if !up.Load() {
				return nil, status.Error(codes.Unavailable, "starting")
			}
			return &pb.HealthResponse{Healthy: true, Version: "2.0.0"}, nil
		},
		EngineInfoFunc: func(context.Context) (*pb.EngineInfo, error) {
			infos.Add(1)
			return &pb.EngineInfo{Version: "2.0.0", EnvManaged: []string{"LLM_PROVIDER"}}, nil
		},
	})
	r := newCredRig(t, credRigOpts{engine: eng})
	srv := r.env.srv
	if st := srv.pinger.Health().State; st != grpc.StateDown {
		t.Fatalf("the boot health check should have found the starting engine down, got %q", st)
	}
	if slices.Contains(srv.rt.manager.Current().EngineEnv, "LLM_PROVIDER") {
		t.Fatal("the engine's env-managed keys are known before it ever answered")
	}

	up.Store(true)
	res := r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credGoodKey, "bind": []string{"decision_model", "judge"}})
	if !res.Stored || res.Verification == nil || !res.Verification.OK {
		t.Fatalf("write result: stored %v verification %+v", res.Stored, res.Verification)
	}
	if !slices.Equal(res.Bindings.Applied, []string{"decision_model"}) ||
		!slices.Equal(res.Bindings.Skipped, []CredentialBindSkip{{Target: "judge", Reason: runtimecfg.SkipEnvManaged}}) {
		t.Errorf("bindings = %+v, want decision_model applied and judge skipped as env_managed", res.Bindings)
	}
	if b := srv.rt.manager.Current().Settings.Bindings.Judge; b != nil {
		t.Errorf("the judge binding was stored as %q although the engine's env sets the judge", *b)
	}

	// Once the engine has reported, a write does not ask it again.
	before := infos.Load()
	r.put("openrouter", map[string]any{"provider": "openrouter", "secret": credOtherGoodKey, "bind": []string{"decision_model", "judge"}})
	if after := infos.Load(); after != before {
		t.Errorf("a write after the engine reported asked it again (%d EngineInfo calls, was %d)", after, before)
	}
}

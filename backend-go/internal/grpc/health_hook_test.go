package grpc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
)

// TestPingerOnHealthSeesEveryCheck (CN-3): OnHealth hooks get every check's result, unchanged
// states included, with the runtime configuration the engine reports and its env-managed keys;
// the transition callback still fires on transitions only.
func TestPingerOnHealthSeesEveryCheck(t *testing.T) {
	version, down := int64(0), false
	eng := enginetest.Start(t, &enginetest.Engine{
		HealthFunc: func(context.Context) (*pb.HealthResponse, error) {
			if down {
				return nil, errors.New("down")
			}
			return &pb.HealthResponse{Healthy: true, Version: "2.0.0", RuntimeConfigVersion: version, RuntimeConfigEpoch: "epoch-a"}, nil
		},
		EngineInfoFunc: func(context.Context) (*pb.EngineInfo, error) {
			return &pb.EngineInfo{Version: "2.0.0", EnvManaged: []string{"ANTHROPIC_API_KEY"}, RuntimeConfigVersion: version}, nil
		},
	})
	transitions := 0
	p := grpc.NewPinger(client(t, eng, time.Second, ""), time.Hour, func(grpc.EngineHealth) { transitions++ })
	var seen []grpc.EngineHealth
	p.OnHealth(func(h grpc.EngineHealth) { seen = append(seen, h) })
	p.OnHealth(nil) // ignored
	ctx := context.Background()
	p.Check(ctx)
	version = 3
	p.Check(ctx)
	down = true
	p.Check(ctx)
	if len(seen) != 3 || transitions != 2 {
		t.Fatalf("hook saw %d checks, transitions %d; want 3 and 2", len(seen), transitions)
	}
	if r := seen[1].Response; r.GetRuntimeConfigVersion() != 3 || r.GetRuntimeConfigEpoch() != "epoch-a" {
		t.Fatalf("second check reports %v", r)
	}
	if info := seen[1].Info; info == nil || len(info.GetEnvManaged()) != 1 || info.GetRuntimeConfigVersion() != 3 {
		t.Fatalf("engine info after a runtime config change: %v (asked again when the version moves)", info)
	}
	if seen[2].Response != nil || seen[2].State != grpc.StateDown {
		t.Fatalf("down check: %+v", seen[2])
	}
}

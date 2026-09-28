package grpc_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
)

func client(t *testing.T, eng *enginetest.Engine, timeout time.Duration, token string) *grpc.AuditClient {
	t.Helper()
	c, err := grpc.NewAuditClient(eng.Target, timeout, token, eng.Dialer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestEvaluateSendsTheRequestAndTheInternalToken(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: func(_ context.Context, req *pb.AuditRequest) (*pb.AuditResult, error) {
		return &pb.AuditResult{AuditId: req.GetAuditId(), Status: pb.AuditStatus_AUDIT_STATUS_COMPLETED,
			Verdict: pb.AuditVerdict_AUDIT_VERDICT_GROUNDED}, nil
	}})
	c := client(t, eng, time.Second, "s3cret")
	res, err := c.Evaluate(context.Background(), &pb.AuditRequest{AuditId: "a1", TraceId: "t1", Query: "q", Response: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetAuditId() != "a1" || res.GetVerdict() != pb.AuditVerdict_AUDIT_VERDICT_GROUNDED {
		t.Errorf("result %v", res)
	}
	reqs := eng.Requests()
	if len(reqs) != 1 || reqs[0].GetTraceId() != "t1" || reqs[0].GetResponse() != "r" {
		t.Errorf("requests %v", reqs)
	}
	if md := eng.Metadata(); len(md) != 1 || len(md[0].Get(grpc.InternalTokenHeader)) != 1 || md[0].Get(grpc.InternalTokenHeader)[0] != "s3cret" {
		t.Errorf("metadata %v", md)
	}
}

func TestEvaluateWithoutTokenSendsNone(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{})
	if _, err := client(t, eng, time.Second, "").Evaluate(context.Background(), &pb.AuditRequest{}); err != nil {
		t.Fatal(err)
	}
	if md := eng.Metadata(); len(md) != 1 || len(md[0].Get(grpc.InternalTokenHeader)) != 0 {
		t.Errorf("metadata %v", md)
	}
}

func TestEvaluateTimesOut(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{EvaluateFunc: func(ctx context.Context, _ *pb.AuditRequest) (*pb.AuditResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	_, err := client(t, eng, 50*time.Millisecond, "").Evaluate(context.Background(), &pb.AuditRequest{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("want DeadlineExceeded, got %v", err)
	}
}

func TestPingerStates(t *testing.T) {
	healthy := true
	fail := false
	eng := enginetest.Start(t, &enginetest.Engine{HealthFunc: func(context.Context) (*pb.HealthResponse, error) {
		if fail {
			return nil, errors.New("down")
		}
		return &pb.HealthResponse{Healthy: healthy, Dependencies: map[string]bool{"judge": healthy}}, nil
	}})
	var changes []string
	p := grpc.NewPinger(client(t, eng, time.Second, ""), time.Hour, func(h grpc.EngineHealth) { changes = append(changes, h.State) })
	if h := p.Health(); h.State != grpc.StateUnknown || h.Reachable() {
		t.Errorf("before the first check: %+v", h)
	}
	ctx := context.Background()
	if h := p.Check(ctx); h.State != grpc.StateUp || !h.Reachable() || h.Response == nil {
		t.Errorf("healthy: %+v", h)
	}
	p.Check(ctx) // no transition
	healthy = false
	if h := p.Check(ctx); h.State != grpc.StateDegraded || !h.Reachable() || h.Detail != "the audit engine reports judge unhealthy" {
		t.Errorf("degraded: %+v", h)
	}
	fail = true
	if h := p.Check(ctx); h.State != grpc.StateDown || h.Reachable() || h.Response != nil {
		t.Errorf("down: %+v", h)
	}
	if p.Health().State != grpc.StateDown {
		t.Errorf("cached %+v", p.Health())
	}
	if got := len(changes); got != 3 || changes[0] != grpc.StateUp || changes[2] != grpc.StateDown {
		t.Errorf("transitions %v", changes)
	}
}

func TestPingerReadsAnEngineWithoutAJudgeAsUp(t *testing.T) {
	deps := map[string]bool{"judge": false}
	eng := enginetest.Start(t, &enginetest.Engine{HealthFunc: func(context.Context) (*pb.HealthResponse, error) {
		return &pb.HealthResponse{Healthy: false, JudgeProvider: "none", Dependencies: deps}, nil
	}})
	p := grpc.NewPinger(client(t, eng, time.Second, ""), time.Hour, nil)
	if h := p.Check(context.Background()); h.State != grpc.StateUp || !strings.Contains(h.Detail, "no deep-audit judge") {
		t.Errorf("no judge: %+v", h)
	}
	// Another failing dependency still degrades the engine.
	deps["qdrant"] = false
	if h := p.Check(context.Background()); h.State != grpc.StateDegraded {
		t.Errorf("no judge and Qdrant down: %+v", h)
	}
}

func TestPingerCachesEngineInfoPerEngineVersion(t *testing.T) {
	version, calls := "2.0.0", 0
	eng := enginetest.Start(t, &enginetest.Engine{
		HealthFunc: func(context.Context) (*pb.HealthResponse, error) {
			return &pb.HealthResponse{Healthy: true, Version: version, Dependencies: map[string]bool{"judge": true}}, nil
		},
		EngineInfoFunc: func(context.Context) (*pb.EngineInfo, error) {
			calls++
			return &pb.EngineInfo{Version: version, MaxAudits: 16, Warnings: []string{"w"}}, nil
		},
	})
	p := grpc.NewPinger(client(t, eng, time.Second, ""), time.Hour, nil)
	ctx := context.Background()
	if h := p.Check(ctx); h.Info.GetMaxAudits() != 16 || calls != 1 {
		t.Fatalf("first check: info %v calls %d", h.Info, calls)
	}
	p.Check(ctx)
	if calls != 1 || p.Health().Info.GetVersion() != "2.0.0" {
		t.Errorf("the same engine is asked once: calls %d", calls)
	}
	version = "2.0.1" // a restarted engine
	if h := p.Check(ctx); calls != 2 || h.Info.GetVersion() != "2.0.1" {
		t.Errorf("a new engine version is asked again: calls %d info %v", calls, h.Info)
	}
}

func TestPingerAsksEngineInfoAgainWhenTheJudgeChanges(t *testing.T) {
	// The owner adds a judge key and restarts the engine within one ping interval: same version,
	// never seen down, but a different judge.
	health := &pb.HealthResponse{Healthy: false, Version: "2.0.0", JudgeProvider: "none", Dependencies: map[string]bool{"judge": false}}
	calls := 0
	eng := enginetest.Start(t, &enginetest.Engine{
		HealthFunc: func(context.Context) (*pb.HealthResponse, error) { return health, nil },
		EngineInfoFunc: func(context.Context) (*pb.EngineInfo, error) {
			calls++
			info := &pb.EngineInfo{Version: "2.0.0", Judge: &pb.JudgeInfo{EffectiveProvider: health.GetJudgeProvider(), CredentialSource: "none"}}
			if health.GetJudgeProvider() == "none" {
				info.Warnings = []string{"no judge configured"}
			} else {
				info.Judge.CredentialSource = "env"
			}
			return info, nil
		},
	})
	p := grpc.NewPinger(client(t, eng, time.Second, ""), time.Hour, nil)
	ctx := context.Background()
	if h := p.Check(ctx); calls != 1 || len(h.Info.GetWarnings()) != 1 {
		t.Fatalf("no judge: calls %d info %v", calls, h.Info)
	}
	health = &pb.HealthResponse{Healthy: true, Version: "2.0.0", JudgeProvider: "anthropic", JudgeModel: "claude-haiku-4-5",
		Dependencies: map[string]bool{"judge": true}}
	h := p.Check(ctx)
	if calls != 2 || len(h.Info.GetWarnings()) != 0 || h.Info.GetJudge().GetCredentialSource() != "env" {
		t.Errorf("a new judge is asked again: calls %d info %v", calls, h.Info)
	}
	p.Check(ctx)
	if calls != 2 {
		t.Errorf("the same setup is asked once: calls %d", calls)
	}
}

func TestPingerWithoutEngineInfoKeepsHealth(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{}) // GetEngineInfo is UNIMPLEMENTED
	p := grpc.NewPinger(client(t, eng, time.Second, ""), time.Hour, nil)
	if h := p.Check(context.Background()); h.State != grpc.StateUp || h.Info != nil {
		t.Errorf("health %+v", h)
	}
}

func TestPingerRunStopsWithTheContext(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{})
	p := grpc.NewPinger(client(t, eng, time.Second, ""), 5*time.Millisecond, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	deadline := time.After(5 * time.Second)
	for p.Health().State != grpc.StateUp {
		select {
		case <-deadline:
			t.Fatal("no health check ran")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

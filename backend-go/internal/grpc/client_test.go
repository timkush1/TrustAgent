package grpc_test

import (
	"context"
	"errors"
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

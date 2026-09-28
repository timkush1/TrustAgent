// Package enginetest runs a fake audit engine (AuditService) on an in-memory listener, for tests
// of the proxy's engine client and the audit worker. It needs no network and no Python.
//
//	eng := enginetest.Start(t, &enginetest.Engine{Evaluate: func(...) {...}})
//	client, _ := grpc.NewAuditClient(eng.Target, time.Second, "", eng.Dialer)
package enginetest

import (
	"context"
	"net"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/truthtable/backend-go/api/audit/v1"
)

// Engine is a scripted AuditService. A nil func answers healthy / an empty completed result.
type Engine struct {
	pb.UnimplementedAuditServiceServer

	EvaluateFunc func(ctx context.Context, req *pb.AuditRequest) (*pb.AuditResult, error)
	HealthFunc   func(ctx context.Context) (*pb.HealthResponse, error)
	// EngineInfoFunc answers GetEngineInfo; nil answers UNIMPLEMENTED (an engine without the RPC).
	EngineInfoFunc func(ctx context.Context) (*pb.EngineInfo, error)

	mu       sync.Mutex
	requests []*pb.AuditRequest
	metadata []metadata.MD

	// Target and Dialer connect a client to the engine (set by Start).
	Target string
	Dialer grpc.DialOption
}

// Evaluate implements AuditService.Evaluate.
func (e *Engine) Evaluate(ctx context.Context, req *pb.AuditRequest) (*pb.AuditResult, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	e.mu.Lock()
	e.requests = append(e.requests, req)
	e.metadata = append(e.metadata, md)
	e.mu.Unlock()
	if e.EvaluateFunc != nil {
		return e.EvaluateFunc(ctx, req)
	}
	return &pb.AuditResult{AuditId: req.GetAuditId(), TraceId: req.GetTraceId(), Status: pb.AuditStatus_AUDIT_STATUS_COMPLETED,
		Verdict: pb.AuditVerdict_AUDIT_VERDICT_NO_CLAIMS, ScoringVersion: "v2"}, nil
}

// GetEngineInfo implements AuditService.GetEngineInfo.
func (e *Engine) GetEngineInfo(ctx context.Context, req *pb.EngineInfoRequest) (*pb.EngineInfo, error) {
	if e.EngineInfoFunc != nil {
		return e.EngineInfoFunc(ctx)
	}
	return e.UnimplementedAuditServiceServer.GetEngineInfo(ctx, req)
}

// HealthCheck implements AuditService.HealthCheck.
func (e *Engine) HealthCheck(ctx context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	e.mu.Lock()
	e.metadata = append(e.metadata, md)
	e.mu.Unlock()
	if e.HealthFunc != nil {
		return e.HealthFunc(ctx)
	}
	return &pb.HealthResponse{Healthy: true, Version: "test", Dependencies: map[string]bool{"judge": true}}, nil
}

// Requests returns the Evaluate requests received so far.
func (e *Engine) Requests() []*pb.AuditRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*pb.AuditRequest(nil), e.requests...)
}

// Metadata returns the incoming metadata of every RPC received so far.
func (e *Engine) Metadata() []metadata.MD {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]metadata.MD(nil), e.metadata...)
}

// Start serves e on an in-memory listener until the test ends and fills Target and Dialer.
func Start(t testing.TB, e *Engine) *Engine {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterAuditServiceServer(srv, e)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	e.Target = "passthrough:///enginetest"
	e.Dialer = grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})
	return e
}

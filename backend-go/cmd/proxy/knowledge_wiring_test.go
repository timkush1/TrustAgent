package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/proxy"
)

// fakeRetrieveClient answers RetrieveContext with resp or err and records the requests.
type fakeRetrieveClient struct {
	resp      *pb.RetrieveContextResponse
	err       error
	reqs      []*pb.RetrieveContextRequest
	deadlines []time.Duration
}

func (f *fakeRetrieveClient) RetrieveContext(_ context.Context, req *pb.RetrieveContextRequest, deadline time.Duration) (*pb.RetrieveContextResponse, error) {
	f.reqs = append(f.reqs, req)
	f.deadlines = append(f.deadlines, deadline)
	return f.resp, f.err
}

func TestEngineRetrieverMapsTheEngineAnswer(t *testing.T) {
	fc := &fakeRetrieveClient{resp: &pb.RetrieveContextResponse{IndexState: "ready", Documents: []*pb.ContextDocument{
		{Id: "doc-1:chunk-0", Content: "Annual plans refund within 30 days.", Label: "refunds.md", Source: "knowledge"},
		{Id: "doc-2:chunk-4", Content: "Support answers within 4 hours.", Metadata: map[string]string{"title": "SLA"}},
		{Id: "doc-3:chunk-1", Content: "No label at all."},
	}}}
	r := &engineRetriever{engine: fc, deadline: 150 * time.Millisecond}
	ctx := proxy.WithKnowledgeOptions(context.Background(), proxy.KnowledgeOptions{TopK: 3, MinScore: 0.25})
	items, err := r.Retrieve(ctx, "How long is the refund window?", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []evidence.Item{
		{ID: "doc-1:chunk-0", Source: evidence.SourceKnowledge, Label: "refunds.md", Text: "Annual plans refund within 30 days."},
		{ID: "doc-2:chunk-4", Source: evidence.SourceKnowledge, Label: "SLA", Text: "Support answers within 4 hours."},
		{ID: "doc-3:chunk-1", Source: evidence.SourceKnowledge, Text: "No label at all."},
	}
	if !slices.Equal(items, want) {
		t.Errorf("items %+v, want %+v", items, want)
	}
	if len(fc.reqs) != 1 {
		t.Fatalf("%d calls", len(fc.reqs))
	}
	req := fc.reqs[0]
	if req.GetQuery() != "How long is the refund window?" || req.GetResponse() != "" || req.GetTopK() != 3 || req.GetMinScore() != 0.25 {
		t.Errorf("request %v, want the query with the route's top_k and min_score", req)
	}
	if fc.deadlines[0] != 150*time.Millisecond {
		t.Errorf("deadline %v, want TRUTHTABLE_KNOWLEDGE_DEADLINE", fc.deadlines[0])
	}
}

func TestEngineRetrieverFailures(t *testing.T) {
	cases := []struct {
		name string
		resp *pb.RetrieveContextResponse
		err  error
		want error
	}{
		{name: "knowledge_disabled", err: status.Error(codes.FailedPrecondition, "knowledge_disabled"), want: proxy.ErrKnowledgeDisabled},
		{name: "old engine", err: status.Error(codes.Unimplemented, "unknown method"), want: proxy.ErrKnowledgeDisabled},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "deadline"), want: context.DeadlineExceeded},
		{name: "index disabled", resp: &pb.RetrieveContextResponse{IndexState: "disabled"}, want: proxy.ErrKnowledgeDisabled},
		{name: "index error", resp: &pb.RetrieveContextResponse{IndexState: "error"}, want: errKnowledgeIndex},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &engineRetriever{engine: &fakeRetrieveClient{resp: tc.resp, err: tc.err}}
			if _, err := r.Retrieve(context.Background(), "q", ""); !errors.Is(err, tc.want) {
				t.Errorf("error %v, want %v", err, tc.want)
			}
		})
	}
	// Unavailable is an error the gateway reports as such.
	r := &engineRetriever{engine: &fakeRetrieveClient{err: status.Error(codes.Unavailable, "down")}}
	if _, err := r.Retrieve(context.Background(), "q", ""); err == nil || errors.Is(err, proxy.ErrKnowledgeDisabled) {
		t.Errorf("unavailable engine: %v, want a plain error", err)
	}
	// An index with nothing in it is no hit, not an error.
	r = &engineRetriever{engine: &fakeRetrieveClient{resp: &pb.RetrieveContextResponse{IndexState: "empty"}}}
	if items, err := r.Retrieve(context.Background(), "q", ""); err != nil || len(items) != 0 {
		t.Errorf("empty index: %v, %v", items, err)
	}
}

func TestEngineRetrieverSkipsAnEngineWithoutKnowledge(t *testing.T) {
	fc := &fakeRetrieveClient{resp: &pb.RetrieveContextResponse{IndexState: "ready"}}
	r := &engineRetriever{engine: fc, knowledgeOff: func() bool { return true }}
	if _, err := r.Retrieve(context.Background(), "q", ""); !errors.Is(err, proxy.ErrKnowledgeDisabled) {
		t.Errorf("error %v, want ErrKnowledgeDisabled", err)
	}
	if len(fc.reqs) != 0 {
		t.Error("an engine whose health reports knowledge off was called")
	}
}

func TestKnowledgeRetrieverNeedsTheAddOnAndAnEngine(t *testing.T) {
	client, err := grpc.NewAuditClient("passthrough:///unused", time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for _, tc := range []struct {
		enabled bool
		engine  *grpc.AuditClient
		want    bool
	}{{false, client, false}, {true, nil, false}, {true, client, true}} {
		s := &server{cfg: &config.Config{KnowledgeEnabled: tc.enabled, KnowledgeDeadline: 150 * time.Millisecond}, status: &statusReporter{}}
		if got := s.knowledgeRetriever(serverDeps{engine: tc.engine}) != nil; got != tc.want {
			t.Errorf("enabled %v, engine %v: retriever %v, want %v", tc.enabled, tc.engine != nil, got, tc.want)
		}
	}
}

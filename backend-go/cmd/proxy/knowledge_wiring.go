package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/proxy"
)

// knowledgeRetriever is the gate's knowledge-base retriever over the engine's RetrieveContext
// (SPEC §7.1, D65): nil, so no request gets knowledge, unless the knowledge add-on is on
// (TRUTHTABLE_KNOWLEDGE_ENABLED) and there is an engine. Each call is bounded by
// TRUTHTABLE_KNOWLEDGE_DEADLINE on top of the route's own deadline.
func (s *server) knowledgeRetriever(d serverDeps) proxy.Retriever {
	if !s.cfg.KnowledgeEnabled || d.engine == nil {
		return nil
	}
	return &engineRetriever{
		engine: d.engine, deadline: s.cfg.KnowledgeDeadline,
		// The engine's health says whether its knowledge add-on runs; until it was checked the call
		// goes out and the engine answers for itself.
		knowledgeOff: func() bool {
			r := s.status.engineHealth().Response
			return r != nil && !r.GetKnowledgeEnabled()
		},
	}
}

// engineRetriever asks the engine for knowledge-base documents.
type engineRetriever struct {
	engine       retrieveClient
	deadline     time.Duration
	knowledgeOff func() bool
}

// retrieveClient is the part of the engine client the retriever calls.
type retrieveClient interface {
	RetrieveContext(ctx context.Context, req *pb.RetrieveContextRequest, deadline time.Duration) (*pb.RetrieveContextResponse, error)
}

var _ retrieveClient = (*grpc.AuditClient)(nil)

// Engine index states (RetrieveContextResponse.index_state).
const (
	indexDisabled = "disabled"
	indexError    = "error"
)

// errKnowledgeIndex is a retrieval the engine answered with index_state error.
var errKnowledgeIndex = errors.New("knowledge: the engine's knowledge index failed the search")

// Retrieve implements proxy.Retriever: the documents the engine found for query (with the route's
// top_k and min_score from ctx), as knowledge evidence items. The add-on off (the engine's health
// or its knowledge_disabled answer) is proxy.ErrKnowledgeDisabled; the deadline passing is
// context.DeadlineExceeded.
func (r *engineRetriever) Retrieve(ctx context.Context, query, response string) ([]evidence.Item, error) {
	if r.knowledgeOff != nil && r.knowledgeOff() {
		return nil, proxy.ErrKnowledgeDisabled
	}
	req := &pb.RetrieveContextRequest{Query: query, Response: response}
	if o, ok := proxy.KnowledgeOptionsFrom(ctx); ok {
		req.TopK, req.MinScore = clampInt32(o.TopK), float32(o.MinScore)
	}
	resp, err := r.engine.RetrieveContext(ctx, req, r.deadline)
	if err != nil {
		switch status.Code(err) {
		case codes.FailedPrecondition, codes.Unimplemented:
			return nil, proxy.ErrKnowledgeDisabled
		case codes.DeadlineExceeded:
			return nil, fmt.Errorf("knowledge: %w", context.DeadlineExceeded)
		}
		return nil, fmt.Errorf("knowledge: %w", err)
	}
	switch resp.GetIndexState() {
	case indexDisabled:
		return nil, proxy.ErrKnowledgeDisabled
	case indexError:
		return nil, errKnowledgeIndex
	}
	items := make([]evidence.Item, 0, len(resp.GetDocuments()))
	for _, d := range resp.GetDocuments() {
		label := strings.TrimSpace(d.GetLabel())
		if label == "" {
			label = strings.TrimSpace(d.GetMetadata()["title"])
		}
		items = append(items, evidence.Item{ID: d.GetId(), Source: evidence.SourceKnowledge, Label: label, Text: d.GetContent()})
	}
	return items, nil
}

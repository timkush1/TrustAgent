// Package grpc is the proxy's client of the audit engine (proto/evaluator.proto): Evaluate for
// deep audits, the engine health pinger, and the knowledge-base RPCs.
package grpc

import (
	"context"
	"fmt"
	"math"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/truthtable/backend-go/api/audit/v1"
)

// InternalTokenHeader is the metadata key of the internal token every RPC carries when one is
// configured (SPEC §2.2, TRUTHTABLE_INTERNAL_TOKEN).
const InternalTokenHeader = "x-trustagent-internal"

// healthTimeout bounds one engine health check; engineInfoTimeout one GetEngineInfo (the engine
// runs a bounded judge probe and Qdrant probes for it).
const (
	healthTimeout     = 2 * time.Second
	engineInfoTimeout = 4 * time.Second
)

// AuditClient talks to the audit engine (AuditService).
type AuditClient struct {
	conn    *grpc.ClientConn
	client  pb.AuditServiceClient
	timeout time.Duration
}

// NewAuditClient creates a client for the engine at address. The connection is established
// lazily on the first RPC, so a missing engine is not an error here. timeout bounds one Evaluate
// call (TRUTHTABLE_GRPC_TIMEOUT); a non-empty token is sent as x-trustagent-internal metadata on
// every RPC. opts are appended to the defaults (tests dial an in-memory listener).
func NewAuditClient(address string, timeout time.Duration, token string, opts ...grpc.DialOption) (*AuditClient, error) {
	dial := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if token != "" {
		dial = append(dial, grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any,
			cc *grpc.ClientConn, invoker grpc.UnaryInvoker, callOpts ...grpc.CallOption) error {
			return invoker(metadata.AppendToOutgoingContext(ctx, InternalTokenHeader, token), method, req, reply, cc, callOpts...)
		}))
	}
	conn, err := grpc.NewClient(address, append(dial, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client for %s: %w", address, err)
	}
	return &AuditClient{conn: conn, client: pb.NewAuditServiceClient(conn), timeout: timeout}, nil
}

// Evaluate runs one deep audit to completion (SPEC §2.2: Go's only audit call; M5 replaced the
// SubmitAudit + GetAuditResult polling). A failed audit is a result with status FAILED, not an
// error; an error means the engine could not be asked or did not answer in time.
func (c *AuditClient) Evaluate(ctx context.Context, req *pb.AuditRequest) (*pb.AuditResult, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	return c.client.Evaluate(ctx, req)
}

// Health asks the engine for its health (M4: the real engine state, not merely "a client
// exists").
func (c *AuditClient) Health(ctx context.Context) (*pb.HealthResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	return c.client.HealthCheck(ctx, &pb.HealthRequest{})
}

// EngineInfo asks the engine what it runs with (SPEC §2.2 GetEngineInfo): version, the judge and
// how it was chosen, knowledge, verifier, scoring settings, configuration warnings and its limits.
// The engine probes its judge for the answer, so it is bounded like a health check.
func (c *AuditClient) EngineInfo(ctx context.Context) (*pb.EngineInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, engineInfoTimeout)
	defer cancel()
	return c.client.GetEngineInfo(ctx, &pb.EngineInfoRequest{})
}

// IngestDocument represents a document to ingest into the knowledge base.
type IngestDocument struct {
	ID       string
	Content  string
	Metadata map[string]string
}

// ClaimIngest is the per-claim outcome of claim-level (Gate-1 gated) ingestion.
type ClaimIngest struct {
	ClaimID         string   `json:"claim_id"`
	Claim           string   `json:"claim"`
	SourceDocID     string   `json:"source_doc_id"`
	Status          string   `json:"status"` // "accepted" | "quarantined"
	EntailmentScore float64  `json:"entailment_score"`
	ConflictsWith   []string `json:"conflicts_with"`
}

// IngestResult summarizes one ingest call.
type IngestResult struct {
	DocumentsIngested int32         `json:"documents_ingested"`
	ClaimsAccepted    int32         `json:"claims_accepted"`
	ClaimsQuarantined int32         `json:"claims_quarantined"`
	ConflictsDetected int32         `json:"conflicts_detected"`
	ClaimResults      []ClaimIngest `json:"claim_results"`
}

var kbStatusNames = map[pb.KBClaimStatus]string{
	pb.KBClaimStatus_KB_CLAIM_STATUS_ACCEPTED:    "accepted",
	pb.KBClaimStatus_KB_CLAIM_STATUS_QUARANTINED: "quarantined",
}

// clampInt32 bounds an int before int32 conversion (gosec G115). Handlers
// already validate ranges; this keeps the conversion provably safe.
func clampInt32(v int) int32 {
	if v < 0 {
		return 0
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

// IngestTimeout bounds one IngestDocuments call: claim-level ingestion makes several LLM calls per
// document, so it gets four times the audit timeout.
func (c *AuditClient) IngestTimeout() time.Duration { return 4 * c.timeout }

// IngestDocuments sends documents to the Python engine for claim-level ingestion.
func (c *AuditClient) IngestDocuments(ctx context.Context, documents []IngestDocument) (*IngestResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.IngestTimeout())
	defer cancel()

	pbDocs := make([]*pb.ContextDocument, len(documents))
	for i, doc := range documents {
		pbDocs[i] = &pb.ContextDocument{
			Id:       doc.ID,
			Content:  doc.Content,
			Metadata: doc.Metadata,
		}
	}

	resp, err := c.client.IngestDocuments(ctx, &pb.IngestRequest{
		Documents: pbDocs,
	})
	if err != nil {
		return nil, fmt.Errorf("ingest documents failed: %w", err)
	}

	result := &IngestResult{
		DocumentsIngested: resp.DocumentsIngested,
		ClaimsAccepted:    resp.ClaimsAccepted,
		ClaimsQuarantined: resp.ClaimsQuarantined,
		ConflictsDetected: resp.ConflictsDetected,
		ClaimResults:      make([]ClaimIngest, len(resp.ClaimResults)),
	}
	for i, claim := range resp.ClaimResults {
		result.ClaimResults[i] = claimIngestFromProto(claim)
	}
	return result, nil
}

func claimIngestFromProto(claim *pb.ClaimIngestResult) ClaimIngest {
	conflicts := claim.ConflictsWith
	if conflicts == nil {
		conflicts = []string{}
	}
	return ClaimIngest{
		ClaimID:         claim.ClaimId,
		Claim:           claim.Claim,
		SourceDocID:     claim.SourceDocId,
		Status:          kbStatusNames[claim.Status],
		EntailmentScore: float64(claim.EntailmentScore),
		ConflictsWith:   conflicts,
	}
}

// KBClaim is a stored knowledge-base claim.
type KBClaim struct {
	ClaimID         string   `json:"claim_id"`
	Claim           string   `json:"claim"`
	SourceDocID     string   `json:"source_doc_id"`
	SourceExcerpt   string   `json:"source_excerpt"`
	Status          string   `json:"status"`
	EntailmentScore float64  `json:"entailment_score"`
	ConflictsWith   []string `json:"conflicts_with"`
	IngestedAtMs    int64    `json:"ingested_at_ms"`
}

func kbClaimFromProto(claim *pb.KBClaim) KBClaim {
	conflicts := claim.ConflictsWith
	if conflicts == nil {
		conflicts = []string{}
	}
	return KBClaim{
		ClaimID:         claim.ClaimId,
		Claim:           claim.Claim,
		SourceDocID:     claim.SourceDocId,
		SourceExcerpt:   claim.SourceExcerpt,
		Status:          kbStatusNames[claim.Status],
		EntailmentScore: float64(claim.EntailmentScore),
		ConflictsWith:   conflicts,
		IngestedAtMs:    claim.IngestedAtMs,
	}
}

// ListKBClaims pages through stored claims. status: "", "accepted", "quarantined".
func (c *AuditClient) ListKBClaims(ctx context.Context, limit, offset int, status string) ([]KBClaim, int32, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	statusFilter := pb.KBClaimStatus_KB_CLAIM_STATUS_UNSPECIFIED
	switch status {
	case "accepted":
		statusFilter = pb.KBClaimStatus_KB_CLAIM_STATUS_ACCEPTED
	case "quarantined":
		statusFilter = pb.KBClaimStatus_KB_CLAIM_STATUS_QUARANTINED
	}

	resp, err := c.client.ListKBClaims(ctx, &pb.ListKBClaimsRequest{
		Limit:        clampInt32(limit),
		Offset:       clampInt32(offset),
		StatusFilter: statusFilter,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list KB claims failed: %w", err)
	}

	claims := make([]KBClaim, len(resp.Claims))
	for i, claim := range resp.Claims {
		claims[i] = kbClaimFromProto(claim)
	}
	return claims, resp.Total, nil
}

// ConflictPair is two claims that contradict each other.
type ConflictPair struct {
	ClaimA KBClaim `json:"claim_a"`
	ClaimB KBClaim `json:"claim_b"`
}

// ListConflicts returns contradiction pairs detected at ingest time.
func (c *AuditClient) ListConflicts(ctx context.Context, limit int) ([]ConflictPair, int32, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.ListConflicts(ctx, &pb.ListConflictsRequest{Limit: clampInt32(limit)})
	if err != nil {
		return nil, 0, fmt.Errorf("list conflicts failed: %w", err)
	}

	pairs := make([]ConflictPair, len(resp.Conflicts))
	for i, pair := range resp.Conflicts {
		pairs[i] = ConflictPair{
			ClaimA: kbClaimFromProto(pair.ClaimA),
			ClaimB: kbClaimFromProto(pair.ClaimB),
		}
	}
	return pairs, resp.Total, nil
}

// KBStats summarizes the knowledge base.
type KBStats struct {
	TotalClaims   int32 `json:"total_claims"`
	Accepted      int32 `json:"accepted"`
	Quarantined   int32 `json:"quarantined"`
	ConflictPairs int32 `json:"conflict_pairs"`
}

// GetKBStats returns knowledge-base counters.
func (c *AuditClient) GetKBStats(ctx context.Context) (*KBStats, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.client.GetKBStats(ctx, &pb.KBStatsRequest{})
	if err != nil {
		return nil, fmt.Errorf("get KB stats failed: %w", err)
	}
	return &KBStats{
		TotalClaims:   resp.TotalClaims,
		Accepted:      resp.Accepted,
		Quarantined:   resp.Quarantined,
		ConflictPairs: resp.ConflictPairs,
	}, nil
}

func (c *AuditClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

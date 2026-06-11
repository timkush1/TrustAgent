package grpc

import (
	"context"
	"fmt"
	"math"
	"time"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type AuditClient struct {
	conn    *grpc.ClientConn
	client  pb.AuditServiceClient
	timeout time.Duration
}

type ClaimResult struct {
	Text       string
	Verdict    string
	Confidence float64
	Evidence   []string
}

type AuditResult struct {
	TrustScore            float64
	Claims                []*ClaimResult
	ReasoningTrace        string
	HallucinationDetected bool
	ProcessingTimeMs      int64
	StepTimings           map[string]int64
}

func NewAuditClient(address string, timeout time.Duration) (*AuditClient, error) {
	// Non-blocking dial - connection happens lazily on first RPC
	conn, err := grpc.Dial(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC client for %s: %w", address, err)
	}

	return &AuditClient{
		conn:    conn,
		client:  pb.NewAuditServiceClient(conn),
		timeout: timeout,
	}, nil
}

func (c *AuditClient) Evaluate(ctx context.Context, requestID, prompt, response string) (*AuditResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req := &pb.AuditRequest{
		RequestId: requestID,
		Query:     prompt,
		Response:  response,
	}

	submission, err := c.client.SubmitAudit(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("audit submission failed: %w", err)
	}

	resultReq := &pb.AuditResultRequest{
		AuditId: submission.AuditId,
	}

	var auditResult *pb.AuditResult
	for i := 0; i < 30; i++ {
		auditResult, err = c.client.GetAuditResult(ctx, resultReq)
		if err != nil {
			return nil, fmt.Errorf("failed to get audit result: %w", err)
		}
		if auditResult.Status == pb.AuditStatus_AUDIT_STATUS_COMPLETED {
			break
		}
		if auditResult.Status == pb.AuditStatus_AUDIT_STATUS_FAILED {
			return nil, fmt.Errorf("audit failed")
		}
		time.Sleep(100 * time.Millisecond)
	}

	if auditResult == nil || auditResult.Status != pb.AuditStatus_AUDIT_STATUS_COMPLETED {
		return nil, fmt.Errorf("audit timed out")
	}

	// Map protobuf VerificationStatus enum to lowercase verdict strings
	verdictMap := map[pb.VerificationStatus]string{
		pb.VerificationStatus_VERIFICATION_STATUS_SUPPORTED:           "supported",
		pb.VerificationStatus_VERIFICATION_STATUS_CONTRADICTED:        "unsupported",
		pb.VerificationStatus_VERIFICATION_STATUS_UNSUPPORTED:         "unsupported",
		pb.VerificationStatus_VERIFICATION_STATUS_PARTIALLY_SUPPORTED: "partially_supported",
		pb.VerificationStatus_VERIFICATION_STATUS_UNSPECIFIED:         "unknown",
	}

	claims := make([]*ClaimResult, len(auditResult.Claims))
	for i, claim := range auditResult.Claims {
		verdict := "unknown"
		if v, ok := verdictMap[claim.Status]; ok {
			verdict = v
		}
		claims[i] = &ClaimResult{
			Text:       claim.Claim,
			Verdict:    verdict,
			Confidence: float64(claim.Confidence),
			Evidence:   claim.Evidence,
		}
	}

	return &AuditResult{
		TrustScore:            float64(auditResult.FaithfulnessScore),
		Claims:                claims,
		ReasoningTrace:        auditResult.ReasoningTrace,
		HallucinationDetected: auditResult.HallucinationDetected,
		ProcessingTimeMs:      auditResult.ProcessingTimeMs,
		StepTimings:           auditResult.StepTimings,
	}, nil
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

// IngestDocuments sends documents to the Python engine for claim-level ingestion.
func (c *AuditClient) IngestDocuments(ctx context.Context, documents []IngestDocument) (*IngestResult, error) {
	// Claim-level ingestion makes multiple LLM calls per document, so allow
	// a longer window than the standard audit timeout.
	ctx, cancel := context.WithTimeout(ctx, 4*c.timeout)
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

func (c *AuditClient) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	_, err := c.client.HealthCheck(ctx, &pb.HealthRequest{})
	if err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}
	return nil
}

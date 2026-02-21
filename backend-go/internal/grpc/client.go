package grpc

import (
	"context"
	"fmt"
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

// IngestDocuments sends documents to the Python engine for embedding and storage.
func (c *AuditClient) IngestDocuments(ctx context.Context, documents []IngestDocument) (int32, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
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
		return 0, fmt.Errorf("ingest documents failed: %w", err)
	}

	return resp.DocumentsIngested, nil
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

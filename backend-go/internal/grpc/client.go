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
}

type AuditResult struct {
	TrustScore float64
	Claims     []*ClaimResult
	Summary    string
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

	claims := make([]*ClaimResult, len(auditResult.Claims))
	for i, claim := range auditResult.Claims {
		claims[i] = &ClaimResult{
			Text:       claim.Claim,
			Verdict:    claim.Status.String(),
			Confidence: float64(claim.Confidence),
		}
	}

	return &AuditResult{
		TrustScore: float64(auditResult.FaithfulnessScore),
		Claims:     claims,
		Summary:    auditResult.ReasoningTrace,
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

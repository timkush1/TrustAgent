package worker

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/websocket"
)

type AuditJob struct {
	RequestID   string
	Prompt      string
	Response    string
	Model       string
	Timestamp   time.Time
	UserID      string
	RequestPath string
}

type Pool struct {
	workers     int
	queue       chan *AuditJob
	auditClient *grpc.AuditClient
	wsHub       *websocket.Hub
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
}

func NewPool(numWorkers, queueSize int, client *grpc.AuditClient, hub *websocket.Hub) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	return &Pool{
		workers:     numWorkers,
		queue:       make(chan *AuditJob, queueSize),
		auditClient: client,
		wsHub:       hub,
		ctx:         ctx,
		cancel:      cancel,
	}
}

func (p *Pool) Start() {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	log.Printf("Worker pool started with %d workers", p.workers)
}

func (p *Pool) Stop() {
	p.cancel()
	close(p.queue)
	p.wg.Wait()
	log.Printf("Worker pool stopped")
}

func (p *Pool) Submit(job *AuditJob) {
	select {
	case p.queue <- job:
		log.Printf("[%s] Job submitted to worker pool", job.RequestID)
	default:
		log.Printf("[%s] Worker queue full, dropping audit job", job.RequestID)
	}
}

func (p *Pool) QueueLength() int {
	return len(p.queue)
}

func (p *Pool) worker(id int) {
	defer p.wg.Done()
	log.Printf("Worker %d started", id)

	for {
		select {
		case <-p.ctx.Done():
			log.Printf("Worker %d stopping", id)
			return
		case job, ok := <-p.queue:
			if !ok {
				log.Printf("Worker %d: queue closed", id)
				return
			}
			p.processJob(id, job)
		}
	}
}

func (p *Pool) processJob(workerID int, job *AuditJob) {
	startTime := time.Now()
	log.Printf("[%s] Worker %d processing job", job.RequestID, workerID)

	if p.auditClient == nil {
		log.Printf("[%s] No audit client available, skipping audit", job.RequestID)
		return
	}

	result, err := p.auditClient.Evaluate(p.ctx, job.RequestID, job.Prompt, job.Response)
	if err != nil {
		log.Printf("[%s] Audit failed: %v", job.RequestID, err)
		if p.wsHub != nil {
			p.wsHub.Broadcast(&websocket.AuditEvent{
				Type:      "audit_error",
				RequestID: job.RequestID,
				Timestamp: time.Now(),
				Error:     err.Error(),
			})
		}
		return
	}

	duration := time.Since(startTime)
	log.Printf("[%s] Audit complete in %v (score: %.2f, claims: %d)",
		job.RequestID, duration, result.TrustScore, len(result.Claims))

	if p.wsHub != nil {
		// Create audit result in the format expected by the frontend
		auditResult := &websocket.AuditResult{
			AuditID:               job.RequestID, // Use request ID as audit ID for now
			RequestID:             job.RequestID,
			UserQuery:             job.Prompt,
			LLMResponse:           job.Response,
			FaithfulnessScore:     result.TrustScore,
			RelevancyScore:        result.TrustScore, // Same for now
			OverallScore:          result.TrustScore,
			HallucinationDetected: result.TrustScore < 0.8,
			Claims:                convertClaimsToVerifications(result.Claims),
			ReasoningTrace:        "",
			ProcessingTimeMs:      duration.Milliseconds(),
			Timestamp:             time.Now().Format(time.RFC3339),
			Provider:              "proxy",
			Model:                 job.Model,
		}
		p.wsHub.BroadcastAuditResult(auditResult)
	}
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func convertClaims(claims []*grpc.ClaimResult) []websocket.ClaimInfo {
	result := make([]websocket.ClaimInfo, len(claims))
	for i, c := range claims {
		result[i] = websocket.ClaimInfo{
			Text:       c.Text,
			Verdict:    c.Verdict,
			Confidence: c.Confidence,
		}
	}
	return result
}

func convertClaimsToVerifications(claims []*grpc.ClaimResult) []websocket.ClaimVerification {
	result := make([]websocket.ClaimVerification, len(claims))
	for i, c := range claims {
		// Map verdict to status (frontend expects uppercase)
		status := "UNSUPPORTED"
		if c.Verdict == "supported" {
			status = "SUPPORTED"
		} else if c.Verdict == "partially_supported" {
			status = "PARTIALLY_SUPPORTED"
		}

		result[i] = websocket.ClaimVerification{
			Claim:      c.Text,
			Status:     status,
			Confidence: c.Confidence,
			Evidence:   []string{}, // No evidence for now
		}
	}
	return result
}

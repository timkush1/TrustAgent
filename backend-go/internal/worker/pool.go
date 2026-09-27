package worker

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/metrics"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/websocket"
)

type AuditJob struct {
	// RequestID is the id echoed to the client in X-Request-ID (client-supplied
	// or proxy-generated). It is never used as a key: it is persisted only as
	// the non-unique client_request_id, and only when it passes
	// store.ClientRequestID (B4).
	RequestID string
	// TraceID and AuditID are server-generated UUIDv7s. Submit assigns any
	// that are empty; a job without a gateway trace uses TraceID = AuditID.
	TraceID     string
	AuditID     string
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
	store       store.Store
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
}

// AttachStore enables persistence of completed audits. Must be called before
// Start; a nil store (the default) disables persistence.
func (p *Pool) AttachStore(s store.Store) {
	p.store = s
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

// Submit enqueues job, assigning its server-side ids first (see assignIDs).
// The pool owns job afterwards.
func (p *Pool) Submit(job *AuditJob) {
	assignIDs(job)
	select {
	case p.queue <- job:
		log.Printf("[%s] Job submitted to worker pool (audit_id=%s)", job.RequestID, job.AuditID)
	default:
		log.Printf("[%s] Worker queue full, dropping audit job", job.RequestID)
	}
}

// assignIDs fills in the server-generated ids of a job (B4): a new UUIDv7
// audit_id, and trace_id = audit_id when the submitter has no trace. The
// client-supplied RequestID never becomes an id.
func assignIDs(job *AuditJob) {
	if job.AuditID == "" {
		job.AuditID = store.NewID()
	}
	if job.TraceID == "" {
		job.TraceID = job.AuditID
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
	log.Printf("[%s] Worker %d processing job (trace_id=%s audit_id=%s)",
		job.RequestID, workerID, job.TraceID, job.AuditID)

	metrics.ActiveAudits.Inc()
	defer metrics.ActiveAudits.Dec()

	if p.auditClient == nil {
		log.Printf("[%s] No audit client available, skipping audit", job.RequestID)
		return
	}

	// The engine's request_id is the trace id, never the client-supplied id.
	result, err := p.auditClient.Evaluate(p.ctx, job.TraceID, job.Prompt, job.Response)
	if err != nil {
		log.Printf("[%s] Audit failed: %v", job.RequestID, err)
		metrics.AuditsTotal.WithLabelValues("error").Inc()
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

	// Record metrics
	metrics.AuditsTotal.WithLabelValues("success").Inc()
	metrics.AuditDuration.Observe(duration.Seconds())
	metrics.FaithfulnessScore.Observe(result.TrustScore)
	if result.HallucinationDetected {
		metrics.HallucinationsDetected.Inc()
	}
	for _, c := range result.Claims {
		metrics.ClaimsTotal.WithLabelValues(c.Verdict).Inc()
	}

	if p.wsHub != nil {
		// Create audit result using actual values from the Python audit engine.
		// faithfulness is the one real score the pipeline computes; overall
		// mirrors it (no separate relevancy metric is fabricated).
		auditResult := &websocket.AuditResult{
			AuditID:               job.AuditID,
			RequestID:             job.RequestID,
			UserQuery:             job.Prompt,
			LLMResponse:           job.Response,
			FaithfulnessScore:     result.TrustScore,
			OverallScore:          result.TrustScore,
			HallucinationDetected: result.HallucinationDetected,
			Claims:                convertClaimsToVerifications(result.Claims),
			ReasoningTrace:        result.ReasoningTrace,
			ProcessingTimeMs:      duration.Milliseconds(),
			Timestamp:             time.Now().Format(time.RFC3339),
			Provider:              "proxy",
			Model:                 job.Model,
			StepTimings:           result.StepTimings,
		}
		p.wsHub.BroadcastAuditResult(auditResult)
	}

	if p.store != nil {
		p.persistAudit(job, result, duration)
	}
}

// persistAudit writes the completed audit to the store. Persistence is
// best-effort relative to the live broadcast: a failed write is logged but
// never blocks or fails the audit pipeline.
func (p *Pool) persistAudit(job *AuditJob, result *grpc.AuditResult, duration time.Duration) {
	record := newAuditRecord(job, result, duration)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.store.SaveAudit(ctx, record); err != nil {
		log.Printf("[%s] Failed to persist audit %s: %v", job.RequestID, job.AuditID, err)
	}
}

// newAuditRecord maps a completed job onto the stored record. The ids are the
// job's server-generated ones (request_id = trace_id, as on the gRPC wire);
// the client's RequestID is only offered as client_request_id, which
// SaveAudit validates.
func newAuditRecord(job *AuditJob, result *grpc.AuditResult, duration time.Duration) *store.AuditRecord {
	claims := make([]store.ClaimRecord, len(result.Claims))
	for i, c := range result.Claims {
		evidence := c.Evidence
		if evidence == nil {
			evidence = []string{}
		}
		text := c.Text
		claims[i] = store.ClaimRecord{
			Claim:      &text,
			Status:     normalizeStatus(c.Verdict),
			Confidence: c.Confidence,
			Evidence:   evidence,
		}
	}

	var clientRequestID *string
	if job.RequestID != "" {
		id := job.RequestID
		clientRequestID = &id
	}
	prompt, response := job.Prompt, job.Response
	score := result.TrustScore
	grade := store.GradeForScore(score)

	return &store.AuditRecord{
		AuditID:               job.AuditID,
		TraceID:               job.TraceID,
		RequestID:             job.TraceID,
		ClientRequestID:       clientRequestID,
		UserQuery:             &prompt,
		LLMResponse:           &response,
		Model:                 job.Model,
		FaithfulnessScore:     &score,
		Grade:                 &grade,
		HallucinationDetected: result.HallucinationDetected,
		ReasoningTrace:        result.ReasoningTrace,
		ProcessingTimeMs:      duration.Milliseconds(),
		StepTimings:           result.StepTimings,
		CreatedAt:             time.Now().UTC(),
		Claims:                claims,
	}
}

func normalizeStatus(verdict string) string {
	switch verdict {
	case "supported":
		return "SUPPORTED"
	case "contradicted":
		return "CONTRADICTED"
	case "unsupported":
		return "UNSUPPORTED"
	case "partially_supported":
		return "PARTIALLY_SUPPORTED"
	default:
		return "UNKNOWN"
	}
}

func convertClaimsToVerifications(claims []*grpc.ClaimResult) []websocket.ClaimVerification {
	result := make([]websocket.ClaimVerification, len(claims))
	for i, c := range claims {
		evidence := c.Evidence
		if evidence == nil {
			evidence = []string{}
		}

		result[i] = websocket.ClaimVerification{
			Claim:      c.Text,
			Status:     normalizeStatus(c.Verdict),
			Confidence: c.Confidence,
			Evidence:   evidence,
		}
	}
	return result
}

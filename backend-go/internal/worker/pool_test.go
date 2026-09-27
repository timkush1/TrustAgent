package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/store"
)

func TestNewPool(t *testing.T) {
	pool := NewPool(5, 100, nil, nil)

	if pool == nil {
		t.Fatal("NewPool returned nil")
	}
	if pool.workers != 5 {
		t.Errorf("Expected 5 workers, got %d", pool.workers)
	}
	if cap(pool.queue) != 100 {
		t.Errorf("Expected queue capacity 100, got %d", cap(pool.queue))
	}
}

func TestPoolQueueLength(t *testing.T) {
	pool := NewPool(2, 10, nil, nil)

	if pool.QueueLength() != 0 {
		t.Errorf("Expected queue length 0, got %d", pool.QueueLength())
	}
}

func TestAuditJob(t *testing.T) {
	job := &AuditJob{
		RequestID:   "req-123",
		Prompt:      "What is 2+2?",
		Response:    "4",
		Model:       "gpt-4",
		Timestamp:   time.Now(),
		UserID:      "user-1",
		RequestPath: "/v1/chat/completions",
	}

	if job.RequestID != "req-123" {
		t.Errorf("Expected RequestID 'req-123', got '%s'", job.RequestID)
	}
	if job.Model != "gpt-4" {
		t.Errorf("Expected Model 'gpt-4', got '%s'", job.Model)
	}
}

func TestPoolSubmitWithNoClient(t *testing.T) {
	pool := NewPool(1, 10, nil, nil)
	go pool.Start()
	defer pool.Stop()

	time.Sleep(50 * time.Millisecond)

	job := &AuditJob{
		RequestID: "test-1",
		Prompt:    "Test prompt",
		Response:  "Test response",
		Model:     "test-model",
		Timestamp: time.Now(),
	}

	pool.Submit(job)
	time.Sleep(100 * time.Millisecond)
}

func isUUIDv7(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.Version() == 7 && parsed.String() == id
}

// B4: ids are server-generated; the client's X-Request-ID never becomes one.
func TestSubmitAssignsServerIDs(t *testing.T) {
	pool := NewPool(1, 10, nil, nil) // not started: jobs stay queued

	job := &AuditJob{RequestID: "client-req-1", Prompt: "p", Response: "r"}
	pool.Submit(job)
	if !isUUIDv7(job.AuditID) {
		t.Errorf("AuditID %q is not a UUIDv7", job.AuditID)
	}
	if job.TraceID != job.AuditID {
		t.Errorf("TraceID %q should default to AuditID %q", job.TraceID, job.AuditID)
	}
	if job.RequestID != "client-req-1" {
		t.Errorf("RequestID must be left untouched, got %q", job.RequestID)
	}

	again := &AuditJob{RequestID: "client-req-1", Prompt: "p", Response: "r"}
	pool.Submit(again)
	if again.AuditID == job.AuditID {
		t.Error("a repeated client request id produced a repeated audit id")
	}

	traced := &AuditJob{RequestID: "x", TraceID: store.NewID()}
	traceID := traced.TraceID
	pool.Submit(traced)
	if traced.TraceID != traceID || traced.AuditID == traceID || !isUUIDv7(traced.AuditID) {
		t.Errorf("a submitter's trace id must be kept with a new audit id: %+v", traced)
	}
}

func TestNewAuditRecord(t *testing.T) {
	job := &AuditJob{RequestID: "client-req-1", Prompt: "question", Response: "answer", Model: "m"}
	assignIDs(job)
	result := &grpc.AuditResult{
		TrustScore:            0.75,
		HallucinationDetected: true,
		ReasoningTrace:        "trace",
		StepTimings:           map[string]int64{"total_ms": 9},
		Claims: []*grpc.ClaimResult{
			{Text: "c1", Verdict: "contradicted", Confidence: 0.9},
			{Text: "c2", Verdict: "supported", Confidence: 0.8, Evidence: []string{"e"}},
		},
	}

	record := newAuditRecord(job, result, 1500*time.Millisecond)
	if record.AuditID != job.AuditID || record.TraceID != job.TraceID || record.RequestID != job.TraceID {
		t.Errorf("ids: audit=%q trace=%q request=%q, job %+v", record.AuditID, record.TraceID, record.RequestID, job)
	}
	if record.ClientRequestID == nil || *record.ClientRequestID != "client-req-1" {
		t.Errorf("client request id not offered for storage: %v", record.ClientRequestID)
	}
	if *record.UserQuery != "question" || *record.LLMResponse != "answer" || record.Model != "m" {
		t.Errorf("texts not mapped: %+v", record)
	}
	if *record.FaithfulnessScore != 0.75 || *record.Grade != "B" || !record.HallucinationDetected ||
		record.ProcessingTimeMs != 1500 || record.StepTimings["total_ms"] != 9 {
		t.Errorf("result not mapped: %+v", record)
	}
	if len(record.Claims) != 2 || *record.Claims[0].Claim != "c1" || record.Claims[0].Status != "CONTRADICTED" ||
		record.Claims[0].Evidence == nil || record.Claims[1].Status != "SUPPORTED" {
		t.Errorf("claims not mapped: %+v", record.Claims)
	}

	anonymous := &AuditJob{Prompt: "q", Response: "a"}
	assignIDs(anonymous)
	if rec := newAuditRecord(anonymous, &grpc.AuditResult{}, 0); rec.ClientRequestID != nil {
		t.Errorf("no client id expected, got %q", *rec.ClientRequestID)
	}
}

type recordingStore struct {
	mu      sync.Mutex
	records []*store.AuditRecord
	err     error
}

func (s *recordingStore) SaveAudit(_ context.Context, r *store.AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	return s.err
}

func (s *recordingStore) ListAudits(context.Context, store.ListFilter) ([]store.AuditRecord, int, error) {
	return nil, 0, nil
}

func (s *recordingStore) GetAudit(context.Context, string) (*store.AuditRecord, error) {
	return nil, errors.New("not implemented")
}

func (s *recordingStore) Close() {}

// Two requests reusing one X-Request-ID are persisted as two audits, and a
// store-side ErrDuplicate is logged, never fatal.
func TestPersistAuditRepeatedClientID(t *testing.T) {
	fake := &recordingStore{}
	pool := NewPool(1, 10, nil, nil)
	pool.AttachStore(fake)

	for i := 0; i < 2; i++ {
		job := &AuditJob{RequestID: "same", Prompt: "p", Response: "r"}
		assignIDs(job)
		pool.persistAudit(job, &grpc.AuditResult{TrustScore: 1}, time.Millisecond)
	}
	if len(fake.records) != 2 || fake.records[0].AuditID == fake.records[1].AuditID {
		t.Fatalf("expected two audits with distinct ids, got %+v", fake.records)
	}

	fake.err = store.ErrDuplicate
	job := &AuditJob{RequestID: "same"}
	assignIDs(job)
	pool.persistAudit(job, &grpc.AuditResult{}, 0) // must only log
}

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// newTestStore connects to TEST_DATABASE_URL or skips. CI provides a postgres
// service container; locally: docker compose up -d postgres and
//
//	TEST_DATABASE_URL=postgres://truthtable:truthtable-dev@localhost:5432/truthtable go test ./internal/store/
func newTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration tests")
	}

	s, err := NewPostgresStore(context.Background(), url)
	if err != nil {
		t.Fatalf("failed to connect to test database: %v", err)
	}
	t.Cleanup(func() {
		// Isolate test runs from each other.
		_, _ = s.pool.Exec(context.Background(), "TRUNCATE audits CASCADE")
		s.Close()
	})
	_, _ = s.pool.Exec(context.Background(), "TRUNCATE audits CASCADE")
	return s
}

func sampleAudit(id string, score float64, flagged bool) *AuditRecord {
	return &AuditRecord{
		AuditID:               id,
		RequestID:             id,
		UserQuery:             "What is the capital of France?",
		LLMResponse:           "Paris is the capital of France.",
		Model:                 "llama3.2",
		FaithfulnessScore:     score,
		Grade:                 GradeForScore(score),
		HallucinationDetected: flagged,
		ReasoningTrace:        "trace",
		ProcessingTimeMs:      1234,
		StepTimings:           map[string]int64{"decompose_ms": 100, "verify_ms": 500},
		CreatedAt:             time.Now().UTC(),
		Claims: []ClaimRecord{
			{Claim: "Paris is the capital of France", Status: "SUPPORTED",
				Confidence: 0.95, Evidence: []string{"Paris is the capital"}},
			{Claim: "France is in Europe", Status: "SUPPORTED",
				Confidence: 0.9, Evidence: []string{}},
		},
	}
}

func TestSaveAndGetAudit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	want := sampleAudit("audit-1", 0.95, false)
	if err := s.SaveAudit(ctx, want); err != nil {
		t.Fatalf("SaveAudit failed: %v", err)
	}

	got, err := s.GetAudit(ctx, "audit-1")
	if err != nil {
		t.Fatalf("GetAudit failed: %v", err)
	}

	if got.UserQuery != want.UserQuery || got.Grade != "A" || got.FaithfulnessScore != 0.95 {
		t.Errorf("audit fields mismatch: got %+v", got)
	}
	if len(got.Claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(got.Claims))
	}
	if got.Claims[0].Claim != want.Claims[0].Claim || got.Claims[0].Status != "SUPPORTED" {
		t.Errorf("claim mismatch: %+v", got.Claims[0])
	}
	if got.StepTimings["verify_ms"] != 500 {
		t.Errorf("step timings not round-tripped: %+v", got.StepTimings)
	}
}

func TestSaveAuditIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	record := sampleAudit("audit-dup", 0.8, false)
	if err := s.SaveAudit(ctx, record); err != nil {
		t.Fatalf("first save failed: %v", err)
	}
	if err := s.SaveAudit(ctx, record); err != nil {
		t.Fatalf("duplicate save should not error: %v", err)
	}
}

func TestGetAuditNotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetAudit(context.Background(), "does-not-exist")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("expected pgx.ErrNoRows, got %v", err)
	}
}

func TestListAuditsPaginationAndFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 3 clean A-grade audits + 2 flagged F-grade audits.
	for i := 0; i < 3; i++ {
		if err := s.SaveAudit(ctx, sampleAudit(fmt.Sprintf("clean-%d", i), 0.95, false)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := s.SaveAudit(ctx, sampleAudit(fmt.Sprintf("flagged-%d", i), 0.1, true)); err != nil {
			t.Fatal(err)
		}
	}

	all, total, err := s.ListAudits(ctx, ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAudits failed: %v", err)
	}
	if total != 5 || len(all) != 5 {
		t.Errorf("expected 5 audits, got total=%d len=%d", total, len(all))
	}

	page, total, err := s.ListAudits(ctx, ListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(page) != 2 {
		t.Errorf("pagination: expected total=5 len=2, got total=%d len=%d", total, len(page))
	}

	flagged := true
	flaggedOnly, total, err := s.ListAudits(ctx, ListFilter{Limit: 10, Flagged: &flagged})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(flaggedOnly) != 2 {
		t.Errorf("flagged filter: expected 2, got total=%d len=%d", total, len(flaggedOnly))
	}

	aGrade, total, err := s.ListAudits(ctx, ListFilter{Limit: 10, Grade: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(aGrade) != 3 {
		t.Errorf("grade filter: expected 3, got total=%d len=%d", total, len(aGrade))
	}
}

func TestGradeForScore(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{1.0, "A"}, {0.9, "A"}, {0.89, "B"}, {0.7, "B"},
		{0.69, "C"}, {0.5, "C"}, {0.49, "D"}, {0.3, "D"}, {0.29, "F"}, {0.0, "F"},
	}
	for _, tc := range cases {
		if got := GradeForScore(tc.score); got != tc.want {
			t.Errorf("GradeForScore(%v) = %q, want %q", tc.score, got, tc.want)
		}
	}
}

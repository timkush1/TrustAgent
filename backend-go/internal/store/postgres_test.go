package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/truthtable/backend-go/internal/storetest"
)

// newTestStore opens a migrated store inside a fresh per-test schema (see
// storetest). It skips when TEST_DATABASE_URL is unset. Locally:
//
//	TEST_DATABASE_URL=postgres://truthtable:truthtable-dev@127.0.0.1:5432/truthtable_test go test ./internal/store/
func newTestStore(t *testing.T) (*PostgresStore, *storetest.Schema) {
	t.Helper()
	schema := storetest.New(t)
	s, err := NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}
	t.Cleanup(s.Close)
	return s, schema
}

func ptr[T any](v T) *T { return &v }

func sampleAudit(id string, score float64, flagged bool) *AuditRecord {
	return &AuditRecord{
		AuditID:               id,
		UserQuery:             ptr("What is the capital of France?"),
		LLMResponse:           ptr("Paris is the capital of France."),
		Model:                 "llama3.2",
		FaithfulnessScore:     ptr(score),
		Grade:                 ptr(GradeForScore(score)),
		HallucinationDetected: flagged,
		ReasoningTrace:        "trace",
		ProcessingTimeMs:      1234,
		StepTimings:           map[string]int64{"decompose_ms": 100, "verify_ms": 500},
		CreatedAt:             time.Now().UTC(),
		Claims: []ClaimRecord{
			{Claim: ptr("Paris is the capital of France"), Status: "SUPPORTED",
				Confidence: 0.95, Evidence: []string{"Paris is the capital"}},
			{Claim: ptr("France is in Europe"), Status: "SUPPORTED",
				Confidence: 0.9, Evidence: []string{}},
		},
	}
}

func countClaims(t *testing.T, s *PostgresStore, auditID string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_claims WHERE audit_id = $1`, auditID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSaveAuditRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	want := sampleAudit(NewID(), 0.95, false)
	want.ClientRequestID = ptr("client-req.1:a_b")
	if err := s.SaveAudit(ctx, want); err != nil {
		t.Fatalf("SaveAudit failed: %v", err)
	}

	got, err := s.GetAudit(ctx, want.AuditID)
	if err != nil {
		t.Fatalf("GetAudit failed: %v", err)
	}
	if got.AuditID != want.AuditID || *got.UserQuery != *want.UserQuery || *got.Grade != "A" ||
		*got.FaithfulnessScore != 0.95 {
		t.Errorf("audit fields mismatch: got %+v", got)
	}
	if got.TraceID != want.AuditID {
		t.Errorf("trace_id should default to audit_id, got %q", got.TraceID)
	}
	if got.RequestID != got.TraceID {
		t.Errorf("request_id should default to trace_id, got %q", got.RequestID)
	}
	if got.ClientRequestID == nil || *got.ClientRequestID != "client-req.1:a_b" {
		t.Errorf("client_request_id not stored: %v", got.ClientRequestID)
	}
	if len(got.Claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(got.Claims))
	}
	if *got.Claims[0].Claim != *want.Claims[0].Claim || got.Claims[0].Status != "SUPPORTED" {
		t.Errorf("claim mismatch: %+v", got.Claims[0])
	}
	if got.StepTimings["verify_ms"] != 500 {
		t.Errorf("step timings not round-tripped: %+v", got.StepTimings)
	}
}

func TestSaveAuditKeepsExplicitTraceAndRequestIDs(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	record := sampleAudit(NewID(), 0.8, false)
	record.TraceID = NewID()
	record.RequestID = record.TraceID
	if err := s.SaveAudit(ctx, record); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAudit(ctx, record.AuditID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TraceID != record.TraceID || got.RequestID != record.TraceID {
		t.Errorf("explicit ids not kept: trace_id=%q request_id=%q", got.TraceID, got.RequestID)
	}
}

// B4: a repeated audit_id must never append claims to the existing audit.
func TestSaveAuditDuplicateIDAddsNoClaims(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	first := sampleAudit(NewID(), 0.8, false)
	if err := s.SaveAudit(ctx, first); err != nil {
		t.Fatalf("first save failed: %v", err)
	}

	second := sampleAudit(first.AuditID, 0.1, true)
	second.UserQuery = ptr("a different question")
	second.Claims = append(second.Claims, ClaimRecord{Claim: ptr("extra"), Status: "UNSUPPORTED"})
	if err := s.SaveAudit(ctx, second); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate save: expected ErrDuplicate, got %v", err)
	}

	if n := countClaims(t, s, first.AuditID); n != 2 {
		t.Errorf("duplicate save changed claims: expected 2, got %d", n)
	}
	got, err := s.GetAudit(ctx, first.AuditID)
	if err != nil {
		t.Fatal(err)
	}
	if *got.UserQuery != *first.UserQuery || *got.FaithfulnessScore != 0.8 || got.HallucinationDetected {
		t.Errorf("duplicate save overwrote the audit: %+v", got)
	}
}

// Concurrent writers of the same audit_id: exactly one wins, the rest get
// ErrDuplicate, and the claims are written exactly once.
func TestSaveAuditConcurrentDuplicates(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	id := NewID()

	const writers = 8
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.SaveAudit(ctx, sampleAudit(id, 0.9, false))
		}()
	}
	wg.Wait()

	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrDuplicate):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("expected exactly one successful save, got %d", wins)
	}
	if n := countClaims(t, s, id); n != 2 {
		t.Errorf("expected 2 claims, got %d", n)
	}
}

func TestSaveAuditClientRequestIDRule(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		client *string
		want   *string
	}{
		{"absent", nil, nil},
		{"empty", ptr(""), nil},
		{"valid", ptr("Req-42.v1:retry_2"), ptr("Req-42.v1:retry_2")},
		{"max length", ptr(strings.Repeat("a", 128)), ptr(strings.Repeat("a", 128))},
		{"too long", ptr(strings.Repeat("a", 129)), nil},
		{"space", ptr("has space"), nil},
		{"newline", ptr("abc\ndef"), nil},
		{"trailing newline", ptr("abc\n"), nil},
		{"nul byte", ptr("a\x00b"), nil}, // would also be rejected by a TEXT column
		{"slash", ptr("a/b"), nil},
		{"non-ascii", ptr("réq"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := sampleAudit(NewID(), 0.9, false)
			record.ClientRequestID = tc.client
			if err := s.SaveAudit(ctx, record); err != nil {
				t.Fatalf("SaveAudit must not fail on a bad client id: %v", err)
			}
			got, err := s.GetAudit(ctx, record.AuditID)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.want == nil && got.ClientRequestID != nil:
				t.Errorf("expected client_request_id dropped, got %q", *got.ClientRequestID)
			case tc.want != nil && (got.ClientRequestID == nil || *got.ClientRequestID != *tc.want):
				t.Errorf("expected client_request_id %q, got %v", *tc.want, got.ClientRequestID)
			}
		})
	}
}

// client_request_id is not unique: two audits may carry the same client id,
// and each keeps its own claims.
func TestSaveAuditSharedClientRequestID(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	a, b := sampleAudit(NewID(), 0.9, false), sampleAudit(NewID(), 0.2, true)
	a.ClientRequestID, b.ClientRequestID = ptr("same-id"), ptr("same-id")
	for _, r := range []*AuditRecord{a, b} {
		if err := s.SaveAudit(ctx, r); err != nil {
			t.Fatalf("SaveAudit: %v", err)
		}
	}
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM audits WHERE client_request_id = 'same-id'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 audits sharing the client id, got %d", n)
	}
	if countClaims(t, s, a.AuditID) != 2 || countClaims(t, s, b.AuditID) != 2 {
		t.Error("claims leaked between audits sharing a client id")
	}
}

func TestSaveAuditRequiresAuditID(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SaveAudit(context.Background(), sampleAudit("", 0.9, false)); err == nil {
		t.Fatal("expected an error for an empty audit_id")
	}
}

// Columns 0002 made nullable round-trip as nil through Save, Get and List.
func TestSaveAuditNullableColumns(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	record := &AuditRecord{
		AuditID: NewID(),
		Claims:  []ClaimRecord{{Status: "NOT_CHECKED", Evidence: []string{}}},
	}
	if err := s.SaveAudit(ctx, record); err != nil {
		t.Fatalf("SaveAudit: %v", err)
	}

	got, err := s.GetAudit(ctx, record.AuditID)
	if err != nil {
		t.Fatalf("GetAudit: %v", err)
	}
	if got.FaithfulnessScore != nil || got.Grade != nil || got.UserQuery != nil ||
		got.LLMResponse != nil || got.ClientRequestID != nil {
		t.Errorf("expected NULL columns to scan as nil: %+v", got)
	}
	if len(got.Claims) != 1 || got.Claims[0].Claim != nil || got.Claims[0].Status != "NOT_CHECKED" {
		t.Errorf("unexpected claims: %+v", got.Claims)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at should default to now")
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"faithfulness_score":null`, `"grade":null`, `"user_query":null`, `"claim":null`} {
		if !strings.Contains(string(body), field) {
			t.Errorf("JSON missing %s: %s", field, body)
		}
	}

	list, total, err := s.ListAudits(ctx, ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAudits: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].FaithfulnessScore != nil {
		t.Errorf("unexpected list: total=%d %+v", total, list)
	}
}

func TestGetAuditNotFound(t *testing.T) {
	s, _ := newTestStore(t)

	_, err := s.GetAudit(context.Background(), "does-not-exist")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("expected pgx.ErrNoRows, got %v", err)
	}
}

func TestListAuditsPaginationAndFilters(t *testing.T) {
	s, _ := newTestStore(t)
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
	for _, a := range all {
		if a.TraceID != a.AuditID {
			t.Errorf("list: trace_id %q should equal audit_id %q", a.TraceID, a.AuditID)
		}
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

// Audits that share created_at are ordered by audit_id, so offset pages
// neither repeat nor skip a row.
func TestListAuditsStableOrder(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	ids := []string{NewID(), NewID(), NewID()} // ascending
	for _, id := range ids {
		record := sampleAudit(id, 0.9, false)
		record.CreatedAt = at
		if err := s.SaveAudit(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	later := sampleAudit(NewID(), 0.9, false)
	later.CreatedAt = at.Add(time.Second)
	if err := s.SaveAudit(ctx, later); err != nil {
		t.Fatal(err)
	}

	want := []string{later.AuditID, ids[2], ids[1], ids[0]}
	var got []string
	for offset := 0; offset < len(want); offset++ {
		page, total, err := s.ListAudits(ctx, ListFilter{Limit: 1, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if total != len(want) || len(page) != 1 {
			t.Fatalf("offset %d: total=%d len=%d", offset, total, len(page))
		}
		got = append(got, page[0].AuditID)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paged order = %v, want %v (created_at DESC, audit_id DESC)", got, want)
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

func TestNewIDIsTimeOrderedUUIDv7(t *testing.T) {
	prev := ""
	for i := 0; i < 1000; i++ {
		id := NewID()
		parsed, err := uuid.Parse(id)
		if err != nil {
			t.Fatalf("NewID returned %q: %v", id, err)
		}
		if parsed.Version() != 7 || parsed.String() != id {
			t.Fatalf("NewID returned %q: want canonical lowercase UUIDv7", id)
		}
		if id <= prev {
			t.Fatalf("NewID not monotonic: %q after %q", id, prev)
		}
		prev = id
	}
}

func TestClientRequestID(t *testing.T) {
	cases := map[string]bool{
		"":                        false,
		"a":                       true,
		"A-Za-z0-9._:-":           true,
		strings.Repeat("x", 128):  true,
		strings.Repeat("x", 129):  false,
		"with space":              false,
		"tab\t":                   false,
		"line\nbreak":             false,
		"trailing-newline\n":      false, // RE2 $ never matches before a final \n
		"\nleading-newline":       false,
		"nul\x00byte":             false,
		"semi;colon":              false,
		"slash/":                  false,
		"ünïcode":                 false,
		"550e8400-e29b-41d4-a716": true,
	}
	for raw, ok := range cases {
		id, got := ClientRequestID(raw)
		if got != ok {
			t.Errorf("ClientRequestID(%q) ok=%v, want %v", raw, got, ok)
		}
		if got && id != raw {
			t.Errorf("ClientRequestID(%q) = %q, want the input unchanged", raw, id)
		}
		if !got && id != "" {
			t.Errorf("ClientRequestID(%q) = %q, want empty when dropped", raw, id)
		}
	}
}

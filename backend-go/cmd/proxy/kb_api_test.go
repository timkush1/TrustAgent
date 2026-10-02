package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/grpc"
	"github.com/truthtable/backend-go/internal/store"
)

func TestParseKBListParamsDefaults(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/kb/claims", nil)

	limit, offset, status, err := parseKBListParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limit != 50 || offset != 0 || status != "" {
		t.Errorf("unexpected defaults: limit=%d offset=%d status=%q", limit, offset, status)
	}
}

func TestParseKBListParamsValid(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/kb/claims?limit=10&offset=5&status=quarantined", nil)

	limit, offset, status, err := parseKBListParams(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limit != 10 || offset != 5 || status != "quarantined" {
		t.Errorf("unexpected values: limit=%d offset=%d status=%q", limit, offset, status)
	}
}

func TestParseKBListParamsRejectsInvalid(t *testing.T) {
	for _, query := range []string{"limit=0", "limit=999", "offset=-1", "status=banana"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/api/kb/claims?"+query, nil)

		if _, _, _, err := parseKBListParams(c); err == nil {
			t.Errorf("query %q should be rejected", query)
		}
	}
}

func TestKBEndpointsReturn503WithoutEngine(t *testing.T) {
	router := gin.New()
	router.GET("/api/kb/claims", handleListKBClaims(nil))
	router.GET("/api/kb/conflicts", handleListConflicts(nil))
	router.GET("/api/kb/stats", handleKBStats(nil))

	for _, path := range []string{"/api/kb/claims", "/api/kb/conflicts", "/api/kb/stats"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without engine: expected 503, got %d", path, w.Code)
		}
	}
}

// fakeIngester answers IngestDocuments with res or err and counts the calls.
type fakeIngester struct {
	res   *grpc.IngestResult
	err   error
	calls int
}

func (f *fakeIngester) IngestDocuments(context.Context, []grpc.IngestDocument) (*grpc.IngestResult, error) {
	f.calls++
	return f.res, f.err
}

// uploadSpy records what the upload booked and audited.
type uploadSpy struct {
	reserveErr error
	reserved   int
	settled    []float64
	audits     []store.AdminAuditEntry
}

func (s *uploadSpy) hooks() kbUploadHooks {
	return kbUploadHooks{
		reserve: func(context.Context) (func(float64), error) {
			if s.reserveErr != nil {
				return nil, s.reserveErr
			}
			s.reserved++
			return func(usd float64) { s.settled = append(s.settled, usd) }, nil
		},
		audit: func(_ context.Context, e store.AdminAuditEntry) error {
			s.audits = append(s.audits, e)
			return nil
		},
		log: slog.New(slog.DiscardHandler),
	}
}

// postUpload sends one multipart upload of content to a router serving h as the admin "ops".
func postUpload(t *testing.T, h gin.HandlerFunc, content string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "policies.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte(content))
	_ = mw.Close()
	r := gin.New()
	r.POST("/api/kb/documents", func(c *gin.Context) { c.Set(apiIdentityKey, &authz.Identity{Label: "ops"}) }, h)
	req := httptest.NewRequest(http.MethodPost, "/api/kb/documents", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const kbUploadBody = `[{"content":"Annual plans can be refunded within 30 days.","metadata":{"source":"policy"}}]`

// TestKBUploadPastTheLLMCap: while the day's LLM cap is spent the upload is refused with 503
// budget_exhausted (details.cap llm, D48) before the engine is called.
func TestKBUploadPastTheLLMCap(t *testing.T) {
	eng, spy := &fakeIngester{}, &uploadSpy{reserveErr: errors.New("cap reached")}
	w := postUpload(t, fileUpload(eng, 1<<20, spy.hooks()), kbUploadBody)
	if w.Code != http.StatusServiceUnavailable || errorCodeOf(t, w.Body.Bytes()) != codeBudgetExhausted ||
		!strings.Contains(w.Body.String(), `"cap":"llm"`) {
		t.Fatalf("HTTP %d %s", w.Code, w.Body)
	}
	contractstest.ValidateJSON(t, schemaError, w.Body.Bytes())
	if eng.calls != 0 || len(spy.audits) != 0 {
		t.Errorf("a refused upload reached the engine (%d calls) or the audit log (%d rows)", eng.calls, len(spy.audits))
	}
}

// TestKBUploadBooksItsCostAndAuditRow: an ingest settles its booking once, after the engine
// answered, and writes the admin audit row kb.upload with the uploader, the file name and the
// counts; an upload that fails (bad JSON, or the engine) releases the booking and writes no row.
func TestKBUploadBooksItsCostAndAuditRow(t *testing.T) {
	eng := &fakeIngester{res: &grpc.IngestResult{DocumentsIngested: 1, ClaimsAccepted: 2, ClaimsQuarantined: 1, ConflictsDetected: 1}}
	spy := &uploadSpy{}
	w := postUpload(t, fileUpload(eng, 1<<20, spy.hooks()), kbUploadBody)
	if w.Code != http.StatusOK || eng.calls != 1 {
		t.Fatalf("HTTP %d %s (%d calls)", w.Code, w.Body, eng.calls)
	}
	if spy.reserved != 1 || len(spy.settled) != 1 || spy.settled[0] != 0 {
		t.Errorf("booking: %d holds, settled %v", spy.reserved, spy.settled)
	}
	if len(spy.audits) != 1 {
		t.Fatalf("audit rows %+v", spy.audits)
	}
	a := spy.audits[0]
	diff, _ := a.Diff.(map[string]any)
	if a.Actor != "ops" || a.Action != "kb.upload" || a.Target != "policies.json" || diff["claims_accepted"] != int32(2) ||
		diff["conflicts_detected"] != int32(1) {
		t.Errorf("audit row %+v", a)
	}

	for _, c := range []struct {
		name string
		eng  *fakeIngester
		body string
		code int
	}{
		{"bad JSON", &fakeIngester{}, `{"content":`, http.StatusBadRequest},
		{"engine error", &fakeIngester{err: errors.New("engine down")}, kbUploadBody, http.StatusInternalServerError},
	} {
		spy := &uploadSpy{}
		w := postUpload(t, fileUpload(c.eng, 1<<20, spy.hooks()), c.body)
		if w.Code != c.code || spy.reserved != 1 || len(spy.settled) != 1 || spy.settled[0] != 0 || len(spy.audits) != 0 {
			t.Errorf("%s: HTTP %d, %d holds, settled %v, %d audit rows", c.name, w.Code, spy.reserved, spy.settled, len(spy.audits))
		}
	}
}

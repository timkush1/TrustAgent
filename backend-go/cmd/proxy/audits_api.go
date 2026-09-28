package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/evidence"
	"github.com/truthtable/backend-go/internal/gate"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/worker"
)

// auditReader is the part of the store GET /api/audits reads.
type auditReader interface {
	ListAudits(ctx context.Context, f store.AuditFilter) ([]store.AuditRecord, string, error)
	GetAudit(ctx context.Context, auditID string) (*store.AuditRecord, error)
}

// auditsAPI serves GET /api/audits, GET /api/audits/:id and POST /api/audit (SPEC §2.4 endpoints
// 13-15). Legacy (v1) audits read back in the same shapes.
type auditsAPI struct {
	store  auditReader // nil when persistence is disabled
	audits auditQueue  // nil when there is no audit engine
	// maxTextChars caps query, response and each context text (TRUTHTABLE_MAX_TEXT_CHARS).
	maxTextChars int
	testMode     bool
	log          *slog.Logger
}

func (a *auditsAPI) available(c *gin.Context) bool {
	if a.store == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Audit history needs a database: set TRUTHTABLE_DATABASE_URL.")
		return false
	}
	return true
}

// list serves GET /api/audits.
func (a *auditsAPI) list(c *gin.Context) {
	if !a.available(c) {
		return
	}
	var f store.AuditFilter
	var ok bool
	if f.Limit, ok = queryLimit(c); !ok {
		return
	}
	f.Cursor = c.Query("cursor")
	if f.Since, ok = queryTime(c, "since"); !ok {
		return
	}
	if f.CompletedSince, ok = queryTime(c, "completed_since"); !ok {
		return
	}
	f.Route = c.Query("route")
	enums := []struct {
		name    string
		dst     *string
		allowed []string
	}{
		{"source", &f.Source, traceSources},
		{"status", &f.Status, auditStatuses},
		{"verdict", &f.Verdict, auditVerdicts},
		{"selection", &f.Selection, auditSelections},
	}
	for _, e := range enums {
		if *e.dst, ok = queryEnum(c, e.name, e.allowed...); !ok {
			return
		}
	}
	records, next, err := a.store.ListAudits(c.Request.Context(), f)
	if errors.Is(err, store.ErrBadCursor) {
		badParam(c, "cursor", "is not a cursor this server issued")
		return
	}
	if err != nil {
		a.internal(c, "list audits", err)
		return
	}
	content := canSeeContent(c)
	items := make([]store.AuditSummary, len(records))
	for i := range records {
		items[i] = records[i].Summary()
		if !content {
			items[i] = items[i].Redact()
		}
	}
	c.JSON(http.StatusOK, newPage(items, next))
}

// get serves GET /api/audits/:id.
func (a *auditsAPI) get(c *gin.Context) {
	if !a.available(c) {
		return
	}
	rec, err := a.store.GetAudit(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		abortAPI(c, http.StatusNotFound, codeNotFound, "No audit has this id.")
		return
	}
	if err != nil {
		a.internal(c, "get audit", err)
		return
	}
	d := rec.Detail()
	if !canSeeContent(c) {
		d = d.Redact()
	}
	c.JSON(http.StatusOK, d)
}

// directAuditRequest is api.ts DirectAuditRequest.
type directAuditRequest struct {
	Query    string `json:"query"`
	Response string `json:"response"`
	Model    string `json:"model"`
	Route    string `json:"route"`
	Context  []struct {
		ID     string `json:"id"`
		Text   string `json:"text"`
		Source string `json:"source"`
	} `json:"context"`
}

// directAuditAccepted is the 202 body of POST /api/audit.
type directAuditAccepted struct {
	TraceID string `json:"trace_id"`
	AuditID string `json:"audit_id"`
	Status  string `json:"status"`
	Lane    string `json:"lane"`
}

var (
	routeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	docIDPattern     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	evidenceSources  = []string{evidence.SourceRequestSystem, evidence.SourceRequestContext, evidence.SourceToolResults,
		evidence.SourceExtension, evidence.SourceKnowledge}
)

// maxDirectContext is the most context documents one direct audit may carry (SPEC §2.2).
const maxDirectContext = maxAuditDocs

// direct serves POST /api/audit: a deep audit of a question and answer the caller supplies, in the
// low lane with selection direct. The trace id is new; no gateway trace is recorded for it.
func (a *auditsAPI) direct(c *gin.Context) {
	var req directAuditRequest
	dec := json.NewDecoder(c.Request.Body)
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			abortAPI(c, http.StatusRequestEntityTooLarge, codeTooLarge, "The request body is too large.")
			return
		}
		abortAPI(c, http.StatusBadRequest, codeInvalidJSON, "The body is not a DirectAuditRequest JSON object.")
		return
	}
	if fields := a.validateDirect(&req); len(fields) > 0 {
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "The audit request is not valid.", fields...)
		return
	}
	if a.audits == nil || !a.audits.HasEngine() {
		abortAPI(c, http.StatusServiceUnavailable, codeEngineUnavailable, "No audit engine is configured (TRUTHTABLE_GRPC_ADDRESS).")
		return
	}

	traceID, auditID := store.NewID(), store.NewID()
	docs := make([]*pb.ContextDocument, 0, len(req.Context))
	items := make([]evidence.Item, 0, len(req.Context))
	for i, doc := range req.Context {
		id := doc.ID
		if id == "" {
			id = fmt.Sprintf("doc-%d", i+1)
		}
		source := doc.Source
		if !slices.Contains(evidenceSources, source) {
			source = evidence.SourceExtension
		}
		docs = append(docs, &pb.ContextDocument{Id: id, Content: doc.Text, Source: source, Label: id})
		items = append(items, evidence.Item{ID: id, Source: source, Label: id, Text: doc.Text})
	}
	opts := &pb.AuditOptions{}
	if a.testMode {
		if s := c.GetHeader(headerScenario); scenarioPattern.MatchString(s) {
			opts.MockScenario = s
		}
	}
	job := &worker.Job{
		Request: &pb.AuditRequest{
			RequestId: traceID, TraceId: traceID, AuditId: auditID, Query: req.Query, Response: req.Response,
			Context: docs, Model: req.Model, Provider: "direct", TimestampMs: time.Now().UnixMilli(), Route: req.Route,
			Source: gate.SourceDirect, Priority: gate.LaneLow, Options: opts,
		},
		Lane: gate.LaneLow, Selection: "direct", InclusionProb: 1,
	}
	job.ContextUsed, _ = json.Marshal(items)
	lane, err := a.audits.Submit(job)
	if err != nil {
		code := codeQueueFull
		if errors.Is(err, worker.ErrNoEngine) {
			code = codeEngineUnavailable
		}
		abortAPI(c, http.StatusServiceUnavailable, code, "The audit was not queued: "+err.Error())
		return
	}
	c.JSON(http.StatusAccepted, directAuditAccepted{TraceID: traceID, AuditID: auditID, Status: "queued", Lane: lane})
}

func (a *auditsAPI) validateDirect(req *directAuditRequest) []fieldError {
	var out []fieldError
	text := func(path, v string, required bool) {
		switch n := utf8.RuneCountInString(v); {
		case required && n == 0:
			out = append(out, fieldError{Path: path, Message: "is required"})
		case n > a.maxTextChars:
			out = append(out, fieldError{Path: path, Message: fmt.Sprintf("must be at most %d characters", a.maxTextChars)})
		}
	}
	text("query", req.Query, true)
	text("response", req.Response, true)
	if utf8.RuneCountInString(req.Model) > 200 {
		out = append(out, fieldError{Path: "model", Message: "must be at most 200 characters"})
	}
	if req.Route != "" && !routeNamePattern.MatchString(req.Route) {
		out = append(out, fieldError{Path: "route", Message: "must be a route name (lowercase letters, digits, - and _)"})
	}
	if len(req.Context) > maxDirectContext {
		out = append(out, fieldError{Path: "context", Message: fmt.Sprintf("must hold at most %d documents", maxDirectContext)})
	}
	for i, doc := range req.Context {
		p := fmt.Sprintf("context[%d]", i)
		text(p+".text", doc.Text, true)
		if doc.ID != "" && !docIDPattern.MatchString(doc.ID) {
			out = append(out, fieldError{Path: p + ".id", Message: "must match ^[A-Za-z0-9._:-]{1,64}$"})
		}
	}
	return out
}

func (a *auditsAPI) internal(c *gin.Context, what string, err error) {
	a.log.Error("api: "+what+" failed", "path", c.FullPath(), "err", err)
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request could not be completed; see the proxy log.")
}

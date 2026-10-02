package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/store"
)

// traceReader is the part of the store GET /api/traces reads.
type traceReader interface {
	ListTraces(ctx context.Context, f store.TraceFilter) ([]store.TraceRecord, string, error)
	GetTrace(ctx context.Context, traceID string) (*store.TraceRecord, error)
	GetTraceBody(ctx context.Context, traceID string) (*store.BodyRecord, error)
	TraceDecisions(ctx context.Context, traceID string) ([]json.RawMessage, error)
	GetAudit(ctx context.Context, auditID string) (*store.AuditRecord, error)
}

// page is api.ts Page<T>: a keyset page whose next_cursor is null at the end.
type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func newPage[T any](items []T, next string) page[T] {
	return page[T]{Items: items, NextCursor: nonEmpty(next)}
}

// Enum values of the /api/traces filters (api.ts).
var (
	gateActions       = []string{"observe", "pass", "flag", "block", "fallback"}
	gateStatuses      = []string{"pending", "ok", "partial", "not_assessed", "skipped", "timeout", "error"}
	gateVerdicts      = []string{"supported", "unsupported", "contradicted", "not_assessed", "unknown"}
	traceAuditStates  = []string{"none", "queued", "sampled_out", "dropped", "disabled", "running", "completed", "degraded", "failed", "not_assessed"}
	auditVerdicts     = []string{"GROUNDED", "HALLUCINATED", "UNVERIFIABLE", "NO_CLAIMS", "NOT_ASSESSED"}
	auditStatuses     = []string{"queued", "running", "completed", "degraded", "failed"}
	auditSelections   = []string{"high", "sample", "error_sample", "uniform", "direct", "playground", "studio"}
	traceSources      = []string{"proxy", "direct", "playground", "test", "trafficgen"}
	searchWindowLimit = 7 * 24 * time.Hour
	// reasonFilterPattern is the shape of a gate reason code ("groundedness:critical",
	// "det:numbers_not_in_evidence", "needs_second_signal") or of a prefix of one ("det:").
	reasonFilterPattern = regexp.MustCompile(`^[A-Za-z0-9_.@/-]+(:[A-Za-z0-9_.@/-]+)*:?$`)
)

// tracesAPI serves GET /api/traces and GET /api/traces/:id (SPEC §2.4 endpoints 10 and 11).
type tracesAPI struct {
	store traceReader // nil when persistence is disabled
	now   func() time.Time
	log   *slog.Logger
	// labels reads a trace's labels for TraceDetail.labels (P4, eval_wiring.go traceLabels);
	// nil means none.
	labels func(ctx context.Context, traceID string) ([]store.LabelView, error)
}

// available answers 503 persistence_disabled when there is no store.
func (a *tracesAPI) available(c *gin.Context) bool {
	if a.store == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Trace history needs a database: set TRUTHTABLE_DATABASE_URL.")
		return false
	}
	return true
}

// list serves GET /api/traces.
func (a *tracesAPI) list(c *gin.Context) {
	if !a.available(c) {
		return
	}
	f, ok := a.traceFilter(c)
	if !ok {
		return
	}
	records, next, err := a.store.ListTraces(c.Request.Context(), f)
	if errors.Is(err, store.ErrBadCursor) {
		badParam(c, "cursor", "is not a cursor this server issued")
		return
	}
	if err != nil {
		a.internal(c, "list traces", err)
		return
	}
	content := canSeeContent(c)
	items := make([]store.TraceSummary, len(records))
	for i := range records {
		items[i] = records[i].Summary()
		if !content {
			items[i] = items[i].Redact()
		}
	}
	c.JSON(http.StatusOK, newPage(items, next))
}

func (a *tracesAPI) traceFilter(c *gin.Context) (store.TraceFilter, bool) {
	var f store.TraceFilter
	var ok bool
	if f.Limit, ok = queryLimit(c); !ok {
		return f, false
	}
	f.Cursor = c.Query("cursor")
	times := []struct {
		name string
		dst  **time.Time
	}{{"since", &f.Since}, {"until", &f.Until}, {"updated_since", &f.UpdatedSince}}
	for _, p := range times {
		if *p.dst, ok = queryTime(c, p.name); !ok {
			return f, false
		}
	}
	enums := []struct {
		name    string
		dst     *string
		allowed []string
	}{
		{"action", &f.Action, gateActions},
		{"gate_status", &f.GateStatus, gateStatuses},
		{"verdict", &f.Verdict, gateVerdicts},
		{"audit_status", &f.AuditStatus, traceAuditStates},
		{"audit_verdict", &f.AuditVerdict, auditVerdicts},
		{"source", &f.Source, traceSources},
		{"outcome", &f.Outcome, store.OutcomeIDs()},
	}
	for _, e := range enums {
		if *e.dst, ok = queryEnum(c, e.name, e.allowed...); !ok {
			return f, false
		}
	}
	f.Route, f.Model, f.Q = c.Query("route"), c.Query("model"), c.Query("q")
	// Text search scans previews, so it is bounded to a recent window (SPEC §2.4).
	if f.Q != "" && (f.Since == nil || a.now().Sub(*f.Since) > searchWindowLimit) {
		badParam(c, "q", "text search needs since within the last 7 days")
		return f, false
	}
	// reason is a whole code or a prefix ending in a colon, bounded by since (P4-PLAN D68).
	if f.Reason = c.Query("reason"); f.Reason != "" {
		if len(f.Reason) > 128 || !reasonFilterPattern.MatchString(f.Reason) {
			badParam(c, "reason", "must be a reason code such as det:secret_pattern, or a prefix ending in a colon such as det:")
			return f, false
		}
		if f.Since == nil {
			badParam(c, "reason", "the reason filter needs since")
			return f, false
		}
	}
	return f, true
}

// get serves GET /api/traces/:id: the summary, the stored body (content scope), the gate
// decisions and the deep audit.
func (a *tracesAPI) get(c *gin.Context) {
	if !a.available(c) {
		return
	}
	ctx, id := c.Request.Context(), c.Param("id")
	rec, err := a.store.GetTrace(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		abortAPI(c, http.StatusNotFound, codeNotFound, "No trace has this id.")
		return
	}
	if err != nil {
		a.internal(c, "get trace", err)
		return
	}
	decisions, err := a.store.TraceDecisions(ctx, id)
	if err != nil {
		a.internal(c, "get trace decisions", err)
		return
	}
	d := store.TraceDetail{
		TraceSummary: rec.Summary(), Body: store.UnavailableBody(store.BodyScope), Decisions: decisions,
		Labels: []store.LabelView{},
	}
	if a.labels != nil {
		labels, err := a.labels(ctx, id)
		if err != nil {
			a.internal(c, "get trace labels", err)
			return
		}
		if labels != nil {
			d.Labels = labels
		}
	}
	if rec.AuditID != nil {
		audit, err := a.store.GetAudit(ctx, *rec.AuditID)
		switch {
		case err == nil:
			detail := audit.Detail()
			d.AuditDetail = &detail
		case !errors.Is(err, store.ErrNotFound): // a queued audit has no row yet
			a.internal(c, "get trace audit", err)
			return
		}
	}
	if !canSeeContent(c) {
		c.JSON(http.StatusOK, d.Redact())
		return
	}
	body, err := a.store.GetTraceBody(ctx, id)
	switch {
	case err == nil:
		d.Body = body.View(a.now())
	case errors.Is(err, store.ErrNotFound):
		d.Body = store.UnavailableBody(store.BodyNotStored)
	default:
		a.internal(c, "get trace body", err)
		return
	}
	c.JSON(http.StatusOK, d)
}

func (a *tracesAPI) internal(c *gin.Context, what string, err error) {
	a.log.Error("api: "+what+" failed", "path", c.FullPath(), "err", err)
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request could not be completed; see the proxy log.")
}

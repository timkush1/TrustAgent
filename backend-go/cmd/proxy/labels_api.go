package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/authz"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// Labels (SPEC §2.4 rows 39-41, P4-PLAN D59, D60): the label queue of an admin, and an admin's
// label of a trace target. A label freezes what it judged (the trace body, and the evaluator's raw
// risk and snapshot of the trace's attempt-0 decision), so calibration fits keep working after
// retention deletes the trace. There is no WebSocket event for labels (D61): the dashboard
// updates its own views.

// Label queue limits (D60).
const (
	labelQueueDefaultLimit = 20
	labelQueueMaxLimit     = 50
	labelQueueWindow       = "7d"
	maxLabelNoteRunes      = 1000
	maxTraceIDBytes        = 64
)

// claimLabels are the verdicts a person can give a claim (a claim:<position> label). UNKNOWN and
// NOT_CHECKED describe what the judge could not do, so they are not a person's verdict.
var claimLabels = []string{"SUPPORTED", "PARTIALLY_SUPPORTED", "CONTRADICTED", "UNSUPPORTED"}

// labelTargetRef is an "<id>@<version>" label target.
var labelTargetRef = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}@[1-9][0-9]{0,8}$`)

// labelTargetClaim is a "claim:<position>" label target.
var labelTargetClaim = regexp.MustCompile(`^claim:(0|[1-9][0-9]{0,5})$`)

// labelStore is the part of the store the label endpoints use (*store.PostgresStore).
type labelStore interface {
	GetTrace(ctx context.Context, traceID string) (*store.TraceRecord, error)
	GetTraceBody(ctx context.Context, traceID string) (*store.BodyRecord, error)
	LabelContextOf(ctx context.Context, traceID, ref string) (store.LabelContext, error)
	TraceHasClaim(ctx context.Context, traceID string, position int) (bool, error)
	UpsertLabel(ctx context.Context, l store.LabelRecord) error
	DeleteLabel(ctx context.Context, traceID, target, labeledBy string) (bool, error)
	LabelQueue(ctx context.Context, f store.LabelQueueFilter) ([]store.LabelQueueRow, error)
}

// labelsAPI serves /api/labels.
type labelsAPI struct {
	store labelStore // nil without a database
	// barrier flushes a trace's pending writes before the label reads it (D83).
	barrier func(ctx context.Context, traceID string) error
	// policy is the active policy and registry the evaluator registry: the queue orders by each
	// route's warn_at.
	policy   func() *policy.Policy
	registry *evaluators.Registry
	// retention is how long a label keeps its frozen sample (TRUTHTABLE_LABEL_SAMPLE_RETENTION).
	retention time.Duration
	now       func() time.Time
	log       *slog.Logger
}

// register mounts the label routes on the /api group.
func (a *labelsAPI) register(api *gin.RouterGroup, g apiGuards) {
	api.GET("/labels/queue", g.admin, g.rateLimit, a.queue)
	api.PUT("/labels", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.put)
	api.DELETE("/labels", g.writable, g.admin, g.rateLimit, g.bodyLimit, a.remove)
}

// labeler is labels.labeled_by for the caller (D59): a session's subject, else the key's label.
func labeler(c *gin.Context) string {
	id := callerIdentity(c)
	switch {
	case id == nil:
		return publicLabel
	case id.Kind == authz.KindSession && id.Subject != "":
		return id.Subject
	case id.Label != "":
		return id.Label
	}
	return publicLabel
}

func (a *labelsAPI) available(c *gin.Context) bool {
	if a.store == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Labels are stored with their traces, and this proxy has no database (TRUTHTABLE_DATABASE_URL).")
		return false
	}
	return true
}

// labelQueueItem is api.ts LabelQueueItem.
type labelQueueItem struct {
	TraceID        string    `json:"trace_id"`
	CreatedAt      time.Time `json:"created_at"`
	Route          string    `json:"route"`
	EvaluatorRef   string    `json:"evaluator_ref"`
	RiskRaw        *float64  `json:"risk_raw"`
	Level          *string   `json:"level"`
	UserRequest    *string   `json:"user_request"`
	ResponseText   *string   `json:"response_text"`
	EvidenceLabels []string  `json:"evidence_labels"`
}

// GET /api/labels/queue?evaluator=&window=7d&limit=20 (#39, D60): attempt-0 results with a numeric
// raw risk and a live body that the caller has not labelled, nearest the flag line first. evaluator
// is a ref, or an id for its latest version; without it every evaluator's results queue.
func (a *labelsAPI) queue(c *gin.Context) {
	if !a.available(c) {
		return
	}
	ref := c.Query("evaluator")
	switch {
	case ref == "":
	case labelTargetRef.MatchString(ref):
	case evaluatorParam.MatchString(ref):
		e, ok := a.registry.Evaluator(ref)
		if !ok {
			badParam(c, "evaluator", "is not a known evaluator")
			return
		}
		ref = e.Latest().Ref()
	default:
		badParam(c, "evaluator", "must be an evaluator ref such as groundedness@1")
		return
	}
	window := c.DefaultQuery("window", labelQueueWindow)
	span, ok := policy.ParseWindow(window)
	if !ok {
		badParam(c, "window", "must be one of 15m, 1h, 24h, 7d, 30d")
		return
	}
	limit := labelQueueDefaultLimit
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > labelQueueMaxLimit {
			badParam(c, "limit", "must be from 1 to "+strconv.Itoa(labelQueueMaxLimit))
			return
		}
		limit = n
	}
	now := a.now().UTC()
	routeWarn, specWarn := a.warnThresholds()
	rows, err := a.store.LabelQueue(c.Request.Context(), store.LabelQueueFilter{
		LabeledBy: labeler(c), Evaluator: ref, From: now.Add(-span), To: now, Limit: limit, Now: now,
		WarnAt: routeWarn, DefaultWarnAt: specWarn,
	})
	if err != nil {
		a.internal(c, "label queue", err)
		return
	}
	items := make([]labelQueueItem, 0, len(rows))
	for _, r := range rows {
		risk := r.RiskRaw
		items = append(items, labelQueueItem{
			TraceID: r.TraceID, CreatedAt: r.CreatedAt, Route: r.Route, EvaluatorRef: r.EvaluatorRef, RiskRaw: &risk,
			Level: knownLevel(r.Level), UserRequest: r.UserRequest, ResponseText: r.ResponseText, EvidenceLabels: evidenceLabels(r.Evidence),
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// knownLevel keeps an evaluator level the API names (EvaluatorLevel), nil otherwise.
func knownLevel(l *string) *string {
	if l == nil || !slices.Contains(evaluatorLevels, *l) {
		return nil
	}
	return l
}

// evidenceLabels lists the distinct labels of a body's evidence items, in order.
func evidenceLabels(raw json.RawMessage) []string {
	var items []struct {
		Label string `json:"label"`
	}
	out := []string{}
	if json.Unmarshal(raw, &items) != nil {
		return out
	}
	for _, it := range items {
		if it.Label != "" && !slices.Contains(out, it.Label) {
			out = append(out, it.Label)
		}
	}
	return out
}

// warnThresholds are the warn_at of each evaluator on each route of the active policy (the route's
// override, else the spec's), and every registered version's own warn_at.
func (a *labelsAPI) warnThresholds() (map[string]map[string]float64, map[string]float64) {
	routes, specs := map[string]map[string]float64{}, map[string]float64{}
	if a.registry != nil {
		for ref, spec := range a.registry.Specs() {
			specs[ref] = spec.Outcomes.WarnAt
		}
	}
	if a.policy == nil {
		return routes, specs
	}
	p := a.policy()
	if p == nil {
		return routes, specs
	}
	for _, r := range p.AllRoutes() {
		m := map[string]float64{}
		for _, e := range r.Evaluators {
			warn, ok := specs[e.Ref]
			if e.Thresholds != nil && e.Thresholds.WarnAt != nil {
				warn, ok = *e.Thresholds.WarnAt, true
			}
			if ok {
				m[e.Ref] = warn
			}
		}
		routes[r.Name] = m
	}
	return routes, specs
}

// labelBody is LabelPut and LabelDelete.
type labelBody struct {
	TraceID string  `json:"trace_id"`
	Target  string  `json:"target"`
	Label   string  `json:"label"`
	Note    *string `json:"note"`
}

func (a *labelsAPI) traceID(c *gin.Context, id string) bool {
	if id == "" || len(id) > maxTraceIDBytes {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "trace_id is required: the trace to label.",
			fieldError{Path: "trace_id", Message: "required trace id"})
		return false
	}
	return true
}

// invalidLabel answers 422 validation_failed for one field.
func invalidLabel(c *gin.Context, path, message string) {
	abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, message, fieldError{Path: path, Message: message})
}

// PUT /api/labels (#40): the caller's label of a trace target. It freezes the trace body into the
// label (409 body_expired when the body is gone), copies the evaluator's raw risk and the snapshot
// of the attempt-0 decision, and answers 204. Saving the same label again changes nothing.
func (a *labelsAPI) put(c *gin.Context) {
	var body labelBody
	if !bindJSON(c, &body, false) || !a.available(c) || !a.traceID(c, body.TraceID) {
		return
	}
	var note *string
	if body.Note != nil {
		if n := strings.TrimSpace(*body.Note); n != "" {
			if utf8.RuneCountInString(n) > maxLabelNoteRunes {
				invalidLabel(c, "note", "The note is too long: at most "+strconv.Itoa(maxLabelNoteRunes)+" characters.")
				return
			}
			note = &n
		}
	}
	ctx := c.Request.Context()
	if a.barrier != nil {
		if err := a.barrier(ctx, body.TraceID); err != nil {
			a.internal(c, "flush the trace before labelling", err)
			return
		}
	}
	if _, err := a.store.GetTrace(ctx, body.TraceID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			abortAPI(c, http.StatusNotFound, codeNotFound, "No trace has this id.")
			return
		}
		a.internal(c, "get trace", err)
		return
	}
	lc, ok := a.checkTarget(c, body)
	if !ok {
		return
	}
	b, err := a.store.GetTraceBody(ctx, body.TraceID)
	now := a.now().UTC()
	switch {
	case errors.Is(err, store.ErrNotFound):
		abortAPI(c, http.StatusConflict, codeBodyExpired,
			"This answer's text was not stored, so it can't be labelled (TRUTHTABLE_BODY_RETENTION).")
		return
	case err != nil:
		a.internal(c, "get trace body", err)
		return
	case !now.Before(b.ExpiresAt):
		abortAPI(c, http.StatusConflict, codeBodyExpired, "This answer's text has expired, so it can't be labelled.")
		return
	}
	sample, err := json.Marshal(store.SampleOf(b))
	if err != nil {
		a.internal(c, "freeze the label sample", err)
		return
	}
	retention := a.retention
	if retention <= 0 {
		retention = defaultLabelSampleRetention
	}
	err = a.store.UpsertLabel(ctx, store.LabelRecord{
		TraceID: body.TraceID, Target: body.Target, Label: body.Label, LabeledBy: labeler(c), Note: note,
		Sample: sample, SampleExpiresAt: now.Add(retention), RiskRaw: lc.RiskRaw, Snapshot: lc.Snapshot,
	})
	if err != nil {
		a.internal(c, "save label", err)
		return
	}
	c.Status(http.StatusNoContent)
}

// defaultLabelSampleRetention is TRUTHTABLE_LABEL_SAMPLE_RETENTION's default (90 days).
const defaultLabelSampleRetention = 2160 * time.Hour

// checkTarget validates the target and label pair (D59) against the trace and returns what the
// trace's attempt-0 decision says about it.
func (a *labelsAPI) checkTarget(c *gin.Context, body labelBody) (store.LabelContext, bool) {
	ctx := c.Request.Context()
	switch {
	case labelTargetRef.MatchString(body.Target):
		if body.Label != store.LabelShouldFire && body.Label != store.LabelShouldNotFire {
			invalidLabel(c, "label", "An evaluator label is should_fire or should_not_fire.")
			return store.LabelContext{}, false
		}
		lc, err := a.store.LabelContextOf(ctx, body.TraceID, body.Target)
		switch {
		case err != nil:
			a.internal(c, "label context", err)
			return lc, false
		case !lc.Decided:
			invalidLabel(c, "target", "The gate did not check this answer, so there is no evaluator result to label.")
			return lc, false
		case !lc.Asked:
			invalidLabel(c, "target", body.Target+" did not check this answer.")
			return lc, false
		}
		return lc, true
	case labelTargetClaim.MatchString(body.Target):
		if !slices.Contains(claimLabels, body.Label) {
			invalidLabel(c, "label", "A claim label is SUPPORTED, PARTIALLY_SUPPORTED, CONTRADICTED or UNSUPPORTED.")
			return store.LabelContext{}, false
		}
		pos, _ := strconv.Atoi(strings.TrimPrefix(body.Target, "claim:"))
		ok, err := a.store.TraceHasClaim(ctx, body.TraceID, pos)
		switch {
		case err != nil:
			a.internal(c, "find claim", err)
			return store.LabelContext{}, false
		case !ok:
			invalidLabel(c, "target", "The deep audit of this answer has no claim "+strconv.Itoa(pos)+".")
			return store.LabelContext{}, false
		}
	case body.Target == "response":
		if body.Label != store.LabelHallucinated && body.Label != store.LabelGrounded {
			invalidLabel(c, "label", "A response label is hallucinated or grounded.")
			return store.LabelContext{}, false
		}
	default:
		invalidLabel(c, "target", "The target is an evaluator ref (groundedness@1), claim:<n> or response.")
		return store.LabelContext{}, false
	}
	// Claim and response labels carry the decision's snapshot but no raw risk.
	lc, err := a.store.LabelContextOf(ctx, body.TraceID, "")
	if err != nil {
		a.internal(c, "label context", err)
		return lc, false
	}
	lc.RiskRaw = nil
	return lc, true
}

// DELETE /api/labels (#41): removes the caller's label of a trace target (Undo); 204 whether or not
// there was one.
func (a *labelsAPI) remove(c *gin.Context) {
	var body labelBody
	if !bindJSON(c, &body, false) || !a.available(c) || !a.traceID(c, body.TraceID) {
		return
	}
	if body.Label != "" || body.Note != nil {
		abortAPI(c, http.StatusBadRequest, codeBadRequest, "DELETE /api/labels takes {trace_id, target}.")
		return
	}
	if body.Target == "" {
		invalidLabel(c, "target", "target is required: the label target to remove.")
		return
	}
	if _, err := a.store.DeleteLabel(c.Request.Context(), body.TraceID, body.Target, labeler(c)); err != nil {
		a.internal(c, "delete label", err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *labelsAPI) internal(c *gin.Context, what string, err error) {
	a.log.Error("api: "+what+" failed", "path", c.FullPath(), "err", err)
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request could not be completed; see the proxy log.")
}

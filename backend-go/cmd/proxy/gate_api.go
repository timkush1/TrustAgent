package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// gateReader is the part of the store the gate-decision reads use (store.PostgresStore).
type gateReader interface {
	ListDecisions(ctx context.Context, f store.DecisionFilter) ([]store.DecisionRow, string, error)
	ReliabilitySamples(ctx context.Context, f store.ReliabilityFilter) ([]store.ReliabilitySample, error)
}

// gateAPI serves GET /api/gate/decisions and GET /api/gate/reliability (SPEC §2.4 endpoints 16 and
// 17): the stored gate decisions, one row per trace, and an evaluator's reliability table. Both
// read the database directly, so without one they answer 503.
type gateAPI struct {
	store gateReader // nil without a database
	// snapshot is the decision-model snapshot in effect ("" before the model reported one): the
	// default snapshot of the reliability table.
	snapshot func() string
	// includeMock keeps simulated traffic in the reliability table (the demo flag, as in the
	// overview's include_mock default).
	includeMock bool
	now         func() time.Time
	log         *slog.Logger
}

// Enum values of the /api/gate filters (api.ts GateStatus, EvaluatorLevel).
var (
	decisionStatuses = []string{"ok", "partial", "not_assessed", "skipped", "timeout", "error"}
	evaluatorLevels  = []string{"ok", "warn", "critical"}
)

// evaluatorParam is an evaluator ref ("<id>@<version>") or a bare evaluator id.
var evaluatorParam = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}(@[1-9][0-9]{0,8})?$`)

// available answers 503 persistence_disabled when there is no database.
func (g *gateAPI) available(c *gin.Context) bool {
	if g.store == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"Gate decisions are read from stored traces, and this proxy has no database (TRUTHTABLE_DATABASE_URL).")
		return false
	}
	return true
}

// decisions serves GET /api/gate/decisions.
func (g *gateAPI) decisions(c *gin.Context) {
	if !g.available(c) {
		return
	}
	f, ok := decisionFilter(c)
	if !ok {
		return
	}
	rows, next, err := g.store.ListDecisions(c.Request.Context(), f)
	if errors.Is(err, store.ErrBadCursor) {
		badParam(c, "cursor", "is not a cursor this server issued")
		return
	}
	if err != nil {
		g.internal(c, "list gate decisions", err)
		return
	}
	c.JSON(http.StatusOK, newPage(rows, next))
}

// decisionFilter parses the query of GET /api/gate/decisions.
func decisionFilter(c *gin.Context) (store.DecisionFilter, bool) {
	var f store.DecisionFilter
	var ok bool
	if f.Limit, ok = queryLimit(c); !ok {
		return f, false
	}
	f.Cursor = c.Query("cursor")
	if f.Since, ok = queryTime(c, "since"); !ok {
		return f, false
	}
	if f.UpdatedSince, ok = queryTime(c, "updated_since"); !ok {
		return f, false
	}
	enums := []struct {
		name    string
		dst     *string
		allowed []string
	}{
		{"action", &f.Action, gateActions},
		{"status", &f.Status, decisionStatuses},
		{"level", &f.Level, evaluatorLevels},
	}
	for _, e := range enums {
		if *e.dst, ok = queryEnum(c, e.name, e.allowed...); !ok {
			return f, false
		}
	}
	f.Route = c.Query("route")
	if f.Evaluator = c.Query("evaluator"); f.Evaluator != "" && !evaluatorParam.MatchString(f.Evaluator) {
		badParam(c, "evaluator", "must be an evaluator id or ref, such as groundedness or groundedness@1")
		return f, false
	}
	if f.Disagreement, ok = queryBool(c, "disagreement"); !ok {
		return f, false
	}
	if f.Late, ok = queryBool(c, "late"); !ok {
		return f, false
	}
	return f, true
}

// Reliability label sources (the labels parameter).
const (
	labelsHuman        = "human"
	labelsUniformAudit = "uniform_audit"
	labelsAll          = "all"
)

// reliabilityResponse is api.ts ReliabilityResponse.
type reliabilityResponse struct {
	Evaluator string                    `json:"evaluator"`
	Snapshot  *string                   `json:"snapshot"`
	Window    string                    `json:"window"`
	NBySource map[string]int            `json:"n_by_source"`
	Bins      []decision.ReliabilityBin `json:"bins"`
	ECE       *float64                  `json:"ece"`
	N         int                       `json:"n"`
}

// reliability serves GET /api/gate/reliability: the reliability table of one evaluator's raw risk
// against its labels, with the decision core's ten equal-width bins and ECE. labels picks the
// label sources: human (the labels table: human and public-dataset labels), uniform_audit (the LLM
// judge's verdicts on the uniform deep-audit sample, a proxy) or all (the default). The samples
// come from one decision-model snapshot: snapshot, or the one in effect when it is not given
// (snapshot= with no value takes every snapshot).
// Simulated traffic is left out outside the demo; n_by_source reports how many samples each
// source gave.
func (g *gateAPI) reliability(c *gin.Context) {
	if !g.available(c) {
		return
	}
	evaluator := c.Query("evaluator")
	switch {
	case evaluator == "":
		badParam(c, "evaluator", "is required: an evaluator id or ref, such as groundedness@1")
		return
	case !evaluatorParam.MatchString(evaluator):
		badParam(c, "evaluator", "must be an evaluator id or ref, such as groundedness or groundedness@1")
		return
	}
	window := c.DefaultQuery("window", "7d")
	span, ok := policy.ParseWindow(window)
	if !ok {
		badParam(c, "window", "must be one of 15m, 1h, 24h, 7d, 30d")
		return
	}
	if bins := c.Query("bins"); bins != "" && bins != strconv.Itoa(decision.ReliabilityBins) {
		badParam(c, "bins", "must be 10: the reliability table has ten equal-width bins")
		return
	}
	labels, ok := queryEnum(c, "labels", labelsHuman, labelsUniformAudit, labelsAll)
	if !ok {
		return
	}
	if labels == "" {
		labels = labelsAll
	}
	// No snapshot parameter: the snapshot in effect; an empty one: every snapshot.
	snapshot, given := c.GetQuery("snapshot")
	switch {
	case !given:
		snapshot = g.snapshot()
	case snapshot != "" && !validSnapshot(snapshot):
		badParam(c, "snapshot", "must be a decision-model snapshot name")
		return
	}
	to := g.now().UTC()
	samples, err := g.store.ReliabilitySamples(c.Request.Context(), store.ReliabilityFilter{
		Evaluator: evaluator, Snapshot: snapshot, From: to.Add(-span), To: to,
		Labels: labels != labelsUniformAudit, UniformAudits: labels != labelsHuman, IncludeMock: g.includeMock,
	})
	if err != nil {
		g.internal(c, "read reliability samples", err)
		return
	}
	res := reliabilityResponse{
		Evaluator: evaluator, Snapshot: nonEmpty(snapshot), Window: window, N: len(samples),
		NBySource: map[string]int{store.LabelSourceHuman: 0, store.LabelSourceUniformAudit: 0},
	}
	scores, preds := make([]decision.LabeledScore, len(samples)), make([]float64, len(samples))
	for i, smp := range samples {
		scores[i], preds[i] = decision.LabeledScore{Risk: smp.Risk, Fire: smp.Fire}, smp.Risk
		res.NBySource[smp.Source]++
	}
	res.Bins, res.ECE = decision.Reliability(scores, preds)
	c.JSON(http.StatusOK, res)
}

// validSnapshot accepts a snapshot name: at most 200 printable characters.
func validSnapshot(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// queryBool reads a true/false parameter; nil when absent.
func queryBool(c *gin.Context, name string) (*bool, bool) {
	raw := c.Query(name)
	if raw == "" {
		return nil, true
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		badParam(c, name, "must be true or false")
		return nil, false
	}
	return &b, true
}

func (g *gateAPI) internal(c *gin.Context, what string, err error) {
	g.log.Error("api: "+what+" failed", "path", c.FullPath(), "err", err)
	abortAPI(c, http.StatusInternalServerError, codeInternal, "The request could not be completed; see the proxy log.")
}

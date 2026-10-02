package main

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// GET /api/overview, /api/roi/summary and /api/evidence/coverage (SPEC §2.4 rows 9, 47 and 50;
// §7.1, §7.4; the ROI handlers are in roi_api.go). Every figure is counted from stored traces,
// decisions, audits and cost rows, and carries its value label: measured, estimate (a model of
// what something would have cost) or simulated (mock backends, test-mode answers). Mock and test
// traffic is left out unless include_mock is set, which defaults to the demo flag.

// statsStore reads the aggregates (store.PostgresStore).
type statsStore interface {
	Overview(ctx context.Context, f store.StatsFilter, bucket time.Duration) (*store.OverviewData, error)
	EvidenceCoverage(ctx context.Context, f store.StatsFilter) (*store.CoverageData, error)
	Roi(ctx context.Context, f store.StatsFilter) (*store.RoiData, error)
	RoiSeries(ctx context.Context, f store.StatsFilter, bucket time.Duration) ([]store.RoiPoint, error)
	UniformToday(ctx context.Context, now time.Time, limit int) (int64, *time.Time, error)
}

// minRateN is LabelledRate.min_n: below it a rate is not shown (SPEC §2.6).
const minRateN = 30

// statsTimeout bounds one aggregate request.
const statsTimeout = 10 * time.Second

// Value labels (api.ts ValueLabel).
const (
	labelMeasured  = "measured"
	labelEstimate  = "estimate"
	labelSimulated = "simulated"
)

// mountStats registers the aggregate endpoints on the /api group.
func (s *server) mountStats(api *gin.RouterGroup, read, rateLimit gin.HandlerFunc) {
	api.GET("/overview", read, rateLimit, s.handleOverview)
	api.GET("/roi/summary", read, rateLimit, s.handleRoiSummary)
	api.GET("/evidence/coverage", read, rateLimit, s.handleEvidenceCoverage)
}

// labelledValue is api.ts LabelledValue.
type labelledValue struct {
	Value *float64 `json:"value"`
	Label string   `json:"label"`
	Basis string   `json:"basis"`
}

// labelledRate is api.ts LabelledRate: value null below min_n.
type labelledRate struct {
	labelledValue
	N    int64       `json:"n"`
	CI95 *[2]float64 `json:"ci95"`
	MinN int         `json:"min_n"`
}

type percentiles struct {
	P50 *float64 `json:"p50"`
	P95 *float64 `json:"p95"`
}

type overviewTotals struct {
	Requests        int64   `json:"requests"`
	Observed        int64   `json:"observed"`
	Passed          int64   `json:"passed"`
	Flagged         int64   `json:"flagged"`
	Blocked         int64   `json:"blocked"`
	Fallback        int64   `json:"fallback"`
	NotAssessed     int64   `json:"not_assessed"`
	GateErrors      int64   `json:"gate_errors"`
	Audited         int64   `json:"audited"`
	AuditsFailed    int64   `json:"audits_failed"`
	DecisionCostUSD float64 `json:"decision_cost_usd"`
	LLMCostUSD      float64 `json:"llm_cost_usd"`
}

// overviewCoverage is OverviewStats.evidence_coverage. rate is groundedness_assessed / requests:
// the share of requests whose groundedness was checked against documents they carried (the
// Overview headline), null without requests.
type overviewCoverage struct {
	WithEvidence         int64            `json:"with_evidence"`
	GroundednessAssessed int64            `json:"groundedness_assessed"`
	Rate                 *float64         `json:"rate"`
	BySource             map[string]int64 `json:"by_source"`
}

type outcomePoint struct {
	T          time.Time `json:"t"`
	Passed     int64     `json:"passed"`
	WouldFlag  int64     `json:"would_flag"`
	Flagged    int64     `json:"flagged"`
	Blocked    int64     `json:"blocked"`
	Replaced   int64     `json:"replaced"`
	NotChecked int64     `json:"not_checked"`
	Error      int64     `json:"error"`
}

type caughtRow struct {
	Reason        string `json:"reason"`
	Count         int64  `json:"count"`
	LatestTraceID string `json:"latest_trace_id"`
}

type routeRow struct {
	Route             string       `json:"route"`
	Requests          int64        `json:"requests"`
	Flagged           int64        `json:"flagged"`
	Blocked           int64        `json:"blocked"`
	Audited           int64        `json:"audited"`
	HallucinationRate labelledRate `json:"hallucination_rate"`
}

type modelRow struct {
	Model             string       `json:"model"`
	Requests          int64        `json:"requests"`
	Audited           int64        `json:"audited"`
	HallucinationRate labelledRate `json:"hallucination_rate"`
}

type contradictedRow struct {
	Claim         *string `json:"claim"`
	Count         int64   `json:"count"`
	LatestTraceID string  `json:"latest_trace_id"`
}

// overviewStats is api.ts OverviewStats.
type overviewStats struct {
	Window                    string                 `json:"window"`
	Bucket                    string                 `json:"bucket"`
	From                      time.Time              `json:"from"`
	To                        time.Time              `json:"to"`
	IncludeMock               bool                   `json:"include_mock"`
	Totals                    overviewTotals         `json:"totals"`
	HallucinationRate         labelledRate           `json:"hallucination_rate"`
	AuditedHallucinationShare labelledRate           `json:"audited_hallucination_share"`
	EvidenceCoverage          overviewCoverage       `json:"evidence_coverage"`
	OutcomesSeries            []outcomePoint         `json:"outcomes_series"`
	WhatWasCaught             []caughtRow            `json:"what_was_caught"`
	LatencyMs                 map[string]percentiles `json:"latency_ms"`
	ByRoute                   []routeRow             `json:"by_route"`
	ByModel                   []modelRow             `json:"by_model"`
	ClaimVerdictsAmongAudited map[string]int64       `json:"claim_verdicts_among_audited"`
	TopContradicted           []contradictedRow      `json:"top_contradicted"`
	StageLatencyMs            map[string]percentiles `json:"stage_latency_ms"`
	BackendMix                map[string]int64       `json:"backend_mix"`
}

// overviewBuckets maps a window onto its outcome-series bucket.
var overviewBuckets = map[string]struct {
	name  string
	width time.Duration
}{
	"15m": {"1m", time.Minute}, "1h": {"1m", time.Minute}, "24h": {"15m", 15 * time.Minute},
	"7d": {"6h", 6 * time.Hour}, "30d": {"1d", 24 * time.Hour},
}

// Rate bases (LabelledValue.basis).
const (
	basisUniformRate  = "uniform-sample deep audits only; answers the audit could not assess are left out"
	basisAuditedShare = "among audited answers (selected by risk; not a traffic rate)"
)

// claimVerdicts are the ClaimVerdict values, all present in claim_verdicts_among_audited.
var claimVerdicts = []string{"SUPPORTED", "PARTIALLY_SUPPORTED", "CONTRADICTED", "UNSUPPORTED", "UNKNOWN", "NOT_CHECKED"}

// statsQuery is the parsed window, route and include_mock of an aggregate request.
type statsQuery struct {
	window string
	filter store.StatsFilter
}

// parseStatsQuery reads window (default def), route and include_mock (default: demo mode).
func (s *server) parseStatsQuery(c *gin.Context, def string) (statsQuery, bool) {
	q := statsQuery{window: c.DefaultQuery("window", def)}
	d, ok := policy.ParseWindow(q.window)
	if !ok {
		badParam(c, "window", "must be one of 15m, 1h, 24h, 7d, 30d")
		return q, false
	}
	include := s.cfg.DemoMode
	if raw := c.Query("include_mock"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			badParam(c, "include_mock", "must be true or false")
			return q, false
		}
		include = b
	}
	to := time.Now().UTC()
	q.filter = store.StatsFilter{From: to.Add(-d), To: to, Route: strings.TrimSpace(c.Query("route")), IncludeMock: include}
	return q, true
}

// statsReady answers 503 without a database: the aggregates are counted from stored traces.
func (s *server) statsReady(c *gin.Context) bool {
	if s.stats == nil {
		abortAPI(c, http.StatusServiceUnavailable, codePersistenceDisabled,
			"These numbers are counted from stored traces, and this proxy has no database (TRUTHTABLE_DATABASE_URL).")
		return false
	}
	return true
}

func (s *server) statsFailed(c *gin.Context, what string, err error) {
	s.log.Error(what+" failed", "err", err)
	if errors.Is(err, context.DeadlineExceeded) {
		abortAPI(c, http.StatusGatewayTimeout, "timeout", "Counting "+what+" took too long; try a shorter window.")
		return
	}
	abortAPI(c, http.StatusInternalServerError, codeInternal, "Counting "+what+" failed.")
}

// rate builds a LabelledRate from verdict counts: the value and its Wilson 95% interval appear
// from min_n assessed answers on.
func rate(n store.RateCounts, label, basis string) labelledRate {
	r := labelledRate{labelledValue: labelledValue{Label: label, Basis: basis}, N: n.Assessed, MinN: minRateN}
	if n.Assessed >= minRateN {
		v := float64(n.Hallucinated) / float64(n.Assessed)
		lo, hi := decision.WilsonInterval(int(n.Hallucinated), int(n.Assessed), 1.96)
		r.Value, r.CI95 = &v, &[2]float64{lo, hi}
	}
	return r
}

func pct(p store.Percentiles) percentiles { return percentiles{P50: p.P50, P95: p.P95} }

// dataLabel is measured, or simulated when the counted traffic includes simulated traces.
func (s *server) dataLabel(mock int64) string {
	if s.cfg.DemoMode || mock > 0 {
		return labelSimulated
	}
	return labelMeasured
}

func (s *server) handleOverview(c *gin.Context) {
	if !s.statsReady(c) {
		return
	}
	q, ok := s.parseStatsQuery(c, "24h")
	if !ok {
		return
	}
	b := overviewBuckets[q.window]
	ctx, cancel := context.WithTimeout(c.Request.Context(), statsTimeout)
	defer cancel()
	o, err := s.stats.Overview(ctx, q.filter, b.width)
	if err != nil {
		s.statsFailed(c, "the overview", err)
		return
	}
	label := s.dataLabel(o.Totals.Mock)
	t := o.Totals
	out := overviewStats{
		Window: q.window, Bucket: b.name, From: q.filter.From, To: q.filter.To, IncludeMock: q.filter.IncludeMock,
		Totals: overviewTotals{
			Requests: t.Requests, Observed: t.Observed, Passed: t.Passed, Flagged: t.Flagged, Blocked: t.Blocked,
			Fallback: t.Fallback, NotAssessed: t.NotAssessed, GateErrors: t.GateErrors, Audited: t.Audited,
			AuditsFailed: t.AuditsFailed, DecisionCostUSD: roundUSD(t.DecisionCostUSD), LLMCostUSD: roundUSD(o.LLMCostUSD),
		},
		HallucinationRate:         rate(o.Uniform, label, basisUniformRate),
		AuditedHallucinationShare: rate(o.Audited, label, basisAuditedShare),
		EvidenceCoverage: overviewCoverage{
			WithEvidence: o.Evidence.WithEvidence, GroundednessAssessed: o.Evidence.GroundednessAssessed,
			Rate: share(o.Evidence.GroundednessAssessed, t.Requests), BySource: o.Evidence.BySource,
		},
		OutcomesSeries:            make([]outcomePoint, 0, len(o.Series)),
		WhatWasCaught:             make([]caughtRow, 0, len(o.Caught)),
		LatencyMs:                 map[string]percentiles{},
		ByRoute:                   make([]routeRow, 0, len(o.ByRoute)),
		ByModel:                   make([]modelRow, 0, len(o.ByModel)),
		ClaimVerdictsAmongAudited: map[string]int64{},
		TopContradicted:           make([]contradictedRow, 0, len(o.TopContradicted)),
		StageLatencyMs:            map[string]percentiles{},
		BackendMix:                o.BackendMix,
	}
	for _, p := range o.Series {
		out.OutcomesSeries = append(out.OutcomesSeries, outcomePoint{
			T: p.T, Passed: p.Counts["passed"],
			// Observe-mode answers the gate would have acted on (would flag or would block).
			WouldFlag: p.Counts["would_flag"] + p.Counts["would_block"],
			Flagged:   p.Counts["flagged"], Blocked: p.Counts["blocked"], Replaced: p.Counts["replaced"],
			NotChecked: p.Counts["not_checked"], Error: p.Counts["error"],
		})
	}
	for _, r := range o.Caught {
		out.WhatWasCaught = append(out.WhatWasCaught, caughtRow{Reason: r.Reason, Count: r.Count, LatestTraceID: r.LatestTraceID})
	}
	for _, k := range []string{"overhead", "gate", "upstream", "audit"} {
		out.LatencyMs[k] = pct(o.Latency[k])
	}
	// Detectors, evidence extraction and knowledge retrieval are not timed per trace yet.
	for _, k := range []string{"detectors", "evidence", "knowledge", "decision", "queue_wait", "decompose", "retrieve", "verify", "score"} {
		out.StageLatencyMs[k] = pct(o.Stages[k])
	}
	for _, g := range o.ByRoute {
		out.ByRoute = append(out.ByRoute, routeRow{Route: g.Key, Requests: g.Requests, Flagged: g.Flagged, Blocked: g.Blocked,
			Audited: g.Audited, HallucinationRate: rate(g.Uniform, label, basisUniformRate)})
	}
	for _, g := range o.ByModel {
		out.ByModel = append(out.ByModel, modelRow{Model: g.Key, Requests: g.Requests, Audited: g.Audited,
			HallucinationRate: rate(g.Uniform, label, basisUniformRate)})
	}
	for _, v := range claimVerdicts {
		out.ClaimVerdictsAmongAudited[v] = o.ClaimVerdicts[v]
	}
	content := canSeeContent(c)
	for _, r := range o.TopContradicted {
		claim := r.Claim
		if !content {
			claim = nil // claim text needs the content scope (SPEC §2.4)
		}
		out.TopContradicted = append(out.TopContradicted, contradictedRow{Claim: claim, Count: r.Count, LatestTraceID: r.LatestTraceID})
	}
	c.JSON(http.StatusOK, out)
}

// evidenceCoverage is api.ts EvidenceCoverage.
type evidenceCoverage struct {
	Window               string           `json:"window"`
	Route                *string          `json:"route"`
	Requests             int64            `json:"requests"`
	WithEvidence         int64            `json:"with_evidence"`
	GroundednessAssessed int64            `json:"groundedness_assessed"`
	BySource             map[string]int64 `json:"by_source"`
	TopReasons           []reasonCount    `json:"top_reasons"`
}

type reasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

func (s *server) handleEvidenceCoverage(c *gin.Context) {
	if !s.statsReady(c) {
		return
	}
	q, ok := s.parseStatsQuery(c, "24h")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), statsTimeout)
	defer cancel()
	cov, err := s.stats.EvidenceCoverage(ctx, q.filter)
	if err != nil {
		s.statsFailed(c, "evidence coverage", err)
		return
	}
	out := evidenceCoverage{
		Window: q.window, Requests: cov.Requests, WithEvidence: cov.WithEvidence,
		GroundednessAssessed: cov.GroundednessAssessed, BySource: cov.BySource, TopReasons: []reasonCount{},
	}
	if q.filter.Route != "" {
		out.Route = &q.filter.Route
	}
	for _, r := range store.CoverageReasons {
		if n := cov.Reasons[r]; n > 0 {
			out.TopReasons = append(out.TopReasons, reasonCount{Reason: r, Count: n})
		}
	}
	// Most frequent first.
	for i := 1; i < len(out.TopReasons); i++ {
		for j := i; j > 0 && out.TopReasons[j].Count > out.TopReasons[j-1].Count; j-- {
			out.TopReasons[j], out.TopReasons[j-1] = out.TopReasons[j-1], out.TopReasons[j]
		}
	}
	c.JSON(http.StatusOK, out)
}

func share(part, whole int64) *float64 {
	if whole <= 0 {
		return nil
	}
	v := float64(part) / float64(whole)
	return &v
}

// roundUSD keeps ten decimals (the NUMERIC(14,10) of the cost columns).
func roundUSD(v float64) float64 { return math.Round(v*1e10) / 1e10 }

func ptrF(v float64) *float64 { return &v }

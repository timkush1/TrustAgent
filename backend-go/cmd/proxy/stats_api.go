package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/contracts"
)

// GET /api/overview, /api/roi/summary and /api/evidence/coverage (SPEC §2.4 rows 9, 47 and 50;
// §7.1, §7.4). Every figure is counted from stored traces, decisions, audits and cost rows, and
// carries its value label: measured, estimate (a model of what something would have cost) or
// simulated (mock backends, test-mode answers). Mock and test traffic is left out unless
// include_mock is set, which defaults to the demo flag.

// statsStore reads the aggregates (store.PostgresStore).
type statsStore interface {
	Overview(ctx context.Context, f store.StatsFilter, bucket time.Duration) (*store.OverviewData, error)
	EvidenceCoverage(ctx context.Context, f store.StatsFilter) (*store.CoverageData, error)
	Roi(ctx context.Context, f store.StatsFilter) (*store.RoiData, error)
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

// roiSummary is api.ts RoiSummary.
type roiSummary struct {
	Window         string           `json:"window"`
	GatedResponses int64            `json:"gated_responses"`
	Simulated      bool             `json:"simulated"`
	DecisionCost   roiDecisionCost  `json:"decision_cost"`
	LLMCost        roiLLMCost       `json:"llm_cost"`
	TotalPer1k     labelledValue    `json:"total_per_1k"`
	Counterfactual []counterfactual `json:"counterfactuals"`
	Savings        roiSavings       `json:"savings_vs_same_checks"`
	Outcomes       roiOutcomes      `json:"outcomes"`
	LatencyMs      roiLatency       `json:"latency_ms"`
	Sampling       roiSampling      `json:"sampling"`
	ThirdParty     []string         `json:"third_party"`
}

type roiDecisionCost struct {
	labelledValue
	USD   float64  `json:"usd"`
	Per1k *float64 `json:"per_1k"`
}

type roiLLMCost struct {
	labelledValue
	USD         float64          `json:"usd"`
	Audits      int64            `json:"audits"`
	BySelection map[string]int64 `json:"by_selection"`
}

type counterfactual struct {
	Kind   string   `json:"kind"`
	Method string   `json:"method"`
	USD    *float64 `json:"usd"`
	Low    *float64 `json:"low"`
	High   *float64 `json:"high"`
	Per1k  *float64 `json:"per_1k"`
	N      int64    `json:"n"`
	Label  string   `json:"label"`
	Basis  string   `json:"basis"`
}

type roiSavings struct {
	USDLow  *float64 `json:"usd_low"`
	USDHigh *float64 `json:"usd_high"`
	Label   string   `json:"label"`
}

type roiOutcomes struct {
	Flagged     int64  `json:"flagged"`
	Blocked     int64  `json:"blocked"`
	Fallback    int64  `json:"fallback"`
	NotAssessed int64  `json:"not_assessed"`
	Label       string `json:"label"`
}

type roiLatency struct {
	GateP50          *float64 `json:"gate_p50"`
	GateP95          *float64 `json:"gate_p95"`
	DeadlineMissRate *float64 `json:"deadline_miss_rate"`
	Label            string   `json:"label"`
}

type roiSampling struct {
	UniformRate      float64    `json:"uniform_rate"`
	UniformToday     int64      `json:"uniform_today"`
	UniformCap       int        `json:"uniform_cap"`
	UniformStoppedAt *time.Time `json:"uniform_stopped_at"`
	LLMCapHitAt      *time.Time `json:"llm_cap_hit_at"`
}

// costLabel labels a sum by where its rows' costs came from: simulated when any is simulated,
// estimate when any was computed from tokens and the price table (or is unknown), else measured
// (reported by the provider per call).
func costLabel(c store.CostCounts, what string) (string, string) {
	switch {
	case c.Simulated > 0:
		return labelSimulated, what + ": simulated by the mock backends, not a real spend"
	case c.Computed+c.Other > 0:
		return labelEstimate, what + ": tokens × the price table where the provider did not report a cost"
	case c.Reported > 0:
		return labelMeasured, what + ": cost reported by the provider per call"
	}
	return labelMeasured, what + ": no calls in this window"
}

func (s *server) handleRoiSummary(c *gin.Context) {
	if !s.statsReady(c) {
		return
	}
	q, ok := s.parseStatsQuery(c, "7d")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), statsTimeout)
	defer cancel()
	r, err := s.stats.Roi(ctx, q.filter)
	if err != nil {
		s.statsFailed(c, "costs", err)
		return
	}
	simulated := s.dataLabel(r.Mock) == labelSimulated || r.DecisionSources.Simulated > 0 || r.LLMSources.Simulated > 0
	dataLabel := labelMeasured
	if simulated {
		dataLabel = labelSimulated
	}
	n := r.Checked
	out := roiSummary{Window: q.window, GatedResponses: n, Simulated: simulated, ThirdParty: []string{}}

	dLabel, dBasis := costLabel(r.DecisionSources, "fast-check calls")
	out.DecisionCost = roiDecisionCost{labelledValue: labelledValue{Value: ptrF(roundUSD(r.DecisionUSD)), Label: dLabel, Basis: dBasis},
		USD: roundUSD(r.DecisionUSD), Per1k: per1k(r.DecisionUSD, n)}
	lLabel, lBasis := costLabel(r.LLMSources, "deep-audit judge calls")
	by := map[string]int64{}
	for sel, cnt := range r.AuditsBySelection {
		if slices.Contains(auditSelections, sel) {
			by[sel] = cnt
		}
	}
	out.LLMCost = roiLLMCost{labelledValue: labelledValue{Value: ptrF(roundUSD(r.LLMUSD)), Label: lLabel, Basis: lBasis},
		USD: roundUSD(r.LLMUSD), Audits: r.Audits, BySelection: by}
	out.TotalPer1k = labelledValue{Value: per1k(r.DecisionUSD+r.LLMUSD, n), Label: weakest(dLabel, lLabel),
		Basis: "fast check plus deep audit, per 1,000 answers the fast check assessed"}

	prices := judgePrice()
	same := counterfactual{Kind: "llm_same_checks", Method: "token_model", N: r.SameChecksCalls, Label: labelEstimate,
		Basis: "the same questions asked of " + prices.name + " at list price: (fast-check input tokens + 400) × input price + 40 output tokens per question × output price"}
	if simulated {
		same.Label = labelSimulated
	}
	if r.SameChecksCalls > 0 && prices.ok {
		usd := (float64(r.SameChecksInputTokens+400*r.SameChecksCalls)*prices.in + float64(40*r.SameChecksQuestions)*prices.out) / 1e6
		same.USD, same.Per1k = ptrF(roundUSD(usd)), per1k(usd, r.SameChecksCalls)
		// Savings: the fast check replaces the same checks (high); counting the deep audits'
		// spend against it gives the low end.
		lo, hi := usd-r.DecisionUSD-r.LLMUSD, usd-r.DecisionUSD
		out.Savings = roiSavings{USDLow: ptrF(roundUSD(lo)), USDHigh: ptrF(roundUSD(hi)), Label: same.Label}
	} else {
		out.Savings = roiSavings{Label: same.Label}
	}
	out.Counterfactual = []counterfactual{same, s.fullAuditCounterfactual(r, n, simulated)}
	if prices.note != "" {
		out.ThirdParty = append(out.ThirdParty, prices.note)
	}

	out.Outcomes = roiOutcomes{Flagged: r.Flagged, Blocked: r.Blocked, Fallback: r.Fallback, NotAssessed: r.NotAssessed, Label: dataLabel}
	out.LatencyMs = roiLatency{GateP50: r.GateLatency.P50, GateP95: r.GateLatency.P95, Label: dataLabel}
	if r.Decisions > 0 {
		out.LatencyMs.DeadlineMissRate = ptrF(float64(r.DeadlineMisses) / float64(r.Decisions))
	}

	out.Sampling = roiSampling{UniformCap: s.cfg.UniformAuditsPerDay}
	if pol := s.policies.Engine.Current(); pol != nil && pol.Default != nil {
		out.Sampling.UniformRate = pol.Default.DeepAudit.UniformSampleRate
	}
	if today, stopped, err := s.stats.UniformToday(ctx, time.Now(), s.cfg.UniformAuditsPerDay); err == nil {
		out.Sampling.UniformToday, out.Sampling.UniformStoppedAt = today, stopped
	} else {
		s.log.Warn("uniform sample count failed", "err", err)
	}
	c.JSON(http.StatusOK, out)
}

// fullAuditCounterfactual is llm_full_claim_audit: the mean cost of uniform audits × N once 30
// uniform audits are costed (with a normal 95% interval), else the static assumption of
// contracts/claims/value-claims.v1.json, which is not measured.
func (s *server) fullAuditCounterfactual(r *store.RoiData, n int64, simulated bool) counterfactual {
	label := labelEstimate
	if simulated {
		label = labelSimulated
	}
	if r.UniformCosted >= minRateN {
		usd := r.UniformMeanUSD * float64(n)
		half := 1.96 * r.UniformStddevUSD / math.Sqrt(float64(r.UniformCosted)) * float64(n)
		return counterfactual{Kind: "llm_full_claim_audit", Method: "uniform_sample", USD: ptrF(roundUSD(usd)),
			Low: ptrF(roundUSD(math.Max(0, usd-half))), High: ptrF(roundUSD(usd + half)), Per1k: ptrF(roundUSD(r.UniformMeanUSD * 1000)),
			N: r.UniformCosted, Label: label, Basis: "mean deep-audit cost of the uniform sample × the answers checked"}
	}
	claim := staticFullAudit()
	cf := counterfactual{Kind: "llm_full_claim_audit", Method: "static_assumptions", N: 0, Label: label,
		Basis: "not measured: " + claim.basis}
	if claim.per1k > 0 {
		cf.Per1k = ptrF(claim.per1k)
		cf.USD = ptrF(roundUSD(claim.per1k * float64(n) / 1000))
	}
	return cf
}

// judgePriceEntry is the list price of the counterfactual judge (Claude Haiku 4.5).
type judgePriceEntry struct {
	name    string
	in, out float64
	note    string
	ok      bool
}

var judgePrice = sync.OnceValue(func() judgePriceEntry {
	p := judgePriceEntry{name: "Claude Haiku 4.5"}
	b, err := contracts.FS.ReadFile("pricing/prices.v1.json")
	if err != nil {
		return p
	}
	var doc struct {
		Entries []struct {
			Match    string  `json:"match"`
			Provider string  `json:"provider"`
			In       float64 `json:"input_per_mtok"`
			Out      float64 `json:"output_per_mtok"`
			AsOf     string  `json:"as_of"`
			Note     string  `json:"note"`
		} `json:"entries"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return p
	}
	for _, e := range doc.Entries {
		if e.Provider == "anthropic" && strings.HasPrefix(e.Match, "claude-haiku-4-5") {
			p.in, p.out, p.ok = e.In, e.Out, true
			p.note = "Claude Haiku 4.5 list price $" + strconv.FormatFloat(e.In, 'f', -1, 64) + " / $" +
				strconv.FormatFloat(e.Out, 'f', -1, 64) + " per million tokens (Anthropic, as of " + e.AsOf + ")"
			return p
		}
	}
	return p
})

type staticClaim struct {
	per1k float64
	basis string
}

var staticFullAudit = sync.OnceValue(func() staticClaim {
	c := staticClaim{basis: "a static assumption about a full claim-level LLM audit"}
	b, err := contracts.FS.ReadFile("claims/value-claims.v1.json")
	if err != nil {
		return c
	}
	var doc struct {
		Claims []struct {
			ID    string  `json:"id"`
			Value float64 `json:"value"`
			Basis string  `json:"basis"`
		} `json:"claims"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return c
	}
	for _, cl := range doc.Claims {
		if cl.ID == "llm_full_audit_cost_per_1k" {
			c.per1k, c.basis = cl.Value, strings.TrimSuffix(cl.Basis, ".")
		}
	}
	return c
})

// weakest is the weaker of two labels: simulated, then estimate, then measured.
func weakest(a, b string) string {
	rank := map[string]int{labelSimulated: 0, labelEstimate: 1, labelMeasured: 2}
	if rank[a] <= rank[b] {
		return a
	}
	return b
}

func share(part, whole int64) *float64 {
	if whole <= 0 {
		return nil
	}
	v := float64(part) / float64(whole)
	return &v
}

func per1k(usd float64, n int64) *float64 {
	if n <= 0 {
		return nil
	}
	return ptrF(roundUSD(usd / float64(n) * 1000))
}

// roundUSD keeps ten decimals (the NUMERIC(14,10) of the cost columns).
func roundUSD(v float64) float64 { return math.Round(v*1e10) / 1e10 }

func ptrF(v float64) *float64 { return &v }

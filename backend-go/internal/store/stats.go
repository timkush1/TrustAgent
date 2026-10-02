package store

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Aggregates behind GET /api/overview, /api/roi/summary and /api/evidence/coverage (SPEC §2.4 rows
// 9, 47 and 50). They are plain reads over traces, gate_decisions, audits, audit_claims and
// cost_events; every number is counted from stored rows, never estimated here (the handlers add
// the labels and the counterfactual models).

// StatsFilter selects the traces an aggregate counts: created in [From, To), on Route when set,
// and without mock or test traffic unless IncludeMock.
type StatsFilter struct {
	From, To    time.Time
	Route       string
	IncludeMock bool
}

// mockTraceSQL marks a trace whose answer or checks are simulated: test-mode scripted answers, the
// mock decision backend, or a mock judge.
const mockTraceSQL = `(t.source = 'test' OR COALESCE(t.decision_backend, '') = 'mock'
	OR t.flags && ARRAY['mock_decision','mock_judge','test_mode']::TEXT[])`

// traceWhere is the WHERE clause of a traces row aliased t, over the arguments of args. The
// aggregates are about gateway traffic: the trace a direct audit (POST /api/audit, source direct)
// records is not a gateway request, and neither is its audit.
const traceWhere = `t.created_at >= $1 AND t.created_at < $2 AND t.source <> 'direct' AND ($3 = '' OR t.route = $3) AND ($4::BOOLEAN OR NOT ` + mockTraceSQL + `)`

func (f StatsFilter) args(extra ...any) []any {
	return append([]any{f.From, f.To, f.Route, f.IncludeMock}, extra...)
}

// evidenceUsedSQL is the number of evidence sources a trace's answer was checked against.
const evidenceUsedSQL = `(CASE WHEN jsonb_typeof(t.evidence_summary->'used') = 'array'
	THEN jsonb_array_length(t.evidence_summary->'used') ELSE 0 END)`

// groundednessAssessedSQL holds when the first decision of trace t assessed a groundedness
// evaluator (any version): the answer was checked against the documents it carried.
const groundednessAssessedSQL = `EXISTS (SELECT 1 FROM gate_decisions gd,
	jsonb_each(CASE WHEN jsonb_typeof(gd.decision->'evaluators') = 'object' THEN gd.decision->'evaluators' ELSE '{}'::JSONB END) ev
	WHERE gd.trace_id = t.trace_id AND gd.attempt = 0 AND ev.key LIKE 'groundedness@%' AND ev.value->>'status' = 'assessed')`

// EvidenceSources are the evidence sources in their SPEC §2.10 order.
var EvidenceSources = []string{"request_system", "request_context", "tool_results", "extension", "knowledge"}

// Percentiles is a p50/p95 pair; nil when no row had a value.
type Percentiles struct {
	P50, P95 *float64
}

// TrafficTotals are the per-action counts of the traces in a window.
type TrafficTotals struct {
	Requests, Observed, Passed, Flagged, Blocked, Fallback int64
	// NotAssessed counts gate_status not_assessed or skipped; GateErrors error or timeout.
	NotAssessed, GateErrors int64
	// Audited counts traces whose deep audit completed (or completed degraded); AuditsFailed failed.
	Audited, AuditsFailed int64
	DecisionCostUSD       float64
	// Mock counts the simulated traces among Requests (only with IncludeMock).
	Mock int64
}

// EvidenceCounts is how many traces carried evidence, per source, and how many were checked for
// groundedness.
type EvidenceCounts struct {
	Requests, WithEvidence, GroundednessAssessed int64
	BySource                                     map[string]int64
}

// RateCounts are the deep-audit verdict counts behind a hallucination rate: Assessed audits had a
// verdict other than NOT_ASSESSED, Hallucinated of them HALLUCINATED.
type RateCounts struct {
	Assessed, Hallucinated int64
}

// GroupStats are the per-route or per-model rows of the overview.
type GroupStats struct {
	Key                                 string
	Requests, Flagged, Blocked, Audited int64
	Uniform, All                        RateCounts
}

// OutcomePoint is one bucket of the outcome series, counted by outcomes.v1 outcome id.
type OutcomePoint struct {
	T      time.Time
	Counts map[string]int64
}

// ReasonCount is a reason with its count and newest trace.
type ReasonCount struct {
	Reason        string
	Count         int64
	LatestTraceID string
}

// ClaimCount is a contradicted claim (nil text when bodies are not kept) with its count.
type ClaimCount struct {
	Claim         *string
	Count         int64
	LatestTraceID string
}

// OverviewData is everything GET /api/overview counts.
type OverviewData struct {
	Totals     TrafficTotals
	Evidence   EvidenceCounts
	LLMCostUSD float64
	// Uniform counts uniform-sample audits only; Audited every audit with a verdict.
	Uniform, Audited RateCounts
	ByRoute, ByModel []GroupStats
	Series           []OutcomePoint
	Caught           []ReasonCount
	// Latency: overhead, gate, upstream and audit; Stages: decision, queue_wait, decompose,
	// retrieve, verify and score (the others are not recorded per trace).
	Latency, Stages map[string]Percentiles
	ClaimVerdicts   map[string]int64
	TopContradicted []ClaimCount
	BackendMix      map[string]int64
}

// Overview counts the traces of f and their decisions and audits. bucket is the width of the
// outcome series (at least a second).
func (s *PostgresStore) Overview(ctx context.Context, f StatsFilter, bucket time.Duration) (*OverviewData, error) {
	out := &OverviewData{Latency: map[string]Percentiles{}, Stages: map[string]Percentiles{}, ClaimVerdicts: map[string]int64{}, BackendMix: map[string]int64{}}
	var err error
	if out.Totals, out.Evidence, err = s.trafficTotals(ctx, f, out.Latency); err != nil {
		return nil, err
	}
	if err := s.auditRates(ctx, f, out); err != nil {
		return nil, err
	}
	if err := s.groups(ctx, f, out); err != nil {
		return nil, err
	}
	if out.Series, err = s.outcomeSeries(ctx, f, bucket); err != nil {
		return nil, err
	}
	if out.Caught, err = s.caught(ctx, f); err != nil {
		return nil, err
	}
	if err := s.auditLatency(ctx, f, out); err != nil {
		return nil, err
	}
	if err := s.claimStats(ctx, f, out); err != nil {
		return nil, err
	}
	// The spend is the ledger's, counted as ROI counts it (D50): fast-check calls of the gate,
	// hedge losers and late attempts included, and the deep audits' LLM calls.
	sums, err := s.costSums(ctx, f)
	if err != nil {
		return nil, err
	}
	out.Totals.DecisionCostUSD, out.LLMCostUSD = sums.DecisionUSD, sums.LLMUSD
	rows, err := s.pool.Query(ctx, `SELECT t.decision_backend, count(*) FROM traces t
		WHERE `+traceWhere+` AND t.decision_backend IS NOT NULL GROUP BY 1`, f.args()...)
	if err != nil {
		return nil, fmt.Errorf("backend mix: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var backend string
		var n int64
		if err := rows.Scan(&backend, &n); err != nil {
			return nil, err
		}
		out.BackendMix[backend] = n
	}
	return out, rows.Err()
}

// trafficTotals reads the action counts, the evidence counts and the trace latencies.
func (s *PostgresStore) trafficTotals(ctx context.Context, f StatsFilter, latency map[string]Percentiles) (TrafficTotals, EvidenceCounts, error) {
	var t TrafficTotals
	e := EvidenceCounts{BySource: map[string]int64{}}
	src := make([]int64, len(EvidenceSources))
	var over, gate, up Percentiles
	dest := []any{
		&t.Requests, &t.Observed, &t.Passed, &t.Flagged, &t.Blocked, &t.Fallback, &t.NotAssessed, &t.GateErrors,
		&t.Audited, &t.AuditsFailed, &t.Mock, &e.WithEvidence, &e.GroundednessAssessed,
	}
	for i := range src {
		dest = append(dest, &src[i])
	}
	dest = append(dest, &over.P50, &over.P95, &gate.P50, &gate.P95, &up.P50, &up.P95)
	bySource := ""
	for i := range EvidenceSources {
		bySource += fmt.Sprintf(",\n\t\tcount(*) FILTER (WHERE t.evidence_summary->'used' @> jsonb_build_array(jsonb_build_object('source', $%d::TEXT)))", 5+i)
	}
	args := f.args()
	for _, s := range EvidenceSources {
		args = append(args, s)
	}
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		count(*) FILTER (WHERE t.action = 'observe'), count(*) FILTER (WHERE t.action = 'pass'),
		count(*) FILTER (WHERE t.action = 'flag'), count(*) FILTER (WHERE t.action = 'block'),
		count(*) FILTER (WHERE t.action = 'fallback'),
		count(*) FILTER (WHERE t.gate_status IN ('not_assessed', 'skipped')),
		count(*) FILTER (WHERE t.gate_status IN ('error', 'timeout')),
		count(*) FILTER (WHERE t.audit_status IN ('completed', 'degraded')),
		count(*) FILTER (WHERE t.audit_status = 'failed'),
		count(*) FILTER (WHERE `+mockTraceSQL+`),
		count(*) FILTER (WHERE `+evidenceUsedSQL+` > 0),
		count(*) FILTER (WHERE `+groundednessAssessedSQL+`)`+bySource+`,
		percentile_cont(0.5) WITHIN GROUP (ORDER BY t.overhead_ms), percentile_cont(0.95) WITHIN GROUP (ORDER BY t.overhead_ms),
		percentile_cont(0.5) WITHIN GROUP (ORDER BY t.gate_ms), percentile_cont(0.95) WITHIN GROUP (ORDER BY t.gate_ms),
		percentile_cont(0.5) WITHIN GROUP (ORDER BY t.upstream_ms), percentile_cont(0.95) WITHIN GROUP (ORDER BY t.upstream_ms)
		FROM traces t WHERE `+traceWhere, args...).Scan(dest...)
	if err != nil {
		return t, e, fmt.Errorf("traffic totals: %w", err)
	}
	e.Requests = t.Requests
	for i, name := range EvidenceSources {
		e.BySource[name] = src[i]
	}
	latency["overhead"], latency["gate"], latency["upstream"] = over, gate, up
	return t, e, nil
}

// auditedTracesFrom joins the audits of the filtered traces (audits aliased a, traces t).
const auditedTracesFrom = `audits a JOIN traces t ON t.trace_id = COALESCE(a.trace_id, a.audit_id)`

// assessedVerdictSQL holds for an audit that reached a verdict about the answer.
const assessedVerdictSQL = `a.status IN ('completed', 'degraded') AND a.verdict IN ('GROUNDED', 'HALLUCINATED', 'UNVERIFIABLE', 'NO_CLAIMS')`

// auditRates reads the hallucination-rate counts overall.
func (s *PostgresStore) auditRates(ctx context.Context, f StatsFilter, out *OverviewData) error {
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE a.selection = 'uniform' AND `+assessedVerdictSQL+`),
		count(*) FILTER (WHERE a.selection = 'uniform' AND `+assessedVerdictSQL+` AND a.verdict = 'HALLUCINATED'),
		count(*) FILTER (WHERE `+assessedVerdictSQL+`),
		count(*) FILTER (WHERE `+assessedVerdictSQL+` AND a.verdict = 'HALLUCINATED')
		FROM `+auditedTracesFrom+` WHERE `+traceWhere, f.args()...).Scan(
		&out.Uniform.Assessed, &out.Uniform.Hallucinated, &out.Audited.Assessed, &out.Audited.Hallucinated)
	if err != nil {
		return fmt.Errorf("audit rates: %w", err)
	}
	return nil
}

// groups reads the per-route and per-model rows.
func (s *PostgresStore) groups(ctx context.Context, f StatsFilter, out *OverviewData) error {
	type key struct{ route, model string }
	traffic := map[key]*GroupStats{}
	rows, err := s.pool.Query(ctx, `
		SELECT t.route, COALESCE(t.model, ''), count(*),
		count(*) FILTER (WHERE t.action = 'flag'), count(*) FILTER (WHERE t.action = 'block'),
		count(*) FILTER (WHERE t.audit_status IN ('completed', 'degraded'))
		FROM traces t WHERE `+traceWhere+` GROUP BY 1, 2`, f.args()...)
	if err != nil {
		return fmt.Errorf("groups: %w", err)
	}
	for rows.Next() {
		var k key
		g := &GroupStats{}
		if err := rows.Scan(&k.route, &k.model, &g.Requests, &g.Flagged, &g.Blocked, &g.Audited); err != nil {
			rows.Close()
			return err
		}
		traffic[k] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT t.route, COALESCE(t.model, ''),
		count(*) FILTER (WHERE a.selection = 'uniform' AND `+assessedVerdictSQL+`),
		count(*) FILTER (WHERE a.selection = 'uniform' AND `+assessedVerdictSQL+` AND a.verdict = 'HALLUCINATED'),
		count(*) FILTER (WHERE `+assessedVerdictSQL+`),
		count(*) FILTER (WHERE `+assessedVerdictSQL+` AND a.verdict = 'HALLUCINATED')
		FROM `+auditedTracesFrom+` WHERE `+traceWhere+` GROUP BY 1, 2`, f.args()...)
	if err != nil {
		return fmt.Errorf("group rates: %w", err)
	}
	for rows.Next() {
		var k key
		var u, a RateCounts
		if err := rows.Scan(&k.route, &k.model, &u.Assessed, &u.Hallucinated, &a.Assessed, &a.Hallucinated); err != nil {
			rows.Close()
			return err
		}
		g, ok := traffic[k]
		if !ok {
			g = &GroupStats{}
			traffic[k] = g
		}
		g.Uniform, g.All = u, a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	byRoute, byModel := map[string]*GroupStats{}, map[string]*GroupStats{}
	add := func(m map[string]*GroupStats, name string, g *GroupStats) {
		dst, ok := m[name]
		if !ok {
			dst = &GroupStats{Key: name}
			m[name] = dst
		}
		dst.Requests += g.Requests
		dst.Flagged += g.Flagged
		dst.Blocked += g.Blocked
		dst.Audited += g.Audited
		dst.Uniform.Assessed += g.Uniform.Assessed
		dst.Uniform.Hallucinated += g.Uniform.Hallucinated
		dst.All.Assessed += g.All.Assessed
		dst.All.Hallucinated += g.All.Hallucinated
	}
	for k, g := range traffic {
		add(byRoute, k.route, g)
		if k.model != "" {
			add(byModel, k.model, g)
		}
	}
	out.ByRoute, out.ByModel = sortedGroups(byRoute), sortedGroups(byModel)
	return nil
}

// sortedGroups orders groups by requests, then name.
func sortedGroups(m map[string]*GroupStats) []GroupStats {
	out := make([]GroupStats, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// outcomeSeries counts the traces per bucket and outcome (contracts/ui/outcomes.v1.json), with
// every bucket of the window present.
func (s *PostgresStore) outcomeSeries(ctx context.Context, f StatsFilter, bucket time.Duration) ([]OutcomePoint, error) {
	secs := int64(bucket / time.Second)
	if secs < 1 {
		secs = 1
	}
	outcome, err := outcomeSQL()
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT floor(extract(EPOCH FROM t.created_at) / $5::BIGINT)::BIGINT, `+outcome+`, count(*)
		FROM traces t WHERE `+traceWhere+` GROUP BY 1, 2`, f.args(secs)...)
	if err != nil {
		return nil, fmt.Errorf("outcome series: %w", err)
	}
	defer rows.Close()
	counts := map[int64]map[string]int64{}
	for rows.Next() {
		var b, n int64
		var o *string
		if err := rows.Scan(&b, &o, &n); err != nil {
			return nil, err
		}
		if o == nil {
			continue
		}
		if counts[b] == nil {
			counts[b] = map[string]int64{}
		}
		counts[b][*o] += n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	first, last := f.From.Unix()/secs, (f.To.Unix()-1)/secs
	out := make([]OutcomePoint, 0, last-first+1)
	for b := first; b <= last; b++ {
		c := counts[b]
		if c == nil {
			c = map[string]int64{}
		}
		out = append(out, OutcomePoint{T: time.Unix(b*secs, 0).UTC(), Counts: c})
	}
	return out, nil
}

// caught counts the reasons of the answers the gate acted on, or would have in observe mode.
func (s *PostgresStore) caught(ctx context.Context, f StatsFilter) ([]ReasonCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.reason, count(*), (array_agg(t.trace_id ORDER BY t.created_at DESC))[1]
		FROM traces t, unnest(t.reasons) AS r(reason)
		WHERE `+traceWhere+` AND (t.action IN ('flag', 'block', 'fallback') OR t.would_action IN ('flag', 'block'))
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 10`, f.args()...)
	if err != nil {
		return nil, fmt.Errorf("caught: %w", err)
	}
	defer rows.Close()
	out := []ReasonCount{}
	for rows.Next() {
		var r ReasonCount
		if err := rows.Scan(&r.Reason, &r.Count, &r.LatestTraceID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// stepMsSQL reads one numeric step timing of an audit.
func stepMsSQL(key string) string {
	return fmt.Sprintf(`(CASE WHEN jsonb_typeof(a.step_timings->'%[1]s') = 'number' THEN (a.step_timings->>'%[1]s')::FLOAT8 END)`, key)
}

func pctSQL(expr, filter string) string {
	f := ""
	if filter != "" {
		f = " FILTER (WHERE " + filter + ")"
	}
	return fmt.Sprintf("percentile_cont(0.5) WITHIN GROUP (ORDER BY %[1]s)%[2]s, percentile_cont(0.95) WITHIN GROUP (ORDER BY %[1]s)%[2]s", expr, f)
}

// auditLatency reads the deep-audit and decision-model latencies.
func (s *PostgresStore) auditLatency(ctx context.Context, f StatsFilter, out *OverviewData) error {
	var audit, queue, decompose, retrieve, verify, score Percentiles
	err := s.pool.QueryRow(ctx, `SELECT `+
		pctSQL("a.processing_time_ms", "a.processing_time_ms > 0")+", "+pctSQL("a.queue_wait_ms", "")+", "+
		pctSQL(stepMsSQL("decompose_ms"), "")+", "+pctSQL(stepMsSQL("retrieve_ms"), "")+", "+
		pctSQL(stepMsSQL("verify_ms"), "")+", "+pctSQL(stepMsSQL("score_ms"), "")+`
		FROM `+auditedTracesFrom+` WHERE `+traceWhere+` AND a.status IN ('completed', 'degraded')`, f.args()...).Scan(
		&audit.P50, &audit.P95, &queue.P50, &queue.P95, &decompose.P50, &decompose.P95,
		&retrieve.P50, &retrieve.P95, &verify.P50, &verify.P95, &score.P50, &score.P95)
	if err != nil {
		return fmt.Errorf("audit latency: %w", err)
	}
	var decision Percentiles
	err = s.pool.QueryRow(ctx, `SELECT `+pctSQL("d.decision_ms", "")+`
		FROM gate_decisions d JOIN traces t ON t.trace_id = d.trace_id WHERE `+traceWhere+` AND d.attempt = 0`,
		f.args()...).Scan(&decision.P50, &decision.P95)
	if err != nil {
		return fmt.Errorf("decision latency: %w", err)
	}
	out.Latency["audit"] = audit
	out.Stages["decision"], out.Stages["queue_wait"] = decision, queue
	out.Stages["decompose"], out.Stages["retrieve"], out.Stages["verify"], out.Stages["score"] = decompose, retrieve, verify, score
	return nil
}

// claimStats reads the claim verdicts of the audited answers and the most contradicted claims.
func (s *PostgresStore) claimStats(ctx context.Context, f StatsFilter, out *OverviewData) error {
	rows, err := s.pool.Query(ctx, `
		SELECT c.status, count(*) FROM audit_claims c JOIN `+auditedTracesFrom+` ON a.audit_id = c.audit_id
		WHERE `+traceWhere+` AND NOT c.shadow GROUP BY 1`, f.args()...)
	if err != nil {
		return fmt.Errorf("claim verdicts: %w", err)
	}
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return err
		}
		out.ClaimVerdicts[status] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT c.claim, count(*), (array_agg(t.trace_id ORDER BY t.created_at DESC))[1]
		FROM audit_claims c JOIN `+auditedTracesFrom+` ON a.audit_id = c.audit_id
		WHERE `+traceWhere+` AND NOT c.shadow AND c.status = 'CONTRADICTED'
		GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 5`, f.args()...)
	if err != nil {
		return fmt.Errorf("top contradicted: %w", err)
	}
	defer rows.Close()
	out.TopContradicted = []ClaimCount{}
	for rows.Next() {
		var c ClaimCount
		if err := rows.Scan(&c.Claim, &c.Count, &c.LatestTraceID); err != nil {
			return err
		}
		out.TopContradicted = append(out.TopContradicted, c)
	}
	return rows.Err()
}

// Coverage reasons (EvidenceCoverage.top_reasons): why an answer's groundedness was not checked.
var CoverageReasons = []string{"no_evidence", "evidence_too_short", "evidence_packed_away", "too_long"}

// CoverageData is GET /api/evidence/coverage.
type CoverageData struct {
	EvidenceCounts
	// Reasons counts the traces not checked for groundedness by their exclusion reason.
	Reasons map[string]int64
}

// EvidenceCoverage counts the evidence of the traces of f, and why the others were not checked
// for groundedness: the groundedness evaluator's exclusion in the first decision, else
// no_evidence for a trace that carried none.
func (s *PostgresStore) EvidenceCoverage(ctx context.Context, f StatsFilter) (*CoverageData, error) {
	_, e, err := s.trafficTotals(ctx, f, map[string]Percentiles{})
	if err != nil {
		return nil, err
	}
	out := &CoverageData{EvidenceCounts: e, Reasons: map[string]int64{}}
	rows, err := s.pool.Query(ctx, `
		SELECT reason, count(*) FROM (
			SELECT COALESCE(
				(SELECT x->>'reason' FROM gate_decisions gd,
					jsonb_array_elements(CASE WHEN jsonb_typeof(gd.excluded) = 'array' THEN gd.excluded ELSE '[]'::JSONB END) x
				 WHERE gd.trace_id = t.trace_id AND gd.attempt = 0 AND x->>'ref' LIKE 'groundedness@%' LIMIT 1),
				CASE WHEN `+evidenceUsedSQL+` = 0 THEN 'no_evidence' END) AS reason
			FROM traces t WHERE `+traceWhere+` AND NOT `+groundednessAssessedSQL+`) r
		WHERE reason = ANY($5) GROUP BY 1`, f.args(CoverageReasons)...)
	if err != nil {
		return nil, fmt.Errorf("coverage reasons: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		var n int64
		if err := rows.Scan(&reason, &n); err != nil {
			return nil, err
		}
		out.Reasons[reason] = n
	}
	return out, rows.Err()
}

// CostCounts count cost rows by cost source: reported by the provider, computed from tokens and
// the price table, simulated (mock), free_local (a local model, no charge), unknown (no price
// covers the call: it counts $0) and anything else.
type CostCounts struct {
	Reported, Computed, Simulated, FreeLocal, Unknown, Other int64
}

// costCountsSQL counts the rows matching filter by cost source, in CostCounts order.
func costCountsSQL(filter string) string {
	if filter != "" {
		filter += " AND "
	}
	out := ""
	for i, src := range []string{"reported", "computed", "simulated", "free_local", "unknown"} {
		if i > 0 {
			out += ", "
		}
		out += "count(*) FILTER (WHERE " + filter + "c.cost_source = '" + src + "')"
	}
	return out + ", count(*) FILTER (WHERE " + filter + "c.cost_source NOT IN ('reported', 'computed', 'simulated', 'free_local', 'unknown'))"
}

func (c *CostCounts) dest() []any {
	return []any{&c.Reported, &c.Computed, &c.Simulated, &c.FreeLocal, &c.Unknown, &c.Other}
}

// CostSums are the ledger's spend on the traces of a filter (D50): the decision model's calls for
// the gate (every attempt: hedge losers and late attempts included; Studio, dry-run, Playground
// and probe calls are not the gate's) and the deep audits' LLM calls.
type CostSums struct {
	DecisionUSD     float64
	DecisionSources CostCounts
	LLMUSD          float64
	LLMSources      CostCounts
}

// gateCostSQL selects the gate's decision-model rows (cost_events aliased c).
const gateCostSQL = `c.kind = 'decision' AND c.component = 'gate'`

// costSums reads the spend of the traces of f from cost_events. A row is never stamped before its
// trace started, so the rows are bounded by the window start as well as joined to the traces.
func (s *PostgresStore) costSums(ctx context.Context, f StatsFilter) (CostSums, error) {
	var out CostSums
	dest := []any{&out.DecisionUSD}
	dest = append(dest, out.DecisionSources.dest()...)
	dest = append(dest, &out.LLMUSD)
	dest = append(dest, out.LLMSources.dest()...)
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(c.cost_usd) FILTER (WHERE `+gateCostSQL+`), 0)::FLOAT8, `+costCountsSQL(gateCostSQL)+`,
		COALESCE(sum(c.cost_usd) FILTER (WHERE c.kind = 'llm'), 0)::FLOAT8, `+costCountsSQL("c.kind = 'llm'")+`
		FROM cost_events c JOIN traces t ON t.trace_id = c.trace_id
		WHERE `+traceWhere+` AND c.occurred_at >= $1`, f.args()...).Scan(dest...)
	if err != nil {
		return out, fmt.Errorf("cost sums: %w", err)
	}
	return out, nil
}

// FitSample is one deep audit of fitted_from_audits: the length of the answer it checked and
// what it cost.
type FitSample struct {
	ResponseChars float64
	CostUSD       float64
}

// maxFitSamples bounds the deep audits fitted_from_audits reads (the newest first).
const maxFitSamples = 5000

// RoiData is what GET /api/roi/summary counts.
type RoiData struct {
	// Checked counts the answers the fast check assessed (gate_status ok or partial); Mock the
	// simulated traces of the window (only with IncludeMock).
	Checked, Mock                           int64
	Flagged, Blocked, Fallback, NotAssessed int64
	GateLatency                             Percentiles
	// Decisions counts the gate decisions (every attempt); DeadlineMisses those that timed out or
	// arrived late.
	Decisions, DeadlineMisses int64
	// CostSums is the spend (cost_events, D50).
	CostSums
	// SameChecks are the inputs of the same-checks token model: the decisions that called the
	// model (one per answer and attempt; hedge losers are not answers), their input tokens and
	// the evaluators they assessed.
	SameChecksCalls, SameChecksInputTokens, SameChecksQuestions int64
	Audits                                                      int64
	AuditsBySelection                                           map[string]int64
	// UniformCosted, UniformMeanUSD and UniformStddevUSD describe the cost of uniform audits.
	UniformCosted                    int64
	UniformMeanUSD, UniformStddevUSD float64
	// FitSamples are the costed deep audits whose answer length is known (fitted_from_audits);
	// CheckedChars sums the answer length of the CheckedWithChars checked answers that recorded
	// one (answers stored before 0005 did not).
	FitSamples                     []FitSample
	CheckedChars, CheckedWithChars int64
}

// Roi counts the costs and outcomes of the traces of f.
func (s *PostgresStore) Roi(ctx context.Context, f StatsFilter) (*RoiData, error) {
	out := &RoiData{AuditsBySelection: map[string]int64{}}
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE t.gate_status IN ('ok', 'partial')), count(*) FILTER (WHERE `+mockTraceSQL+`),
		count(*) FILTER (WHERE t.action = 'flag'), count(*) FILTER (WHERE t.action = 'block'),
		count(*) FILTER (WHERE t.action = 'fallback'), count(*) FILTER (WHERE t.gate_status IN ('not_assessed', 'skipped')),
		`+pctSQL("t.gate_ms", "t.gate_status IN ('ok', 'partial')")+`,
		COALESCE(sum(t.response_chars) FILTER (WHERE t.gate_status IN ('ok', 'partial')), 0),
		count(t.response_chars) FILTER (WHERE t.gate_status IN ('ok', 'partial'))
		FROM traces t WHERE `+traceWhere, f.args()...).Scan(
		&out.Checked, &out.Mock, &out.Flagged, &out.Blocked, &out.Fallback, &out.NotAssessed, &out.GateLatency.P50, &out.GateLatency.P95,
		&out.CheckedChars, &out.CheckedWithChars)
	if err != nil {
		return nil, fmt.Errorf("roi outcomes: %w", err)
	}
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE d.status = 'timeout' OR d.late),
		count(*) FILTER (WHERE d.input_tokens > 0),
		COALESCE(sum(d.input_tokens) FILTER (WHERE d.input_tokens > 0), 0),
		COALESCE(sum((SELECT count(*) FROM jsonb_each(CASE WHEN jsonb_typeof(d.decision->'evaluators') = 'object'
			THEN d.decision->'evaluators' ELSE '{}'::JSONB END) ev WHERE ev.value->>'status' = 'assessed')) FILTER (WHERE d.input_tokens > 0), 0)
		FROM gate_decisions d JOIN traces t ON t.trace_id = d.trace_id WHERE `+traceWhere, f.args()...).Scan(
		&out.Decisions, &out.DeadlineMisses, &out.SameChecksCalls, &out.SameChecksInputTokens, &out.SameChecksQuestions)
	if err != nil {
		return nil, fmt.Errorf("roi decisions: %w", err)
	}
	if out.CostSums, err = s.costSums(ctx, f); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT COALESCE(a.selection, ''), count(*) FROM `+auditedTracesFrom+`
		WHERE `+traceWhere+` GROUP BY 1`, f.args()...)
	if err != nil {
		return nil, fmt.Errorf("roi audits: %w", err)
	}
	for rows.Next() {
		var sel string
		var n int64
		if err := rows.Scan(&sel, &n); err != nil {
			rows.Close()
			return nil, err
		}
		out.Audits += n
		if sel != "" {
			out.AuditsBySelection[sel] += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var mean, stddev *float64
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), avg(a.cost_usd)::FLOAT8, stddev_samp(a.cost_usd)::FLOAT8 FROM `+auditedTracesFrom+`
		WHERE `+traceWhere+` AND a.selection = 'uniform' AND a.cost_usd IS NOT NULL AND a.status IN ('completed', 'degraded')`,
		f.args()...).Scan(&out.UniformCosted, &mean, &stddev)
	if err != nil {
		return nil, fmt.Errorf("roi uniform audits: %w", err)
	}
	if mean != nil {
		out.UniformMeanUSD = *mean
	}
	if stddev != nil {
		out.UniformStddevUSD = *stddev
	}
	if out.FitSamples, err = s.fitSamples(ctx, f); err != nil {
		return nil, err
	}
	return out, nil
}

// fitSamples reads the costed deep audits of the window whose answer length is known, newest
// first, at most maxFitSamples.
func (s *PostgresStore) fitSamples(ctx context.Context, f StatsFilter) ([]FitSample, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.response_chars::FLOAT8, a.cost_usd::FLOAT8 FROM `+auditedTracesFrom+`
		WHERE `+traceWhere+` AND a.cost_usd IS NOT NULL AND t.response_chars IS NOT NULL
			AND a.status IN ('completed', 'degraded')
		ORDER BY a.created_at DESC LIMIT `+strconv.Itoa(maxFitSamples), f.args()...)
	if err != nil {
		return nil, fmt.Errorf("roi fit samples: %w", err)
	}
	defer rows.Close()
	out := []FitSample{}
	for rows.Next() {
		var fs FitSample
		if err := rows.Scan(&fs.ResponseChars, &fs.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}

// RoiPoint is one bucket of GET /api/roi/timeseries: the spend on the traces created in it and
// the same-checks inputs of their decisions.
type RoiPoint struct {
	T                                                           time.Time
	DecisionUSD, LLMUSD                                         float64
	Gated                                                       int64
	SameChecksCalls, SameChecksInputTokens, SameChecksQuestions int64
}

// RoiSeries counts the spend and the checked answers of the traces of f per bucket of the UTC
// clock (an hour or a day), every bucket of the window present.
func (s *PostgresStore) RoiSeries(ctx context.Context, f StatsFilter, bucket time.Duration) ([]RoiPoint, error) {
	secs := int64(bucket / time.Second)
	if secs < 1 {
		return nil, fmt.Errorf("roi series: bucket %s is shorter than a second", bucket)
	}
	points := map[int64]*RoiPoint{}
	at := func(b int64) *RoiPoint {
		p := points[b]
		if p == nil {
			p = &RoiPoint{}
			points[b] = p
		}
		return p
	}
	bucketSQL := `floor(extract(EPOCH FROM t.created_at) / $5::BIGINT)::BIGINT`
	// Gated answers.
	rows, err := s.pool.Query(ctx, `SELECT `+bucketSQL+`, count(*) FROM traces t
		WHERE `+traceWhere+` AND t.gate_status IN ('ok', 'partial') GROUP BY 1`, f.args(secs)...)
	if err != nil {
		return nil, fmt.Errorf("roi series gated: %w", err)
	}
	for rows.Next() {
		var b, n int64
		if err := rows.Scan(&b, &n); err != nil {
			rows.Close()
			return nil, err
		}
		at(b).Gated = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Spend (D50: the gate's decision calls and the deep audits' LLM calls).
	rows, err = s.pool.Query(ctx, `SELECT `+bucketSQL+`,
		COALESCE(sum(c.cost_usd) FILTER (WHERE `+gateCostSQL+`), 0)::FLOAT8,
		COALESCE(sum(c.cost_usd) FILTER (WHERE c.kind = 'llm'), 0)::FLOAT8
		FROM cost_events c JOIN traces t ON t.trace_id = c.trace_id
		WHERE `+traceWhere+` AND c.occurred_at >= $1 GROUP BY 1`, f.args(secs)...)
	if err != nil {
		return nil, fmt.Errorf("roi series spend: %w", err)
	}
	for rows.Next() {
		var b int64
		var d, l float64
		if err := rows.Scan(&b, &d, &l); err != nil {
			rows.Close()
			return nil, err
		}
		p := at(b)
		p.DecisionUSD, p.LLMUSD = d, l
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Same-checks inputs.
	rows, err = s.pool.Query(ctx, `SELECT `+bucketSQL+`, count(*),
		COALESCE(sum(d.input_tokens), 0),
		COALESCE(sum((SELECT count(*) FROM jsonb_each(CASE WHEN jsonb_typeof(d.decision->'evaluators') = 'object'
			THEN d.decision->'evaluators' ELSE '{}'::JSONB END) ev WHERE ev.value->>'status' = 'assessed')), 0)
		FROM gate_decisions d JOIN traces t ON t.trace_id = d.trace_id
		WHERE `+traceWhere+` AND d.input_tokens > 0 GROUP BY 1`, f.args(secs)...)
	if err != nil {
		return nil, fmt.Errorf("roi series same checks: %w", err)
	}
	for rows.Next() {
		var b, calls, tokens, questions int64
		if err := rows.Scan(&b, &calls, &tokens, &questions); err != nil {
			rows.Close()
			return nil, err
		}
		p := at(b)
		p.SameChecksCalls, p.SameChecksInputTokens, p.SameChecksQuestions = calls, tokens, questions
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	first, last := f.From.Unix()/secs, (f.To.Unix()-1)/secs
	out := make([]RoiPoint, 0, last-first+1)
	for b := first; b <= last; b++ {
		p := RoiPoint{}
		if got := points[b]; got != nil {
			p = *got
		}
		p.T = time.Unix(b*secs, 0).UTC()
		out = append(out, p)
	}
	return out, nil
}

// UniformToday is today's uniform deep-audit sample (UTC day): how many were drawn and, when
// limit was reached, when the limit-th was drawn.
func (s *PostgresStore) UniformToday(ctx context.Context, now time.Time, limit int) (int64, *time.Time, error) {
	day := now.UTC().Truncate(24 * time.Hour)
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM traces WHERE created_at >= $1 AND 'uniform_sample' = ANY(flags)`,
		day).Scan(&n); err != nil {
		return 0, nil, fmt.Errorf("uniform today: %w", err)
	}
	if limit <= 0 || n < int64(limit) {
		return n, nil, nil
	}
	var at time.Time
	if err := s.pool.QueryRow(ctx, `SELECT created_at FROM traces WHERE created_at >= $1 AND 'uniform_sample' = ANY(flags)
		ORDER BY created_at OFFSET $2 LIMIT 1`, day, limit-1).Scan(&at); err != nil {
		return n, nil, fmt.Errorf("uniform cap: %w", err)
	}
	at = at.UTC()
	return n, &at, nil
}

// StatusFigures are the /api/status numbers read from stored rows: the decision model's latency
// over the last 24 hours and today's deep-audit (LLM) spend (UTC day).
type StatusFigures struct {
	DecisionLatency  Percentiles
	LLMSpentTodayUSD float64
}

// StatusFigures reads the status numbers at now.
func (s *PostgresStore) StatusFigures(ctx context.Context, now time.Time) (StatusFigures, error) {
	var f StatusFigures
	now = now.UTC()
	if err := s.pool.QueryRow(ctx, `SELECT `+pctSQL("decision_ms", "")+` FROM gate_decisions
		WHERE created_at >= $1 AND decision_ms IS NOT NULL`, now.Add(-24*time.Hour)).Scan(
		&f.DecisionLatency.P50, &f.DecisionLatency.P95); err != nil {
		return f, fmt.Errorf("decision latency: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(cost_usd), 0)::FLOAT8 FROM cost_events
		WHERE occurred_at >= $1 AND kind <> 'decision'`, now.Truncate(24*time.Hour)).Scan(&f.LLMSpentTodayUSD); err != nil {
		return f, fmt.Errorf("llm spend today: %w", err)
	}
	return f, nil
}

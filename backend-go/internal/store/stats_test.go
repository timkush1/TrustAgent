package store

import (
	"context"
	"math"
	"testing"
	"time"
)

// seedStats inserts a small window of traffic: two real traces on support-bot (one flagged with
// evidence and a uniform audit, one passed without evidence), one mock trace on payments-faq, and
// one real trace outside the window.
func seedStats(t *testing.T, s *PostgresStore, now time.Time) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	trace := `INSERT INTO traces (trace_id, created_at, source, api_format, path, route, mode, model, action, would_action,
		gate_status, reasons, evidence_summary, decision_backend, decision_cost_usd, audit_status, audit_selection, flags,
		overhead_ms, gate_ms, upstream_ms)
		VALUES ($1, $2, 'proxy', 'openai_chat', '/v1/chat/completions', $3, 'flag', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 12, 40, 900)`
	used := `{"configured":["request_system"],"used":[{"source":"request_system","items":1,"labels":["refund.md"],"tokens":22}],"empty":[],"knowledge":"not_requested","truncated":false,"tokens":22}`
	empty := `{"configured":["request_system"],"used":[],"empty":["request_system"],"knowledge":"not_requested","truncated":false,"tokens":0}`
	exec(trace, "t-flag", now.Add(-10*time.Minute), "support-bot", "gpt-4o-mini", "flag", "flag", "ok",
		[]string{"contradiction:critical"}, used, "openrouter-decisions", 0.001, "completed", "uniform", []string{"uniform_sample"})
	exec(trace, "t-pass", now.Add(-5*time.Minute), "support-bot", "gpt-4o-mini", "pass", "pass", "ok",
		[]string{}, empty, "openrouter-decisions", 0.001, "none", nil, []string{})
	exec(trace, "t-mock", now.Add(-3*time.Minute), "payments-faq", "gpt-4o-mini", "observe", "flag", "ok",
		[]string{"groundedness:warn"}, used, "mock", 0, "none", nil, []string{"mock_decision"})
	exec(trace, "t-old", now.Add(-48*time.Hour), "support-bot", "gpt-4o-mini", "pass", "pass", "ok",
		[]string{}, used, "openrouter-decisions", 0.001, "none", nil, []string{})

	decision := `INSERT INTO gate_decisions (trace_id, route, mode, effective_mode, status, action, decision, excluded,
		cost_usd, input_tokens, decision_ms) VALUES ($1, 'support-bot', 'flag', 'flag', 'ok', $2, $3, $4, $5, $6, 30)`
	exec(decision, "t-flag", "flag",
		`{"evaluators":{"groundedness@1":{"status":"assessed"},"contradiction@1":{"status":"assessed"}},"decision_model":{"cost_source":"computed"}}`,
		`[]`, 0.001, 1000)
	exec(decision, "t-pass", "pass",
		`{"evaluators":{"groundedness@1":{"status":"not_assessed"}},"decision_model":{"cost_source":"computed"}}`,
		`[{"ref":"groundedness@1","reason":"no_evidence"}]`, 0.001, 500)
	exec(decision, "t-mock", "observe",
		`{"evaluators":{"groundedness@1":{"status":"assessed"}},"decision_model":{"cost_source":"simulated"}}`, `[]`, 0, 800)

	exec(`INSERT INTO audits (audit_id, trace_id, request_id, hallucination_detected, created_at, route, selection, status,
		verdict, cost_usd, processing_time_ms, queue_wait_ms, step_timings)
		VALUES ('a-flag', 't-flag', 't-flag', true, $1, 'support-bot', 'uniform', 'completed', 'HALLUCINATED', 0.01, 8000, 30,
		'{"decompose_ms": 2000, "verify_ms": 5000, "score_ms": 3}')`, now.Add(-9*time.Minute))
	exec(`INSERT INTO audit_claims (audit_id, position, claim, status, confidence) VALUES
		('a-flag', 0, 'Refunds within 60 days.', 'CONTRADICTED', 0.9), ('a-flag', 1, 'Shipping is free.', 'SUPPORTED', 0.8)`)
	exec(`INSERT INTO cost_events (occurred_at, trace_id, route, component, kind, cost_usd, cost_source)
		VALUES ($1, 't-flag', 'support-bot', 'verify', 'llm', 0.01, 'reported')`, now.Add(-9*time.Minute))
	// The ledger's decision rows (D50): the gate's attempts count, a hedge loser and a late
	// attempt included; a Studio test of the same trace and a probe do not.
	gateRow := `INSERT INTO cost_events (occurred_at, trace_id, route, component, kind, attempt, cost_usd, cost_source, hedge, late)
		VALUES ($1, $2, 'support-bot', $3, 'decision', $4, $5, $6, $7, $8)`
	exec(gateRow, now.Add(-10*time.Minute), "t-flag", "gate", 0, 0.001, "computed", false, false)
	exec(gateRow, now.Add(-10*time.Minute), "t-flag", "gate", 1, 0.0005, "computed", true, false)
	exec(gateRow, now.Add(-5*time.Minute), "t-pass", "gate", 0, 0.001, "computed", false, false)
	exec(gateRow, now.Add(-4*time.Minute), "t-pass", "gate", 0, 0.0002, "computed", false, true)
	exec(gateRow, now.Add(-4*time.Minute), "t-pass", "studio_test", 0, 0.5, "computed", false, false)
	exec(gateRow, now.Add(-3*time.Minute), "t-mock", "gate", 0, 0.004, "simulated", false, false)
	exec(gateRow, now.Add(-48*time.Hour), "t-old", "gate", 0, 0.001, "computed", false, false)
	exec(`INSERT INTO cost_events (occurred_at, component, kind, cost_usd, cost_source) VALUES ($1, 'probe', 'decision', 0.3, 'computed')`,
		now.Add(-time.Minute))
}

func TestOverviewAggregates(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedStats(t, s, now)
	f := StatsFilter{From: now.Add(-24 * time.Hour), To: now.Add(time.Minute)}

	o, err := s.Overview(ctx, f, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tot := o.Totals
	if tot.Requests != 2 || tot.Flagged != 1 || tot.Passed != 1 || tot.Observed != 0 || tot.Audited != 1 || math.Abs(tot.DecisionCostUSD-0.0027) > 1e-9 {
		t.Errorf("totals without mock %+v", tot)
	}
	if e := o.Evidence; e.WithEvidence != 1 || e.GroundednessAssessed != 1 || e.BySource["request_system"] != 1 || e.BySource["knowledge"] != 0 {
		t.Errorf("evidence %+v", e)
	}
	if o.Uniform != (RateCounts{Assessed: 1, Hallucinated: 1}) || o.Audited != (RateCounts{Assessed: 1, Hallucinated: 1}) || math.Abs(o.LLMCostUSD-0.01) > 1e-9 {
		t.Errorf("rates uniform %+v audited %+v llm %v", o.Uniform, o.Audited, o.LLMCostUSD)
	}
	if len(o.ByRoute) != 1 || o.ByRoute[0].Key != "support-bot" || o.ByRoute[0].Requests != 2 || o.ByRoute[0].Uniform.Assessed != 1 {
		t.Errorf("by route %+v", o.ByRoute)
	}
	if len(o.ByModel) != 1 || o.ByModel[0].Key != "gpt-4o-mini" {
		t.Errorf("by model %+v", o.ByModel)
	}
	// 24h of 15-minute buckets, plus the partial ones at either end.
	if want := (f.To.Unix()-1)/900 - f.From.Unix()/900 + 1; int64(len(o.Series)) != want {
		t.Errorf("%d buckets, want %d", len(o.Series), want)
	}
	var flagged, passed int64
	for _, p := range o.Series {
		flagged += p.Counts["flagged"]
		passed += p.Counts["passed"]
	}
	if flagged != 1 || passed != 1 {
		t.Errorf("series flagged %d passed %d", flagged, passed)
	}
	if len(o.Caught) != 1 || o.Caught[0].Reason != "contradiction:critical" || o.Caught[0].LatestTraceID != "t-flag" {
		t.Errorf("caught %+v", o.Caught)
	}
	if o.ClaimVerdicts["CONTRADICTED"] != 1 || o.ClaimVerdicts["SUPPORTED"] != 1 {
		t.Errorf("claims %+v", o.ClaimVerdicts)
	}
	if len(o.TopContradicted) != 1 || *o.TopContradicted[0].Claim != "Refunds within 60 days." {
		t.Errorf("top contradicted %+v", o.TopContradicted)
	}
	if p := o.Latency["audit"]; p.P50 == nil || *p.P50 != 8000 {
		t.Errorf("audit latency %+v", p)
	}
	if p := o.Stages["verify"]; p.P50 == nil || *p.P50 != 5000 {
		t.Errorf("verify latency %+v", p)
	}
	if p := o.Stages["retrieve"]; p.P50 != nil {
		t.Errorf("no retrieve timing was recorded: %+v", p)
	}
	if o.BackendMix["openrouter-decisions"] != 2 || o.BackendMix["mock"] != 0 {
		t.Errorf("backend mix %+v", o.BackendMix)
	}

	f.IncludeMock = true
	o, err = s.Overview(ctx, f, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if o.Totals.Requests != 3 || o.Totals.Observed != 1 || o.BackendMix["mock"] != 1 || len(o.Caught) != 2 {
		t.Errorf("with mock: totals %+v mix %+v caught %+v", o.Totals, o.BackendMix, o.Caught)
	}

	f.IncludeMock, f.Route = true, "payments-faq"
	o, err = s.Overview(ctx, f, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if o.Totals.Requests != 1 || len(o.ByRoute) != 1 || o.ByRoute[0].Key != "payments-faq" {
		t.Errorf("route filter %+v %+v", o.Totals, o.ByRoute)
	}
}

func TestEvidenceCoverageAndRoi(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedStats(t, s, now)
	f := StatsFilter{From: now.Add(-24 * time.Hour), To: now.Add(time.Minute)}

	c, err := s.EvidenceCoverage(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if c.Requests != 2 || c.WithEvidence != 1 || c.GroundednessAssessed != 1 || c.Reasons["no_evidence"] != 1 || len(c.Reasons) != 1 {
		t.Errorf("coverage %+v", c)
	}

	r, err := s.Roi(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	// The decision spend is the gate's rows: both answers' attempts, the hedge loser and the late
	// attempt, not the Studio test, the probe, the mock trace or yesterday's trace.
	if r.Checked != 2 || r.Flagged != 1 || r.Decisions != 2 || r.DeadlineMisses != 0 || math.Abs(r.DecisionUSD-0.0027) > 1e-9 {
		t.Errorf("roi traffic %+v", r)
	}
	if r.DecisionSources != (CostCounts{Computed: 4}) || r.SameChecksCalls != 2 || r.SameChecksInputTokens != 1500 || r.SameChecksQuestions != 2 {
		t.Errorf("roi decisions %+v", r)
	}
	if math.Abs(r.LLMUSD-0.01) > 1e-9 || r.LLMSources != (CostCounts{Reported: 1}) || r.Audits != 1 || r.AuditsBySelection["uniform"] != 1 {
		t.Errorf("roi audits %+v", r)
	}
	if r.UniformCosted != 1 || math.Abs(r.UniformMeanUSD-0.01) > 1e-9 {
		t.Errorf("roi uniform %+v", r)
	}
	if r.GateLatency.P50 == nil || *r.GateLatency.P50 != 40 {
		t.Errorf("gate latency %+v", r.GateLatency)
	}

	sf, err := s.StatusFigures(ctx, now)
	if err != nil || sf.DecisionLatency.P50 == nil || *sf.DecisionLatency.P50 != 30 {
		t.Errorf("status figures %+v %v", sf, err)
	}
	if now.Add(-9*time.Minute).Truncate(24*time.Hour) == now.Truncate(24*time.Hour) && math.Abs(sf.LLMSpentTodayUSD-0.01) > 1e-9 {
		t.Errorf("llm spend today %v", sf.LLMSpentTodayUSD)
	}
	// The last 24 hours of decision calls (first attempts) and their real cost: the seeded rows
	// within the day, simulated ones counted as calls but not as cost; the probe row counts too.
	if sf.Decision24h.Calls != 6 || math.Abs(sf.Decision24h.CostUSD-(0.0027+0.5+0.3)) > 1e-9 {
		t.Errorf("decision 24h %+v", sf.Decision24h)
	}

	if now.Add(-10*time.Minute).Truncate(24*time.Hour) != now.Truncate(24*time.Hour) {
		return // the uniform trace was drawn yesterday (UTC)
	}
	n, at, err := s.UniformToday(ctx, now, 50)
	if err != nil || n != 1 || at != nil {
		t.Errorf("uniform today %d %v %v", n, at, err)
	}
	if n, at, err = s.UniformToday(ctx, now, 1); err != nil || n != 1 || at == nil {
		t.Errorf("uniform cap reached %d %v %v", n, at, err)
	}
}

func TestAggregatesLeaveOutDirectAudits(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedStats(t, s, now)
	f := StatsFilter{From: now.Add(-24 * time.Hour), To: now.Add(time.Minute)}
	before, err := s.Overview(ctx, f, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// POST /api/audit records a trace (source direct, skipped, no action) and audits it.
	if _, err := s.pool.Exec(ctx, `INSERT INTO traces (trace_id, created_at, source, api_format, path, route, mode, gate_status,
		audit_id, audit_status, audit_selection) VALUES ('t-direct', $1, 'direct', 'direct', '/api/audit', 'support-bot', 'observe',
		'skipped', 'a-direct', 'completed', 'direct')`, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO audits (audit_id, trace_id, request_id, hallucination_detected, created_at, route,
		selection, status, verdict, source) VALUES ('a-direct', 't-direct', 't-direct', true, $1, 'support-bot', 'direct', 'completed',
		'HALLUCINATED', 'direct')`, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	after, err := s.Overview(ctx, f, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if after.Totals != before.Totals || after.Audited != before.Audited || len(after.ByRoute) != len(before.ByRoute) ||
		after.ByRoute[0].Requests != before.ByRoute[0].Requests {
		t.Errorf("a direct audit changed the gateway aggregates:\nbefore %+v %+v\nafter  %+v %+v", before.Totals, before.Audited,
			after.Totals, after.Audited)
	}
}

// fitted_from_audits reads the costed deep audits whose answer length is known, and the length of
// the checked answers; the series zero-fills every bucket and puts each trace's spend, checked
// answer and same-checks inputs in the bucket it was created in.
func TestRoiFitSamplesAndSeries(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedStats(t, s, now)
	if _, err := s.pool.Exec(ctx, `UPDATE traces SET response_chars = CASE trace_id WHEN 't-flag' THEN 1200 WHEN 't-pass' THEN 300 END`); err != nil {
		t.Fatal(err)
	}
	f := StatsFilter{From: now.Add(-24 * time.Hour), To: now.Add(time.Minute)}
	r, err := s.Roi(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if r.CheckedChars != 1500 || r.CheckedWithChars != 2 || len(r.FitSamples) != 1 ||
		r.FitSamples[0] != (FitSample{ResponseChars: 1200, CostUSD: 0.01}) {
		t.Errorf("fit inputs: chars %d over %d, samples %+v", r.CheckedChars, r.CheckedWithChars, r.FitSamples)
	}

	series, err := s.RoiSeries(ctx, f, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	first, last := f.From.Unix()/3600, (f.To.Unix()-1)/3600
	if int64(len(series)) != last-first+1 {
		t.Fatalf("%d points, want %d", len(series), last-first+1)
	}
	var gated int64
	var decision, llm float64
	for i, p := range series {
		if p.T.Unix() != (first+int64(i))*3600 || p.T.Location() != time.UTC {
			t.Errorf("point %d at %v", i, p.T)
		}
		gated += p.Gated
		decision += p.DecisionUSD
		llm += p.LLMUSD
	}
	if gated != 2 || math.Abs(decision-0.0027) > 1e-9 || math.Abs(llm-0.01) > 1e-9 {
		t.Errorf("series totals: gated %d decision %v llm %v", gated, decision, llm)
	}
	flagBucket := now.Add(-10*time.Minute).Unix()/3600 - first
	if p := series[flagBucket]; p.Gated < 1 || p.SameChecksCalls < 1 || p.SameChecksInputTokens < 500 {
		t.Errorf("the flagged trace's bucket %+v", p)
	}
	f.IncludeMock = true
	withMock, err := s.RoiSeries(ctx, f, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var mockDecision float64
	for _, p := range withMock {
		mockDecision += p.DecisionUSD
	}
	if math.Abs(mockDecision-0.0067) > 1e-9 {
		t.Errorf("include_mock decision spend %v", mockDecision)
	}
	if _, err := s.RoiSeries(ctx, f, time.Millisecond); err == nil {
		t.Error("a bucket shorter than a second was accepted")
	}
}

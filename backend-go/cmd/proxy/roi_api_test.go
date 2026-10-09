package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/config"
	"github.com/truthtable/backend-go/internal/contractstest"
	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/contracts"
)

const (
	schemaRoiSummary    = "api.v1.schema.json#/$defs/RoiSummary"
	schemaRoiTimeseries = "api.v1.schema.json#/$defs/RoiTimeseries"
)

func nearUSD(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// linearSamples are n deep audits whose cost is exactly 0.002 + 0.00001·chars.
func linearSamples(n int) []store.FitSample {
	out := make([]store.FitSample, n)
	for i := range out {
		chars := float64(200 + 50*i)
		out[i] = store.FitSample{ResponseChars: chars, CostUSD: 0.002 + 0.00001*chars}
	}
	return out
}

// llm_full_claim_audit takes the first method the data allows: 30 costed uniform audits, else 10
// costed deep audits to fit, else the static assumption; there is no shadow-sample method.
func TestFullAuditMethodOrder(t *testing.T) {
	uniform := &store.RoiData{UniformCosted: minRateN, UniformMeanUSD: 0.03, UniformStddevUSD: 0.01, FitSamples: linearSamples(40)}
	fitted := &store.RoiData{UniformCosted: minRateN - 1, UniformMeanUSD: 0.03, FitSamples: linearSamples(fitMinN),
		CheckedChars: 50_000, CheckedWithChars: 100}
	static := &store.RoiData{UniformCosted: 5, FitSamples: linearSamples(fitMinN - 1)}
	for _, c := range []struct {
		r      *store.RoiData
		method string
		n      int64
	}{{uniform, "uniform_sample", minRateN}, {fitted, "fitted_from_audits", fitMinN}, {static, "static_assumptions", 0}} {
		cf := fullAuditCounterfactual(c.r, 100)
		if cf.Method != c.method || cf.N != c.n || cf.Kind != "llm_full_claim_audit" || cf.Label != labelEstimate || cf.USD == nil {
			t.Errorf("%s: %+v", c.method, cf)
		}
		if strings.Contains(cf.Method+cf.Basis, "shadow") {
			t.Errorf("%s mentions shadow sampling: %+v", c.method, cf)
		}
	}
	if cf := fullAuditCounterfactual(uniform, 100); !nearUSD(*cf.USD, 3) || *cf.Low >= 3 || *cf.High <= 3 || !nearUSD(*cf.Per1k, 30) {
		t.Errorf("uniform: %+v", cf)
	}
	if cf := fullAuditCounterfactual(static, 2000); !strings.HasPrefix(cf.Basis, "not measured: ") || cf.Per1k == nil ||
		!nearUSD(*cf.USD, *cf.Per1k*2) || cf.Low != nil {
		t.Errorf("static: %+v", cf)
	}
}

// A window whose deep audits all came from the free mock judge says so, with what a real judge
// would add per audit; any real, local or unpriced judge call, or a spend, keeps the usual basis.
func TestFreeMockJudgeNote(t *testing.T) {
	mock := &store.RoiData{Audits: 12, CostSums: store.CostSums{LLMSources: store.CostCounts{Simulated: 30}}}
	note := freeMockJudgeNote(mock)
	if !strings.Contains(note, "the mock judge is free in the demo") || !strings.Contains(note, "a real judge adds about $0.0355 per deep audit") ||
		!strings.Contains(note, "not measured") {
		t.Errorf("mock note %q", note)
	}
	for name, r := range map[string]*store.RoiData{
		"no audits":      {CostSums: store.CostSums{LLMSources: store.CostCounts{Simulated: 3}}},
		"a real call":    {Audits: 2, CostSums: store.CostSums{LLMSources: store.CostCounts{Simulated: 3, Reported: 1}}},
		"a local judge":  {Audits: 2, CostSums: store.CostSums{LLMSources: store.CostCounts{FreeLocal: 3}}},
		"a priced mock":  {Audits: 2, CostSums: store.CostSums{LLMUSD: 0.5, LLMSources: store.CostCounts{Simulated: 3}}},
		"no judge calls": {Audits: 2},
	} {
		if got := freeMockJudgeNote(r); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// The fit recovers an exact line, applies it to every checked answer (those without a recorded
// length at the mean length), and its bootstrap interval is the same on every run.
func TestFittedFromAuditsIsDeterministic(t *testing.T) {
	samples := linearSamples(25)
	// 100 answers checked, 80 with a recorded length averaging 500 characters.
	fit := fitFullAudit(samples, 100, 40_000, 80)
	want := 0.002*100 + 0.00001*(40_000+20*500)
	if !nearUSD(fit.usd, want) || !nearUSD(fit.low, want) || !nearUSD(fit.high, want) {
		t.Errorf("exact line: %+v, want %v", fit, want)
	}
	// Noisy costs: an interval around the estimate, identical on a second run.
	noisy := linearSamples(30)
	for i := range noisy {
		noisy[i].CostUSD *= 1 + 0.3*math.Sin(float64(i)*1.7)
	}
	a, b := fitFullAudit(noisy, 1000, 450_000, 1000), fitFullAudit(noisy, 1000, 450_000, 1000)
	if a != b {
		t.Errorf("two runs differ: %+v %+v", a, b)
	}
	if !(a.low < a.usd && a.usd < a.high) || a.low <= 0 {
		t.Errorf("interval %+v does not bracket the estimate", a)
	}
	// Without spread in the lengths the line is flat at the mean cost; a negative total is $0.
	flat := []store.FitSample{{ResponseChars: 400, CostUSD: 0.01}, {ResponseChars: 400, CostUSD: 0.03}}
	if alpha, beta := leastSquares(flat, nil); !nearUSD(alpha, 0.02) || beta != 0 {
		t.Errorf("flat fit %v %v", alpha, beta)
	}
	steep := []store.FitSample{{ResponseChars: 1000, CostUSD: 0.001}, {ResponseChars: 2000, CostUSD: 0.011}}
	if fit := fitFullAudit(steep, 10, 100, 10); fit.usd != 0 || fit.low < 0 {
		t.Errorf("a negative extrapolation is not $0: %+v", fit)
	}
}

// The same-checks basis says whether a published run validated the token model
// (contracts/claims/measured.json); a mock run or an empty value never does.
func TestTokenModelValidationBasis(t *testing.T) {
	validated := parseTokenModelCheck([]byte(`{"status":"measured","tier":"bench-lite","mock":false,
		"values":{"same_checks_cost_ratio":{"value":31.5,"run_id":"run-2026-10-01"}}}`))
	if !validated.validated || !strings.Contains(validated.basis(), "validated: a published bench-lite run measured the same checks at 31.5× the fast-check cost (run run-2026-10-01)") {
		t.Errorf("validated basis %q", validated.basis())
	}
	for name, raw := range map[string]string{
		"not measured": `{"status":"not_measured","mock":false,"values":{"same_checks_cost_ratio":{"value":null}}}`,
		"mock run":     `{"status":"measured","tier":"bench-lite","mock":true,"values":{"same_checks_cost_ratio":{"value":31.5}}}`,
		"no ratio":     `{"status":"measured","mock":false,"values":{}}`,
		"broken":       `{`,
	} {
		if c := parseTokenModelCheck([]byte(raw)); c.validated || c.basis() != "The token model is not yet validated against a measured run." {
			t.Errorf("%s: %+v %q", name, c, c.basis())
		}
	}
	// The committed measured.json is not measured yet.
	if measuredTokenModel().validated {
		t.Error("contracts/claims/measured.json reads as validated")
	}
	price := judgePriceInfo{in: 1, out: 5, ok: true, name: referenceJudgeName, why: "no deep-audit judge has reported its model yet"}
	cf := sameChecksCounterfactual(2, 2000, 4, price, validated)
	if cf.USD == nil || !nearUSD(*cf.USD, 0.0036) || !nearUSD(*cf.Per1k, 1.8) || !strings.Contains(cf.Basis, "31.5×") ||
		!strings.Contains(cf.Basis, "Claude Haiku 4.5 at list price at $1 / $5 per million tokens") {
		t.Errorf("same checks %+v", cf)
	}
	if cf := sameChecksCounterfactual(2, 2000, 4, judgePriceInfo{name: "x"}, validated); cf.USD != nil || cf.Per1k != nil {
		t.Errorf("no price, yet a figure: %+v", cf)
	}
}

// D47: the same checks are priced at the effective judge's price when the price book has a real
// one, else at the Claude Haiku 4.5 list price with the reason in the basis.
func TestSameChecksJudgePrice(t *testing.T) {
	book, err := costs.LoadPriceBook("")
	if err != nil {
		t.Fatal(err)
	}
	book.SetOverrides([]decision.PriceEntry{{Match: "acme-judge-*", Provider: "openai", InputPerMTok: 3, OutputPerMTok: 12, Label: "override"}})
	for _, c := range []struct {
		provider, model string
		in, out         float64
		configured      bool
		why             string
	}{
		{"openrouter", "anthropic/claude-haiku-4.5", 1, 5, true, ""},
		{"openai", "acme-judge-2", 3, 12, true, ""},
		{"mock", "mock", 1, 5, false, "mock judge"},
		{"ollama", "llama3.1", 1, 5, false, "runs locally at no charge"},
		{"openai", "gpt-unknown", 1, 5, false, "is not in the price table"},
		{"", "", 1, 5, false, "has reported its model yet"},
	} {
		p := judgePriceFor(book, c.provider, c.model)
		if !p.ok || p.in != c.in || p.out != c.out || (p.why == "") != c.configured || !strings.Contains(p.why, c.why) {
			t.Errorf("%s %s: %+v", c.provider, c.model, p)
		}
		if c.configured != strings.HasPrefix(p.describe(), "the configured judge") {
			t.Errorf("%s %s describes %q", c.provider, c.model, p.describe())
		}
	}
	if p := judgePriceFor(book, "openai", "acme-judge-2"); !strings.Contains(p.note, "your price override") {
		t.Errorf("override note %q", p.note)
	}
	if p := judgePriceFor(book, "mock", "mock"); !strings.Contains(p.note, "Claude Haiku 4.5 list price $1 / $5 per million tokens (Anthropic, as of 2026-06-24)") {
		t.Errorf("reference note %q", p.note)
	}
}

// The weakest label wins: simulated, then estimate, then measured; a window with simulated
// traffic labels its spend simulated even before its rows are written.
func TestWeakestLabelWins(t *testing.T) {
	if weakest() != labelMeasured || weakest(labelMeasured, labelEstimate) != labelEstimate ||
		weakest(labelEstimate, labelSimulated, labelMeasured) != labelSimulated {
		t.Error("weakest")
	}
	for _, c := range []struct {
		counts store.CostCounts
		label  string
	}{
		{store.CostCounts{Reported: 3}, labelMeasured},
		{store.CostCounts{Reported: 3, FreeLocal: 1}, labelMeasured},
		{store.CostCounts{FreeLocal: 2}, labelMeasured},
		{store.CostCounts{Reported: 3, Computed: 1}, labelEstimate},
		{store.CostCounts{Reported: 3, Unknown: 1}, labelEstimate},
		{store.CostCounts{Reported: 3, Simulated: 1}, labelSimulated},
		{store.CostCounts{}, labelMeasured},
	} {
		if l, basis := costLabel(c.counts, "calls"); l != c.label || basis == "" {
			t.Errorf("%+v: %s %q", c.counts, l, basis)
		}
	}
	if l, basis := spendLabel(store.CostCounts{}, "calls", true); l != labelSimulated || !strings.Contains(basis, "simulated traffic") {
		t.Errorf("simulated traffic: %s %q", l, basis)
	}
}

// roiSeed writes the traffic of the ROI endpoint tests: two checked answers 40 and 20 minutes ago
// (the first audited uniformly), a mock answer 10 minutes ago, and their ledger rows: the gate's
// attempts (a hedge loser included), a Studio test and the deep audit's judge call.
func roiSeed(t *testing.T, pg *store.PostgresStore, now time.Time) {
	t.Helper()
	ctx := context.Background()
	pv := int64(1)
	trace := func(at time.Time, action, backend string, chars int, flags ...string) string {
		id := store.NewID()
		mode := "flag"
		tr := &store.TraceRecord{TraceID: id, CreatedAt: at, Source: "proxy", APIFormat: "openai_chat", Path: "/v1/chat/completions",
			Route: "support-bot", PolicyVersion: &pv, Mode: mode, EffectiveMode: &mode, Action: &action, GateStatus: "ok",
			DecisionBackend: &backend, AuditStatus: "none", Flags: append([]string{}, flags...), ResponseChars: &chars}
		tokens := 1000
		doc, _ := json.Marshal(map[string]any{"spec": "decision/v1", "trace_id": id, "evaluators": map[string]any{
			"groundedness@1": map[string]any{"status": "assessed"}, "contradiction@1": map[string]any{"status": "assessed"}}})
		dec := &store.DecisionRecord{TraceID: id, PolicyVersion: &pv, Route: "support-bot", Mode: mode, EffectiveMode: mode,
			Status: "ok", Action: action, Calibration: "none", Decision: doc, InputTokens: &tokens, DecisionBackend: &backend}
		if err := pg.RecordDecision(ctx, tr, dec, nil); err != nil {
			t.Fatal(err)
		}
		return id
	}
	flagTrace := trace(now.Add(-40*time.Minute), "flag", "openrouter-decisions", 800, "uniform_sample")
	passTrace := trace(now.Add(-20*time.Minute), "pass", "openrouter-decisions", 400)
	mockTrace := trace(now.Add(-10*time.Minute), "observe", "mock", 300, "mock_decision")
	row := func(trace, component, kind, source string, at time.Time, usd float64, hedge bool) store.CostEvent {
		return store.CostEvent{OccurredAt: at, TraceID: trace, Route: "support-bot", Component: component, Kind: kind,
			CostUSD: &usd, CostSource: source, Hedge: hedge}
	}
	selection := "uniform"
	audit := &store.AuditRecord{AuditID: store.NewID(), TraceID: flagTrace, Model: "gpt-4o-mini", Source: "proxy",
		Selection: &selection, Status: store.AuditCompleted, ScoringVersion: "v2", CreatedAt: now.Add(-39 * time.Minute),
		CostUSD: ptrF(0.012)}
	if err := pg.SaveAuditResultWithCosts(ctx, audit, store.AuditCompleted, []store.CostEvent{
		row(flagTrace, "deep_audit_verify", "llm", "computed", now.Add(-38*time.Minute), 0.012, false)}); err != nil {
		t.Fatal(err)
	}
	if err := pg.InsertCostEvents(ctx, []store.CostEvent{
		row(flagTrace, "gate", "decision", "reported", now.Add(-40*time.Minute), 0.0001, false),
		row(flagTrace, "gate", "decision", "reported", now.Add(-40*time.Minute), 0.00005, true),
		row(passTrace, "gate", "decision", "reported", now.Add(-20*time.Minute), 0.0001, false),
		row(passTrace, "studio_test", "decision", "computed", now.Add(-19*time.Minute), 0.2, false),
		row(mockTrace, "gate", "decision", "simulated", now.Add(-10*time.Minute), 0.003, false),
	}); err != nil {
		t.Fatal(err)
	}
}

// GET /api/roi/summary counts the ledger: the gate's attempts with the hedge loser and without the
// Studio test (D50), the deep audit's judge call; the weakest label wins for the total and the
// savings; the same checks are priced at the reference judge with the reason, not yet validated;
// the LLM cap's hit time is reported. include_mock adds the simulated answer and labels it all.
func TestRoiSummaryCountsTheLedger(t *testing.T) {
	env, pg := pgEnv(t, envOpts{mutate: func(c *config.Config) { c.SpendCapLLMUSDDay = 1 }})
	roiSeed(t, pg, time.Now().UTC())
	code, body := env.get("/api/roi/summary?window=24h", testKey)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaRoiSummary, body)
	var r roiSummary
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	if r.GatedResponses != 2 || r.Simulated || !nearUSD(r.DecisionCost.USD, 0.00025) || r.DecisionCost.Label != labelMeasured ||
		!nearUSD(*r.DecisionCost.Per1k, 0.125) {
		t.Errorf("decision cost %+v (%d gated)", r.DecisionCost, r.GatedResponses)
	}
	if !nearUSD(r.LLMCost.USD, 0.012) || r.LLMCost.Label != labelEstimate || r.LLMCost.Audits != 1 || r.LLMCost.BySelection["uniform"] != 1 {
		t.Errorf("llm cost %+v", r.LLMCost)
	}
	if r.TotalPer1k.Label != labelEstimate || !nearUSD(*r.TotalPer1k.Value, 6.125) {
		t.Errorf("total per 1k %+v", r.TotalPer1k)
	}
	same := r.Counterfactual[0]
	// (2000 input tokens + 2·400)·$1 + (40·4 questions)·$5 per million tokens.
	if same.Kind != "llm_same_checks" || same.Method != "token_model" || same.N != 2 || !nearUSD(*same.USD, 0.0036) ||
		same.Label != labelEstimate || !strings.Contains(same.Basis, "Claude Haiku 4.5 at list price") ||
		!strings.Contains(same.Basis, "not yet validated") {
		t.Errorf("same checks %+v", same)
	}
	if r.Savings.Label != labelEstimate || !nearUSD(*r.Savings.USDHigh, 0.00335) || !nearUSD(*r.Savings.USDLow, 0.0036-0.00025-0.012) {
		t.Errorf("savings %+v", r.Savings)
	}
	if full := r.Counterfactual[1]; full.Method != "static_assumptions" || full.Label != labelEstimate {
		t.Errorf("full audit %+v", full)
	}
	if len(r.ThirdParty) != 1 || !strings.Contains(r.ThirdParty[0], "Claude Haiku 4.5 list price") {
		t.Errorf("third party %v", r.ThirdParty)
	}
	if r.Sampling.LLMCapHitAt != nil {
		t.Errorf("the LLM cap was not hit: %v", r.Sampling.LLMCapHitAt)
	}

	// Spend the LLM cap; the next hold is refused and the summary says when.
	settle, err := env.srv.costs.reserve(context.Background(), 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	settle(1)
	if _, err := env.srv.costs.reserve(context.Background(), 0.01, "test"); err == nil {
		t.Fatal("a hold past the cap was taken")
	}
	_, body = env.get("/api/roi/summary?window=24h&include_mock=true", testKey)
	contractstest.ValidateJSON(t, schemaRoiSummary, body)
	r = roiSummary{}
	_ = json.Unmarshal(body, &r)
	if r.Sampling.LLMCapHitAt == nil {
		t.Error("llm_cap_hit_at is null after the cap was hit")
	}
	if !r.Simulated || r.GatedResponses != 3 || !nearUSD(r.DecisionCost.USD, 0.00325) || r.DecisionCost.Label != labelSimulated ||
		r.LLMCost.Label != labelSimulated || r.TotalPer1k.Label != labelSimulated || r.Savings.Label != labelSimulated {
		t.Errorf("with the mock answer: %+v", r)
	}
	// Over simulated traffic the same checks are simulated; the published full-audit assumption
	// stays an estimate, and its basis says the answers it is applied to are simulated.
	if same := r.Counterfactual[0]; same.Label != labelSimulated {
		t.Errorf("the same checks over simulated traffic are labelled %s", same.Label)
	}
	if full := r.Counterfactual[1]; full.Method != "static_assumptions" || full.Label != labelEstimate ||
		!strings.Contains(full.Basis, "simulated answers") || !nearUSD(*full.Per1k, 35.5) {
		t.Errorf("the published full audit over simulated traffic: %+v", full)
	}
}

// GET /api/roi/timeseries zero-fills every UTC bucket of the window, puts the spend and the
// same checks in the bucket of their answer, counts simulated traffic only with include_mock, and
// answers 422 to a bucket the window does not take (400 to an unknown one).
func TestRoiTimeseries(t *testing.T) {
	env, pg := pgEnv(t, envOpts{})
	now := time.Now().UTC()
	roiSeed(t, pg, now)
	code, body := env.get("/api/roi/timeseries?window=24h", testKey)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d %s", code, body)
	}
	contractstest.ValidateJSON(t, schemaRoiTimeseries, body)
	var ts roiTimeseries
	_ = json.Unmarshal(body, &ts)
	if ts.Window != "24h" || ts.Bucket != "1h" || len(ts.Points) < 24 || len(ts.Points) > 25 {
		t.Fatalf("series %s/%s with %d points", ts.Window, ts.Bucket, len(ts.Points))
	}
	var gated int64
	var decisionUSD, llmUSD, sameUSD float64
	for i, p := range ts.Points {
		if p.T.Minute() != 0 || p.T.Second() != 0 || (i > 0 && p.T.Sub(ts.Points[i-1].T) != time.Hour) {
			t.Errorf("point %d at %v", i, p.T)
		}
		if p.SameChecksUSD == nil {
			t.Fatalf("point %d has no same-checks figure", i)
		}
		gated += p.Gated
		decisionUSD += p.DecisionUSD
		llmUSD += p.LLMUSD
		sameUSD += *p.SameChecksUSD
	}
	if gated != 2 || !nearUSD(decisionUSD, 0.00025) || !nearUSD(llmUSD, 0.012) || !nearUSD(sameUSD, 0.0036) {
		t.Errorf("series sums: gated %d decision %v llm %v same %v", gated, decisionUSD, llmUSD, sameUSD)
	}
	if last := ts.Points[len(ts.Points)-1]; last.T.After(now) || now.Sub(last.T) >= time.Hour {
		t.Errorf("the last bucket starts at %v (now %v)", last.T, now)
	}

	_, body = env.get("/api/roi/timeseries?window=7d&include_mock=true", testKey)
	ts = roiTimeseries{}
	_ = json.Unmarshal(body, &ts)
	gated, decisionUSD = 0, 0
	for _, p := range ts.Points {
		gated += p.Gated
		decisionUSD += p.DecisionUSD
	}
	if ts.Bucket != "1d" || len(ts.Points) < 7 || len(ts.Points) > 8 || gated != 3 || !nearUSD(decisionUSD, 0.00325) {
		t.Errorf("7d with include_mock: bucket %s, %d points, gated %d, decision %v", ts.Bucket, len(ts.Points), gated, decisionUSD)
	}
	if code, body := env.get("/api/roi/timeseries?window=7d&bucket=1h", testKey); code != http.StatusOK || !strings.Contains(string(body), `"bucket":"1h"`) {
		t.Errorf("7d by the hour: HTTP %d", code)
	}

	for _, q := range []string{"window=24h&bucket=1d", "window=1h&bucket=1d", "window=30d&bucket=1h", "window=15m"} {
		code, body := env.get("/api/roi/timeseries?"+q, testKey)
		if code != http.StatusUnprocessableEntity || errorCodeOf(t, body) != codeValidationFailed || !strings.Contains(string(body), `"path":"bucket"`) {
			t.Errorf("%s: HTTP %d %s", q, code, body)
		}
		contractstest.ValidateJSON(t, schemaError, body)
	}
	for _, q := range []string{"bucket=2h", "window=2d", "include_mock=maybe"} {
		if code, _ := env.get("/api/roi/timeseries?"+q, testKey); code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d", q, code)
		}
	}
	if code, _ := env.get("/api/roi/timeseries", ""); code != http.StatusUnauthorized {
		t.Errorf("without a key: HTTP %d", code)
	}
	if code, body := newTestEnv(t, envOpts{}).get("/api/roi/timeseries", testKey); code != http.StatusServiceUnavailable ||
		errorCodeOf(t, body) != codePersistenceDisabled {
		t.Errorf("without a database: HTTP %d %s", code, body)
	}
}

// The committed ROI, time-series and budget-error fixtures are valid answers, and decode strictly
// into the shapes the handlers serve.
func TestRoiFixturesMatchTheHandlers(t *testing.T) {
	for _, c := range []struct {
		file, schema string
		into         any
	}{
		{"RoiSummary.24h.json", schemaRoiSummary, &roiSummary{}},
		{"RoiTimeseries.7d.json", schemaRoiTimeseries, &roiTimeseries{}},
		{"ApiErrorBody.budget-exhausted.json", schemaError, nil},
	} {
		raw, err := contracts.FS.ReadFile("fixtures/api/" + c.file)
		if err != nil {
			t.Fatal(err)
		}
		contractstest.ValidateJSON(t, c.schema, raw)
		if c.into == nil {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c.into); err != nil {
			t.Errorf("%s: %v", c.file, err)
		}
	}
}

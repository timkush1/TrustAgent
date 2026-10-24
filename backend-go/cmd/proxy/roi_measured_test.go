package main

import (
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/contracts"
)

// measuredDoc is a measured.json whose full audit a published lite run measured (D212).
const measuredDoc = `{"schema":"trustagent.measured/v1","status":"measured","tier":"lite","mock":false,
 "generated_by":"make bench-publish","generated_at":"2026-10-12T10:00:00Z","runs":["lite-p6-x"],"snapshot":"typesafe/jev-1.13-20260917",
 "values":{"llm_full_audit_usd_per_1k":{"value":28.5,"unit":"usd_per_1k","description":"d","run_id":"lite-p6-x","system":"llm_full_audit"}},
 "sources":{"llm_full_audit_usd_per_1k":{"cost_source":"reported"},"attempts":{"bench_runs":1,"refused_run_ids":[]}}}`

func TestROIMeasuredFullAuditParse(t *testing.T) {
	m := parseMeasuredFullAudit([]byte(measuredDoc))
	if !m.ok || m.per1k != 28.5 || m.runID != "lite-p6-x" || m.tier != "lite" || m.costSource != "reported" {
		t.Fatalf("measured figure %+v", m)
	}
	if b := m.basis(); !strings.HasPrefix(b, "measured · run lite-p6-x: ") || strings.Contains(b, "vendor price") || strings.Contains(b, ", on ") {
		t.Errorf("basis %q", b)
	}
	// D229: what the cost was measured on follows it (a cost per answer grows with the documents).
	const conditions = "RAGTruth-QA test split, n=50, about 4,210 input tokens per answer, judge anthropic/claude-haiku-4.5"
	withConditions := parseMeasuredFullAudit([]byte(strings.Replace(measuredDoc, `"cost_source":"reported"}`,
		`"cost_source":"reported","conditions":"`+conditions+`"}`, 1)))
	if want := "in the published lite run, on " + conditions; !strings.Contains(withConditions.basis(), want) {
		t.Errorf("basis %q does not say %q", withConditions.basis(), want)
	}
	computed := parseMeasuredFullAudit([]byte(strings.Replace(measuredDoc, `"cost_source":"reported"`, `"cost_source":"computed"`, 1)))
	if !strings.Contains(computed.basis(), "(measured tokens × vendor price)") {
		t.Errorf("a computed cost does not say how it was taken: %q", computed.basis())
	}
	for name, doc := range map[string]string{
		"mock":         strings.Replace(measuredDoc, `"mock":false`, `"mock":true`, 1),
		"not measured": strings.Replace(measuredDoc, `"status":"measured"`, `"status":"not_measured"`, 1),
		"null value":   strings.Replace(measuredDoc, `"value":28.5`, `"value":null`, 1),
		"no run":       strings.Replace(measuredDoc, `"run_id":"lite-p6-x","system"`, `"run_id":null,"system"`, 1),
		"zero":         strings.Replace(measuredDoc, `"value":28.5`, `"value":0`, 1),
		"not json":     "{",
	} {
		if got := parseMeasuredFullAudit([]byte(doc)); got.ok {
			t.Errorf("%s: read as measured: %+v", name, got)
		}
	}
	// The committed measured.json is not measured yet: the row keeps the published estimate.
	raw, err := contracts.FS.ReadFile("claims/measured.json")
	if err != nil {
		t.Fatal(err)
	}
	if parseMeasuredFullAudit(raw).ok {
		t.Error("contracts/claims/measured.json reads as a measured full audit")
	}
}

func TestROIMeasuredFullAuditRow(t *testing.T) {
	static := &store.RoiData{UniformCosted: 5, FitSamples: linearSamples(fitMinN - 1)}
	estimate := fullAuditCounterfactual(static, 2000)
	if estimate.Label != labelEstimate || !strings.HasPrefix(estimate.Basis, "not measured: ") {
		t.Fatalf("without a measured figure: %+v", estimate)
	}

	orig := measuredFullAudit
	t.Cleanup(func() { measuredFullAudit = orig })
	measuredFullAudit = func() measuredFigure { return parseMeasuredFullAudit([]byte(measuredDoc)) }

	cf := fullAuditCounterfactual(static, 2000)
	if cf.Method != "static_assumptions" || cf.Label != labelMeasured || cf.Per1k == nil || *cf.Per1k != 28.5 ||
		cf.USD == nil || !nearUSD(*cf.USD, 57) || cf.Low != nil || cf.High != nil {
		t.Errorf("measured row %+v", cf)
	}
	if !strings.HasPrefix(cf.Basis, "measured · run lite-p6-x: ") || !strings.HasSuffix(cf.Basis, "applied to the answers checked") {
		t.Errorf("basis %q", cf.Basis)
	}
	// A window's own audits still come first: uniform samples and the fit are this window's data.
	uniform := &store.RoiData{UniformCosted: minRateN, UniformMeanUSD: 0.03, UniformStddevUSD: 0.01}
	if got := fullAuditCounterfactual(uniform, 100); got.Method != "uniform_sample" {
		t.Errorf("uniform window: %+v", got)
	}
	// The demo's free mock judge names the measured figure instead of the assumption.
	mock := &store.RoiData{Audits: 12, CostSums: store.CostSums{LLMSources: store.CostCounts{Simulated: 30}}}
	if note := freeMockJudgeNote(mock); !strings.Contains(note, "a real judge adds about $0.0285 per deep audit (measured · run lite-p6-x") ||
		strings.Contains(note, "not measured") {
		t.Errorf("mock note %q", note)
	}
}

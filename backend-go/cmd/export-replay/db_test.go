package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

func sp(s string) *string { return &s }

// seed records a trace through the store's writers, as the gate sink and the worker pool do.
func seed(t *testing.T, st *store.PostgresStore, at time.Time, answerSource string, audited bool, decisionDoc json.RawMessage) string {
	t.Helper()
	ctx := context.Background()
	id := store.NewID()
	pv := int64(3)
	tr := &store.TraceRecord{
		TraceID: id, CreatedAt: at, Source: "trafficgen", APIFormat: "openai_chat", Path: "/v1/chat/completions",
		Route: "support-bot", PolicyVersion: &pv, Mode: "flag", KeyLabel: sp("trafficgen"), Model: sp("gpt-4o-mini"),
		Scenario: sp("refund-contradiction-flag"), AnswerSource: sp(answerSource),
		QuestionPreview: sp("Mail jane@acme-corp.com: can I still get a refund?"), Flags: []string{"test_mode", "mock_decision"},
	}
	if err := st.StartTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	action, would, verdict, risk := "flag", "block", "contradicted", 0.98
	gateMs, total, cost := int64(41), int64(45), 0.0000257
	tr.EffectiveMode, tr.Action, tr.WouldAction, tr.GateStatus = sp("flag"), &action, &would, "ok"
	tr.Verdict, tr.MaxRisk, tr.Reasons = &verdict, &risk, []string{"contradiction:critical", "groundedness:critical"}
	tr.DecisionBackend, tr.DecisionSnapshot, tr.DecisionCostUSD = sp("mock"), sp("mock/jev-heuristic-v1"), &cost
	tr.GateMs, tr.TotalMs, tr.OverheadMs = &gateMs, &total, &total
	tr.ResponsePreview = sp("You can get a full refund within 60 days of purchase.")
	if audited {
		tr.AuditID, tr.AuditStatus, tr.AuditLane, tr.AuditSelection = sp(store.NewID()), "queued", sp("high"), sp("high")
	}
	var doc map[string]any
	if err := json.Unmarshal(decisionDoc, &doc); err != nil {
		t.Fatal(err)
	}
	doc["trace_id"] = id
	raw, _ := json.Marshal(doc)
	dec := &store.DecisionRecord{
		TraceID: id, PolicyVersion: &pv, Route: "support-bot", Mode: "flag", EffectiveMode: "flag", Status: "ok",
		Action: action, WouldAction: &would, Verdict: &verdict, MaxRisk: &risk, Calibration: "none",
		Reasons: tr.Reasons, Decision: raw, DecisionBackend: sp("mock"), GateMs: &gateMs,
	}
	body := &store.BodyRecord{
		TraceID: id, Messages: json.RawMessage(`[{"role":"user","content":"Can I get a refund? My key is sk-ant-abcdefghijklmnopqrstuvwxyz0123"}]`),
		UserRequest: sp("Can I get a refund?"), ResponseText: sp("You can get a full refund within 60 days of purchase."),
		FinalResponseText: sp("You can get a full refund within 60 days of purchase."),
		Evidence:          json.RawMessage(`[{"id":"refund-policy.md","source":"request_system","label":"refund-policy.md","text":"Annual plans can be refunded within 30 days.","truncated":false}]`),
		Rules:             map[string]string{}, Bytes: 300, InclusionProb: 1, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := st.RecordDecision(ctx, tr, dec, body); err != nil {
		t.Fatal(err)
	}
	if audited {
		score, grade, llm := 0.0, "F", 0.0012
		audit := &store.AuditRecord{
			AuditID: *tr.AuditID, TraceID: id, UserQuery: sp("Can I get a refund?"),
			LLMResponse: sp("You can get a full refund within 60 days of purchase."), Model: "gpt-4o-mini",
			FaithfulnessScore: &score, Grade: &grade, HallucinationDetected: true, Source: "trafficgen",
			Route: sp("support-bot"), Status: store.AuditCompleted, Verdict: sp(store.VerdictHallucinated), ScoringVersion: "v2",
			ProcessingTimeMs: 1800, CreatedAt: at.Add(time.Second), CostUSD: &llm,
			Judge: json.RawMessage(`{"configured_provider":"mock","effective_provider":"mock","model":"mock-judge","fallback_active":false,"fallback_reason":null,"structured_outputs":false,"healthy":true,"credential_source":"none"}`),
			Claims: []store.ClaimRecord{{Claim: sp("A full refund is available within 60 days."), Status: "CONTRADICTED",
				Confidence: 0.96, Evidence: []string{"Annual plans can be refunded within 30 days."}}},
		}
		if err := st.SaveAuditResult(ctx, audit, store.AuditCompleted); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestRunAgainstPostgres(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := store.OpenPostgresStore(ctx, s.URL); !errors.Is(err, store.ErrSchemaBehind) {
		t.Fatalf("OpenPostgresStore on an empty schema: %v, want ErrSchemaBehind", err)
	}
	st, err := store.NewPostgresStore(ctx, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	doc := fixture(t, "refund-contradiction-flag").Decisions[0]
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	old := seed(t, st, base.Add(-48*time.Hour), "scripted", true, doc) // outside --since
	first := seed(t, st, base, "scripted", true, doc)
	second := seed(t, st, base.Add(5*time.Second), "generated", false, doc)

	d := deps{
		getenv: func(k string) string {
			if k == "TRUTHTABLE_DATABASE_URL" {
				return s.URL
			}
			return ""
		},
		now: time.Now, rev: func() string { return "0123abcd4567" },
		openDB: func(ctx context.Context, url string) (traceStore, func(), error) {
			ro, err := store.OpenPostgresStore(ctx, url)
			if err != nil {
				return nil, nil, err
			}
			return ro, ro.Close, nil
		},
	}
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := run(ctx, []string{"--out", dir, "--since", base.Add(-time.Minute).Format(time.RFC3339Nano), "--source", "trafficgen"}, &out, &errb, d)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	m := validateRecording(t, dir)
	if m["kind"] != "mock" || m["label"] != mockLabel || dig(m, "decision_model", "snapshot") != "mock/jev-heuristic-v1" ||
		dig(m, "judge", "provider") != "mock" || dig(m, "answer_sources", "scripted") != 1.0 || dig(m, "answer_sources", "generated") != 1.0 ||
		dig(m, "totals", "llm_usd") != 0.0012 || dig(m, "git_sha") != "0123abcd4567" {
		t.Errorf("manifest = %v", m)
	}
	traces := m["traces"].([]any)
	if len(traces) != 2 || traces[0].(map[string]any)["trace_id"] != first || traces[1].(map[string]any)["trace_id"] != second {
		t.Errorf("traces = %v (old %s)", traces, old)
	}
	b, err := os.ReadFile(filepath.Join(dir, "traces", first+".json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, leak := range []string{"jane@acme-corp.com", "abcdefghijklmnopqrstuvwxyz0123", "acme-corp"} {
		if strings.Contains(text, leak) {
			t.Errorf("trace file leaks %q", leak)
		}
	}
	if !strings.Contains(text, "sk-a") || !strings.Contains(text, `"CONTRADICTED"`) || !strings.Contains(text, "60 days") {
		t.Errorf("trace file lost content:\n%s", text)
	}
	if manifest, _ := os.ReadFile(filepath.Join(dir, "manifest.json")); strings.Contains(string(manifest), "jane@") {
		t.Error("the manifest previews leak an email")
	}
}

func dig(m map[string]any, path ...string) any {
	var v any = m
	for _, p := range path {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[p]
	}
	return v
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func strp(s string) *string { return &s }

// pendingTrace is a trace as the gate sink records it before the upstream call.
func pendingTrace(route string, at time.Time) *TraceRecord {
	pv := int64(3)
	return &TraceRecord{
		TraceID: NewID(), CreatedAt: at, Source: "trafficgen", APIFormat: "openai_chat", Path: "/v1/chat/completions",
		Route: route, PolicyVersion: &pv, Mode: "flag", KeyLabel: strp("trafficgen"), Model: strp("gpt-4o-mini"),
		Scenario: strp("grounded-pass"), AnswerSource: strp("scripted"), QuestionPreview: strp("How long is the refund window?"),
		Flags: []string{"test_mode"},
	}
}

// decided returns t with the decision columns of a flagged or passed exchange.
func decided(t *TraceRecord, action, would, verdict string, risk float64, reasons []string) (*TraceRecord, *DecisionRecord) {
	d := *t
	d.EffectiveMode, d.Action, d.WouldAction, d.GateStatus = strp("flag"), &action, &would, "ok"
	d.Verdict, d.MaxRisk, d.Reasons = &verdict, &risk, reasons
	d.EvidenceSummary = json.RawMessage(`{"configured":["request_system"],"used":[{"source":"request_system","items":1,"labels":["refund-policy.md"],"tokens":22}],"empty":[],"knowledge":"not_requested","truncated":false,"tokens":22}`)
	d.DecisionBackend, d.DecisionSnapshot = strp("mock"), strp("mock/jev-heuristic-v1")
	gateMs, total := int64(41), int64(45)
	d.GateMs, d.TotalMs, d.OverheadMs = &gateMs, &total, &total
	d.ResponsePreview = strp("You can get a full refund within 60 days of purchase.")
	chars := 53
	d.ResponseChars = &chars
	d.Flags = []string{"test_mode", "mock_decision"}
	doc := `{"spec":"decision/v1","trace_id":"` + t.TraceID + `","detectors":[{"id":"numbers_not_in_evidence","ran":true,"fired":true,"findings":[{"text":"60","start":33,"end":35}]}]}`
	rec := &DecisionRecord{
		TraceID: t.TraceID, PolicyVersion: t.PolicyVersion, Route: t.Route, Mode: "flag", EffectiveMode: "flag",
		Status: "ok", Action: action, WouldAction: &would, Verdict: &verdict, MaxRisk: &risk, Calibration: "none",
		Reasons: reasons, Decision: json.RawMessage(doc), DecisionBackend: strp("mock"), GateMs: &gateMs,
	}
	return &d, rec
}

func body(traceID string, expires time.Time) *BodyRecord {
	return &BodyRecord{
		TraceID:  traceID,
		Messages: json.RawMessage(`[{"role":"user","content":"Can I get a refund?"}]`), UserRequest: strp("Can I get a refund?"),
		SystemInstructions: strp("You are the Acme support assistant."), ResponseText: strp("Within 60 days."),
		FinalResponseText: strp("Within 60 days."),
		Evidence:          json.RawMessage(`[{"id":"refund-policy.md","source":"request_system","label":"refund-policy.md","text":"30 days.","truncated":false}]`),
		Rules:             map[string]string{}, Bytes: 120, InclusionProb: 1, ExpiresAt: expires,
	}
}

func TestTraceLifecycle(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	start := pendingTrace("support-bot", time.Now().UTC())

	if err := s.StartTrace(ctx, start); err != nil {
		t.Fatalf("StartTrace: %v", err)
	}
	// Starting twice is idempotent.
	if err := s.StartTrace(ctx, start); err != nil {
		t.Fatalf("StartTrace again: %v", err)
	}
	got, err := s.GetTrace(ctx, start.TraceID)
	if err != nil {
		t.Fatalf("GetTrace: %v", err)
	}
	sum := got.Summary()
	if sum.GateStatus != "pending" || sum.Action != nil || sum.Audit.Status != "none" || sum.Audit.VerdictCounts != nil {
		t.Errorf("pending summary %+v", sum)
	}
	if outcome := Outcome(OutcomeFields{GateStatus: sum.GateStatus}); outcome != "checking" {
		t.Errorf("pending outcome %q", outcome)
	}

	tr, dec := decided(start, "flag", "block", "contradicted", 0.98,
		[]string{"contradiction:critical", "groundedness:critical", "det:numbers_not_in_evidence"})
	tr.AuditID, tr.AuditStatus, tr.AuditLane, tr.AuditSelection = strp(NewID()), "queued", strp("high"), strp("high")
	tr.QuestionPreview = nil // the decision does not repeat it; the stored preview stays
	if err := s.RecordDecision(ctx, tr, dec, body(tr.TraceID, time.Now().Add(time.Hour))); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	got, err = s.GetTrace(ctx, start.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	sum = got.Summary()
	if sum.GateStatus != "ok" || *sum.Action != "flag" || *sum.WouldAction != "block" || *sum.Verdict != "contradicted" {
		t.Errorf("decided summary %+v", sum)
	}
	if sum.MaxRisk == nil || *sum.MaxRisk != 0.98 {
		t.Errorf("max_risk must read back as written (REAL column): %v", sum.MaxRisk)
	}
	if got.ResponseChars == nil || *got.ResponseChars != 53 {
		t.Errorf("response_chars (0005) must read back as written: %v", got.ResponseChars)
	}
	if len(sum.Reasons) != 3 || sum.QuestionPreview == nil || *sum.QuestionPreview != "How long is the refund window?" {
		t.Errorf("reasons %v, question preview %v", sum.Reasons, sum.QuestionPreview)
	}
	if sum.Audit.Status != "queued" || *sum.Audit.Lane != "high" || sum.DecisionModel == nil || sum.DecisionModel.Backend != "mock" {
		t.Errorf("audit %+v decision model %+v", sum.Audit, sum.DecisionModel)
	}
	if !strings.Contains(string(sum.CheckedAgainst), `"refund-policy.md"`) || !sum.UpdatedAt.After(sum.CreatedAt) {
		t.Errorf("checked_against %s, updated_at %v created_at %v", sum.CheckedAgainst, sum.UpdatedAt, sum.CreatedAt)
	}

	decisions, err := s.TraceDecisions(ctx, start.TraceID)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("TraceDecisions: %v %v", decisions, err)
	}
	if red := string(RedactDecision(decisions[0])); strings.Contains(red, `"60"`) || !strings.Contains(red, `"text":""`) {
		t.Errorf("redacted decision keeps finding text: %s", red)
	}

	b, err := s.GetTraceBody(ctx, start.TraceID)
	if err != nil {
		t.Fatalf("GetTraceBody: %v", err)
	}
	if v := b.View(time.Now()); !v.Available || *v.ResponseText != "Within 60 days." || v.UnavailableReason != nil {
		t.Errorf("body view %+v", v)
	}
	if v := b.View(time.Now().Add(2 * time.Hour)); v.Available || *v.UnavailableReason != BodyExpired {
		t.Errorf("an expired body is unavailable: %+v", v)
	}

	// The audit completes: saved with its claims, and the trace's audit status in the same step.
	score, grade := 0.2, "F"
	audit := &AuditRecord{
		AuditID: *tr.AuditID, TraceID: tr.TraceID, UserQuery: strp("Can I get a refund?"), LLMResponse: strp("Within 60 days."),
		FaithfulnessScore: &score, Grade: &grade, HallucinationDetected: true, Source: "trafficgen",
		Route: strp("support-bot"), Status: AuditCompleted, Verdict: strp(VerdictHallucinated), ScoringVersion: "v2",
		Claims: []ClaimRecord{{Claim: strp("A full refund is available within 60 days."), Status: "CONTRADICTED",
			Confidence: 0.96, Evidence: []string{"Annual plans can be refunded within 30 days of purchase."},
			Quote: strp("60 days"), SpanStart: new(int), SpanEnd: new(int)}},
	}
	*audit.Claims[0].SpanStart, *audit.Claims[0].SpanEnd = 33, 40
	if err := s.SaveAuditResult(ctx, audit, AuditCompleted); err != nil {
		t.Fatalf("SaveAuditResult: %v", err)
	}
	got, err = s.GetTrace(ctx, start.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	sum = got.Summary()
	if sum.Audit.Status != "completed" || *sum.Audit.Verdict != VerdictHallucinated || *sum.Audit.TrustScore != 0.2 ||
		sum.Audit.VerdictCounts["CONTRADICTED"] != 1 || len(sum.Audit.VerdictCounts) != len(ClaimVerdicts) {
		t.Errorf("audit join %+v", sum.Audit)
	}
	a, err := s.LatestAuditForTrace(ctx, start.TraceID)
	if err != nil {
		t.Fatalf("LatestAuditForTrace: %v", err)
	}
	detail := a.Detail()
	if len(detail.Claims) != 1 || detail.Claims[0].Span == nil || detail.Claims[0].Span.Start != 33 ||
		detail.Claims[0].DecidedBy != "llm" || *detail.Claims[0].Quote != "60 days" {
		t.Errorf("claims %+v", detail.Claims)
	}
	if red := detail.Redact(); red.UserQuery != nil || red.Claims[0].Claim != nil || red.Claims[0].Status != "CONTRADICTED" || !red.Redacted {
		t.Errorf("redacted audit %+v", red)
	}
}

func TestNotAssessedTraceReportsNotAssessedAudit(t *testing.T) {
	r := &TraceRecord{TraceID: "t", Route: "support-bot", Mode: "flag", GateStatus: "not_assessed", AuditStatus: TraceAuditNotAssessed}
	if v := r.Summary().Audit.Verdict; v == nil || *v != VerdictNotAssessed {
		t.Errorf("verdict %v", v)
	}
	r.AuditStatus = TraceAuditSampledOut
	if v := r.Summary().Audit.Verdict; v != nil {
		t.Errorf("a sampled-out trace has no audit verdict: %v", *v)
	}
}

func TestSetTraceAudit(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	tr := pendingTrace("support-bot", time.Now().UTC())
	if err := s.StartTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTraceAudit(ctx, tr.TraceID, TraceAuditUpdate{AuditID: strp("audit-1"), Status: TraceAuditDropped}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTraceAudit(ctx, tr.TraceID, TraceAuditUpdate{Lane: strp("low")}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTrace(ctx, tr.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AuditStatus != TraceAuditDropped || *got.AuditID != "audit-1" || *got.AuditLane != "low" {
		t.Errorf("audit fields %v %v %v", got.AuditStatus, got.AuditID, got.AuditLane)
	}
	if _, err := s.GetTrace(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing trace: %v", err)
	}
	if _, err := s.GetTraceBody(ctx, tr.TraceID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a trace without a kept body: %v", err)
	}
}

func TestListTracesFiltersAndPages(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	// Five traces a second apart: pass, flag, not_assessed on support-bot; a pending and an observe
	// trace on the default route.
	var ids []string
	add := func(i int, route string, fn func(*TraceRecord) (*TraceRecord, *DecisionRecord)) {
		tr := pendingTrace(route, base.Add(time.Duration(i)*time.Second))
		ids = append(ids, tr.TraceID)
		if err := s.StartTrace(ctx, tr); err != nil {
			t.Fatal(err)
		}
		if fn == nil {
			return
		}
		d, rec := fn(tr)
		if err := s.RecordDecision(ctx, d, rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	add(0, "support-bot", func(tr *TraceRecord) (*TraceRecord, *DecisionRecord) {
		return decided(tr, "pass", "pass", "supported", 0.06, nil)
	})
	add(1, "support-bot", func(tr *TraceRecord) (*TraceRecord, *DecisionRecord) {
		return decided(tr, "flag", "block", "contradicted", 0.98, []string{"contradiction:critical"})
	})
	add(2, "support-bot", func(tr *TraceRecord) (*TraceRecord, *DecisionRecord) {
		d, rec := decided(tr, "pass", "pass", "not_assessed", 0, nil)
		d.GateStatus, d.MaxRisk, d.AuditStatus, d.ResponsePreview = "not_assessed", nil, TraceAuditNotAssessed, strp("No 50% discount_codes here")
		rec.Status, rec.MaxRisk = "not_assessed", nil
		return d, rec
	})
	add(3, "default", nil)
	add(4, "default", func(tr *TraceRecord) (*TraceRecord, *DecisionRecord) {
		d, rec := decided(tr, "observe", "flag", "unsupported", 0.7, []string{"groundedness:warn"})
		d.Mode = "observe"
		return d, rec
	})

	list := func(f TraceFilter) []string {
		t.Helper()
		items, _, err := s.ListTraces(ctx, f)
		if err != nil {
			t.Fatalf("ListTraces %+v: %v", f, err)
		}
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.TraceID
		}
		return out
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}

	eq("all, newest first", list(TraceFilter{}), ids[4], ids[3], ids[2], ids[1], ids[0])
	eq("route", list(TraceFilter{Route: "support-bot"}), ids[2], ids[1], ids[0])
	eq("outcome passed", list(TraceFilter{Outcome: "passed"}), ids[0])
	eq("outcome flagged", list(TraceFilter{Outcome: "flagged"}), ids[1])
	eq("outcome not_checked", list(TraceFilter{Outcome: "not_checked"}), ids[2])
	eq("outcome checking", list(TraceFilter{Outcome: "checking"}), ids[3])
	eq("outcome would_flag", list(TraceFilter{Outcome: "would_flag"}), ids[4])
	eq("route and outcome", list(TraceFilter{Route: "default", Outcome: "passed"}))
	eq("action", list(TraceFilter{Action: "pass"}), ids[2], ids[0])
	eq("gate status", list(TraceFilter{GateStatus: "pending"}), ids[3])
	eq("audit verdict", list(TraceFilter{AuditVerdict: VerdictNotAssessed}), ids[2])
	eq("q escapes LIKE wildcards", list(TraceFilter{Q: "50% DISCOUNT_"}), ids[2])
	eq("q", list(TraceFilter{Q: "zzz"}))
	since := base.Add(3 * time.Second)
	eq("since", list(TraceFilter{Since: &since}), ids[4], ids[3])
	eq("until", list(TraceFilter{Until: &since}), ids[2], ids[1], ids[0])
	future := time.Now().Add(time.Hour)
	eq("updated_since", list(TraceFilter{UpdatedSince: &future}))

	// Keyset pages of two cover every trace once.
	var paged []string
	cursor := ""
	for {
		items, next, err := s.ListTraces(ctx, TraceFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			paged = append(paged, it.TraceID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	eq("pages", paged, ids[4], ids[3], ids[2], ids[1], ids[0])
	if _, _, err := s.ListTraces(ctx, TraceFilter{Cursor: "%%%"}); !errors.Is(err, ErrBadCursor) {
		t.Errorf("bad cursor: %v", err)
	}
}

func TestPreviewAndFloat32(t *testing.T) {
	if p := Preview("héllo wörld", 5); *p != "héllo" {
		t.Errorf("preview %q", *p)
	}
	if p := Preview("short", 200); *p != "short" {
		t.Errorf("preview %q", *p)
	}
	if Preview("", 10) != nil {
		t.Error("an empty text has no preview")
	}
	if v := Float64From32(float32(0.06)); v != 0.06 {
		t.Errorf("float32 0.06 -> %v", v)
	}
}

func TestRecordAttemptWritesOnlyItsDecision(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	start := pendingTrace("payments-faq", time.Now().UTC())
	if err := s.StartTrace(ctx, start); err != nil {
		t.Fatal(err)
	}
	tr, dec := decided(start, "fallback", "block", "contradicted", 0.97, []string{"groundedness:critical"})
	// The fallback re-gate (attempt 1) is recorded before the trace's outcome.
	re := *dec
	re.Attempt, re.Action, re.WouldAction, re.Reasons = 1, "pass", strp("pass"), []string{}
	re.Decision = json.RawMessage(`{"spec":"decision/v1","trace_id":"` + start.TraceID + `","attempt":1,"action":"pass"}`)
	if err := s.RecordAttempt(ctx, &re); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	got, err := s.GetTrace(ctx, start.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GateStatus != GateStatusPending || got.Action != nil {
		t.Errorf("the attempt changed the trace: %s %v", got.GateStatus, got.Action)
	}
	if err := s.RecordDecision(ctx, tr, dec, nil); err != nil {
		t.Fatal(err)
	}
	docs, err := s.TraceDecisions(ctx, start.TraceID)
	if err != nil || len(docs) != 2 {
		t.Fatalf("decisions by attempt: %s %v", docs, err)
	}
	var second struct {
		Attempt int    `json:"attempt"`
		Action  string `json:"action"`
	}
	if err := json.Unmarshal(docs[1], &second); err != nil || !strings.Contains(string(docs[0]), `"detectors"`) ||
		second.Attempt != 1 || second.Action != "pass" {
		t.Errorf("decisions by attempt: %s (%v)", docs, err)
	}

	// Writing the attempt again replaces it; the trace keeps the attempt-0 outcome.
	re.Action, re.Decision = "flag", json.RawMessage(`{"spec":"decision/v1","trace_id":"`+start.TraceID+`","attempt":1,"action":"flag"}`)
	if err := s.RecordAttempt(ctx, &re); err != nil {
		t.Fatal(err)
	}
	var n int
	var action string
	if err := s.pool.QueryRow(ctx, `SELECT count(*), max(action) FILTER (WHERE attempt = 1) FROM gate_decisions WHERE trace_id = $1`,
		start.TraceID).Scan(&n, &action); err != nil || n != 2 || action != "flag" {
		t.Errorf("rows %d attempt-1 action %q (%v)", n, action, err)
	}
	if got, _ := s.GetTrace(ctx, start.TraceID); got.GateStatus != "ok" || *got.Action != "fallback" {
		t.Errorf("trace %s %v", got.GateStatus, got.Action)
	}

	// Attempt 0 goes with its trace, never alone.
	zero := *dec
	if err := s.RecordAttempt(ctx, &zero); err == nil {
		t.Error("RecordAttempt accepted attempt 0")
	}
	if err := s.RecordAttempt(ctx, nil); err == nil {
		t.Error("RecordAttempt accepted nil")
	}
}

func TestRecordDecisionKeepsWhatTheClientGot(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	start := pendingTrace("payments-faq", time.Now().UTC())
	if err := s.StartTrace(ctx, start); err != nil {
		t.Fatal(err)
	}
	// The fallback answer went out: the trace's action and flags say so, the body keeps both texts,
	// and the stream's time to first token is kept.
	tr, dec := decided(start, "fallback", "block", "contradicted", 0.97, []string{"groundedness:critical"})
	ttft := int64(120)
	tr.TTFTMs, tr.Flags = &ttft, []string{"test_mode", "mock_decision", "fallback_used"}
	b := body(tr.TraceID, time.Now().Add(time.Hour))
	b.FinalResponseText = strp("Approved refunds go back to your original payment method within 5 to 10 business days.")
	if err := s.RecordDecision(ctx, tr, dec, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTrace(ctx, start.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	sum := got.Summary()
	if *sum.Action != "fallback" || sum.Timings.TTFTMs == nil || *sum.Timings.TTFTMs != 120 ||
		strings.Join(sum.Flags, ",") != "test_mode,mock_decision,fallback_used" {
		t.Errorf("trace action %v ttft %v flags %v", sum.Action, sum.Timings.TTFTMs, sum.Flags)
	}
	gb, err := s.GetTraceBody(ctx, start.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	if v := gb.View(time.Now()); *v.ResponseText != "Within 60 days." || v.FinalResponseText == nil ||
		*v.FinalResponseText != *b.FinalResponseText {
		t.Errorf("body texts %v / %v", v.ResponseText, v.FinalResponseText)
	}

	// An error-style block delivered no answer text: final_response_text is NULL.
	start = pendingTrace("payments-faq", time.Now().UTC())
	tr, dec = decided(start, "block", "block", "contradicted", 0.97, []string{"groundedness:critical"})
	b = body(tr.TraceID, time.Now().Add(time.Hour))
	b.FinalResponseText = nil
	if err := s.RecordDecision(ctx, tr, dec, b); err != nil {
		t.Fatal(err)
	}
	if gb, err := s.GetTraceBody(ctx, start.TraceID); err != nil || gb.FinalResponseText != nil || *gb.ResponseText != "Within 60 days." {
		t.Errorf("error-style block body %+v (%v)", gb, err)
	}
}

func TestRecordLateDecisionReplacesItsAttemptRow(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	start := pendingTrace("support-bot", time.Now().UTC())
	if err := s.StartTrace(ctx, start); err != nil {
		t.Fatal(err)
	}
	// The gate timed out and passed the answer unchecked.
	tr, dec := decided(start, "pass", "pass", "unknown", 0, []string{})
	tr.GateStatus, dec.Status, dec.MaxRisk, dec.Verdict = "timeout", "timeout", nil, nil
	tr.MaxRisk, tr.Verdict = nil, nil
	if err := s.RecordDecision(ctx, tr, dec, nil); err != nil {
		t.Fatal(err)
	}

	// The decision model answered late: the whole row is the late decision's, usage included.
	late := *dec
	calls, attempts, ms, cost := 1, 2, int64(3400), 0.000042
	late.Late, late.Status, late.Action, late.WouldAction, late.Verdict = true, "ok", "flag", strp("block"), strp("contradicted")
	risk := 0.97
	late.MaxRisk, late.Reasons, late.Calibration = &risk, []string{"contradiction:critical"}, "all"
	late.DecisionSnapshot, late.DecisionCalls, late.DecisionAttempts, late.DecisionMs, late.CostUSD = strp("jev-1.2"), &calls, &attempts, &ms, &cost
	late.RequestSHA256 = []string{"abc"}
	late.Decision = json.RawMessage(`{"spec":"decision/v1","trace_id":"` + start.TraceID + `","attempt":0,"action":"flag","late":true}`)
	if err := s.RecordLateDecision(ctx, &late); err != nil {
		t.Fatalf("RecordLateDecision: %v", err)
	}
	// Recording it again (a retried write) changes nothing more: the flag is there once.
	if err := s.RecordLateDecision(ctx, &late); err != nil {
		t.Fatal(err)
	}
	var (
		n                  int
		isLate             bool
		status, action     string
		snapshot           *string
		dCalls, dAttempts  *int
		dMs                *int
		dCost              *float64
		reqSHA             []string
		created, updatedAt time.Time
	)
	if err := s.pool.QueryRow(ctx, `SELECT count(*) OVER (), late, status, action, decision_snapshot, decision_calls,
		decision_attempts, decision_ms, cost_usd::float8, request_sha256, created_at, updated_at
		FROM gate_decisions WHERE trace_id = $1`, start.TraceID).Scan(&n, &isLate, &status, &action, &snapshot, &dCalls,
		&dAttempts, &dMs, &dCost, &reqSHA, &created, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !isLate || status != "ok" || action != "flag" || *snapshot != "jev-1.2" || *dCalls != 1 || *dAttempts != 2 ||
		*dMs != 3400 || *dCost != cost || len(reqSHA) != 1 || updatedAt.Before(created) {
		t.Errorf("row: n %d late %v %s %s snapshot %v calls %v attempts %v ms %v cost %v sha %v", n, isLate, status, action,
			snapshot, dCalls, dAttempts, dMs, dCost, reqSHA)
	}
	got, err := s.GetTrace(ctx, start.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	if *got.Action != "pass" || got.GateStatus != "timeout" || got.MaxRisk != nil {
		t.Errorf("the trace keeps what was enforced: %v %s %v", *got.Action, got.GateStatus, got.MaxRisk)
	}
	if c := strings.Count(strings.Join(got.Flags, ","), "late_decision"); c != 1 {
		t.Errorf("flags %v", got.Flags)
	}
	docs, _ := s.TraceDecisions(ctx, start.TraceID)
	if len(docs) != 1 || !strings.Contains(strings.ReplaceAll(string(docs[0]), " ", ""), `"late":true`) {
		t.Errorf("decisions %s", docs)
	}

	// A regular write of the same attempt after it never buries the late decision.
	if err := s.RecordDecision(ctx, tr, dec, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT late, action FROM gate_decisions WHERE trace_id = $1`, start.TraceID).Scan(&isLate, &action); err != nil ||
		!isLate || action != "flag" {
		t.Errorf("after a regular write: late %v action %s (%v)", isLate, action, err)
	}
	// Nor does it drop the trace's late_decision flag.
	if got, err := s.GetTrace(ctx, start.TraceID); err != nil {
		t.Fatal(err)
	} else if !containsString(got.Flags, "late_decision") {
		t.Errorf("after a regular write: flags %v", got.Flags)
	}

	// The fallback records attempt 1 before the trace write of attempt 0: a late decision of
	// attempt 1 recorded in between keeps its flag through that write.
	fb := pendingTrace("support-bot", time.Now().UTC())
	if err := s.StartTrace(ctx, fb); err != nil {
		t.Fatal(err)
	}
	fbTrace, fbDec := decided(fb, "fallback", "block", "contradicted", 0.9, []string{"contradiction:critical"})
	fbTrace.Flags = append(fbTrace.Flags, "fallback_used")
	regate := *fbDec
	regate.Attempt, regate.Status, regate.Action = 1, "timeout", "pass"
	if err := s.RecordAttempt(ctx, &regate); err != nil {
		t.Fatal(err)
	}
	lateRegate := regate
	lateRegate.Late, lateRegate.Status = true, "ok"
	if err := s.RecordLateDecision(ctx, &lateRegate); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDecision(ctx, fbTrace, fbDec, nil); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetTrace(ctx, fb.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(got.Flags, "late_decision") || !containsString(got.Flags, "fallback_used") || *got.Action != "fallback" {
		t.Errorf("attempt 1 late before attempt 0: flags %v action %v", got.Flags, *got.Action)
	}

	// A late attempt 1 upserts its own row; it needs the late mark and a trace id.
	one := late
	one.Attempt = 1
	if err := s.RecordLateDecision(ctx, &one); err != nil {
		t.Fatal(err)
	}
	if docs, _ := s.TraceDecisions(ctx, start.TraceID); len(docs) != 2 {
		t.Errorf("attempt 1: %d rows", len(docs))
	}
	notLate := *dec
	if err := s.RecordLateDecision(ctx, &notLate); err == nil {
		t.Error("RecordLateDecision accepted a decision that is not late")
	}
	if err := s.RecordLateDecision(ctx, nil); err == nil {
		t.Error("RecordLateDecision accepted nil")
	}
}

// TestListTracesReasonFilter: reason=<code> matches a whole reason code and reason=<prefix>:
// every code it starts (P4-PLAN D68), within since; LIKE wildcards in the prefix stay literal.
func TestListTracesReasonFilter(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	reasons := [][]string{
		{"contradiction:critical", "groundedness:critical", "det:numbers_not_in_evidence"},
		{"det:urls_not_in_evidence"},
		{"groundedness:warn"},
		{"needs_second_signal"},
		nil,
		{"det_x:warn"}, // "det_" must not match "det:" through a LIKE wildcard
	}
	ids := make([]string, len(reasons))
	for i, r := range reasons {
		tr := pendingTrace("support-bot", base.Add(time.Duration(i)*time.Second))
		ids[i] = tr.TraceID
		d, rec := decided(tr, "flag", "flag", "unsupported", 0.7, r)
		if err := s.RecordDecision(ctx, d, rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	since := base.Add(-time.Minute)
	list := func(reason string, since *time.Time) string {
		t.Helper()
		items, _, err := s.ListTraces(ctx, TraceFilter{Reason: reason, Since: since})
		if err != nil {
			t.Fatalf("reason %q: %v", reason, err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.TraceID)
		}
		return strings.Join(out, ",")
	}
	join := func(ids ...string) string { return strings.Join(ids, ",") }
	for _, tc := range []struct{ reason, want string }{
		{"det:numbers_not_in_evidence", join(ids[0])},
		{"groundedness:warn", join(ids[2])},
		{"needs_second_signal", join(ids[3])},
		{"det:", join(ids[1], ids[0])},
		{"groundedness:", join(ids[2], ids[0])},
		{"contradiction:", join(ids[0])},
		{"det", ""},          // a whole code, not a prefix
		{"groundedness", ""}, // likewise
		{"det_", ""},
		{"zzz:", ""},
	} {
		if got := list(tc.reason, &since); got != tc.want {
			t.Errorf("reason %q: got %s, want %s", tc.reason, got, tc.want)
		}
	}
	late := base.Add(500 * time.Millisecond)
	if got := list("det:", &late); got != ids[1] {
		t.Errorf("reason det: since the second trace: got %s, want %s", got, ids[1])
	}
	if !ReasonPrefix("det:") || ReasonPrefix("det:secret_pattern") {
		t.Error("ReasonPrefix")
	}
}

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

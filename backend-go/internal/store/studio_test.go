package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

// studioTrace is a decided trace for the Studio queries.
type studioTrace struct {
	route, action, would string
	disagreement         bool
	levels               map[string]string
	// body: none, live or expired.
	body     string
	prob     float64
	at       time.Time
	response string
	noAnswer bool
}

func seedStudioTrace(t *testing.T, s *PostgresStore, st studioTrace) string {
	t.Helper()
	ctx := context.Background()
	if st.at.IsZero() {
		st.at = time.Now().UTC()
	}
	tr := pendingTrace(st.route, st.at)
	if err := s.StartTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	evs := map[string]any{}
	for ref, level := range st.levels {
		e := map[string]any{"ref": ref, "level": nil}
		if level != "" {
			e["level"] = level
		}
		evs[ref] = e
	}
	doc, _ := json.Marshal(map[string]any{"spec": "decision/v1", "trace_id": tr.TraceID, "evaluators": evs,
		"disagreement": st.disagreement, "compile": map[string]any{"cpt": 3.7123456789, "requests": 1}})
	d, rec := decided(tr, st.action, st.would, "supported", 0.2, nil)
	rec.Decision, rec.WouldAction = doc, &st.would
	rec.RequestSHA256 = []string{"aa", "bb"}
	model := "typesafe/jev-1.13"
	rec.DecisionModelRequested = &model
	var b *BodyRecord
	switch st.body {
	case "live", "expired":
		expires := time.Now().Add(time.Hour)
		if st.body == "expired" {
			expires = time.Now().Add(-time.Minute)
		}
		b = body(tr.TraceID, expires)
		if st.response != "" {
			b.ResponseText = &st.response
		}
		if st.noAnswer {
			b.ResponseText = nil
		}
		if st.prob > 0 {
			b.InclusionProb = st.prob
		}
	}
	if err := s.RecordDecision(ctx, d, rec, b); err != nil {
		t.Fatal(err)
	}
	// The gateway records the decision as the trace is gated; date it like the trace.
	if _, err := s.pool.Exec(ctx, `UPDATE gate_decisions SET created_at = $1 WHERE trace_id = $2`, st.at, tr.TraceID); err != nil {
		t.Fatal(err)
	}
	return tr.TraceID
}

func TestStudioResultsRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	risk, raw, cost, ms := 0.75, 0.8, 0.0000123, int64(41)
	level, status, snap := "warn", "assessed", "jev-1.13.0"
	rows := []StudioResult{
		{RunID: "run-1", Kind: StudioKindTest, SampleID: "s2", EvaluatorRef: "my_check@draft:0123456789ab",
			Answer: json.RawMessage(`{"noul":0.8}`), RiskRaw: &raw, Risk: &risk, Level: &level, Status: &status,
			Snapshot: &snap, LatencyMs: &ms, CostUSD: &cost},
		{RunID: "run-1", Kind: StudioKindTest, SampleID: "s1", EvaluatorRef: "my_check@draft:0123456789ab", Status: strp("not_assessed")},
	}
	if err := s.InsertStudioResults(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// A repeated row is kept as stored.
	again := rows[:1:1]
	again[0].Status = strp("error")
	if err := s.InsertStudioResults(ctx, again); err != nil {
		t.Fatal(err)
	}
	got, err := s.StudioRun(ctx, "run-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("run: %d rows, err %v", len(got), err)
	}
	if got[0].SampleID != "s1" || got[0].Answer != nil || got[0].RiskRaw != nil || got[0].CostUSD != nil {
		t.Errorf("s1 = %+v", got[0])
	}
	g := got[1]
	if *g.Status != "assessed" || *g.RiskRaw != 0.8 || *g.Risk != 0.75 || *g.Level != "warn" || *g.LatencyMs != 41 ||
		*g.CostUSD != cost || *g.Snapshot != snap || g.Kind != StudioKindTest || g.CreatedAt.IsZero() {
		t.Errorf("s2 = %+v", g)
	}
	var answer map[string]float64
	if err := json.Unmarshal(g.Answer, &answer); err != nil || answer["noul"] != 0.8 {
		t.Errorf("answer = %s", g.Answer)
	}
	if err := s.InsertStudioResults(ctx, []StudioResult{{RunID: "r", Kind: "replay", SampleID: "s", EvaluatorRef: "x@1"}}); err == nil {
		t.Error("an unknown kind was stored")
	}
	if err := s.InsertStudioResults(ctx, []StudioResult{{RunID: "r", Kind: StudioKindDryRun, EvaluatorRef: "x@1"}}); err == nil {
		t.Error("a row without a sample id was stored")
	}
	if err := s.InsertStudioResults(ctx, nil); err != nil {
		t.Error(err)
	}
}

func TestStudioSamples(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	flagged := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "flag", would: "flag", body: "live", at: now.Add(-3 * time.Minute)})
	observed := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "observe", would: "block", body: "live", at: now.Add(-2 * time.Minute)})
	passed := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "pass", would: "pass", body: "live", disagreement: true, at: now.Add(-time.Minute)})
	seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "flag", would: "flag", body: "expired"})
	seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "flag", would: "flag", body: "none"})
	seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "flag", would: "flag", body: "live", noAnswer: true})
	other := seedStudioTrace(t, s, studioTrace{route: "faq", action: "flag", would: "flag", body: "live"})
	old := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "flag", would: "flag", body: "live", at: now.Add(-48 * time.Hour)})

	since := now.Add(-24 * time.Hour)
	f := StudioSampleFilter{Route: "support-bot", Since: since, Filter: SampleFlagged, Limit: 50, Now: now}
	got, err := s.StudioSampleTraces(ctx, f)
	if err != nil || !slices.Equal(got, []string{observed, flagged}) {
		t.Fatalf("flagged = %v err %v, want %s,%s", got, err, observed, flagged)
	}
	f.Filter = SampleDisagreement
	if got, _ := s.StudioSampleTraces(ctx, f); !slices.Equal(got, []string{passed}) {
		t.Errorf("disagreement = %v", got)
	}
	f.Filter, f.Route = SampleRandom, ""
	got, _ = s.StudioSampleTraces(ctx, f)
	slices.Sort(got)
	want := []string{flagged, observed, passed, other}
	slices.Sort(want)
	if !slices.Equal(got, want) || slices.Contains(got, old) {
		t.Errorf("random = %v, want %v", got, want)
	}
	f.Limit = 1
	if got, _ := s.StudioSampleTraces(ctx, f); len(got) != 1 {
		t.Errorf("limit 1: %v", got)
	}
	f.Filter = "everything"
	if _, err := s.StudioSampleTraces(ctx, f); err == nil {
		t.Error("an unknown filter was accepted")
	}
}

func TestStudioDryRunSample(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "flag", would: "flag", body: "live", prob: 1,
		levels: map[string]string{"groundedness@1": "warn", "contradiction@1": ""}})
	b := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "pass", would: "pass", body: "live", prob: 0.25})
	seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "pass", would: "pass", body: "none"})
	seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "pass", would: "pass", body: "expired"})
	seedStudioTrace(t, s, studioTrace{route: "faq", action: "pass", would: "pass", body: "live"})
	// a was audited HALLUCINATED.
	auditID := NewID()
	if err := s.SetTraceAudit(ctx, a, TraceAuditUpdate{AuditID: &auditID, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	audit := sampleAudit(auditID, 0.2, true)
	audit.TraceID, audit.Status, audit.Verdict, audit.ScoringVersion = a, AuditCompleted, strp(VerdictHallucinated), "v2"
	if err := s.SaveAuditResult(ctx, audit, AuditCompleted); err != nil {
		t.Fatal(err)
	}

	f := DryRunFilter{Route: "support-bot", Since: now.Add(-time.Hour), Now: now}
	pop, withBody, err := s.DryRunPopulation(ctx, f)
	if err != nil || pop != 4 || withBody != 2 {
		t.Fatalf("population %d, with body %d, err %v", pop, withBody, err)
	}
	got, err := s.DryRunSample(ctx, f, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("sample: %d, err %v", len(got), err)
	}
	byID := map[string]DryRunCandidate{}
	for _, c := range got {
		byID[c.TraceID] = c
	}
	ca, cb := byID[a], byID[b]
	if ca.AuditVerdict == nil || *ca.AuditVerdict != VerdictHallucinated || cb.AuditVerdict != nil {
		t.Errorf("audit verdicts: %v %v", ca.AuditVerdict, cb.AuditVerdict)
	}
	if l := ca.Levels["groundedness@1"]; l == nil || *l != "warn" || ca.Levels["contradiction@1"] != nil || len(cb.Levels) != 0 {
		t.Errorf("levels: %v %v", ca.Levels, cb.Levels)
	}
	if ca.Body.InclusionProb != 1 || cb.Body.InclusionProb != 0.25 || *cb.Body.ResponseText != "Within 60 days." || cb.Body.TraceID != b {
		t.Errorf("bodies: %+v %+v", ca.Body, cb.Body)
	}
	if got, _ := s.DryRunSample(ctx, f, 1); len(got) != 1 {
		t.Errorf("limit 1: %d", len(got))
	}
	if got, _ := s.DryRunSample(ctx, f, 0); len(got) != 0 {
		t.Errorf("limit 0: %d", len(got))
	}
}

func TestStudioRecordedCompile(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	id := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "flag", would: "flag", body: "live"})
	rc, err := s.GetRecordedCompile(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Route != "support-bot" || rc.PolicyVersion == nil || *rc.PolicyVersion != 3 || rc.CPT == nil || *rc.CPT != 3.7123456789 ||
		!slices.Equal(rc.RequestSHA256, []string{"aa", "bb"}) || rc.ModelRequested == nil || *rc.ModelRequested != "typesafe/jev-1.13" {
		t.Errorf("recorded = %+v", rc)
	}
	if _, err := s.GetRecordedCompile(ctx, id, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("attempt 1: %v", err)
	}
}

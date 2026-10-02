package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// seedGated records an attempt-0 decision of route at `at` whose evaluators map gives each ref its
// raw risk (and calibrated risk when set), with a body that expires at bodyExpires (no body when
// zero). It returns the trace id.
func seedGated(t *testing.T, s *PostgresStore, route string, at time.Time, results map[string][2]*float64, bodyExpires time.Time) string {
	t.Helper()
	ctx := context.Background()
	tr := pendingTrace(route, at)
	if err := s.StartTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	d, rec := decided(tr, "pass", "pass", "grounded", 0.1, nil)
	snap := liveSnapshot
	d.DecisionSnapshot, rec.DecisionSnapshot = &snap, &snap
	evs := map[string]any{}
	for ref, r := range results {
		res := map[string]any{"ref": ref, "risk_raw": nil, "risk": nil, "level": "ok"}
		if r[0] != nil {
			res["risk_raw"] = *r[0]
		}
		if r[1] != nil {
			res["risk"] = *r[1]
		}
		evs[ref] = res
	}
	rec.Decision, _ = json.Marshal(map[string]any{"spec": "decision/v1", "evaluators": evs})
	var b *BodyRecord
	if !bodyExpires.IsZero() {
		b = body(tr.TraceID, bodyExpires)
	}
	if err := s.RecordDecision(ctx, d, rec, b); err != nil {
		t.Fatal(err)
	}
	// The decision row is stamped when it is written; date it with the trace.
	if _, err := s.pool.Exec(ctx, `UPDATE gate_decisions SET created_at = $2 WHERE trace_id = $1`, tr.TraceID, at); err != nil {
		t.Fatal(err)
	}
	return tr.TraceID
}

func f64(v float64) *float64 { return &v }

func TestLabelUpsertDeleteAndViews(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := seedGated(t, s, "support-bot", now, map[string][2]*float64{"groundedness@1": {f64(0.62), nil}, "contradiction@1": {nil, nil}}, now.Add(time.Hour))

	lc, err := s.LabelContextOf(ctx, id, "groundedness@1")
	if err != nil || !lc.Decided || !lc.Asked || lc.RiskRaw == nil || *lc.RiskRaw != 0.62 || lc.Snapshot == nil || *lc.Snapshot != liveSnapshot {
		t.Fatalf("label context %+v %v", lc, err)
	}
	if lc, _ := s.LabelContextOf(ctx, id, "contradiction@1"); !lc.Asked || lc.RiskRaw != nil {
		t.Errorf("an asked evaluator without a raw risk: %+v", lc)
	}
	if lc, _ := s.LabelContextOf(ctx, id, "tone@1"); !lc.Decided || lc.Asked {
		t.Errorf("an evaluator the decision did not ask: %+v", lc)
	}
	if lc, _ := s.LabelContextOf(ctx, "no-such-trace", "groundedness@1"); lc.Decided {
		t.Errorf("a trace without a decision: %+v", lc)
	}

	b, err := s.GetTraceBody(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	sample, _ := json.Marshal(SampleOf(b))
	note := "the refund window is 30 days"
	l := LabelRecord{TraceID: id, Target: "groundedness@1", Label: LabelShouldFire, LabeledBy: "admin", Note: &note,
		Sample: sample, SampleExpiresAt: now.Add(90 * 24 * time.Hour), RiskRaw: lc.RiskRaw, Snapshot: lc.Snapshot}
	if err := s.UpsertLabel(ctx, l); err != nil {
		t.Fatal(err)
	}
	first, _ := s.TraceLabels(ctx, id)
	// The same label again changes nothing (idempotent).
	if err := s.UpsertLabel(ctx, l); err != nil {
		t.Fatal(err)
	}
	again, _ := s.TraceLabels(ctx, id)
	if len(again) != 1 || !again[0].CreatedAt.Equal(first[0].CreatedAt) || again[0].Label != LabelShouldFire || *again[0].Note != note {
		t.Fatalf("idempotent upsert: %+v then %+v", first, again)
	}
	var frozen LabelSample
	var risk float64
	var snap string
	if err := s.pool.QueryRow(ctx, `SELECT sample, risk_raw, snapshot FROM labels WHERE trace_id = $1`, id).Scan(&frozen, &risk, &snap); err != nil {
		t.Fatal(err)
	}
	if frozen.UserRequest == nil || *frozen.UserRequest != "Can I get a refund?" || !strings.Contains(string(frozen.Evidence), "refund-policy.md") ||
		risk < 0.6199 || risk > 0.6201 || snap != liveSnapshot {
		t.Errorf("frozen sample %+v risk %v snapshot %q", frozen, risk, snap)
	}
	// A second labeller and a changed label.
	l2 := l
	l2.LabeledBy, l2.Label, l2.Note = "reviewer", LabelShouldNotFire, nil
	if err := s.UpsertLabel(ctx, l2); err != nil {
		t.Fatal(err)
	}
	l.Label = LabelShouldNotFire
	if err := s.UpsertLabel(ctx, l); err != nil {
		t.Fatal(err)
	}
	views, _ := s.TraceLabels(ctx, id)
	if len(views) != 2 || views[0].LabeledBy != "admin" || views[0].Label != LabelShouldNotFire || views[1].LabeledBy != "reviewer" || views[1].Note != nil {
		t.Fatalf("labels %+v", views)
	}
	// Delete removes only the caller's row.
	if ok, err := s.DeleteLabel(ctx, id, "groundedness@1", "admin"); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, _ := s.DeleteLabel(ctx, id, "groundedness@1", "admin"); ok {
		t.Error("deleting twice reports a row")
	}
	if views, _ := s.TraceLabels(ctx, id); len(views) != 1 || views[0].LabeledBy != "reviewer" {
		t.Errorf("after delete %+v", views)
	}
	if views, err := s.TraceLabels(ctx, "no-such-trace"); err != nil || views == nil || len(views) != 0 {
		t.Errorf("no labels: %v %v", views, err)
	}
}

func TestCalibrationLabelCounts(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedLabel(t, s, "groundedness@1", LabelShouldFire, "human", f64(0.9), liveSnapshot, now.Add(-time.Hour))
	seedLabel(t, s, "groundedness@1", LabelShouldFire, "public_dataset", f64(0.8), liveSnapshot, now.Add(-time.Hour))
	seedLabel(t, s, "groundedness@1", LabelShouldNotFire, "human", f64(0.1), liveSnapshot, now.Add(-time.Hour))
	seedLabel(t, s, "groundedness@1", LabelShouldFire, "human", nil, liveSnapshot, now.Add(-time.Hour))            // no risk
	seedLabel(t, s, "groundedness@1", LabelShouldFire, "human", f64(0.7), "typesafe/jev-1.12-20260801", now)       // other snapshot
	seedLabel(t, s, "groundedness@1", LabelShouldFire, "human", f64(0.7), liveSnapshot, now.Add(-31*24*time.Hour)) // old
	seedLabel(t, s, "groundedness@2", LabelShouldFire, "human", f64(0.7), liveSnapshot, now.Add(-time.Hour))       // other version
	seedAudited(t, s, "groundedness@1", 0.7, "uniform", VerdictHallucinated, false, liveSnapshot)
	seedAudited(t, s, "groundedness@1", 0.2, "uniform", VerdictGrounded, true, liveSnapshot) // simulated
	f := LabelCountFilter{Target: "groundedness@1", Snapshot: liveSnapshot, From: now.Add(-30 * 24 * time.Hour), To: now.Add(time.Minute)}
	c, err := s.CalibrationLabelCounts(ctx, f)
	if err != nil || c != (LabelCounts{N: 3, Pos: 2, Neg: 1, Proxy: 1}) {
		t.Errorf("counts %+v %v", c, err)
	}
	f.IncludeMock = true
	if c, _ := s.CalibrationLabelCounts(ctx, f); c.Proxy != 2 {
		t.Errorf("with simulated traffic: %+v", c)
	}
	f.Snapshot = ""
	if c, _ := s.CalibrationLabelCounts(ctx, f); c.N != 4 {
		t.Errorf("every snapshot: %+v", c)
	}
	// The counts are exactly the samples a fit reads.
	samples, err := s.ReliabilitySamples(ctx, ReliabilityFilter{Evaluator: "groundedness@1", Snapshot: liveSnapshot,
		From: f.From, To: f.To, Labels: true, UniformAudits: true})
	if err != nil || len(samples) != 4 {
		t.Errorf("samples %d %v", len(samples), err)
	}
}

func TestLabelQueueOrder(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	live := now.Add(time.Hour)
	// support-bot overrides groundedness@1's warn_at to 0.6; elsewhere the spec's 0.8 applies.
	far := seedGated(t, s, "support-bot", now.Add(-time.Minute), map[string][2]*float64{"groundedness@1": {f64(0.05), nil}}, live)
	near := seedGated(t, s, "support-bot", now.Add(-2*time.Minute), map[string][2]*float64{"groundedness@1": {f64(0.58), nil}}, live)
	nearNewer := seedGated(t, s, "support-bot", now.Add(-time.Second), map[string][2]*float64{"groundedness@1": {f64(0.62), nil}}, live)
	other := seedGated(t, s, "billing", now.Add(-3*time.Minute), map[string][2]*float64{"groundedness@1": {f64(0.3), f64(0.79)}, "contradiction@1": {f64(0.5), nil}}, live)
	seedGated(t, s, "support-bot", now.Add(-time.Minute), map[string][2]*float64{"groundedness@1": {f64(0.6), nil}}, now.Add(-time.Second)) // body expired
	seedGated(t, s, "support-bot", now.Add(-time.Minute), map[string][2]*float64{"groundedness@1": {f64(0.6), nil}}, time.Time{})           // no body
	seedGated(t, s, "support-bot", now.Add(-time.Minute), map[string][2]*float64{"groundedness@1": {nil, nil}}, live)                       // no risk
	seedGated(t, s, "support-bot", now.Add(-10*24*time.Hour), map[string][2]*float64{"groundedness@1": {f64(0.6), nil}}, live)              // outside the window
	labelled := seedGated(t, s, "support-bot", now.Add(-time.Minute), map[string][2]*float64{"groundedness@1": {f64(0.6), nil}}, live)
	if err := s.UpsertLabel(ctx, LabelRecord{TraceID: labelled, Target: "groundedness@1", Label: LabelShouldFire, LabeledBy: "admin",
		SampleExpiresAt: live}); err != nil {
		t.Fatal(err)
	}
	f := LabelQueueFilter{
		LabeledBy: "admin", From: now.Add(-7 * 24 * time.Hour), To: now.Add(time.Minute), Limit: 50, Now: now,
		WarnAt:        map[string]map[string]float64{"support-bot": {"groundedness@1": 0.6}},
		DefaultWarnAt: map[string]float64{"groundedness@1": 0.8, "contradiction@1": 0.8},
	}
	rows, err := s.LabelQueue(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, r.TraceID+" "+r.EvaluatorRef)
	}
	// |0.62-0.6| ties |0.58-0.6|: newest first. billing's calibrated 0.79 is 0.01 from the spec's
	// 0.8 (risk in the route's space), contradiction's 0.5 is 0.3 away, and the 0.05 is 0.55 away.
	want := []string{other + " groundedness@1", nearNewer + " groundedness@1", near + " groundedness@1",
		other + " contradiction@1", far + " groundedness@1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queue order\n got %v\nwant %v", got, want)
	}
	if r := rows[1]; r.Route != "support-bot" || r.RiskRaw != 0.62 || r.Level == nil || *r.Level != "ok" || r.UserRequest == nil ||
		r.ResponseText == nil || !strings.Contains(string(r.Evidence), "refund-policy.md") {
		t.Errorf("row %+v", r)
	}
	// Another admin still sees the labelled trace; a filter on one evaluator and the limit apply.
	f.LabeledBy, f.Evaluator, f.Limit = "reviewer", "contradiction@1", 1
	if rows, _ := s.LabelQueue(ctx, f); len(rows) != 1 || rows[0].TraceID != other {
		t.Errorf("one evaluator: %+v", rows)
	}
	f.Evaluator, f.Limit = "groundedness@1", 50
	if rows, _ := s.LabelQueue(ctx, f); len(rows) != 5 {
		t.Errorf("another admin sees %d rows, want 5 (the labelled trace included)", len(rows))
	}
}

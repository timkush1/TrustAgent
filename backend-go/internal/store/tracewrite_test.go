package store

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// comparable returns rec with what a memory copy cannot match exactly cleared: the timestamps
// (the database sets updated_at) and the evidence summary (jsonb reorders keys), which the caller
// compares as JSON.
func comparableTrace(t *testing.T, rec TraceRecord) (TraceRecord, any) {
	t.Helper()
	var summary any
	if rec.EvidenceSummary != nil {
		if err := json.Unmarshal(rec.EvidenceSummary, &summary); err != nil {
			t.Fatal(err)
		}
	}
	rec.CreatedAt, rec.UpdatedAt, rec.EvidenceSummary = time.Time{}, time.Time{}, nil
	return rec, summary
}

// TestTraceStoredAndMergeDecidedMatchPostgres: the Go form of a trace write (Stored, MergeDecided)
// reads back exactly as Postgres stores it, for a decision over a started trace, a decision with
// no start, and a decision after a late decision (the flag stays).
func TestTraceStoredAndMergeDecidedMatchPostgres(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	check := func(name string, want TraceRecord, id string) {
		t.Helper()
		got, err := s.GetTrace(ctx, id)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		g, gs := comparableTrace(t, *got)
		w, ws := comparableTrace(t, want)
		g.Audit = AuditJoin{}
		if !reflect.DeepEqual(g, w) || !reflect.DeepEqual(gs, ws) {
			t.Errorf("%s:\n database %+v\n memory   %+v", name, g, w)
		}
	}

	// Started, then decided: the decision leaves the question preview and model unset.
	start := pendingTrace("support-bot", now.Add(-time.Minute))
	if err := s.StartTrace(ctx, start); err != nil {
		t.Fatal(err)
	}
	check("started", start.Stored(now), start.TraceID)
	tr, dec := decided(start, "flag", "block", "contradicted", 0.98, []string{"contradiction:critical"})
	tr.QuestionPreview, tr.Model, tr.Scenario, tr.KeyLabel = nil, nil, strp("other"), strp("other")
	tr.AuditID, tr.AuditStatus, tr.AuditLane = strp("a-1"), "queued", strp("high")
	if err := s.RecordDecision(ctx, tr, dec, nil); err != nil {
		t.Fatal(err)
	}
	stored, decidedRec := start.Stored(now), tr.Stored(now)
	check("decided over started", MergeDecided(&stored, &decidedRec), start.TraceID)

	// Decided with no start: the decided trace as it is.
	only := pendingTrace("support-bot", now)
	tr2, dec2 := decided(only, "pass", "pass", "supported", 0.1, nil)
	if err := s.RecordDecision(ctx, tr2, dec2, nil); err != nil {
		t.Fatal(err)
	}
	check("decided only", tr2.Stored(now), only.TraceID)

	// A late decision before the decision write: the flag stays through the decision.
	late := pendingTrace("support-bot", now)
	if err := s.StartTrace(ctx, late); err != nil {
		t.Fatal(err)
	}
	ld := &DecisionRecord{TraceID: late.TraceID, Route: "support-bot", Mode: "flag", Status: "ok", Action: "block",
		Late: true, Calibration: "none", Decision: json.RawMessage(`{"late":true}`)}
	if err := s.RecordLateDecision(ctx, ld); err != nil {
		t.Fatal(err)
	}
	tr3, dec3 := decided(late, "flag", "flag", "unsupported", 0.5, nil)
	if err := s.RecordDecision(ctx, tr3, dec3, nil); err != nil {
		t.Fatal(err)
	}
	startLate := late.Stored(now)
	startLate.Flags = append(startLate.Flags, FlagLateDecision)
	decLate := tr3.Stored(now)
	merged := MergeDecided(&startLate, &decLate)
	if !slices.Contains(merged.Flags, FlagLateDecision) {
		t.Fatalf("MergeDecided dropped the late flag: %v", merged.Flags)
	}
	check("late flag kept", merged, late.TraceID)
	docs, err := s.TraceDecisions(ctx, late.TraceID)
	if err != nil || len(docs) != 1 || !strings.Contains(string(docs[0]), `"late"`) {
		t.Errorf("the late decision stays over a later attempt-0 write: %s, %v", docs, err)
	}
}

// TestTraceWriteBatch: a batch writes exactly what the methods write one by one, in order and in
// one transaction; a batch with a write Postgres refuses writes nothing.
func TestTraceWriteBatch(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	a, b := pendingTrace("support-bot", now.Add(-2*time.Second)), pendingTrace("default", now.Add(-time.Second))
	trA, decA := decided(a, "flag", "block", "contradicted", 0.98, []string{"contradiction:critical"})
	trB, decB := decided(b, "pass", "pass", "supported", 0.06, nil)
	attempt := *decA
	attempt.Attempt, attempt.Action, attempt.Decision = 1, "pass", json.RawMessage(`{"attempt":1}`)
	late := *decB
	late.Late, late.Action, late.Decision = true, "block", json.RawMessage(`{"late":true}`)
	writes := []TraceWrite{
		{Kind: WriteStart, Trace: a},
		{Kind: WriteStart, Trace: b},
		{Kind: WriteDecision, Trace: trA, Decision: decA, Body: body(a.TraceID, now.Add(time.Hour))},
		{Kind: WriteAttempt, Decision: &attempt},
		{Kind: WriteDecision, Trace: trB, Decision: decB},
		{Kind: WriteLateDecision, Decision: &late},
	}
	if err := s.WriteTraceBatch(ctx, writes); err != nil {
		t.Fatal(err)
	}
	gotA, err := s.GetTrace(ctx, a.TraceID)
	if err != nil || gotA.GateStatus != "ok" || *gotA.Action != "flag" || *gotA.QuestionPreview != *a.QuestionPreview {
		t.Fatalf("trace a %+v, %v", gotA, err)
	}
	docsA, _ := s.TraceDecisions(ctx, a.TraceID)
	if len(docsA) != 2 || !strings.Contains(string(docsA[1]), `"attempt": 1`) {
		t.Errorf("trace a decisions %s", docsA)
	}
	if bd, err := s.GetTraceBody(ctx, a.TraceID); err != nil || *bd.ResponseText != "Within 60 days." {
		t.Errorf("trace a body %+v, %v", bd, err)
	}
	gotB, _ := s.GetTrace(ctx, b.TraceID)
	docsB, _ := s.TraceDecisions(ctx, b.TraceID)
	if !slices.Contains(gotB.Flags, FlagLateDecision) || len(docsB) != 1 || !strings.Contains(string(docsB[0]), `"late"`) {
		t.Errorf("trace b flags %v decisions %s", gotB.Flags, docsB)
	}

	// One refused write (a decision document that is not JSON) rolls the whole batch back.
	c, d := pendingTrace("support-bot", now), pendingTrace("support-bot", now)
	trD, decD := decided(d, "pass", "pass", "supported", 0.1, nil)
	decD.Decision = json.RawMessage(`{not json`)
	err = s.WriteTraceBatch(ctx, []TraceWrite{{Kind: WriteStart, Trace: c}, {Kind: WriteDecision, Trace: trD, Decision: decD}})
	if err == nil || !strings.Contains(err.Error(), "insert gate decision") {
		t.Fatalf("a refused write: %v", err)
	}
	if _, err := s.GetTrace(ctx, c.TraceID); err != ErrNotFound {
		t.Errorf("the batch was not rolled back: %v", err)
	}
	// A write its method would refuse is refused before anything is sent.
	if err := s.WriteTraceBatch(ctx, []TraceWrite{{Kind: WriteAttempt, Decision: &DecisionRecord{TraceID: "x"}}}); err == nil {
		t.Error("an attempt-0 later attempt was accepted")
	}
	if err := s.WriteTraceBatch(ctx, nil); err != nil {
		t.Errorf("an empty batch: %v", err)
	}
}

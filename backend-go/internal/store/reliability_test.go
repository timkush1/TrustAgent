package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

const liveSnapshot = "typesafe/jev-1.13-20260917"

// seedLabel inserts a labels row (the labels API arrives in P4).
func seedLabel(t *testing.T, s *PostgresStore, target, label, source string, risk *float64, snapshot string, at time.Time) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `
		INSERT INTO labels (trace_id, target, label, labeled_by, created_at, label_source, risk_raw, snapshot)
		VALUES ($1, $2, $3, 'reviewer', $4, $5, $6, $7)`, NewID(), target, label, at, source, risk, snapshot); err != nil {
		t.Fatal(err)
	}
}

// seedAudited records a gated trace whose decision gave evaluator ref the raw risk, and its deep
// audit with selection and verdict. Simulated traces carry the test-mode and mock flags.
func seedAudited(t *testing.T, s *PostgresStore, ref string, risk float64, selection, verdict string, simulated bool, snapshot string) {
	t.Helper()
	ctx := context.Background()
	tr := pendingTrace("support-bot", time.Now().UTC())
	tr.Source, tr.Flags = "proxy", []string{}
	if simulated {
		tr.Flags = []string{"test_mode"}
	}
	if err := s.StartTrace(ctx, tr); err != nil {
		t.Fatal(err)
	}
	d, rec := decided(tr, "flag", "block", "contradicted", risk, nil)
	d.Flags, d.DecisionBackend, d.DecisionSnapshot = tr.Flags, strp("openrouter-decisions"), &snapshot
	rec.DecisionSnapshot = &snapshot
	rec.Decision, _ = json.Marshal(map[string]any{"spec": "decision/v1", "evaluators": map[string]any{
		ref:             map[string]any{"ref": ref, "risk_raw": risk, "level": "warn"},
		"other_check@1": map[string]any{"ref": "other_check@1", "risk_raw": 0.99, "level": "critical"},
	}})
	auditID := NewID()
	d.AuditID, d.AuditStatus, d.AuditSelection = &auditID, "queued", &selection
	if err := s.RecordDecision(ctx, d, rec, nil); err != nil {
		t.Fatal(err)
	}
	a := sampleAudit(auditID, 0.5, verdict == VerdictHallucinated)
	a.TraceID, a.Selection, a.Status, a.Verdict, a.ScoringVersion = tr.TraceID, &selection, AuditCompleted, &verdict, "v2"
	if err := s.SaveAuditResult(ctx, a, AuditCompleted); err != nil {
		t.Fatal(err)
	}
}

func describe(samples []ReliabilitySample) string {
	out := make([]string, len(samples))
	for i, smp := range samples {
		out[i] = fmt.Sprintf("%s:%.2f:%t", smp.Source, smp.Risk, smp.Fire)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestReliabilitySamples(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	r := func(v float64) *float64 { return &v }

	seedLabel(t, s, "groundedness@1", "should_fire", "human", r(0.91), liveSnapshot, now.Add(-time.Hour))
	seedLabel(t, s, "groundedness@1", "should_not_fire", "human", r(0.12), liveSnapshot, now.Add(-2*time.Hour))
	seedLabel(t, s, "groundedness@1", "should_fire", "public_dataset", r(0.55), liveSnapshot, now.Add(-3*time.Hour))
	seedLabel(t, s, "groundedness@2", "should_not_fire", "human", r(0.30), liveSnapshot, now.Add(-time.Hour))
	seedLabel(t, s, "groundedness@1", "should_fire", "human", r(0.80), "typesafe/jev-1.12-20260801", now.Add(-time.Hour))
	seedLabel(t, s, "groundedness@1", "should_fire", "human", nil, liveSnapshot, now.Add(-time.Hour))           // no risk
	seedLabel(t, s, "groundedness@1", "should_fire", "human", r(0.70), liveSnapshot, now.Add(-30*24*time.Hour)) // old
	seedLabel(t, s, "contradiction@1", "should_fire", "human", r(0.95), liveSnapshot, now.Add(-time.Hour))      // other evaluator
	seedLabel(t, s, "claim:0", "CONTRADICTED", "human", r(0.95), liveSnapshot, now.Add(-time.Hour))             // a claim label
	seedLabel(t, s, "response", "hallucinated", "human", r(0.95), liveSnapshot, now.Add(-time.Hour))            // a response label

	seedAudited(t, s, "groundedness@1", 0.83, "uniform", VerdictHallucinated, false, liveSnapshot)
	seedAudited(t, s, "groundedness@1", 0.08, "uniform", VerdictGrounded, false, liveSnapshot)
	seedAudited(t, s, "groundedness@1", 0.40, "uniform", VerdictUnverifiable, false, liveSnapshot) // no label
	seedAudited(t, s, "groundedness@1", 0.66, "high", VerdictHallucinated, false, liveSnapshot)    // selected by risk
	seedAudited(t, s, "groundedness@1", 0.77, "uniform", VerdictHallucinated, true, liveSnapshot)  // simulated
	seedAudited(t, s, "groundedness@1", 0.22, "uniform", VerdictGrounded, false, "mock/jev-heuristic-v1")

	week := ReliabilityFilter{Evaluator: "groundedness@1", From: now.Add(-7 * 24 * time.Hour), To: now.Add(time.Minute),
		Snapshot: liveSnapshot, Labels: true, UniformAudits: true}
	for name, tc := range map[string]struct {
		f    func(ReliabilityFilter) ReliabilityFilter
		want string
	}{
		"all sources": {func(f ReliabilityFilter) ReliabilityFilter { return f },
			"human:0.12:false human:0.91:true public_dataset:0.55:true uniform_audit:0.08:false uniform_audit:0.83:true"},
		"labels only": {func(f ReliabilityFilter) ReliabilityFilter { f.UniformAudits = false; return f },
			"human:0.12:false human:0.91:true public_dataset:0.55:true"},
		"uniform audits only": {func(f ReliabilityFilter) ReliabilityFilter { f.Labels = false; return f },
			"uniform_audit:0.08:false uniform_audit:0.83:true"},
		"with simulated traffic": {func(f ReliabilityFilter) ReliabilityFilter { f.Labels, f.IncludeMock = false, true; return f },
			"uniform_audit:0.08:false uniform_audit:0.77:true uniform_audit:0.83:true"},
		"every version": {func(f ReliabilityFilter) ReliabilityFilter {
			f.Evaluator, f.UniformAudits = "groundedness", false
			return f
		},
			"human:0.12:false human:0.30:false human:0.91:true public_dataset:0.55:true"},
		"every snapshot": {func(f ReliabilityFilter) ReliabilityFilter { f.Snapshot = ""; return f },
			"human:0.12:false human:0.80:true human:0.91:true public_dataset:0.55:true uniform_audit:0.08:false uniform_audit:0.22:false uniform_audit:0.83:true"},
		"another evaluator": {func(f ReliabilityFilter) ReliabilityFilter { f.Evaluator = "contradiction@1"; return f },
			"human:0.95:true"},
		"nothing asked": {func(f ReliabilityFilter) ReliabilityFilter { f.Labels, f.UniformAudits = false, false; return f }, ""},
	} {
		got, err := s.ReliabilitySamples(ctx, tc.f(week))
		if err != nil || describe(got) != tc.want {
			t.Errorf("%s:\n got  %s (%v)\n want %s", name, describe(got), err, tc.want)
		}
	}
}

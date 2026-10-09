package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

// An audit a mock judge gave flags its trace mock_judge (simulated, D39): the trace is labelled
// Simulated and stays out of the measured figures; a real judge's audit flags nothing, and a
// second save never adds the flag twice.
func TestSaveAuditResultFlagsAMockJudge(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	for _, c := range []struct {
		id, provider string
		want         bool
	}{{"tr-mock-judge", "mock", true}, {"tr-real-judge", "openrouter", false}} {
		if err := s.StartTrace(ctx, &TraceRecord{TraceID: c.id, Source: "proxy", APIFormat: "openai_chat", Route: "default", Mode: "observe"}); err != nil {
			t.Fatal(err)
		}
		judge, _ := json.Marshal(map[string]any{"configured_provider": c.provider, "effective_provider": c.provider, "model": "m"})
		rec := &AuditRecord{AuditID: c.id + "-a", TraceID: c.id, Source: "proxy", Status: AuditCompleted,
			Verdict: strp(VerdictGrounded), ScoringVersion: "v2", Judge: judge}
		if rec.MockJudge() != c.want {
			t.Fatalf("%s: MockJudge() = %v", c.id, rec.MockJudge())
		}
		for range 2 {
			if err := s.SaveAuditResult(ctx, rec, AuditCompleted); err != nil && err != ErrDuplicate {
				t.Fatal(err)
			}
		}
		got, err := s.GetTrace(ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, f := range got.Flags {
			if f == FlagMockJudge {
				n++
			}
		}
		if (n == 1) != c.want || n > 1 {
			t.Errorf("%s: flags %v", c.id, got.Flags)
		}
		if c.want && !slices.Contains(got.Flags, FlagMockJudge) {
			t.Errorf("%s: no mock_judge flag", c.id)
		}
	}
	if (&AuditRecord{}).MockJudge() || (&AuditRecord{Judge: json.RawMessage(`{`)}).MockJudge() {
		t.Error("an audit without a readable judge is not a mock judge's")
	}
}

package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestSuggestNoEvidenceBodies(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	noEvidence := func(id string) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `UPDATE traces SET evidence_summary = '{"configured":["request_system"],"used":[],"empty":["request_system"]}' WHERE trace_id = $1`, id); err != nil {
			t.Fatal(err)
		}
	}
	older := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "pass", would: "pass", body: "live", at: now.Add(-2 * time.Minute)})
	newer := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "pass", would: "pass", body: "live", at: now.Add(-time.Minute)})
	other := seedStudioTrace(t, s, studioTrace{route: "faq", action: "pass", would: "pass", body: "live", at: now.Add(-time.Minute)})
	withEvidence := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "pass", would: "pass", body: "live"})
	expired := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "pass", would: "pass", body: "expired"})
	direct := seedStudioTrace(t, s, studioTrace{route: "support-bot", action: "pass", would: "pass", body: "live"})
	for _, id := range []string{older, newer, other, expired, direct} {
		noEvidence(id)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE traces SET source = 'direct' WHERE trace_id = $1`, direct); err != nil {
		t.Fatal(err)
	}
	// The traces are dated by their trace time.
	for id, at := range map[string]time.Time{older: now.Add(-2 * time.Minute), newer: now.Add(-time.Minute)} {
		if _, err := s.pool.Exec(ctx, `UPDATE traces SET created_at = $1 WHERE trace_id = $2`, at, id); err != nil {
			t.Fatal(err)
		}
	}

	f := ScanFilter{Route: "support-bot", Since: now.Add(-time.Hour), Limit: 10_000, Now: now}
	got, err := s.NoEvidenceBodies(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(got))
	for i, b := range got {
		ids[i] = b.TraceID
	}
	if !slices.Equal(ids, []string{newer, older}) {
		t.Fatalf("bodies = %v, want %s,%s", ids, newer, older)
	}
	if got[0].Route != "support-bot" || len(got[0].Messages) == 0 || got[0].Messages[0] != '[' {
		t.Errorf("body = %+v", got[0])
	}
	f.Route = ""
	if all, _ := s.NoEvidenceBodies(ctx, f); len(all) != 3 || slices.ContainsFunc(all, func(b ScanBody) bool { return b.TraceID == withEvidence }) {
		t.Errorf("every route: %+v", all)
	}
	f.Limit = 1
	if one, _ := s.NoEvidenceBodies(ctx, f); len(one) != 1 {
		t.Errorf("limit 1: %d", len(one))
	}
	if MaxScanBodies != 500 {
		t.Errorf("the scan cap is %d, D64 says 500", MaxScanBodies)
	}
}

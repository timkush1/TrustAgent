package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
)

func dashboardVersion(id string, v int) *EvaluatorVersionRecord {
	return &EvaluatorVersionRecord{
		EvaluatorID: id, Version: v, Spec: json.RawMessage(`{"id":"` + id + `","version":` + strconv.Itoa(v) + `}`),
		SpecSHA256: "s" + strconv.Itoa(v), QuestionSHA256: "q" + strconv.Itoa(v), CalibrationKey: "k" + strconv.Itoa(v),
		CreatedBy: "admin", Changelog: "v" + strconv.Itoa(v),
	}
}

func auditActions(t *testing.T, s *PostgresStore) []string {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT action || ' ' || coalesce(target, '') FROM admin_audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestEvaluatorWrites(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	audit := func(action, target string) AdminAuditEntry {
		return AdminAuditEntry{Actor: "admin", Action: action, Target: target, Diff: map[string]any{"x": 1}}
	}
	if _, err := s.UpsertEvaluatorVersions(ctx, "", []EvaluatorUpsert{
		{Evaluator: EvaluatorRecord{ID: "groundedness", Name: "G", Category: "grounding", Origin: EvaluatorOriginBuiltin}, Version: *dashboardVersion("groundedness", 1)},
		{Evaluator: EvaluatorRecord{ID: "house_style", Name: "H", Category: "quality", Origin: EvaluatorOriginFile}, Version: *dashboardVersion("house_style", 1)},
	}); err != nil {
		t.Fatal(err)
	}

	v1 := dashboardVersion("tone", 1)
	if err := s.CreateEvaluator(ctx, "", EvaluatorRecord{ID: "tone", Name: "Tone", Category: "quality"}, v1, audit("evaluator.create", "tone@1")); err != nil {
		t.Fatal(err)
	}
	if v1.CreatedAt.IsZero() {
		t.Error("created_at not filled in")
	}
	for _, id := range []string{"tone", "groundedness", "house_style"} {
		err := s.CreateEvaluator(ctx, "", EvaluatorRecord{ID: id, Name: "x", Category: "quality"}, dashboardVersion(id, 1), audit("evaluator.create", id))
		if !errors.Is(err, ErrEvaluatorExists) {
			t.Errorf("create %s again: %v", id, err)
		}
	}

	// New versions: base must be the latest (compare and swap), built-in and file ones refused.
	if err := s.CreateEvaluatorVersion(ctx, "", 1, "Tone 2", "safety", dashboardVersion("tone", 2), audit("evaluator.version", "tone@2")); err != nil {
		t.Fatal(err)
	}
	var stale *StaleEvaluatorError
	if err := s.CreateEvaluatorVersion(ctx, "", 1, "Tone", "quality", dashboardVersion("tone", 2), audit("evaluator.version", "tone@2")); !errors.As(err, &stale) || stale.Latest != 2 {
		t.Errorf("stale base: %v", err)
	}
	if err := s.CreateEvaluatorVersion(ctx, "", 1, "G", "grounding", dashboardVersion("groundedness", 2), audit("x", "")); !errors.Is(err, ErrEvaluatorBuiltin) {
		t.Errorf("built-in: %v", err)
	}
	if err := s.CreateEvaluatorVersion(ctx, "", 1, "H", "quality", dashboardVersion("house_style", 2), audit("x", "")); !errors.Is(err, ErrEvaluatorFileManaged) {
		t.Errorf("file: %v", err)
	}
	if err := s.CreateEvaluatorVersion(ctx, "", 1, "N", "quality", dashboardVersion("nope", 2), audit("x", "")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if err := s.CreateEvaluatorVersion(ctx, "", 2, "Tone", "quality", dashboardVersion("tone", 4), audit("x", "")); err == nil {
		t.Error("a version that is not base+1 must be refused")
	}
	evs, _ := s.ListEvaluators(ctx)
	var tone EvaluatorRecord
	for _, e := range evs {
		if e.ID == "tone" {
			tone = e
		}
	}
	if tone.Origin != EvaluatorOriginDashboard || tone.LatestVersion != 2 || tone.Name != "Tone 2" || tone.Category != "safety" {
		t.Errorf("tone row %+v", tone)
	}

	// Archive and restore; setting the same flag again writes nothing.
	rec, err := s.SetEvaluatorArchived(ctx, "", "tone", true, audit("evaluator.archive", "tone"))
	if err != nil || !rec.Archived {
		t.Fatalf("archive: %+v %v", rec, err)
	}
	if rec, err = s.SetEvaluatorArchived(ctx, "", "tone", true, audit("evaluator.archive", "tone")); err != nil || !rec.Archived {
		t.Fatalf("archive again: %+v %v", rec, err)
	}
	if rec, err = s.SetEvaluatorArchived(ctx, "", "tone", false, audit("evaluator.archive", "tone")); err != nil || rec.Archived {
		t.Fatalf("restore: %+v %v", rec, err)
	}
	if _, err := s.SetEvaluatorArchived(ctx, "", "nope", true, audit("evaluator.archive", "nope")); !errors.Is(err, ErrNotFound) {
		t.Errorf("archive unknown: %v", err)
	}
	// Every write that changed something has its audit row; refused writes have none.
	want := []string{"evaluator.create tone@1", "evaluator.version tone@2", "evaluator.archive tone", "evaluator.archive tone"}
	if got := auditActions(t, s); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("audit rows %q, want %q", got, want)
	}
	// Stored versions stay immutable (migration 0003 trigger), dashboard ones included.
	if _, err := s.pool.Exec(ctx, `UPDATE evaluator_versions SET changelog = 'x' WHERE evaluator_id = 'tone'`); err == nil {
		t.Error("a dashboard version was updated in place")
	}
}

// TestEvaluatorVersionCAS: of writers that race from the same base, exactly one saves.
func TestEvaluatorVersionCAS(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	if err := s.CreateEvaluator(ctx, "", EvaluatorRecord{ID: "tone", Name: "Tone", Category: "quality"}, dashboardVersion("tone", 1),
		AdminAuditEntry{Actor: "a", Action: "evaluator.create"}); err != nil {
		t.Fatal(err)
	}
	const writers = 6
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := dashboardVersion("tone", 2)
			v.Changelog = "writer " + strconv.Itoa(i)
			errs[i] = s.CreateEvaluatorVersion(ctx, "", 1, "Tone", "quality", v, AdminAuditEntry{Actor: "a", Action: "evaluator.version"})
		}()
	}
	wg.Wait()
	saved := 0
	for _, err := range errs {
		var stale *StaleEvaluatorError
		switch {
		case err == nil:
			saved++
		case !errors.As(err, &stale):
			t.Errorf("unexpected error %v", err)
		}
	}
	if saved != 1 {
		t.Errorf("%d writers saved version 2, want exactly 1", saved)
	}
	vers, _ := s.ListEvaluatorVersions(ctx)
	if len(vers) != 2 {
		t.Errorf("%d versions stored", len(vers))
	}
}

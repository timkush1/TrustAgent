package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

const calSnapshot = "typesafe/jev-1.13-20260917"

func calRecord(origin, key, quality string, fileSHA *string) *CalibrationRecord {
	return &CalibrationRecord{
		CreatedBy: "admin", Origin: origin, Backend: "openrouter-decisions", Snapshot: calSnapshot, Family: "typesafe/jev-1.13",
		TargetKind: "evaluator", TargetKey: key, Quality: quality, LabelSource: json.RawMessage(`{"human":10}`),
		Entry: json.RawMessage(`{"quality":"` + quality + `"}`), FileSHA256: fileSHA,
	}
}

// activeIDs returns the ids of the active rows of a key.
func activeIDs(t *testing.T, s *PostgresStore, key string) []int64 {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT id FROM calibrations WHERE target_key = $1 AND active ORDER BY id`, key)
	if err != nil {
		t.Fatal(err)
	}
	var out []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

func TestCalibrationFileMirrorAndOverrides(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const key = "groundedness@1#0123456789abcdef#-"
	audit := AdminAuditEntry{Actor: "admin", Action: "calibration.fit"}

	// A file is mirrored once: its row is active, and mirroring the same content adds nothing.
	fileA := calRecord("", key, "measured", strp("aaa"))
	if n, err := s.MirrorCalibrationFile(ctx, "", []*CalibrationRecord{fileA}); err != nil || n != 1 {
		t.Fatalf("mirror: %d %v", n, err)
	}
	if n, err := s.MirrorCalibrationFile(ctx, "", []*CalibrationRecord{calRecord("", key, "measured", strp("aaa"))}); err != nil || n != 0 {
		t.Fatalf("mirror again: %d %v", n, err)
	}
	if got := activeIDs(t, s, key); len(got) != 1 || got[0] != fileA.ID {
		t.Fatalf("active after mirror %v, want [%d]", got, fileA.ID)
	}
	// A changed file replaces the active file row.
	fileB := calRecord("", key, "measured", strp("bbb"))
	if _, err := s.MirrorCalibrationFile(ctx, "", []*CalibrationRecord{fileB}); err != nil {
		t.Fatal(err)
	}
	if got := activeIDs(t, s, key); len(got) != 1 || got[0] != fileB.ID {
		t.Fatalf("active after a changed file %v, want [%d]", got, fileB.ID)
	}

	// A fit stored inactive changes nothing; activating it swaps it in (DB beats file).
	fit := calRecord(CalibrationOriginFit, key, "provisional", nil)
	if err := s.InsertCalibrations(ctx, "", []*CalibrationRecord{fit}, false, audit); err != nil {
		t.Fatal(err)
	}
	if fit.Active || len(activeIDs(t, s, key)) != 1 || activeIDs(t, s, key)[0] != fileB.ID {
		t.Fatalf("an inactive fit took over: %+v %v", fit, activeIDs(t, s, key))
	}
	rec, changed, err := s.SetCalibrationActive(ctx, "", fit.ID, true, AdminAuditEntry{Actor: "admin", Action: "calibration.activate"})
	if err != nil || !changed || !rec.Active {
		t.Fatalf("activate: %+v %v %v", rec, changed, err)
	}
	if got := activeIDs(t, s, key); len(got) != 1 || got[0] != fit.ID {
		t.Fatalf("active after activation %v, want [%d]", got, fit.ID)
	}
	// While the fit is active, a changed file is mirrored inactive.
	fileC := calRecord("", key, "measured", strp("ccc"))
	if _, err := s.MirrorCalibrationFile(ctx, "", []*CalibrationRecord{fileC}); err != nil {
		t.Fatal(err)
	}
	if got := activeIDs(t, s, key); len(got) != 1 || got[0] != fit.ID {
		t.Fatalf("a file overrode an active fit: %v", got)
	}
	// Activating again is a no-op; deactivating the fit brings back the newest file row.
	if _, changed, _ := s.SetCalibrationActive(ctx, "", fit.ID, true, audit); changed {
		t.Error("activating an active row reports a change")
	}
	if _, changed, err := s.SetCalibrationActive(ctx, "", fit.ID, false, AdminAuditEntry{Actor: "admin", Action: "calibration.deactivate"}); err != nil || !changed {
		t.Fatalf("deactivate: %v %v", changed, err)
	}
	if got := activeIDs(t, s, key); len(got) != 1 || got[0] != fileC.ID {
		t.Fatalf("active after deactivating the fit %v, want the newest file row %d", got, fileC.ID)
	}
	// Deactivating a file row leaves the key uncalibrated.
	if _, _, err := s.SetCalibrationActive(ctx, "", fileC.ID, false, audit); err != nil {
		t.Fatal(err)
	}
	if got := activeIDs(t, s, key); len(got) != 0 {
		t.Errorf("active after deactivating the file row: %v", got)
	}
	if _, _, err := s.SetCalibrationActive(ctx, "", 999999, true, audit); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}

	// Reads.
	all, err := s.ListCalibrations(ctx, CalibrationFilter{})
	if err != nil || len(all) != 4 || all[0].ID != fileC.ID || all[0].Entry != nil {
		t.Fatalf("list %+v %v", all, err)
	}
	inactive := false
	if got, _ := s.ListCalibrations(ctx, CalibrationFilter{Active: &inactive}); len(got) != 4 {
		t.Errorf("inactive rows %d", len(got))
	}
	if got, _ := s.ListCalibrations(ctx, CalibrationFilter{Snapshot: "other/snapshot"}); len(got) != 0 {
		t.Errorf("another snapshot %d", len(got))
	}
	full, err := s.GetCalibration(ctx, fit.ID)
	if err != nil || full.Origin != CalibrationOriginFit || full.CreatedBy != "admin" || string(full.Entry) != `{"quality": "provisional"}` {
		t.Errorf("get %+v %v", full, err)
	}
	if _, err := s.GetCalibration(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("get unknown: %v", err)
	}
	if got, _ := s.CalibrationsByID(ctx, []int64{fit.ID, fileA.ID, 999999}); len(got) != 2 || got[0].ID != fileA.ID || got[1].Entry == nil {
		t.Errorf("by id %+v", got)
	}
	// One audit row per write that changed something; mirroring writes none.
	want := []string{"calibration.fit ", "calibration.activate ", "calibration.deactivate ", "calibration.fit "}
	if got := auditActions(t, s); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Errorf("audit rows %q, want %q", got, want)
	}
}

// TestCalibrationActivationRace: concurrent activations of the rows of one key leave exactly one
// active row and never trip the unique index.
func TestCalibrationActivationRace(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	const key = "contradiction@1#0123456789abcdef#-"
	var recs []*CalibrationRecord
	for range 6 {
		recs = append(recs, calRecord(CalibrationOriginImport, key, "measured", nil))
	}
	if err := s.InsertCalibrations(ctx, "", recs, false, AdminAuditEntry{Actor: "admin", Action: "calibration.import"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(recs))
	for i, r := range recs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = s.SetCalibrationActive(ctx, "", r.ID, true, AdminAuditEntry{Actor: "admin", Action: "calibration.activate"})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Errorf("activation failed: %v", err)
		}
	}
	if got := activeIDs(t, s, key); len(got) != 1 {
		t.Errorf("%d active rows, want 1", len(got))
	}
	// The index refuses a second active row written around the store.
	if _, err := s.pool.Exec(ctx, `UPDATE calibrations SET active = true WHERE target_key = $1`, key); err == nil {
		t.Error("the unique index allowed two active rows")
	}
	// Importing with activate swaps in the last of the batch.
	batch := []*CalibrationRecord{calRecord(CalibrationOriginImport, key, "measured", nil), calRecord(CalibrationOriginImport, "other@1#0123456789abcdef#-", "measured", nil)}
	if err := s.InsertCalibrations(ctx, "", batch, true, AdminAuditEntry{Actor: "admin", Action: "calibration.import"}); err != nil {
		t.Fatal(err)
	}
	if got := activeIDs(t, s, key); len(got) != 1 || got[0] != batch[0].ID || !batch[1].Active {
		t.Errorf("after an activating import: %v", got)
	}
	if err := s.InsertCalibrations(ctx, "", []*CalibrationRecord{calRecord(CalibrationOriginFile, key, "measured", strp("x"))}, true, AdminAuditEntry{}); err == nil {
		t.Error("file rows must be mirrored, not inserted")
	}
}

package calibration

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

func pgStore(t *testing.T) *store.PostgresStore {
	t.Helper()
	schema := storetest.New(t)
	pg, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	return pg
}

// apiRecord is a calibration imported or fitted through the API for ref on snapshot.
func apiRecord(t *testing.T, origin, snapshot string, e decision.CalibrationFileEntry) *store.CalibrationRecord {
	t.Helper()
	doc, _ := json.Marshal(e)
	src, _ := json.Marshal(e.LabelSource)
	return &store.CalibrationRecord{
		CreatedBy: "admin", Origin: origin, Backend: "openrouter-decisions", Snapshot: snapshot, Family: decision.SnapshotFamily(snapshot),
		TargetKind: e.Target.Kind, TargetKey: e.Target.Key(), Quality: e.Quality, LabelSource: src, Entry: doc,
	}
}

func TestDatabaseOverridesFiles(t *testing.T) {
	pg := pgStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	const snap = "typesafe/jev-1.13-20260917"
	fileTable, fitTable, newerFile := ramp(0, 0.9), ramp(0.1, 1), ramp(0.05, 0.95)
	writeCal(t, dir, "a.json", snap, day, calEntry(t, "groundedness@1", nil, decision.QualityMeasured, fileTable))
	s := newStore(t, Config{Dir: dir, DB: pg})

	// The file is mirrored and applies.
	if c := s.Lookup("groundedness@1", nil, snap); c.State != policy.CalibrationMeasured || !slices.Equal(c.Table, fileTable) {
		t.Fatalf("mirrored file: %+v", c)
	}
	rows, _ := pg.ListCalibrations(ctx, store.CalibrationFilter{})
	if len(rows) != 1 || rows[0].Origin != store.CalibrationOriginFile || !rows[0].Active || rows[0].FileSHA256 == nil {
		t.Fatalf("mirror rows %+v", rows)
	}
	// An inactive fit changes nothing; activated, it beats the file.
	fit := apiRecord(t, store.CalibrationOriginFit, snap, calEntry(t, "groundedness@1", nil, decision.QualityProvisional, fitTable))
	if err := pg.InsertCalibrations(ctx, "", []*store.CalibrationRecord{fit}, false, store.AdminAuditEntry{Actor: "admin", Action: "calibration.fit"}); err != nil {
		t.Fatal(err)
	}
	if ch, _ := s.Reload(); len(ch) != 0 {
		t.Errorf("an inactive fit changed the lookup: %+v", ch)
	}
	if _, _, err := pg.SetCalibrationActive(ctx, "", fit.ID, true, store.AdminAuditEntry{Actor: "admin", Action: "calibration.activate"}); err != nil {
		t.Fatal(err)
	}
	ch, err := s.Reload()
	if err != nil || len(ch) != 1 || ch[0] != (Change{Snapshot: snap, Imported: 1}) {
		t.Fatalf("activation changes %+v %v", ch, err)
	}
	if c := s.Lookup("groundedness@1", nil, snap); c.State != policy.CalibrationProvisional || !slices.Equal(c.Table, fitTable) {
		t.Fatalf("an active fit beats the file: %+v", c)
	}
	// A changed file is mirrored inactive while the fit is active.
	writeCal(t, dir, "a.json", snap, day.Add(time.Hour), calEntry(t, "groundedness@1", nil, decision.QualityMeasured, newerFile))
	if _, err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if c := s.Lookup("groundedness@1", nil, snap); !slices.Equal(c.Table, fitTable) {
		t.Fatalf("a changed file overrode the active fit: %v", c.Table[:2])
	}
	// Deactivating the fit brings back the newest file content.
	if _, _, err := pg.SetCalibrationActive(ctx, "", fit.ID, false, store.AdminAuditEntry{Actor: "admin", Action: "calibration.deactivate"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if c := s.Lookup("groundedness@1", nil, snap); c.State != policy.CalibrationMeasured || !slices.Equal(c.Table, newerFile) {
		t.Fatalf("after deactivating the fit: %+v", c)
	}

	// The family fallback still applies a database row as provisional, state family.
	fam := newStore(t, Config{Dir: dir, DB: pg, FamilyFallback: true})
	if c := fam.Lookup("groundedness@1", nil, "typesafe/jev-1.13-20261015"); c.State != policy.CalibrationFamily ||
		c.Quality != policy.CalibrationProvisional || !slices.Equal(c.Table, newerFile) {
		t.Errorf("family fallback from the database: %+v", c)
	}
	// A second lookup on the same database (another replica) sees the same calibrations without
	// mirroring anything again.
	other := newStore(t, Config{DB: pg})
	if c := other.Lookup("groundedness@1", nil, snap); !slices.Equal(c.Table, newerFile) {
		t.Errorf("another replica: %+v", c)
	}
	if rows, _ := pg.ListCalibrations(ctx, store.CalibrationFilter{}); len(rows) != 3 {
		t.Errorf("%d rows, want 3 (two file contents and the fit)", len(rows))
	}
}

func TestDatabaseMockRowsAndOverlay(t *testing.T) {
	pg := pgStore(t)
	ctx := context.Background()
	const snap, mockSnap = "typesafe/jev-1.13-20260917", "mock/jev-heuristic-v1"
	live := apiRecord(t, store.CalibrationOriginImport, snap, calEntry(t, "groundedness@1", nil, decision.QualityMeasured, ramp(0, 1)))
	mock := apiRecord(t, store.CalibrationOriginImport, mockSnap, calEntry(t, "groundedness@1", nil, decision.QualityProvisional, ramp(0, 1)))
	mock.Quality = decision.QualityMock
	if err := pg.InsertCalibrations(ctx, "", []*store.CalibrationRecord{live, mock}, true, store.AdminAuditEntry{Actor: "a", Action: "calibration.import"}); err != nil {
		t.Fatal(err)
	}
	s := newStore(t, Config{DB: pg})
	if c := s.Lookup("groundedness@1", nil, mockSnap); c.Table != nil {
		t.Errorf("a mock row applies where mock calibrations do not load: %+v", c)
	}
	if c := newStore(t, Config{DB: pg, AllowMock: true}).Lookup("groundedness@1", nil, mockSnap); c.State != policy.CalibrationMock {
		t.Errorf("a mock row in the demo: %+v", c)
	}

	// With: an inactive record previews in place of the active one; the store is unchanged.
	preview := apiRecord(t, store.CalibrationOriginFit, snap, calEntry(t, "groundedness@1", nil, decision.QualityProvisional, ramp(0.2, 0.8)))
	other := apiRecord(t, store.CalibrationOriginFit, snap, calEntry(t, "contradiction@1", nil, decision.QualityProxy, ramp(0.3, 0.7)))
	if err := pg.InsertCalibrations(ctx, "", []*store.CalibrationRecord{preview, other}, false, store.AdminAuditEntry{Actor: "a", Action: "calibration.fit"}); err != nil {
		t.Fatal(err)
	}
	recs, _ := pg.CalibrationsByID(ctx, []int64{preview.ID, other.ID})
	l, err := s.With(recs)
	if err != nil {
		t.Fatal(err)
	}
	if c := l.Lookup("groundedness@1", nil, snap); c.State != policy.CalibrationProvisional || !slices.Equal(c.Table, ramp(0.2, 0.8)) {
		t.Errorf("overlay groundedness: %+v", c)
	}
	if c := l.Lookup("contradiction@1", nil, snap); c.State != policy.CalibrationProxy {
		t.Errorf("overlay contradiction: %+v", c)
	}
	if c := s.Lookup("groundedness@1", nil, snap); c.State != policy.CalibrationMeasured {
		t.Errorf("the overlay changed the store: %+v", c)
	}
	if _, err := s.With([]store.CalibrationRecord{{ID: 9, Snapshot: snap, TargetKey: "x", Entry: json.RawMessage(`{"table":[1]}`)}}); err == nil {
		t.Error("an overlay row without a valid table must be refused")
	}
}

// TestEntriesCarryOtherSnapshotAndStateFields: decide()'s input says when a target is calibrated
// only on another snapshot of the family (D56), and with which state fields a calibration was fit.
func TestEntriesCarryOtherSnapshotAndStateFields(t *testing.T) {
	dir := t.TempDir()
	const old, current = "typesafe/jev-1.13-20260917", "typesafe/jev-1.13-20261015"
	e := calEntry(t, "groundedness@1", nil, decision.QualityMeasured, ramp(0, 1))
	e.FitContext.StateFields = []string{"assistant_response", "evidence", "user_request"}
	writeCal(t, dir, "a.json", old, day, e)
	evs := []decision.EvaluatorInput{
		{Ref: "groundedness@1", Spec: spec(t, "groundedness@1")},
		{Ref: "contradiction@1", Spec: spec(t, "contradiction@1")},
	}
	s := newStore(t, Config{Dir: dir})
	got := s.Entries(evs, nil, old)
	if g := got["groundedness@1"]; !slices.Equal(g.FitStateFields, e.FitContext.StateFields) || len(got) != 1 {
		t.Errorf("exact snapshot entries %+v", got)
	}
	got = s.Entries(evs, nil, current)
	if len(got) != 1 || got["groundedness@1"].State != decision.CalibrationOtherSnapshot || got["groundedness@1"].Table != nil {
		t.Errorf("other-snapshot entries %+v", got)
	}
	// With the family fallback the family's table applies instead.
	fam := newStore(t, Config{Dir: dir, FamilyFallback: true})
	if g := fam.Entries(evs, nil, current)["groundedness@1"]; g.State != policy.CalibrationFamily || g.Table == nil ||
		!slices.Equal(g.FitStateFields, e.FitContext.StateFields) {
		t.Errorf("family entry %+v", g)
	}
}

// TestTriggerWakesWatch: a calibrations notification reloads before the poll would.
func TestTriggerWakesWatch(t *testing.T) {
	pg := pgStore(t)
	s := newStore(t, Config{DB: pg})
	got := make(chan []Change, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Watch(ctx, time.Hour, func(ch []Change) { got <- ch }) }()
	const snap = "typesafe/jev-1.13-20260917"
	rec := apiRecord(t, store.CalibrationOriginImport, snap, calEntry(t, "groundedness@1", nil, decision.QualityMeasured, ramp(0, 1)))
	if err := pg.InsertCalibrations(context.Background(), "", []*store.CalibrationRecord{rec}, true, store.AdminAuditEntry{Actor: "a", Action: "calibration.import"}); err != nil {
		t.Fatal(err)
	}
	s.Trigger()
	s.Trigger() // a burst coalesces
	select {
	case ch := <-got:
		if len(ch) != 1 || ch[0] != (Change{Snapshot: snap, Imported: 1}) {
			t.Errorf("changes %+v", ch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the trigger did not reload")
	}
	cancel()
	<-done
}

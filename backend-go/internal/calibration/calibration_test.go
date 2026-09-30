package calibration

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/evaluators"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
)

// repoCalibrationDir is the committed config/calibration.
var repoCalibrationDir = filepath.Join("..", "..", "..", "config", "calibration")

func registry(t *testing.T) *evaluators.Registry {
	t.Helper()
	r, err := evaluators.Builtins()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func spec(t *testing.T, ref string) *decision.Spec {
	t.Helper()
	s, ok := registry(t).Get(ref)
	if !ok {
		t.Fatalf("no evaluator %s", ref)
	}
	return s
}

func target(t *testing.T, ref string, params map[string]string) decision.CalibrationTarget {
	t.Helper()
	tg, err := decision.EvaluatorTarget(spec(t, ref), params)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

func newStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	reg := registry(t)
	if cfg.Specs == nil {
		cfg.Specs = func() policy.Resolver { return reg }
	}
	cfg.Logger = slog.New(slog.DiscardHandler)
	s := New(cfg)
	if _, err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	return s
}

// ramp is a valid 101-point table: an identity squeezed into [lo, hi].
func ramp(lo, hi float64) []float64 {
	out := make([]float64, 101)
	for i := range out {
		out[i] = lo + (hi-lo)*float64(i)/100
	}
	return out
}

// calEntry is a calibration entry that passes Check: quality measured with 300 public-dataset
// labels, provisional with 20 human ones, proxy with uniform-audit labels.
func calEntry(t *testing.T, ref string, params map[string]string, quality string, table []float64) decision.CalibrationFileEntry {
	t.Helper()
	sp := spec(t, ref)
	qs, err := decision.SoloQuestionSetSHA256(sp)
	if err != nil {
		t.Fatal(err)
	}
	e := decision.CalibrationFileEntry{
		Target:     target(t, ref, params),
		FitContext: decision.FitContext{StateFields: []string{}, Partition: decision.CalibrationPartitionSingle, QuestionSetSHA256: qs},
		Method:     decision.MethodIsotonic, Table: table, Quality: quality, Datasets: []string{},
		Reliability: []decision.ReliabilityBin{}, Recommended: decision.Recommended{Space: decision.SpaceCalibrated},
	}
	switch quality {
	case decision.QualityMeasured:
		e.N, e.NPos, e.NNeg, e.LabelSource = 300, 100, 200, decision.LabelSourceCounts{PublicDataset: 300}
	case decision.QualityProvisional:
		e.N, e.NPos, e.NNeg, e.LabelSource = 20, 10, 10, decision.LabelSourceCounts{Human: 20}
	case decision.QualityProxy:
		e.N, e.NPos, e.NNeg, e.LabelSource = 40, 20, 20, decision.LabelSourceCounts{UniformAudit: 40}
	case decision.QualityMock:
	default:
		t.Fatalf("quality %s", quality)
	}
	return e
}

// writeCal writes a calibration file for snapshot into dir and returns its path.
func writeCal(t *testing.T, dir, name, snapshot string, created time.Time, entries ...decision.CalibrationFileEntry) string {
	t.Helper()
	f := decision.NewCalibrationFile("openrouter-decisions", snapshot, "calibration_test", created)
	for _, e := range entries {
		f.Upsert(e)
	}
	if err := f.Check(); err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var day = time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)

func TestCommittedMockCalibration(t *testing.T) {
	p := filepath.Join(repoCalibrationDir, "mock__jev-heuristic-v1.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	f, err := decision.ParseCalibrationFile(b) // strict decode plus Check
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Check(); err != nil {
		t.Fatal(err)
	}
	// It keys to the snapshot the mock decision model reports (its answers carry the same one:
	// gate TestS0Scenarios).
	cl, err := jev.New(jev.Config{Backend: jev.BackendMock})
	if err != nil {
		t.Fatal(err)
	}
	reporter, ok := cl.(jev.StatusReporter)
	if !ok {
		t.Fatal("the mock client reports no status")
	}
	if f.DecisionModel.Snapshot != jev.DefaultMockModel || reporter.Status().Snapshot != f.DecisionModel.Snapshot ||
		f.DecisionModel.Backend != jev.BackendMock {
		t.Errorf("file snapshot %s (%s), mock status snapshot %s", f.DecisionModel.Snapshot, f.DecisionModel.Backend, reporter.Status().Snapshot)
	}
	if name, _ := decision.CalibrationFileName(f.DecisionModel.Snapshot); filepath.Base(p) != name {
		t.Errorf("file name %s, want %s", filepath.Base(p), name)
	}
	var keys []string
	for _, e := range f.Entries {
		keys = append(keys, e.Target.Key())
		if e.Quality != decision.QualityMock {
			t.Errorf("%s: quality %s", e.Target.Key(), e.Quality)
		}
	}
	want := []string{target(t, "groundedness@1", nil).Key(), target(t, "contradiction@1", nil).Key()}
	if !slices.Equal(keys, want) {
		t.Errorf("keys %v, want %v", keys, want)
	}

	// Loaded for the demo, it calibrates both evidence checks on the snapshot the mock reports.
	s := newStore(t, Config{Dir: repoCalibrationDir, AllowMock: true, Current: func() string { return reporter.Status().Snapshot }})
	for _, ref := range []string{"groundedness@1", "contradiction@1"} {
		c := s.Lookup(ref, map[string]string{}, "")
		if c.State != policy.CalibrationMock || c.Quality != policy.CalibrationMock || len(c.Table) != 101 || c.Stale {
			t.Errorf("%s: %+v", ref, c)
		}
	}
	if st, n := s.State(""); st != policy.CalibrationMock || n != 2 {
		t.Errorf("state %s targets %d", st, n)
	}
	if c := s.Lookup("refusal@1", nil, ""); c.State != policy.CalibrationNone || c.Table != nil {
		t.Errorf("uncalibrated evaluator: %+v", c)
	}

	// Anywhere else, the loader refuses a mock calibration.
	s = newStore(t, Config{Dir: repoCalibrationDir, Current: func() string { return jev.DefaultMockModel }})
	if c := s.Lookup("groundedness@1", nil, ""); c.State != policy.CalibrationNone || c.Table != nil {
		t.Errorf("mock file loaded without AllowMock: %+v", c)
	}
	if st, n := s.State(""); st != policy.CalibrationNone || n != 0 {
		t.Errorf("state %s targets %d", st, n)
	}
}

func TestLookupExactFamilyAndStale(t *testing.T) {
	dir := t.TempDir()
	const old, current = "typesafe/jev-1.13-20260917", "typesafe/jev-1.13-20261015"
	measured := ramp(0, 0.9)
	writeCal(t, dir, "typesafe__jev-1.13-20260917.json", old, day,
		calEntry(t, "groundedness@1", nil, decision.QualityMeasured, measured))

	s := newStore(t, Config{Dir: dir})
	c := s.Lookup("groundedness@1", nil, old)
	if c.State != policy.CalibrationMeasured || c.Quality != policy.CalibrationMeasured || !slices.Equal(c.Table, measured) || c.Stale {
		t.Errorf("exact snapshot: %+v", c)
	}
	c.Table[0] = 0.5 // a copy
	if again := s.Lookup("groundedness@1", nil, old); again.Table[0] != 0 {
		t.Error("the lookup handed out the store's table")
	}
	// The model moved on: without the family fallback nothing applies, and the calibration is stale.
	if c := s.Lookup("groundedness@1", nil, current); c.State != policy.CalibrationNone || c.Table != nil || !c.Stale {
		t.Errorf("new snapshot, no fallback: %+v", c)
	}
	if st, n := s.State(current); st != policy.CalibrationNone || n != 0 {
		t.Errorf("state %s %d", st, n)
	}
	// Other families and uncalibrated evaluators are simply uncalibrated.
	for _, tc := range []struct{ ref, snapshot string }{
		{"groundedness@1", "typesafe/jev-2.0-20261015"}, {"contradiction@1", current}, {"nosuch@1", old},
	} {
		if c := s.Lookup(tc.ref, nil, tc.snapshot); c.State != policy.CalibrationNone || c.Stale || c.Table != nil {
			t.Errorf("%s on %s: %+v", tc.ref, tc.snapshot, c)
		}
	}

	// With the fallback, the family's calibration applies as provisional, state family.
	s = newStore(t, Config{Dir: dir, FamilyFallback: true})
	c = s.Lookup("groundedness@1", nil, current)
	if c.State != policy.CalibrationFamily || c.Quality != policy.CalibrationProvisional || !slices.Equal(c.Table, measured) || !c.Stale {
		t.Errorf("family fallback: %+v", c)
	}
	if st, n := s.State(current); st != policy.CalibrationFamily || n != 1 {
		t.Errorf("family state %s %d", st, n)
	}
	// The newest file of the family wins.
	newer := ramp(0.05, 0.95)
	writeCal(t, dir, "typesafe__jev-1.13-20261001.json", "typesafe/jev-1.13-20261001", day.AddDate(0, 0, 14),
		calEntry(t, "groundedness@1", nil, decision.QualityMeasured, newer))
	if _, err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if c := s.Lookup("groundedness@1", nil, current); !slices.Equal(c.Table, newer) {
		t.Errorf("family fallback took an older calibration: %v", c.Table[:3])
	}
	// The exact snapshot still wins over its family.
	if c := s.Lookup("groundedness@1", nil, old); c.State != policy.CalibrationMeasured || !slices.Equal(c.Table, measured) {
		t.Errorf("exact snapshot with a newer family member: %+v", c)
	}
}

func TestLookupUsesTheCurrentSnapshotAndTheLiveRegistry(t *testing.T) {
	dir := t.TempDir()
	const snap = "typesafe/jev-1.13-20260917"
	writeCal(t, dir, "a.json", snap, day, calEntry(t, "contradiction@1", nil, decision.QualityMeasured, ramp(0, 1)))
	var mu sync.Mutex
	current := ""
	var specs policy.Resolver
	reg := registry(t)
	s := newStore(t, Config{Dir: dir,
		Current: func() string { mu.Lock(); defer mu.Unlock(); return current },
		Specs:   func() policy.Resolver { mu.Lock(); defer mu.Unlock(); return specs }})
	// The registry is still loading: nothing resolves.
	if c := s.Lookup("contradiction@1", nil, snap); c.State != policy.CalibrationNone {
		t.Errorf("before the registry: %+v", c)
	}
	mu.Lock()
	specs = reg
	mu.Unlock()
	// No snapshot known yet: "" finds nothing.
	if c := s.Lookup("contradiction@1", nil, ""); c.State != policy.CalibrationNone {
		t.Errorf("unknown snapshot: %+v", c)
	}
	if st, n := s.State(""); st != policy.CalibrationNone || n != 0 {
		t.Errorf("state before a snapshot: %s %d", st, n)
	}
	mu.Lock()
	current = snap
	mu.Unlock()
	if c := s.Lookup("contradiction@1", nil, ""); c.State != policy.CalibrationMeasured {
		t.Errorf("current snapshot: %+v", c)
	}
	if st, n := s.State(""); st != policy.CalibrationMeasured || n != 1 {
		t.Errorf("state %s %d", st, n)
	}
}

func TestLookupKeysByRouteParams(t *testing.T) {
	dir := t.TempDir()
	const snap = "typesafe/jev-1.13-20260917"
	strict := map[string]string{"brand_voice": "Formal and terse."}
	writeCal(t, dir, "tone.json", snap, day,
		calEntry(t, "off_brand_tone@1", nil, decision.QualityMeasured, ramp(0, 0.5)),
		calEntry(t, "off_brand_tone@1", strict, decision.QualityProvisional, ramp(0, 0.7)))
	s := newStore(t, Config{Dir: dir})

	// The spec's default params and an explicit copy of them are the same target.
	def := spec(t, "off_brand_tone@1").Params["brand_voice"].Default
	for _, params := range []map[string]string{nil, {}, {"brand_voice": def}, {"brand_voice": def, "undeclared": "x"}} {
		if c := s.Lookup("off_brand_tone@1", params, snap); c.State != policy.CalibrationMeasured || c.Table[100] != 0.5 {
			t.Errorf("default params %v: %+v", params, c.State)
		}
	}
	if c := s.Lookup("off_brand_tone@1", strict, snap); c.State != policy.CalibrationProvisional || c.Table[100] != 0.7 {
		t.Errorf("route params: %+v", c.State)
	}
	if c := s.Lookup("off_brand_tone@1", map[string]string{"brand_voice": "Playful."}, snap); c.State != policy.CalibrationNone {
		t.Errorf("other params: %+v", c.State)
	}
	// The weakest quality is the snapshot's state.
	if st, n := s.State(snap); st != policy.CalibrationProvisional || n != 2 {
		t.Errorf("state %s %d", st, n)
	}
}

func TestReloadReportsChanges(t *testing.T) {
	dir := t.TempDir()
	const a, b = "typesafe/jev-1.13-20260917", "typesafe/jev-1.13-20261015"
	pa := writeCal(t, dir, "a.json", a, day, calEntry(t, "groundedness@1", nil, decision.QualityMeasured, ramp(0, 1)))
	s := New(Config{Dir: dir, Specs: func() policy.Resolver { return registry(t) }, Logger: slog.New(slog.DiscardHandler)})
	ch, err := s.Reload()
	if err != nil || !reflect.DeepEqual(ch, []Change{{Snapshot: a, Imported: 1}}) {
		t.Fatalf("first load %+v %v", ch, err)
	}
	if ch, err := s.Reload(); err != nil || ch != nil {
		t.Errorf("unchanged directory: %+v %v", ch, err)
	}

	// A second target for a, a new snapshot b: both reported.
	writeCal(t, dir, "a.json", a, day, calEntry(t, "groundedness@1", nil, decision.QualityMeasured, ramp(0, 1)),
		calEntry(t, "contradiction@1", nil, decision.QualityMeasured, ramp(0, 1)))
	writeCal(t, dir, "b.json", b, day, calEntry(t, "groundedness@1", nil, decision.QualityProxy, ramp(0, 1)))
	if ch, _ := s.Reload(); !reflect.DeepEqual(ch, []Change{{Snapshot: a, Imported: 2}, {Snapshot: b, Imported: 1}}) {
		t.Errorf("added %+v", ch)
	}

	// A file that stops parsing keeps its last good content; so does a half-written one.
	if err := os.WriteFile(pa, []byte(`{"schema":"trustagent.calibration/v1","entries":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ch, _ := s.Reload(); ch != nil {
		t.Errorf("a broken file changed calibrations: %+v", ch)
	}
	if c := s.Lookup("contradiction@1", nil, a); c.State != policy.CalibrationMeasured {
		t.Errorf("last good content: %+v", c)
	}

	// Removing a file drops its calibrations: imported 0.
	if err := os.Remove(pa); err != nil {
		t.Fatal(err)
	}
	if ch, _ := s.Reload(); !reflect.DeepEqual(ch, []Change{{Snapshot: a}}) {
		t.Errorf("removed %+v", ch)
	}
	if c := s.Lookup("contradiction@1", nil, a); c.State != policy.CalibrationNone {
		t.Errorf("removed file still applies: %+v", c)
	}

	// Files that are not calibrations are skipped; a missing directory has none.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ch, err := s.Reload(); err != nil || ch != nil {
		t.Errorf("ignored files: %+v %v", ch, err)
	}
	gone := New(Config{Dir: filepath.Join(dir, "missing"), Logger: slog.New(slog.DiscardHandler)})
	if ch, err := gone.Reload(); err != nil || len(ch) != 0 {
		t.Errorf("missing directory: %+v %v", ch, err)
	}
	if c := gone.Lookup("groundedness@1", nil, a); c.State != policy.CalibrationNone {
		t.Errorf("missing directory: %+v", c)
	}
}

func TestNewestFileWinsATarget(t *testing.T) {
	dir := t.TempDir()
	const snap = "typesafe/jev-1.13-20260917"
	older, newer := ramp(0, 1), ramp(0.1, 0.9)
	// Name order and age disagree: created_at decides.
	writeCal(t, dir, "a-new.json", snap, day.Add(time.Hour), calEntry(t, "groundedness@1", nil, decision.QualityMeasured, newer))
	writeCal(t, dir, "b-old.json", snap, day, calEntry(t, "groundedness@1", nil, decision.QualityProvisional, older),
		calEntry(t, "contradiction@1", nil, decision.QualityMeasured, older))
	s := newStore(t, Config{Dir: dir})
	if c := s.Lookup("groundedness@1", nil, snap); c.State != policy.CalibrationMeasured || !slices.Equal(c.Table, newer) {
		t.Errorf("groundedness %+v", c.State)
	}
	if c := s.Lookup("contradiction@1", nil, snap); c.State != policy.CalibrationMeasured || !slices.Equal(c.Table, older) {
		t.Errorf("contradiction %+v", c.State)
	}
	if st, n := s.State(snap); st != policy.CalibrationMeasured || n != 2 {
		t.Errorf("state %s %d", st, n)
	}
}

func TestEntriesForDecide(t *testing.T) {
	dir := t.TempDir()
	const snap = "typesafe/jev-1.13-20260917"
	strict := map[string]string{"brand_voice": "Formal and terse."}
	writeCal(t, dir, "a.json", snap, day,
		calEntry(t, "groundedness@1", nil, decision.QualityMeasured, ramp(0, 1)),
		calEntry(t, "off_brand_tone@1", strict, decision.QualityProxy, ramp(0, 1)))
	s := newStore(t, Config{Dir: dir, FamilyFallback: true})
	evs := []decision.EvaluatorInput{
		{Ref: "groundedness@1", Spec: spec(t, "groundedness@1")},
		{Ref: "contradiction@1", Spec: spec(t, "contradiction@1")},
		{Ref: "off_brand_tone@1", Spec: spec(t, "off_brand_tone@1")},
		{Ref: "factual_claims@1", Spec: spec(t, "factual_claims@1"), Internal: true},
	}
	got := s.Entries(evs, map[string]map[string]string{"off_brand_tone@1": strict}, snap)
	if len(got) != 2 || got["groundedness@1"].Quality != decision.QualityMeasured || got["groundedness@1"].State != policy.CalibrationMeasured ||
		got["off_brand_tone@1"].Quality != decision.QualityProxy || len(got["groundedness@1"].Table) != 101 {
		t.Errorf("entries %+v", got)
	}
	for ref, e := range got {
		if err := decision.CheckCalibrationTable(e.Table); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
	// A family member's calibration: provisional, state family.
	fam := s.Entries(evs, nil, "typesafe/jev-1.13-20261015")
	if len(fam) != 1 || fam["groundedness@1"].State != policy.CalibrationFamily || fam["groundedness@1"].Quality != decision.QualityProvisional {
		t.Errorf("family entries %+v", fam)
	}
	if e := Entries(nil, evs, nil, snap); e == nil || len(e) != 0 {
		t.Errorf("no lookup: %v", e)
	}
	if e := Entries(policy.NoCalibration{}, evs, nil, snap); len(e) != 0 {
		t.Errorf("no calibrations: %v", e)
	}
}

func TestWatchReportsChangesUntilCancelled(t *testing.T) {
	dir := t.TempDir()
	s := newStore(t, Config{Dir: dir})
	got := make(chan []Change, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Watch(ctx, 10*time.Millisecond, func(ch []Change) { got <- ch }) }()
	writeCal(t, dir, "a.json", "typesafe/jev-1.13-20260917", day, calEntry(t, "groundedness@1", nil, decision.QualityMeasured, ramp(0, 1)))
	select {
	case ch := <-got:
		if !reflect.DeepEqual(ch, []Change{{Snapshot: "typesafe/jev-1.13-20260917", Imported: 1}}) {
			t.Errorf("changes %+v", ch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported")
	}
	cancel()
	<-done
}

// TestLookupsDuringReloads runs the gate's and the status reporter's reads against reloads (run
// with -race): a reload swaps in a new index while lookups keep reading the old one.
func TestLookupsDuringReloads(t *testing.T) {
	dir := t.TempDir()
	const snap = "typesafe/jev-1.13-20260917"
	s := newStore(t, Config{Dir: dir, FamilyFallback: true, Current: func() string { return snap }})
	evs := []decision.EvaluatorInput{
		{Ref: "groundedness@1", Spec: spec(t, "groundedness@1")},
		{Ref: "off_brand_tone@1", Spec: spec(t, "off_brand_tone@1")},
	}
	params := map[string]map[string]string{"off_brand_tone@1": {"brand_voice": "Formal."}}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				if c := s.Lookup("groundedness@1", nil, ""); c.Table != nil && len(c.Table) != 101 {
					t.Errorf("table of %d points", len(c.Table))
				}
				s.Entries(evs, params, snap)
				s.State("")
			}
		}()
	}
	var last []float64
	for i := range 20 {
		last = ramp(0, 0.5+float64(i)/100)
		writeCal(t, dir, "a.json", snap, day.Add(time.Duration(i)*time.Minute),
			calEntry(t, "groundedness@1", nil, decision.QualityMeasured, last))
		if _, err := s.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	wg.Wait()
	if c := s.Lookup("groundedness@1", nil, snap); c.State != policy.CalibrationMeasured || !slices.Equal(c.Table, last) {
		t.Errorf("after the reloads: %s, %d points, want the last table written", c.State, len(c.Table))
	}
}

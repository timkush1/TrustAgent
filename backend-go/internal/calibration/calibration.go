// Package calibration is the gateway's calibration lookup (SPEC §2.11; P3 decision D14, P4 D55):
// the calibrations that apply, keyed by decision-model snapshot and calibration target, and
// re-read by a poll. Store implements policy.CalibrationLookup for the gate (decide()'s calibration
// input, Entries) and for the policy readiness checks.
//
// Without a database the calibrations are the trustagent.calibration/v1 files in
// TRUTHTABLE_CALIBRATION_DIR. With one, the files are mirrored into the calibrations table (origin
// file) and the lookup reads the table's active rows, so a calibration imported or fitted through
// the API (and activated) overrides the file's; a write anywhere reaches every replica through the
// config notifications ("calibrations:<snapshot>", Trigger) and the poll.
//
// A lookup finds, for an evaluator ref with its route params (the target key of
// decision.EvaluatorTarget) and a snapshot:
//
//  1. the calibration of that exact snapshot;
//  2. with TRUTHTABLE_CALIBRATION_FAMILY_FALLBACK, the newest calibration of another snapshot of
//     the same family (the snapshot minus -YYYYMMDD), forced to quality provisional with state
//     family;
//  3. otherwise none.
//
// A calibration that exists only for another snapshot of the family is stale: the decision model
// moved on since it was fitted (policy readiness then says snapshot_changed).
package calibration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/policy"
	"github.com/truthtable/backend-go/internal/store"
)

// ReloadInterval is how often the gateway re-reads the calibration directory (D14).
const ReloadInterval = 30 * time.Second

// maxFileBytes bounds one calibration file: a target is about 2 KB (its 101-point table and fit
// report), so this leaves room for thousands.
const maxFileBytes = 8 << 20

// mockSnapshotPrefix marks the mock decision model's snapshots, whose calibrations are quality mock.
const mockSnapshotPrefix = "mock/"

// keyCacheMax bounds the target-key cache; it starts over when full.
const keyCacheMax = 4096

// stateOrder lists the calibration states from weakest to strongest (as internal/recording and the
// dashboard's recorder rank them).
var stateOrder = []string{
	policy.CalibrationNone, policy.CalibrationMock, policy.CalibrationProxy, policy.CalibrationFamily,
	policy.CalibrationProvisional, policy.CalibrationMeasured,
}

// Config configures a Store.
type Config struct {
	// Dir is the calibration directory (TRUTHTABLE_CALIBRATION_DIR). Every *.json file in it is a
	// trustagent.calibration/v1 document; "" or a missing directory has no calibrations.
	Dir string
	// FamilyFallback applies a calibration of the snapshot's family when the exact snapshot has
	// none (TRUTHTABLE_CALIBRATION_FAMILY_FALLBACK).
	FamilyFallback bool
	// AllowMock loads calibrations of mock/* snapshots (quality mock). Only the zero-key demo and
	// the test stacks, which run the mock decision model, set it (SPEC §2.11: the loader refuses
	// mock files otherwise); decide() counts a mock calibration only with the mock backend anyway.
	AllowMock bool
	// Specs returns the evaluator specs (the live evaluator registry). It returns nil while the
	// registry is not loaded yet; lookups then find nothing.
	Specs func() policy.Resolver
	// Current returns the current decision-model snapshot ("" when unknown), for a lookup or a
	// state that names no snapshot. nil means unknown.
	Current func() string
	// DB is the calibration store; nil reads the files only. Channel is its NOTIFY channel
	// (store.ConfigChannel when empty).
	DB      DB
	Channel string
	Logger  *slog.Logger
}

// DB is the database side of the lookup (*store.PostgresStore).
type DB interface {
	MirrorCalibrationFile(ctx context.Context, channel string, recs []*store.CalibrationRecord) (int, error)
	ActiveCalibrations(ctx context.Context) ([]store.CalibrationRecord, error)
}

// dbTimeout bounds one reload's database work.
const dbTimeout = 10 * time.Second

// Change is a snapshot whose calibrations a reload added, changed or removed (WS
// calibration_updated {snapshot, imported}).
type Change struct {
	Snapshot string
	// Imported is how many calibration targets the snapshot has now; 0 when its files are gone.
	Imported int
}

// Store is the file lookup. It is safe for concurrent use: Reload serializes itself and swaps in
// an immutable index that lookups read without locking.
type Store struct {
	cfg Config
	log *slog.Logger

	mu    sync.Mutex // serializes Reload; guards files
	files map[string]*file
	idx   atomic.Pointer[index]
	keys  keyCache
	// trigger asks Watch to reload now (a calibration notification arrived).
	trigger chan struct{}
}

var _ policy.CalibrationLookup = (*Store)(nil)

// file is one calibration file as last read.
type file struct {
	// sum is the sha256 of the content last read, parsed or not.
	sum string
	// cal is the last content that parsed; nil when none did.
	cal *decision.CalibrationFile
	// calSum is the sha256 of cal's content, and mirrored the one last mirrored into the database.
	calSum, mirrored string
}

// index is an immutable view of every loaded calibration.
type index struct {
	snapshots map[string]*snapshotCal
	// families lists each family's snapshots.
	families map[string][]*snapshotCal
}

// snapshotCal is the calibrations of one snapshot, by target key.
type snapshotCal struct {
	name, family string
	entries      map[string]*entry
	// digest identifies the entries' keys, qualities and tables, to tell a reload what changed.
	digest string
}

// entry is one calibrated target.
type entry struct {
	table   []float64
	quality string
	// stateFields are the state fields it was fitted with (fit_context.state_fields).
	stateFields []string
	// created is the created_at of the file it came from (the row's, from the database); path is
	// that file ("calibrations/<id>" for a row).
	created time.Time
	path    string
}

// New returns a Store with no calibrations loaded; call Reload.
func New(cfg Config) *Store {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Store{cfg: cfg, log: log, files: map[string]*file{}, trigger: make(chan struct{}, 1)}
}

// Trigger asks Watch to reload as soon as it can (a "calibrations:" notification). It never blocks.
func (s *Store) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Reload is ReloadContext without a deadline of its own (the database work is still bounded).
func (s *Store) Reload() ([]Change, error) { return s.ReloadContext(context.Background()) }

// ReloadContext re-reads the directory (and, with a database, mirrors new file content into it and
// re-reads its active rows) and reports the snapshots whose calibrations changed, sorted by
// snapshot; the first reload reports every snapshot it loads. A file whose new content does not
// parse keeps its last good content, so a half-written file never drops a calibration; a removed
// file drops its calibrations (without a database: a mirrored row stays until it is replaced or
// deactivated). An unreadable directory or database is an error and keeps what is loaded.
func (s *Store) ReloadContext(ctx context.Context) ([]Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths, err := s.list()
	if err != nil {
		return nil, fmt.Errorf("calibration: %w", err)
	}
	dirty := s.idx.Load() == nil
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		seen[p] = true
		b, err := readFile(p)
		if err != nil {
			s.log.Warn("calibration: file not read; its calibrations stay as they were", "file", p, "err", err)
			continue
		}
		sum := sha256.Sum256(b)
		hexSum := hex.EncodeToString(sum[:])
		f := s.files[p]
		if f != nil && f.sum == hexSum {
			continue
		}
		if f == nil {
			f = &file{}
			s.files[p] = f
		}
		f.sum = hexSum
		cal, err := s.parse(b)
		switch {
		case errors.Is(err, errMockRefused):
			s.log.Info("calibration: skipping a mock calibration, which applies only to the zero-key demo and test stacks",
				"file", p)
			if f.cal != nil {
				f.cal, f.calSum, dirty = nil, "", true
			}
		case err != nil && f.cal != nil:
			s.log.Warn("calibration: file does not parse; it keeps its last good content", "file", p, "err", err)
		case err != nil:
			s.log.Warn("calibration: file does not parse; ignored", "file", p, "err", err)
		default:
			f.cal, f.calSum, dirty = cal, hexSum, true
		}
	}
	for p := range s.files {
		if !seen[p] {
			delete(s.files, p)
			dirty = true
		}
	}
	var next *index
	if s.cfg.DB != nil {
		dctx, cancel := context.WithTimeout(ctx, dbTimeout)
		defer cancel()
		if next, err = s.fromDB(dctx); err != nil {
			return nil, fmt.Errorf("calibration: %w", err)
		}
	} else {
		if !dirty {
			return nil, nil
		}
		next = s.build()
	}
	changes := diff(s.idx.Load(), next)
	s.idx.Store(next)
	return changes, nil
}

// fromDB mirrors the file content not mirrored yet (oldest created_at first, so the newest file
// of a target ends up active) and indexes the database's active rows.
func (s *Store) fromDB(ctx context.Context) (*index, error) {
	var pending []*file
	for _, f := range s.files {
		if f.cal != nil && f.calSum != f.mirrored {
			pending = append(pending, f)
		}
	}
	slices.SortFunc(pending, func(a, b *file) int {
		if c := a.cal.CreatedAt.Compare(b.cal.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.calSum, b.calSum)
	})
	for _, f := range pending {
		recs, err := FileRecords(f.calSum, f.cal)
		if err != nil {
			return nil, err
		}
		n, err := s.cfg.DB.MirrorCalibrationFile(ctx, s.cfg.Channel, recs)
		if err != nil {
			return nil, fmt.Errorf("mirror %s: %w", f.cal.DecisionModel.Snapshot, err)
		}
		f.mirrored = f.calSum
		if n > 0 {
			s.log.Info("calibration: mirrored a calibration file into the database", "snapshot", f.cal.DecisionModel.Snapshot, "targets", n)
		}
	}
	rows, err := s.cfg.DB.ActiveCalibrations(ctx)
	if err != nil {
		return nil, err
	}
	return s.buildRows(rows), nil
}

// FileRecords are the calibrations rows that mirror a calibration file (origin file, D55). sum is
// the sha256 of the file's content.
func FileRecords(sum string, cal *decision.CalibrationFile) ([]*store.CalibrationRecord, error) {
	out := make([]*store.CalibrationRecord, 0, len(cal.Entries))
	for i := range cal.Entries {
		e := &cal.Entries[i]
		doc, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		src, err := json.Marshal(e.LabelSource)
		if err != nil {
			return nil, err
		}
		fileSum := sum
		out = append(out, &store.CalibrationRecord{
			Backend: cal.DecisionModel.Backend, Snapshot: cal.DecisionModel.Snapshot, Family: cal.DecisionModel.Family,
			TargetKind: e.Target.Kind, TargetKey: e.Target.Key(), Quality: e.Quality, LabelSource: src, Entry: doc, FileSHA256: &fileSum,
		})
	}
	return out, nil
}

// Watch reloads every interval until ctx ends and passes each non-empty change set to changed
// (nil ignores them). A failed reload is logged and keeps the calibrations loaded.
func (s *Store) Watch(ctx context.Context, every time.Duration, changed func([]Change)) {
	if every <= 0 {
		every = ReloadInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.trigger:
		}
		ch, err := s.ReloadContext(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			s.log.Warn("calibration: reload failed", "dir", s.cfg.Dir, "err", err)
			continue
		}
		for _, c := range ch {
			s.log.Info("calibration: reloaded", "snapshot", c.Snapshot, "targets", c.Imported)
		}
		if len(ch) > 0 && changed != nil {
			changed(ch)
		}
	}
}

// Lookup implements policy.CalibrationLookup: the calibration of the evaluator ref with its route
// params (resolved or not; the spec's defaults fill the rest) for snapshot ("" = the current one).
// The table is a copy.
func (s *Store) Lookup(ref string, params map[string]string, snapshot string) policy.CalibrationStatus {
	return s.lookupIn(s.idx.Load(), ref, params, snapshot)
}

func (s *Store) lookupIn(idx *index, ref string, params map[string]string, snapshot string) policy.CalibrationStatus {
	none := policy.CalibrationStatus{State: policy.CalibrationNone}
	if idx == nil || len(idx.snapshots) == 0 {
		return none
	}
	if snapshot == "" {
		if snapshot = s.current(); snapshot == "" {
			return none
		}
	}
	key, ok := s.targetKey(ref, params)
	if !ok {
		return none
	}
	if sc := idx.snapshots[snapshot]; sc != nil {
		if e := sc.entries[key]; e != nil {
			return policy.CalibrationStatus{State: e.quality, Quality: e.quality, Table: slices.Clone(e.table),
				StateFields: slices.Clone(e.stateFields)}
		}
	}
	e := idx.family(snapshot, key)
	switch {
	case e == nil:
		return none
	case !s.cfg.FamilyFallback:
		return policy.CalibrationStatus{State: policy.CalibrationNone, Stale: true}
	}
	return policy.CalibrationStatus{State: policy.CalibrationFamily, Quality: policy.CalibrationProvisional,
		Table: slices.Clone(e.table), Stale: true, StateFields: slices.Clone(e.stateFields)}
}

// With returns a lookup in which the given rows (any origin, active or not) replace the
// calibration of their (snapshot, target key): the active rows a Preview impact replays with
// instead (POST /api/policies/dry-run calibration_ids, D62). The store itself is unchanged. A row
// whose entry does not parse or has no valid table is an error.
func (s *Store) With(rows []store.CalibrationRecord) (policy.CalibrationLookup, error) {
	base := s.idx.Load()
	next := &index{snapshots: map[string]*snapshotCal{}, families: map[string][]*snapshotCal{}}
	if base != nil {
		for name, sc := range base.snapshots {
			next.snapshots[name] = sc
		}
	}
	for i := range rows {
		r := &rows[i]
		var ce decision.CalibrationFileEntry
		if err := json.Unmarshal(r.Entry, &ce); err != nil {
			return nil, fmt.Errorf("calibration %d: %w", r.ID, err)
		}
		if err := decision.CheckCalibrationTable(ce.Table); err != nil {
			return nil, fmt.Errorf("calibration %d: %w", r.ID, err)
		}
		sc := next.snapshots[r.Snapshot]
		copied := &snapshotCal{name: r.Snapshot, family: r.Family, entries: map[string]*entry{}}
		if sc != nil {
			copied.family = sc.family
			for k, v := range sc.entries {
				copied.entries[k] = v
			}
		}
		copied.entries[r.TargetKey] = &entry{table: ce.Table, quality: r.Quality, stateFields: ce.FitContext.StateFields,
			created: r.CreatedAt, path: "calibrations/" + strconv.FormatInt(r.ID, 10)}
		next.snapshots[r.Snapshot] = copied
	}
	next.finish()
	return overlay{s: s, idx: next}, nil
}

// overlay is the lookup With returns.
type overlay struct {
	s   *Store
	idx *index
}

func (o overlay) Lookup(ref string, params map[string]string, snapshot string) policy.CalibrationStatus {
	return o.s.lookupIn(o.idx, ref, params, snapshot)
}

// Entries is decide()'s calibration input (decision.Input.calibration) for evs on snapshot, from
// this store (Entries with s).
func (s *Store) Entries(evs []decision.EvaluatorInput, params map[string]map[string]string, snapshot string) map[string]decision.CalibrationEntry {
	return Entries(s, evs, params, snapshot)
}

// Entries is decide()'s calibration input (decision.Input.calibration) on snapshot: for each
// evaluator of evs (internal ones included) that has a calibration, its table, quality, state and
// fit state fields, keyed by ref; for one calibrated only on other snapshots of the family (and no
// family fallback), an entry with state other_snapshot and no table (D56), so decide() can tell a
// snapshot change from a missing calibration. params holds the route params of an evaluator by
// ref; the spec's defaults fill the rest. A nil lookup has no calibrations. The map is never nil.
func Entries(l policy.CalibrationLookup, evs []decision.EvaluatorInput, params map[string]map[string]string, snapshot string) map[string]decision.CalibrationEntry {
	out := map[string]decision.CalibrationEntry{}
	if l == nil {
		return out
	}
	for _, e := range evs {
		if _, done := out[e.Ref]; done || e.Spec == nil {
			continue
		}
		c := l.Lookup(e.Ref, decision.ResolvedParams(e.Spec, params[e.Ref]), snapshot)
		if entry, ok := policy.DecideCalibration(c); ok {
			out[e.Ref] = entry
		}
	}
	return out
}

// State is the calibration state of a snapshot ("" = the current one) and how many calibration
// targets apply to it: the weakest quality of the snapshot's calibrations; family when only
// calibrations of its family apply (TRUTHTABLE_CALIBRATION_FAMILY_FALLBACK); else none and 0.
func (s *Store) State(snapshot string) (state string, targets int) {
	idx := s.idx.Load()
	if snapshot == "" {
		snapshot = s.current()
	}
	if idx == nil || snapshot == "" {
		return policy.CalibrationNone, 0
	}
	if sc := idx.snapshots[snapshot]; sc != nil && len(sc.entries) > 0 {
		weakest := len(stateOrder) - 1
		for _, e := range sc.entries {
			weakest = min(weakest, max(slices.Index(stateOrder, e.quality), 0))
		}
		return stateOrder[weakest], len(sc.entries)
	}
	if !s.cfg.FamilyFallback {
		return policy.CalibrationNone, 0
	}
	keys := map[string]bool{}
	for _, sc := range idx.families[decision.SnapshotFamily(snapshot)] {
		if sc.name == snapshot {
			continue
		}
		for k := range sc.entries {
			keys[k] = true
		}
	}
	if len(keys) == 0 {
		return policy.CalibrationNone, 0
	}
	return policy.CalibrationFamily, len(keys)
}

func (s *Store) current() string {
	if s.cfg.Current == nil {
		return ""
	}
	return s.cfg.Current()
}

// targetKey is the calibration target key of ref with params, or false when the ref is unknown.
func (s *Store) targetKey(ref string, params map[string]string) (string, bool) {
	if s.cfg.Specs == nil {
		return "", false
	}
	specs := s.cfg.Specs()
	if specs == nil {
		return "", false
	}
	spec, ok := specs.Get(ref)
	if !ok || spec == nil {
		return "", false
	}
	key, err := s.keys.key(spec, params)
	if err != nil {
		s.log.Warn("calibration: no target key for an evaluator", "ref", ref, "err", err)
		return "", false
	}
	return key, true
}

// errMockRefused is a mock calibration on a stack that does not run the mock decision model.
var errMockRefused = errors.New("a calibration of the mock decision model")

// parse decodes and checks one file (decision.ParseCalibrationFile) and refuses mock snapshots
// unless the config allows them.
func (s *Store) parse(b []byte) (*decision.CalibrationFile, error) {
	cal, err := decision.ParseCalibrationFile(b)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(cal.DecisionModel.Snapshot, mockSnapshotPrefix) && !s.cfg.AllowMock {
		return nil, errMockRefused
	}
	return cal, nil
}

// list returns the directory's *.json files, sorted; none when the directory does not exist.
func (s *Store) list() ([]string, error) {
	if s.cfg.Dir == "" {
		return nil, nil
	}
	ents, err := os.ReadDir(s.cfg.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.EqualFold(filepath.Ext(name), ".json") {
			continue
		}
		p := filepath.Join(s.cfg.Dir, name)
		// Stat follows symlinks: a mounted ConfigMap links its files.
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// readFile reads one calibration file, at most maxFileBytes.
func readFile(p string) ([]byte, error) {
	f, err := os.Open(p) // #nosec G304 -- the operator names the calibration directory (TRUTHTABLE_CALIBRATION_DIR)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxFileBytes)
	}
	return b, nil
}

// build indexes the files that parsed. Two files that calibrate the same target of a snapshot:
// the newest created_at applies (the first file by name on a tie), with a warning.
func (s *Store) build() *index {
	idx := &index{snapshots: map[string]*snapshotCal{}, families: map[string][]*snapshotCal{}}
	paths := make([]string, 0, len(s.files))
	for p := range s.files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range paths {
		cal := s.files[p].cal
		if cal == nil {
			continue
		}
		name := cal.DecisionModel.Snapshot
		sc := idx.snapshots[name]
		if sc == nil {
			sc = &snapshotCal{name: name, family: cal.DecisionModel.Family, entries: map[string]*entry{}}
			idx.snapshots[name] = sc
		}
		for i := range cal.Entries {
			ce := &cal.Entries[i]
			key := ce.Target.Key()
			e := &entry{table: ce.Table, quality: ce.Quality, stateFields: ce.FitContext.StateFields, created: cal.CreatedAt, path: p}
			old := sc.entries[key]
			if old == nil {
				sc.entries[key] = e
				continue
			}
			keep, drop := old, e
			if e.created.After(old.created) {
				keep, drop = e, old
			}
			s.log.Warn("calibration: two files calibrate the same target; the newest applies",
				"snapshot", name, "target", key, "applies", keep.path, "ignored", drop.path)
			sc.entries[key] = keep
		}
	}
	idx.finish()
	return idx
}

// finish computes the digests and the family lists.
func (x *index) finish() {
	for _, sc := range x.snapshots {
		sc.digest = digest(sc)
		x.families[sc.family] = append(x.families[sc.family], sc)
	}
}

// buildRows indexes the active rows of the database. A row whose entry does not parse or whose
// table is not a calibration table is skipped with a warning, and so is a mock snapshot's row
// where mock calibrations do not load.
func (s *Store) buildRows(rows []store.CalibrationRecord) *index {
	idx := &index{snapshots: map[string]*snapshotCal{}, families: map[string][]*snapshotCal{}}
	for i := range rows {
		r := &rows[i]
		if r.TargetKind != decision.CalibrationTargetEvaluator {
			continue // other target kinds are v2.1
		}
		if strings.HasPrefix(r.Snapshot, mockSnapshotPrefix) && !s.cfg.AllowMock {
			continue
		}
		var ce decision.CalibrationFileEntry
		if err := json.Unmarshal(r.Entry, &ce); err != nil {
			s.log.Warn("calibration: a stored calibration does not parse; skipped", "id", r.ID, "err", err)
			continue
		}
		if err := decision.CheckCalibrationTable(ce.Table); err != nil {
			s.log.Warn("calibration: a stored calibration has no valid table; skipped", "id", r.ID, "err", err)
			continue
		}
		sc := idx.snapshots[r.Snapshot]
		if sc == nil {
			sc = &snapshotCal{name: r.Snapshot, family: r.Family, entries: map[string]*entry{}}
			idx.snapshots[r.Snapshot] = sc
		}
		sc.entries[r.TargetKey] = &entry{table: ce.Table, quality: r.Quality, stateFields: ce.FitContext.StateFields,
			created: r.CreatedAt, path: "calibrations/" + strconv.FormatInt(r.ID, 10)}
	}
	idx.finish()
	return idx
}

// family returns the newest calibration of key among the other snapshots of snapshot's family.
func (x *index) family(snapshot, key string) *entry {
	var best *entry
	for _, sc := range x.families[decision.SnapshotFamily(snapshot)] {
		if sc.name == snapshot {
			continue
		}
		e := sc.entries[key]
		if e == nil {
			continue
		}
		if best == nil || e.created.After(best.created) || (e.created.Equal(best.created) && e.path > best.path) {
			best = e
		}
	}
	return best
}

// digest identifies a snapshot's calibrations: each target key with its quality, fit state fields
// and table.
func digest(sc *snapshotCal) string {
	keys := make([]string, 0, len(sc.entries))
	for k := range sc.entries {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	h := sha256.New()
	var buf []byte
	for _, k := range keys {
		e := sc.entries[k]
		buf = append(buf[:0], k...)
		buf = append(buf, 0)
		buf = append(buf, e.quality...)
		buf = append(buf, 0)
		buf = append(buf, strings.Join(e.stateFields, ",")...)
		buf = append(buf, 0)
		for _, v := range e.table {
			buf = strconv.AppendFloat(buf, v, 'g', -1, 64)
			buf = append(buf, ',')
		}
		buf = append(buf, '\n')
		h.Write(buf) //nolint:errcheck // hash.Hash never fails
	}
	return hex.EncodeToString(h.Sum(nil))
}

// diff lists the snapshots whose calibrations differ between two indexes, sorted.
func diff(old, next *index) []Change {
	var out []Change
	for name, sc := range next.snapshots {
		if old == nil || old.snapshots[name] == nil || old.snapshots[name].digest != sc.digest {
			out = append(out, Change{Snapshot: name, Imported: len(sc.entries)})
		}
	}
	if old != nil {
		for name := range old.snapshots {
			if next.snapshots[name] == nil {
				out = append(out, Change{Snapshot: name})
			}
		}
	}
	slices.SortFunc(out, func(a, b Change) int { return strings.Compare(a.Snapshot, b.Snapshot) })
	return out
}

// keyCache caches target keys by spec and resolved params: hashing a spec's question is the
// costly part of a lookup, which runs for every evaluator of every gated answer.
type keyCache struct {
	mu sync.Mutex
	m  map[keyCacheKey]string
}

type keyCacheKey struct {
	spec   *decision.Spec
	params string
}

func (c *keyCache) key(spec *decision.Spec, params map[string]string) (string, error) {
	resolved := decision.ResolvedParams(spec, params)
	ck := keyCacheKey{spec: spec, params: canonicalParams(resolved)}
	c.mu.Lock()
	k, ok := c.m[ck]
	c.mu.Unlock()
	if ok {
		return k, nil
	}
	t, err := decision.EvaluatorTarget(spec, resolved)
	if err != nil {
		return "", err
	}
	k = t.Key()
	c.mu.Lock()
	if c.m == nil || len(c.m) >= keyCacheMax {
		c.m = map[keyCacheKey]string{}
	}
	c.m[ck] = k
	c.mu.Unlock()
	return k, nil
}

// canonicalParams is an unambiguous rendering of params, for the cache key.
func canonicalParams(p map[string]string) string {
	if len(p) == 0 {
		return ""
	}
	names := make([]string, 0, len(p))
	for n := range p {
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(strconv.Quote(n))
		b.WriteByte('=')
		b.WriteString(strconv.Quote(p[n]))
		b.WriteByte(';')
	}
	return b.String()
}

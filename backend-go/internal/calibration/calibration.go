// Package calibration is the gateway's calibration lookup (SPEC §2.11; P3 decision D14): a
// read-only view of the trustagent.calibration/v1 files in TRUTHTABLE_CALIBRATION_DIR, keyed by
// decision-model snapshot and calibration target, and re-read by a poll. Store implements
// policy.CalibrationLookup for the gate (decide()'s calibration input, Entries) and for the policy
// readiness checks. The P4 calibration store (API imports and fits, which override files) builds
// on this package.
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
	Logger  *slog.Logger
}

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
}

var _ policy.CalibrationLookup = (*Store)(nil)

// file is one calibration file as last read.
type file struct {
	// sum is the sha256 of the content last read, parsed or not.
	sum string
	// cal is the last content that parsed; nil when none did.
	cal *decision.CalibrationFile
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
	// created is the created_at of the file it came from; path is that file.
	created time.Time
	path    string
}

// New returns a Store with no calibrations loaded; call Reload.
func New(cfg Config) *Store {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Store{cfg: cfg, log: log, files: map[string]*file{}}
}

// Reload re-reads the directory and reports the snapshots whose calibrations changed, sorted by
// snapshot; the first Reload reports every snapshot it loads. A file whose new content does not
// parse keeps its last good content, so a half-written file never drops a calibration; a removed
// file drops its calibrations. An unreadable directory is an error and keeps what is loaded.
func (s *Store) Reload() ([]Change, error) {
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
				f.cal, dirty = nil, true
			}
		case err != nil && f.cal != nil:
			s.log.Warn("calibration: file does not parse; it keeps its last good content", "file", p, "err", err)
		case err != nil:
			s.log.Warn("calibration: file does not parse; ignored", "file", p, "err", err)
		default:
			f.cal, dirty = cal, true
		}
	}
	for p := range s.files {
		if !seen[p] {
			delete(s.files, p)
			dirty = true
		}
	}
	if !dirty {
		return nil, nil
	}
	next := s.build()
	changes := diff(s.idx.Load(), next)
	s.idx.Store(next)
	return changes, nil
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
		}
		ch, err := s.Reload()
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
	none := policy.CalibrationStatus{State: policy.CalibrationNone}
	idx := s.idx.Load()
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
			return policy.CalibrationStatus{State: e.quality, Quality: e.quality, Table: slices.Clone(e.table)}
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
		Table: slices.Clone(e.table), Stale: true}
}

// Entries is decide()'s calibration input (decision.Input.calibration) for evs on snapshot, from
// this store (Entries with s).
func (s *Store) Entries(evs []decision.EvaluatorInput, params map[string]map[string]string, snapshot string) map[string]decision.CalibrationEntry {
	return Entries(s, evs, params, snapshot)
}

// Entries is decide()'s calibration input (decision.Input.calibration) on snapshot: for each
// evaluator of evs (internal ones included) that has a calibration, its table, quality and state,
// keyed by ref. params holds the route params of an evaluator by ref; the spec's defaults fill
// the rest. A nil lookup has no calibrations. The map is never nil.
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
		if c.Table == nil {
			continue
		}
		state := c.State
		if state == "" || state == policy.CalibrationNone {
			state = c.Quality
		}
		out[e.Ref] = decision.CalibrationEntry{Table: c.Table, Quality: c.Quality, State: state}
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
			e := &entry{table: ce.Table, quality: ce.Quality, created: cal.CreatedAt, path: p}
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
	for _, sc := range idx.snapshots {
		sc.digest = digest(sc)
		idx.families[sc.family] = append(idx.families[sc.family], sc)
	}
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

// digest identifies a snapshot's calibrations: each target key with its quality and table.
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

// Package evaluators is the registry of evaluator specs the gate can attach to a route (SPEC §3.1,
// §7.2): versioned, immutable decision specs from three sources, in increasing precedence:
//
//  1. built-in templates, embedded from contracts/evaluators;
//  2. files in TRUTHTABLE_EVALUATORS_DIR (config/evaluators/*.json, policy as code);
//  3. the database (evaluators, evaluator_versions).
//
// A ref ("<id>@<version>") resolves to the spec of the highest-precedence source that defines it.
// With a database, the built-in and file specs are registered at startup (insert-only), so the
// database holds every version a decision, a calibration or a replay can refer to; a version
// that is already stored keeps its stored spec (versions are immutable) and the registry reports
// the mismatch as a warning. Dashboard-authored versions (P4 writes) join through Reload.
package evaluators

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/store"
)

// Origins of an evaluator (EvaluatorMeta.origin).
const (
	OriginBuiltin   = store.EvaluatorOriginBuiltin
	OriginFile      = store.EvaluatorOriginFile
	OriginDashboard = store.EvaluatorOriginDashboard
)

// Sources of the spec the registry serves for a version (Version.Source).
const (
	SourceBuiltin  = "builtin"
	SourceFile     = "file"
	SourceDatabase = "database"
)

// Version is one registered evaluator version.
type Version struct {
	Spec   *decision.Spec
	Hashes decision.Hashes
	// Source is where the served spec came from: builtin, file or database.
	Source string
	// File is the file a file version was read from (base name).
	File      string
	CreatedBy string
	// CreatedAt is the stored creation time; zero for a version that is not in the database.
	CreatedAt time.Time
	Changelog string
}

// Ref returns "<id>@<version>".
func (v *Version) Ref() string { return v.Spec.Ref() }

// Evaluator is one evaluator id with its versions.
type Evaluator struct {
	ID       string
	Name     string
	Category string
	// Origin is builtin, file or dashboard: the source that created the id.
	Origin        string
	LatestVersion int
	Archived      bool
	// CreatedAt and UpdatedAt are the stored times; zero without a database.
	CreatedAt time.Time
	UpdatedAt time.Time
	// Versions are ascending by version.
	Versions []*Version
}

// Latest returns the newest version.
func (e *Evaluator) Latest() *Version {
	for i := len(e.Versions) - 1; i >= 0; i-- {
		if e.Versions[i].Spec.Version == e.LatestVersion {
			return e.Versions[i]
		}
	}
	return e.Versions[len(e.Versions)-1]
}

// Version returns version v.
func (e *Evaluator) Version(v int) (*Version, bool) {
	for _, ver := range e.Versions {
		if ver.Spec.Version == v {
			return ver, true
		}
	}
	return nil, false
}

// Store is the database side of the registry; *store.PostgresStore implements it.
type Store interface {
	UpsertEvaluatorVersions(ctx context.Context, channel string, items []store.EvaluatorUpsert) ([]store.EvaluatorUpsertResult, error)
	ListEvaluators(ctx context.Context) ([]store.EvaluatorRecord, error)
	ListEvaluatorVersions(ctx context.Context) ([]store.EvaluatorVersionRecord, error)
	EvaluatorsFingerprint(ctx context.Context) (string, error)
}

// Config configures a Registry.
type Config struct {
	// Dir is TRUTHTABLE_EVALUATORS_DIR. Empty, or a directory that does not exist, means no
	// file evaluators.
	Dir string
	// Store is the database; nil runs on built-ins and files only.
	Store Store
	// Channel is the NOTIFY channel of registrations (store.ConfigChannel when empty).
	Channel string
	Logger  *slog.Logger
}

// Registry resolves evaluator refs to specs. It is safe for concurrent use: readers see an
// immutable snapshot that Reload replaces atomically.
type Registry struct {
	cfg  Config
	log  *slog.Logger
	snap atomic.Pointer[snapshot]
	// static is the built-in layer overlaid with the file layer, read once.
	static      []*Version
	builtinIDs  map[string]bool
	staticWarns []string
	// conflicts are the refs whose stored spec differs from the static one (already reported).
	conflicts map[string]bool
	reloadMu  sync.Mutex
}

type snapshot struct {
	specs       map[string]*decision.Spec
	evaluators  map[string]*Evaluator
	refs        []string
	warnings    []string
	fingerprint string
}

// Builtins returns a registry of the built-in templates only (no files, no database).
func Builtins() (*Registry, error) {
	return New(context.Background(), Config{})
}

// New reads the built-in templates and the evaluator files, registers them in the database when
// there is one, and loads every stored version. A malformed file is skipped with a warning; a
// malformed built-in template or an unreachable database is an error.
func New(ctx context.Context, cfg Config) (*Registry, error) {
	r := &Registry{cfg: cfg, log: cfg.Logger, builtinIDs: map[string]bool{}, conflicts: map[string]bool{}}
	if r.log == nil {
		r.log = slog.Default()
	}
	builtins, err := readDir(contracts.FS, "evaluators", SourceBuiltin, true)
	if err != nil {
		return nil, err
	}
	for _, v := range builtins {
		r.builtinIDs[v.Spec.ID] = true
	}
	files, warns := r.readFiles()
	r.static, r.staticWarns = overlay(nil, builtins, files)
	r.staticWarns = append(warns, r.staticWarns...)

	if cfg.Store != nil {
		syncWarns, err := r.register(ctx)
		if err != nil {
			return nil, err
		}
		r.staticWarns = append(r.staticWarns, syncWarns...)
	}
	if _, err := r.Reload(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// readFiles reads the evaluator files. Errors are warnings: the policy refuses a ref that does
// not resolve, which names the problem where it matters.
func (r *Registry) readFiles() ([]*Version, []string) {
	if r.cfg.Dir == "" {
		return nil, nil
	}
	st, err := os.Stat(r.cfg.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []string{fmt.Sprintf("evaluators directory %s: %v", r.cfg.Dir, err)}
	}
	if !st.IsDir() {
		return nil, []string{fmt.Sprintf("evaluators directory %s is not a directory", r.cfg.Dir)}
	}
	var warns []string
	entries, err := os.ReadDir(r.cfg.Dir)
	if err != nil {
		return nil, []string{fmt.Sprintf("evaluators directory %s: %v", r.cfg.Dir, err)}
	}
	var out []*Version
	seen := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(r.cfg.Dir, e.Name())) // #nosec G304 -- operator-configured directory
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: %v (skipped)", e.Name(), err))
			continue
		}
		v, err := newVersion(b, SourceFile)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: %v (skipped)", e.Name(), err))
			continue
		}
		v.File = e.Name()
		if first, dup := seen[v.Ref()]; dup {
			warns = append(warns, fmt.Sprintf("%s: %s is already defined by %s (skipped)", e.Name(), v.Ref(), first))
			continue
		}
		seen[v.Ref()] = e.Name()
		out = append(out, v)
	}
	return out, warns
}

// readDir reads <id>.v<version>.json specs from an embedded directory.
func readDir(fsys fs.FS, dir, source string, strictNames bool) ([]*Version, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []*Version
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		v, err := newVersion(b, source)
		if err != nil {
			return nil, fmt.Errorf("evaluator %s: %w", e.Name(), err)
		}
		if want := fmt.Sprintf("%s.v%d.json", v.Spec.ID, v.Spec.Version); strictNames && e.Name() != want {
			return nil, fmt.Errorf("evaluator %s: file name must be %s", e.Name(), want)
		}
		if seen[v.Ref()] {
			return nil, fmt.Errorf("evaluator %s is defined twice", v.Ref())
		}
		seen[v.Ref()] = true
		v.File = e.Name()
		out = append(out, v)
	}
	return out, nil
}

// newVersion parses a spec document and runs the hard rules that need no route (SPEC §2.8,
// P4-PLAN D54): a version that breaks one is excluded, and the caller reports why. References to
// other evaluators are checked where a route attaches them (policy check, Studio validate).
func newVersion(doc []byte, source string) (*Version, error) {
	spec, err := decision.ParseSpec(doc)
	if err != nil {
		return nil, err
	}
	if errs := decision.HardErrors(decision.LintSpec(spec, decision.LintOptions{})); len(errs) > 0 {
		return nil, hardRuleError(spec.Ref(), errs)
	}
	h, err := spec.Hashes()
	if err != nil {
		return nil, err
	}
	v := &Version{Spec: spec, Hashes: h, Source: source}
	var meta struct {
		Changelog string `json:"changelog"`
	}
	if json.Unmarshal(doc, &meta) == nil {
		v.Changelog = meta.Changelog
	}
	return v, nil
}

// hardRuleError names the hard rules a spec breaks: the first finding in full, then a count.
func hardRuleError(ref string, errs []decision.Lint) error {
	first := errs[0]
	msg := fmt.Sprintf("%s breaks the hard rule %s at %s: %s", ref, first.Code, first.Path, first.Message)
	if n := len(errs) - 1; n == 1 {
		msg += " (and 1 more)"
	} else if n > 1 {
		msg += fmt.Sprintf(" (and %d more)", n)
	}
	return errors.New(msg)
}

// overlay merges layers in increasing precedence: a later layer's version replaces an earlier
// one with the same ref. A replacement with a different spec is reported.
func overlay(base []*Version, layers ...[]*Version) ([]*Version, []string) {
	byRef := map[string]*Version{}
	var order []string
	var warns []string
	put := func(v *Version) {
		ref := v.Ref()
		if old, ok := byRef[ref]; ok {
			if old.Hashes.SpecSHA256 != v.Hashes.SpecSHA256 {
				warns = append(warns, fmt.Sprintf("%s: the %s spec replaces a different %s spec", ref, describe(v), describe(old)))
			}
		} else {
			order = append(order, ref)
		}
		byRef[ref] = v
	}
	for _, v := range base {
		put(v)
	}
	for _, layer := range layers {
		for _, v := range layer {
			put(v)
		}
	}
	out := make([]*Version, 0, len(order))
	for _, ref := range order {
		out = append(out, byRef[ref])
	}
	return out, warns
}

func describe(v *Version) string {
	switch v.Source {
	case SourceFile:
		return "file " + v.File
	case SourceDatabase:
		return "stored"
	}
	return "built-in"
}

// register adds the built-in and file versions to the database (insert-only).
func (r *Registry) register(ctx context.Context) ([]string, error) {
	items := make([]store.EvaluatorUpsert, 0, len(r.static))
	for _, v := range r.static {
		spec := v.Spec
		origin, by := OriginFile, "file:"+v.File
		if r.builtinIDs[spec.ID] {
			origin = OriginBuiltin
		}
		if v.Source == SourceBuiltin {
			by = "system"
		}
		doc, err := json.Marshal(spec)
		if err != nil {
			return nil, err
		}
		items = append(items, store.EvaluatorUpsert{
			Evaluator: store.EvaluatorRecord{ID: spec.ID, Name: spec.Name, Category: spec.Category, Origin: origin},
			Version: store.EvaluatorVersionRecord{
				EvaluatorID: spec.ID, Version: spec.Version, Spec: doc, SpecSHA256: v.Hashes.SpecSHA256,
				QuestionSHA256: v.Hashes.QuestionSHA256, CalibrationKey: v.Hashes.CalibrationKey, CreatedBy: by,
				Changelog: v.Changelog,
			},
		})
	}
	results, err := r.cfg.Store.UpsertEvaluatorVersions(ctx, r.cfg.Channel, items)
	if err != nil {
		return nil, fmt.Errorf("evaluators: register: %w", err)
	}
	var warns []string
	for i, res := range results {
		if !res.Inserted && res.StoredSHA256 != items[i].Version.SpecSHA256 {
			r.conflicts[res.Ref] = true
			warns = append(warns, fmt.Sprintf("%s: the stored version differs from the %s spec; stored versions are immutable, so the stored spec is used (give a changed spec a new version)",
				res.Ref, describe(r.static[i])))
		}
	}
	return warns, nil
}

// Reload re-reads the database (dashboard versions, archive flags) and swaps the snapshot. It
// reports whether anything changed. Without a database it only builds the snapshot once.
func (r *Registry) Reload(ctx context.Context) (bool, error) {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	if r.cfg.Store == nil {
		if r.snap.Load() != nil {
			return false, nil
		}
		r.swap(r.build(r.static, nil, nil, ""))
		return true, nil
	}
	fp, err := r.cfg.Store.EvaluatorsFingerprint(ctx)
	if err != nil {
		return false, fmt.Errorf("evaluators: reload: %w", err)
	}
	if cur := r.snap.Load(); cur != nil && cur.fingerprint == fp {
		return false, nil
	}
	recs, err := r.cfg.Store.ListEvaluators(ctx)
	if err != nil {
		return false, fmt.Errorf("evaluators: reload: %w", err)
	}
	rows, err := r.cfg.Store.ListEvaluatorVersions(ctx)
	if err != nil {
		return false, fmt.Errorf("evaluators: reload: %w", err)
	}
	var stored []*Version
	var warns []string
	for _, row := range rows {
		v, err := newVersion(row.Spec, SourceDatabase)
		if err == nil && v.Ref() != row.Ref() {
			err = fmt.Errorf("the stored spec is %s", v.Ref())
		}
		if err != nil {
			warns = append(warns, fmt.Sprintf("stored %s: %v (skipped)", row.Ref(), err))
			continue
		}
		v.CreatedBy, v.CreatedAt, v.Changelog = row.CreatedBy, row.CreatedAt, row.Changelog
		stored = append(stored, v)
	}
	r.swap(r.build(stored, recs, warns, fp))
	return true, nil
}

// swap installs a snapshot and logs the warnings the previous one did not have, so a version
// that a reload excludes (a stored spec that breaks a hard rule) is reported once.
func (r *Registry) swap(next *snapshot) {
	prev := r.snap.Swap(next)
	for _, w := range next.warnings {
		if prev == nil || !slices.Contains(prev.warnings, w) {
			r.log.Warn("evaluators: " + w)
		}
	}
}

// build assembles a snapshot from the static layer and the stored versions and rows.
func (r *Registry) build(stored []*Version, recs []store.EvaluatorRecord, warns []string, fp string) *snapshot {
	versions, overlayWarns := overlay(r.static, stored)
	// register() already explained the stored versions that differ from their static spec.
	for _, w := range overlayWarns {
		if ref, _, _ := strings.Cut(w, ":"); !r.conflicts[ref] {
			warns = append(warns, w)
		}
	}
	s := &snapshot{specs: map[string]*decision.Spec{}, evaluators: map[string]*Evaluator{}, fingerprint: fp}
	s.warnings = append(slices.Clone(r.staticWarns), warns...)
	for _, v := range versions {
		s.specs[v.Ref()] = v.Spec
		e := s.evaluators[v.Spec.ID]
		if e == nil {
			e = &Evaluator{ID: v.Spec.ID, Origin: OriginFile}
			if r.builtinIDs[v.Spec.ID] {
				e.Origin = OriginBuiltin
			} else if v.Source == SourceDatabase {
				e.Origin = OriginDashboard
			}
			s.evaluators[v.Spec.ID] = e
		}
		e.Versions = append(e.Versions, v)
		if v.Spec.Version >= e.LatestVersion {
			e.LatestVersion, e.Name, e.Category = v.Spec.Version, v.Spec.Name, v.Spec.Category
		}
	}
	for _, rec := range recs {
		e := s.evaluators[rec.ID]
		if e == nil {
			continue // an evaluator row without a readable version
		}
		e.Origin, e.Archived, e.CreatedAt, e.UpdatedAt = rec.Origin, rec.Archived, rec.CreatedAt, rec.UpdatedAt
		if rec.LatestVersion >= e.LatestVersion {
			if _, ok := e.Version(rec.LatestVersion); ok {
				e.LatestVersion, e.Name, e.Category = rec.LatestVersion, rec.Name, rec.Category
			}
		}
	}
	for _, e := range s.evaluators {
		sort.Slice(e.Versions, func(i, j int) bool { return e.Versions[i].Spec.Version < e.Versions[j].Spec.Version })
	}
	s.refs = make([]string, 0, len(s.specs))
	for ref := range s.specs {
		s.refs = append(s.refs, ref)
	}
	sort.Strings(s.refs)
	return s
}

func (r *Registry) current() *snapshot { return r.snap.Load() }

// Get returns the spec for ref.
func (r *Registry) Get(ref string) (*decision.Spec, bool) {
	s, ok := r.current().specs[ref]
	return s, ok
}

// Specs returns the specs keyed by ref, as compile() and decide() take them. The map belongs to
// the current snapshot: callers must not modify it.
func (r *Registry) Specs() map[string]*decision.Spec { return r.current().specs }

// Refs lists the registered refs, sorted.
func (r *Registry) Refs() []string { return slices.Clone(r.current().refs) }

// Warnings lists the problems found while loading (skipped files, versions that break a hard
// rule, spec conflicts).
func (r *Registry) Warnings() []string { return slices.Clone(r.current().warnings) }

// Evaluators lists the evaluators by id; archived ones only when includeArchived.
func (r *Registry) Evaluators(includeArchived bool) []*Evaluator {
	s := r.current()
	out := make([]*Evaluator, 0, len(s.evaluators))
	for _, e := range s.evaluators {
		if e.Archived && !includeArchived {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Evaluator returns one evaluator by id (archived ones included).
func (r *Registry) Evaluator(id string) (*Evaluator, bool) {
	e, ok := r.current().evaluators[id]
	return e, ok
}

// Version returns one version of an evaluator.
func (r *Registry) Version(id string, version int) (*Version, bool) {
	e, ok := r.Evaluator(id)
	if !ok {
		return nil, false
	}
	return e.Version(version)
}

// IsBuiltin reports whether id is a built-in template id.
func (r *Registry) IsBuiltin(id string) bool { return r.builtinIDs[id] }

// VersionRef formats "<id>@<version>".
func VersionRef(id string, version int) string { return id + "@" + strconv.Itoa(version) }

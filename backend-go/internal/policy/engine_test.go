package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truthtable/backend-go/internal/policy/policytest"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

const (
	fileA = "version: 1\ndefaults: {mode: flag}\n"
	fileB = "version: 1\ndefaults: {mode: flag, deadline_ms: 900}\n"
	fileC = "version: 1\ndefaults: {mode: flag, deadline_ms: 1200}\n"
	dashY = "version: 1\ndefaults: {mode: observe}\n"
)

// backend is a version store under test with its NOTIFY channel.
type backend struct {
	store   VersionStore
	channel string
}

// backends returns the in-memory store and, when TEST_DATABASE_URL is set, Postgres.
func backends(t *testing.T) map[string]func(t *testing.T) backend {
	return map[string]func(t *testing.T) backend{
		"memory": func(*testing.T) backend { return backend{store: policytest.New()} },
		"postgres": func(t *testing.T) backend {
			schema := storetest.New(t)
			pg, err := store.NewPostgresStore(context.Background(), schema.URL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pg.Close)
			return backend{store: pg, channel: store.ScopedConfigChannel(schema.Name)}
		},
	}
}

// forEachBackend runs f against every backend.
func forEachBackend(t *testing.T, f func(t *testing.T, b backend)) {
	for name, mk := range backends(t) {
		t.Run(name, func(t *testing.T) { f(t, mk(t)) })
	}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type events struct {
	mu      sync.Mutex
	updates []Update
	errs    []PolicyError
}

func (e *events) update(u Update) { e.mu.Lock(); e.updates = append(e.updates, u); e.mu.Unlock() }
func (e *events) failed(p PolicyError) {
	e.mu.Lock()
	e.errs = append(e.errs, p)
	e.mu.Unlock()
}

func (e *events) counts() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.updates), len(e.errs)
}

type fixture struct {
	path string
	ev   *events
	b    backend
}

func newFixture(t *testing.T, b backend, content string) *fixture {
	t.Helper()
	f := &fixture{path: filepath.Join(t.TempDir(), "policies.yaml"), ev: &events{}, b: b}
	if content != "" {
		f.write(t, content)
	}
	return f
}

func (f *fixture) write(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(f.path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) config(t *testing.T) EngineConfig {
	return EngineConfig{
		File: f.path, Env: env(t), Store: f.b.store, Channel: f.b.channel,
		OnUpdate: f.ev.update, OnError: f.ev.failed, Logger: quiet,
	}
}

func (f *fixture) engine(t *testing.T, mut ...func(*EngineConfig)) *Engine {
	t.Helper()
	cfg := f.config(t)
	for _, m := range mut {
		m(&cfg)
	}
	e, err := NewEngine(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func versions(t *testing.T, e *Engine) []VersionMeta {
	t.Helper()
	vs, _, err := e.Versions(context.Background(), 200, "")
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

func TestEngineFileManaged(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, backend{}, "")
	e := f.engine(t)
	st := e.State()
	if st.Policy.Source != SourceBuiltin || st.Version.Source != SourceBootstrap || st.Version.ID != 1 || st.File.Status != FileMissing || st.Persisted {
		t.Fatalf("boot state %+v %+v", st.Version, st.File)
	}
	f.write(t, fileA)
	if err := e.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	st = e.State()
	if st.Version.ID != 2 || st.Policy.Default.Mode != "flag" || st.File.Status != FileOK || st.File.SHA256 == nil || st.Policy.Label(st.Policy.Default) != "default@v2" {
		t.Fatalf("after file %+v %+v", st.Version, st.File)
	}
	f.write(t, "version: 1\ndefaults: {mode: nope}\n")
	for range 2 {
		if err := e.Reload(ctx); err != nil {
			t.Fatal(err)
		}
	}
	st = e.State()
	if st.Version.ID != 2 || st.File.Status != FileInvalid || len(st.File.Errors) != 1 || st.File.Errors[0].Path != "defaults.mode" {
		t.Fatalf("invalid file %+v", st.File)
	}
	if u, errs := f.ev.counts(); u != 1 || errs != 1 || f.ev.errs[0].Source != ErrorSourceFile {
		t.Errorf("events: %d updates, %d errors %+v", u, errs, f.ev.errs)
	}
	if s := e.Status(); s.FileStatus != FileInvalid || s.FileError == nil || s.Editable || s.Persisted || *s.Version != 2 {
		t.Errorf("status %+v", s)
	}

	for name, err := range map[string]error{
		"save":     errOf(e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: `"v2"`})),
		"rollback": errOf(e.Rollback(ctx, 1, "me", "")),
		"adopt":    errOf(e.AdoptFile(ctx, "me", "")),
	} {
		if !errors.Is(err, ErrFileManaged) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if vs := versions(t, e); len(vs) != 1 || vs[0].ID != 2 {
		t.Errorf("versions %+v", vs)
	}
	if _, _, err := e.Versions(ctx, 10, "x"); !errors.Is(err, store.ErrBadCursor) {
		t.Errorf("cursor: %v", err)
	}
	if v, err := e.Version(ctx, 2); err != nil || v.YAML != fileA {
		t.Errorf("version 2: %v", err)
	}
	if _, err := e.Version(ctx, 1); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("version 1: %v", err)
	}
	view := e.View(e.State(), true)
	if view.Editable || view.EditableReason == nil || *view.EditableReason != ReasonFileManaged || view.ETag != `"v2"` || len(view.EffectiveRoutes) != 1 {
		t.Errorf("view %+v", view)
	}

	// An invalid file at boot is fatal without a database.
	bad := newFixture(t, backend{}, "version: 1\ndefaults: {mode: nope}\n")
	if _, err := NewEngine(ctx, bad.config(t)); err == nil || !strings.Contains(err.Error(), bad.path) {
		t.Errorf("boot with an invalid file: %v", err)
	}
	if _, err := NewEngine(ctx, EngineConfig{}); err == nil {
		t.Error("a resolver is required")
	}
}

func errOf(_ *State, err error) error { return err }

// TestEngineFileVersionsAreDeduplicated boots several engines at once on one database: the file
// becomes exactly one version, under the policy write lock.
func TestEngineFileVersionsAreDeduplicated(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		f := newFixture(t, b, fileA)
		var wg sync.WaitGroup
		engines := make([]*Engine, 6)
		errs := make([]error, len(engines))
		for i := range engines {
			wg.Add(1)
			go func() {
				defer wg.Done()
				engines[i], errs[i] = NewEngine(context.Background(), f.config(t))
			}()
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		vs := versions(t, engines[0])
		if len(vs) != 1 || vs[0].Source != SourceFile || vs[0].Author != "file" {
			t.Fatalf("versions %+v", vs)
		}
		for _, e := range engines {
			st := e.State()
			if st.Version.ID != vs[0].ID || st.File.Status != FileOK || !st.Persisted || st.Policy.Path != f.path {
				t.Errorf("engine state %+v %+v", st.Version, st.File)
			}
		}
		// A restart with the same file writes nothing.
		again := f.engine(t)
		if len(versions(t, again)) != 1 {
			t.Error("restart inserted a version")
		}
	})
}

func TestEngineBootstrapThenFile(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		f := newFixture(t, b, "")
		e := f.engine(t)
		st := e.State()
		if st.Version.Source != SourceBootstrap || st.Version.Author != "system" || st.File.Status != FileMissing || st.Policy.Default.Mode != "observe" {
			t.Fatalf("bootstrap %+v %+v", st.Version, st.File)
		}
		f.write(t, fileA)
		if err := e.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		st = e.State()
		if st.Version.Source != SourceFile || st.File.Status != FileOK || st.Policy.Default.Mode != "flag" || len(versions(t, e)) != 2 {
			t.Fatalf("after the file appeared %+v %+v", st.Version, st.File)
		}
		// Deleting the file keeps the version and reports it missing.
		if err := os.Remove(f.path); err != nil {
			t.Fatal(err)
		}
		if err := e.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		if st := e.State(); st.File.Status != FileMissing || st.Policy.Default.Mode != "flag" {
			t.Errorf("file removed %+v", st.File)
		}
		// A fresh database with an invalid file cannot start.
		bad := newFixture(t, b, "version: 7\n")
		cfg := bad.config(t)
		cfg.Store = backends(t)["memory"](t).store
		if _, err := NewEngine(ctx, cfg); err == nil {
			t.Error("an invalid file on a fresh database must fail")
		}
	})
}

func TestEngineSaveWithIfMatch(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		f := newFixture(t, b, fileA)
		e := f.engine(t)
		v1 := e.State().Version.ID
		etag := e.State().ETag()

		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, Author: "admin"}); !errors.Is(err, ErrPreconditionRequired) {
			t.Errorf("no If-Match: %v", err)
		}
		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: `"v999"`}); !errors.Is(err, ErrStaleVersion) {
			t.Errorf("stale: %v", err)
		}
		_, err := e.Save(ctx, SaveRequest{Source: Source{YAML: "version: 1\ndefaults: {mode: bogus}\n"}, IfMatch: etag})
		if fe := FieldErrors(err); len(fe) != 1 || fe[0].Path != "defaults.mode" {
			t.Errorf("invalid: %v", err)
		}
		if _, err := e.Save(ctx, SaveRequest{Source: Source{}, IfMatch: etag}); !errors.Is(err, ErrBadSource) {
			t.Errorf("empty source: %v", err)
		}
		st, err := e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: etag, Author: "admin", Message: "quieter"})
		if err != nil {
			t.Fatal(err)
		}
		if st.Version.ID <= v1 || st.Version.Source != SourceDashboard || st.Version.Author != "admin" || st.Version.Message != "quieter" ||
			e.Current().Default.Mode != "observe" || e.Current().Version != st.Version.ID || st.File.Status != FileOK {
			t.Fatalf("saved %+v file %+v", st.Version, st.File)
		}
		// The old ETag is now stale; the doc form, unquoted and * tags work.
		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: fileB}, IfMatch: etag}); !errors.Is(err, ErrStaleVersion) {
			t.Errorf("reused etag: %v", err)
		}
		doc := json.RawMessage(`{"version":1,"defaults":{"mode":"flag","deadline_ms":700}}`)
		st, err = e.Save(ctx, SaveRequest{Source: Source{Doc: doc}, IfMatch: strings.Trim(st.ETag(), `"`), Author: "admin"})
		if err != nil || e.Current().Default.DeadlineMs != 700 || !strings.Contains(st.Version.YAML, "deadline_ms: 700") {
			t.Fatalf("doc save: %v", err)
		}
		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: fileC}, IfMatch: "*", Author: "admin"}); err != nil {
			t.Errorf("If-Match *: %v", err)
		}
		if u, _ := f.ev.counts(); u != 3 {
			t.Errorf("%d updates", u)
		}
		if vs := versions(t, e); len(vs) != 4 || vs[0].ID != e.State().Version.ID {
			t.Errorf("versions %+v", vs)
		}
		view := e.View(e.State(), false)
		if view.Editable || *view.EditableReason != ReasonAdminRequired {
			t.Errorf("non-admin view %+v", view.EditableReason)
		}
		if view := e.View(e.State(), true); !view.Editable || view.EditableReason != nil {
			t.Errorf("admin view %+v", view.EditableReason)
		}
	})
}

// TestEngineFileDivergence: a file change after a dashboard edit is not applied; adopting the file
// resolves it, and later file changes apply again.
func TestEngineFileDivergence(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		f := newFixture(t, b, fileA)
		e := f.engine(t)
		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: e.State().ETag(), Author: "admin"}); err != nil {
			t.Fatal(err)
		}
		dashID := e.State().Version.ID
		f.write(t, fileB)
		if err := e.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		st := e.State()
		if st.File.Status != FileDiverged || st.Version.ID != dashID || e.Current().Default.Mode != "observe" || len(versions(t, e)) != 2 {
			t.Fatalf("diverged: %+v %+v", st.File, st.Version)
		}
		if w := e.View(st, true).Warnings; len(w) != 1 || !strings.Contains(w[0], "adopt the file") {
			t.Errorf("warnings %v", w)
		}
		// Another replica booting now agrees.
		if other := f.engine(t); other.State().File.Status != FileDiverged || other.State().Version.ID != dashID {
			t.Errorf("replica %+v", other.State().File)
		}

		st, err := e.AdoptFile(ctx, "admin", "")
		if err != nil {
			t.Fatal(err)
		}
		if st.File.Status != FileOK || st.Version.Source != SourceAdoptFile || !strings.HasPrefix(st.Version.Message, "Adopted ") ||
			e.Current().Default.DeadlineMs != 900 || e.Current().Path != f.path {
			t.Fatalf("adopted %+v %+v", st.Version, st.File)
		}
		// Adopting again changes nothing.
		if again, err := e.AdoptFile(ctx, "admin", ""); err != nil || again.Version.ID != st.Version.ID {
			t.Errorf("second adopt %v %+v", err, again.Version)
		}
		// The next file change applies: the last file version is newer than the dashboard edit.
		f.write(t, fileC)
		if err := e.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		if st := e.State(); st.File.Status != FileOK || st.Version.Source != SourceFile || e.Current().Default.DeadlineMs != 1200 {
			t.Fatalf("after adopt %+v %+v", st.Version, st.File)
		}
		// A dashboard edit identical to a later file is applied as that file (not diverged).
		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: e.State().ETag(), Author: "admin"}); err != nil {
			t.Fatal(err)
		}
		f.write(t, "# same content as the dashboard edit\n"+dashY)
		if err := e.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		if st := e.State(); st.File.Status != FileOK || st.Version.Source != SourceFile {
			t.Errorf("identical content %+v %+v", st.Version, st.File)
		}
		// Adopting a missing or invalid file fails.
		f.write(t, "version: 1\ndefaults: {mode: nope}\n")
		if _, err := e.AdoptFile(ctx, "admin", ""); FieldErrors(err) == nil {
			t.Errorf("adopt invalid: %v", err)
		}
		if err := os.Remove(f.path); err != nil {
			t.Fatal(err)
		}
		if _, err := e.AdoptFile(ctx, "admin", ""); !errors.Is(err, ErrFileMissing) {
			t.Errorf("adopt missing: %v", err)
		}
	})
}

func TestEngineConfigLock(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		f := newFixture(t, b, fileA)
		locked := func(c *EngineConfig) { c.Lock = true }
		e := f.engine(t, locked)
		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: e.State().ETag()}); !errors.Is(err, ErrLocked) {
			t.Errorf("save: %v", err)
		}
		if _, err := e.Rollback(ctx, 1, "admin", ""); !errors.Is(err, ErrLocked) {
			t.Errorf("rollback: %v", err)
		}
		// A dashboard version from an unlocked replica, then a file change: the file wins.
		unlocked := f.engine(t)
		if _, err := unlocked.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: unlocked.State().ETag(), Author: "admin"}); err != nil {
			t.Fatal(err)
		}
		f.write(t, fileB)
		if err := e.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		st := e.State()
		if st.File.Status != FileOK || st.Version.Source != SourceFile || e.Current().Default.DeadlineMs != 900 || !st.Locked {
			t.Fatalf("locked sync %+v %+v", st.Version, st.File)
		}
		if v := e.View(st, true); v.Editable || *v.EditableReason != ReasonLocked {
			t.Errorf("view %+v", v.EditableReason)
		}
		// Without a file change the file still wins: a dashboard edit from an unlocked replica
		// is superseded on the next reload, and once only.
		if err := unlocked.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := unlocked.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: unlocked.State().ETag(), Author: "admin"}); err != nil {
			t.Fatal(err)
		}
		before := len(versions(t, e))
		for range 2 {
			if err := e.Reload(ctx); err != nil {
				t.Fatal(err)
			}
		}
		st = e.State()
		if st.Version.Source != SourceFile || e.Current().Default.DeadlineMs != 900 || e.Current().Default.Mode != "flag" ||
			len(versions(t, e)) != before+1 {
			t.Fatalf("locked engine after an unlocked edit %+v (%d versions, %d before)", st.Version, len(versions(t, e)), before)
		}
	})
}

// TestEngineConfigLockSupersedesEarlierEdits: turning the lock on after dashboard edits makes the
// unchanged file the active policy again at boot.
func TestEngineConfigLockSupersedesEarlierEdits(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		f := newFixture(t, b, fileA)
		unlocked := f.engine(t)
		if _, err := unlocked.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: unlocked.State().ETag(), Author: "admin"}); err != nil {
			t.Fatal(err)
		}
		locked := f.engine(t, func(c *EngineConfig) { c.Lock = true })
		st := locked.State()
		if st.Version.Source != SourceFile || locked.Current().Default.Mode != "flag" || st.File.Status != FileOK || len(versions(t, locked)) != 3 {
			t.Fatalf("locked boot runs %+v (mode %s)", st.Version, locked.Current().Default.Mode)
		}
		// A second locked replica agrees without writing again.
		if again := f.engine(t, func(c *EngineConfig) { c.Lock = true }); again.State().Version.ID != st.Version.ID || len(versions(t, again)) != 3 {
			t.Errorf("second locked replica %+v", again.State().Version)
		}
	})
}

func TestEngineRollback(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		f := newFixture(t, b, fileA)
		e := f.engine(t)
		v1 := e.State().Version
		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: e.State().ETag(), Author: "admin"}); err != nil {
			t.Fatal(err)
		}
		st, err := e.Rollback(ctx, v1.ID, "admin", "")
		if err != nil {
			t.Fatal(err)
		}
		if st.Version.Source != SourceRollback || st.Version.SHA256 != v1.SHA256 || st.Version.Message != "Roll back to v1" ||
			e.Current().Default.Mode != "flag" || st.Version.ID != 3 || st.File.Status != FileOK {
			t.Fatalf("rollback %+v %+v", st.Version, st.File)
		}
		if again, err := e.Rollback(ctx, v1.ID, "admin", "again"); err != nil || again.Version.ID != 3 {
			t.Errorf("rollback to the active content: %v %+v", err, again.Version)
		}
		if _, err := e.Rollback(ctx, 99, "admin", ""); !errors.Is(err, ErrVersionNotFound) {
			t.Errorf("unknown version: %v", err)
		}
		v, err := e.Version(ctx, 2)
		if err != nil || v.Source != SourceDashboard || v.YAML != dashY {
			t.Errorf("version 2 %+v %v", v, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(v.Doc, &doc); err != nil || doc["defaults"] == nil {
			t.Errorf("doc %s", v.Doc)
		}
		if _, err := e.Version(ctx, 42); !errors.Is(err, ErrVersionNotFound) {
			t.Errorf("version 42: %v", err)
		}
		page, next, err := e.Versions(ctx, 2, "")
		if err != nil || len(page) != 2 || page[0].ID != 3 || next == "" {
			t.Fatalf("page %+v %q %v", page, next, err)
		}
		page, next, err = e.Versions(ctx, 2, next)
		if err != nil || len(page) != 1 || page[0].ID != 1 || next != "" {
			t.Errorf("page 2 %+v %q %v", page, next, err)
		}
	})
}

// insertVersion stores a version as another writer would.
func insertVersion(t *testing.T, b backend, yaml, sha string) {
	t.Helper()
	err := b.store.WithPolicyLock(context.Background(), b.channel, func(tx store.PolicyTx) error {
		head, err := tx.Head(context.Background())
		if err != nil {
			return err
		}
		return tx.Insert(context.Background(), &store.PolicyVersionRecord{Source: store.PolicySourceDashboard, Author: "other",
			Doc: json.RawMessage(`{"version":1}`), YAML: yaml, SHA256: sha, ParentID: headID(head)})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEngineInvalidStoredVersions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		f := newFixture(t, b, fileA)
		e := f.engine(t)
		good := e.State().Version.ID
		// A newer version that no longer validates (an evaluator it names is gone).
		insertVersion(t, b, "version: 1\ndefaults:\n  evaluators: [{ref: vanished@1}]\n", "bad")
		for range 3 {
			if err := e.Reload(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if e.State().Version.ID != good {
			t.Fatal("an invalid version became active")
		}
		if _, errs := f.ev.counts(); errs != 1 || f.ev.errs[0].Source != ErrorSourceDatabase || f.ev.errs[0].Errors[0].Path != "defaults.evaluators[0].ref" {
			t.Fatalf("errors %+v", f.ev.errs)
		}
		bad := good + 1
		if st := e.State(); st.HeadID != bad || st.ETag() != fmt.Sprintf(`"v%d"`, bad) || len(st.Warnings) != 1 ||
			!strings.Contains(st.Warnings[0], "no longer validates") {
			t.Errorf("running state %+v %s %v", st.Version, st.ETag(), st.Warnings)
		}
		// A restart runs the newest valid version and says why.
		restarted := f.engine(t)
		st := restarted.State()
		if st.Version.ID != good || len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "no longer validates") || st.HeadID != bad {
			t.Errorf("restart %+v %v", st.Version, st.Warnings)
		}
		// The served ETag names the newest stored version, so a save can replace the bad one.
		saved, err := restarted.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: st.ETag(), Author: "admin"})
		if err != nil {
			t.Fatalf("save over the invalid version: %v", err)
		}
		if saved.Version.ID != bad+1 || saved.HeadID != bad+1 || len(saved.Warnings) != 0 || restarted.Current().Default.Mode != "observe" {
			t.Errorf("after the save %+v %v", saved.Version, saved.Warnings)
		}
		// Another replica picks the valid version up.
		if err := e.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		if st := e.State(); st.Version.ID != bad+1 || len(st.Warnings) != 0 || st.ETag() != fmt.Sprintf(`"v%d"`, bad+1) {
			t.Errorf("after a valid version %+v %v", st.Version, st.Warnings)
		}
	})
}

// TestEngineSaveMatchingDivergedFile: a dashboard save whose content equals a diverged file
// records that file on top of it; the save answers with that newest version, once.
func TestEngineSaveMatchingDivergedFile(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		ctx := context.Background()
		f := newFixture(t, b, fileA)
		e := f.engine(t)
		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: e.State().ETag(), Author: "admin"}); err != nil {
			t.Fatal(err)
		}
		f.write(t, fileB)
		if err := e.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		if e.State().File.Status != FileDiverged {
			t.Fatalf("not diverged %+v", e.State().File)
		}
		updates, _ := f.ev.counts()
		st, err := e.Save(ctx, SaveRequest{Source: Source{YAML: fileB}, IfMatch: e.State().ETag(), Author: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		vs := versions(t, e)
		if len(vs) != 4 || vs[0].Source != SourceFile || vs[1].Source != SourceDashboard {
			t.Fatalf("versions %+v", vs)
		}
		if st.Version.ID != vs[0].ID || st.ETag() != fmt.Sprintf(`"v%d"`, vs[0].ID) || st.File.Status != FileOK || e.Current().Default.DeadlineMs != 900 {
			t.Errorf("save answered %+v %s %+v", st.Version, st.ETag(), st.File)
		}
		if u, _ := f.ev.counts(); u != updates+1 {
			t.Errorf("%d policy updates for one save", u-updates)
		}
		// The answered ETag is current: the next save needs no reload.
		if _, err := e.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: st.ETag(), Author: "admin"}); err != nil {
			t.Errorf("next save: %v", err)
		}
	})
}

// hideListener strips Listen from a store, so only the poll reloads.
type hideListener struct{ VersionStore }

// TestEngineHotReload: a save on one replica reaches another through NOTIFY (polling off), and
// through the poll when notifications are unavailable.
func TestEngineHotReload(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b backend) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := newFixture(t, b, fileA)
		writer := f.engine(t)
		notified := f.engine(t)
		polled := f.engine(t, func(c *EngineConfig) {
			c.Store, c.ReloadInterval = hideListener{b.store}, 20*time.Millisecond
		})
		go notified.Run(ctx)
		go polled.Run(ctx)
		// Let the listener subscribe and finish its catch-up reload, so the save below can only
		// reach the notify-only engine through a notification.
		time.Sleep(300 * time.Millisecond)
		st, err := writer.Save(ctx, SaveRequest{Source: Source{YAML: dashY}, IfMatch: writer.State().ETag(), Author: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		for name, e := range map[string]*Engine{"notify": notified, "poll": polled} {
			deadline := time.Now().Add(10 * time.Second)
			for e.State().Version.ID != st.Version.ID {
				if time.Now().After(deadline) {
					t.Fatalf("%s: still on v%d", name, e.State().Version.ID)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if e.Current().Default.Mode != "observe" {
				t.Errorf("%s: mode %s", name, e.Current().Default.Mode)
			}
		}
		// A file change is picked up by the poll (every replica watches its own file).
		f.write(t, fileB)
		deadline := time.Now().Add(10 * time.Second)
		for polled.State().File.Status != FileDiverged {
			if time.Now().After(deadline) {
				t.Fatalf("poll did not see the file: %+v", polled.State().File)
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	})
}

func TestEngineReloadErrors(t *testing.T) {
	ctx := context.Background()
	mem := policytest.New()
	f := newFixture(t, backend{store: mem}, fileA)
	e := f.engine(t)
	mem.FailHead = errors.New("connection refused")
	if err := e.Reload(ctx); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("reload: %v", err)
	}
	// Run logs and keeps going; it stops with the context.
	ctx2, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	e2 := f.engine(t, func(c *EngineConfig) { c.ReloadInterval = time.Millisecond })
	go func() { e2.Run(ctx2); close(done) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestEngineValidate(t *testing.T) {
	f := newFixture(t, backend{store: policytest.New()}, fileA)
	e := f.engine(t)
	res, err := e.Validate(Source{YAML: "version: 1\ndefaults: {mode: flag}\nroutes:\n  - {name: pay, match: {}, mode: observe}\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || len(res.Errors) != 0 || len(res.BlockReadiness) != 2 || !strings.Contains(res.Diff, "+routes:") ||
		!strings.HasPrefix(res.Diff, "--- v1\n+++ proposed\n") {
		t.Errorf("validate %+v", res)
	}
	res, err = e.Validate(Source{YAML: "version: 1\ndefaults: {mode: block}\n"})
	if err != nil || res.OK || findError(res.Errors, "defaults.mode") == nil || len(res.BlockReadiness) != 0 || res.Diff == "" {
		t.Errorf("block on a non-enforcing build %+v %v", res, err)
	}
	res, err = e.Validate(Source{Doc: json.RawMessage(`{"version":1,"defaults":{"mode":"flag"}}`)})
	if err != nil || !res.OK || !strings.Contains(res.Diff, "+defaults:") {
		t.Errorf("doc %+v %v", res, err)
	}
	if res, _ := e.Validate(Source{YAML: fileA}); res.Diff != "" {
		t.Errorf("no change, diff %q", res.Diff)
	}
	if _, err := e.Validate(Source{YAML: "x", Doc: json.RawMessage(`{}`)}); !errors.Is(err, ErrBadSource) {
		t.Errorf("both: %v", err)
	}
	// Proposed: previews accept block even on a non-enforcing build, and stored versions.
	if p, err := e.Proposed(context.Background(), Source{YAML: "version: 1\ndefaults: {mode: block}\n"}, nil); err != nil || p.Default.Mode != "block" {
		t.Errorf("proposed %v", err)
	}
	one := int64(1)
	if p, err := e.Proposed(context.Background(), Source{}, &one); err != nil || p.Default.Mode != "flag" {
		t.Errorf("proposed version %v", err)
	}
	if _, err := e.Proposed(context.Background(), Source{YAML: fileA}, &one); !errors.Is(err, ErrBadSource) {
		t.Errorf("both: %v", err)
	}
	two := int64(2)
	if _, err := e.Proposed(context.Background(), Source{}, &two); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("missing version: %v", err)
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	b := "a\nb\nC\nd\ne\nf\ng\nh\ni\nj\nk\n"
	want := "--- x\n+++ y\n@@ -1,6 +1,6 @@\n a\n b\n-c\n+C\n d\n e\n f\n@@ -8,3 +8,4 @@\n h\n i\n j\n+k\n"
	if got := UnifiedDiff("x", "y", a, b); got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	if UnifiedDiff("x", "y", a, a) != "" {
		t.Error("equal texts")
	}
	if got := UnifiedDiff("x", "y", "", "a\n"); got != "--- x\n+++ y\n@@ -0,0 +1 @@\n+a\n" {
		t.Errorf("from empty %q", got)
	}
	if got := UnifiedDiff("x", "y", "a", "b"); !strings.Contains(got, "\\ No newline at end of file") {
		t.Errorf("no newline %q", got)
	}
}

// TestEngineForwardsOtherNotifications: the engine's listener hands non-policy payloads to
// OnNotify, so one connection serves every config consumer.
func TestEngineForwardsOtherNotifications(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mem := policytest.New()
	f := newFixture(t, backend{store: mem}, fileA)
	got := make(chan string, 4)
	e := f.engine(t, func(c *EngineConfig) { c.OnNotify = func(p string) { got <- p } })
	go e.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mem.Notify("evaluator_versions:tone@2")
		select {
		case p := <-got:
			if p != "evaluator_versions:tone@2" {
				t.Fatalf("payload %q", p)
			}
			return
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("no notification forwarded")
		}
	}
}

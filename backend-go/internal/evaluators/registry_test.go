package evaluators

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/backend-go/internal/storetest"
)

func TestBuiltins(t *testing.T) {
	r, err := Builtins()
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"groundedness@1", "contradiction@1", "refusal@1", "pii_secret_leak@1", "factual_claims@1"} {
		s, ok := r.Get(ref)
		if !ok || s.Ref() != ref {
			t.Errorf("%s: not registered", ref)
		}
	}
	if len(r.Refs()) != 11 || !slices.IsSorted(r.Refs()) {
		t.Errorf("refs %v: want the 11 built-in templates, sorted", r.Refs())
	}
	if len(r.Specs()) != len(r.Refs()) {
		t.Error("Specs and Refs disagree")
	}
	if _, ok := r.Get("groundedness@2"); ok {
		t.Error("an unknown version must not resolve")
	}
	g, ok := r.Evaluator("groundedness")
	if !ok || g.Origin != OriginBuiltin || g.LatestVersion != 1 || g.Name != "Groundedness" || g.Category != "grounding" || g.Latest().Source != SourceBuiltin {
		t.Errorf("groundedness %+v", g)
	}
	if len(r.Evaluators(false)) != 11 || len(r.Warnings()) != 0 || !r.IsBuiltin("refusal") || r.IsBuiltin("tone") {
		t.Errorf("evaluators %d warnings %v", len(r.Evaluators(false)), r.Warnings())
	}
	if v, ok := r.Version("groundedness", 1); !ok || v.Hashes.SpecSHA256 == "" || v.Hashes.CalibrationKey == "" {
		t.Errorf("version %+v", v)
	}
	if _, ok := r.Version("nope", 1); ok {
		t.Error("unknown evaluator")
	}
}

const toneSpec = `{"spec_version":"evaluator/v1","id":"tone","version":1,"name":"Tone","category":"quality",
  "state_fields":["assistant_response"],"question":{"type":"noul","instructions":"Is %s rude?","criteria":{"true":"Rude","false":"Polite"}},
  "evidence":{"requires":"none"},"signal":{"invert":false},"outcomes":{"warn_at":0.7},"changelog":"first"}`

func tone(version int, word string) string {
	s := strings.Replace(toneSpec, `"version":1`, `"version":`+string(rune('0'+version)), 1)
	return strings.Replace(s, "%s", word, 1)
}

func TestReadDirRejectsBadEmbeddedFiles(t *testing.T) {
	good, err := readDir(fstest.MapFS{"e/x.txt": {}}, "e", SourceBuiltin, true)
	if err != nil || len(good) != 0 {
		t.Fatalf("empty dir: %v %v", good, err)
	}
	for name, fsys := range map[string]fstest.MapFS{
		"misnamed": {"e/tone.json": {Data: []byte(tone(1, "it"))}},
		"invalid":  {"e/tone.v1.json": {Data: []byte(`{"id":1}`)}},
	} {
		if _, err := readDir(fsys, "e", SourceBuiltin, true); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	vs, err := readDir(fstest.MapFS{"e/tone.v1.json": {Data: []byte(tone(1, "it"))}, "e/README.md": {Data: []byte("x")}}, "e", SourceBuiltin, true)
	if err != nil || len(vs) != 1 || vs[0].Ref() != "tone@1" || vs[0].Changelog != "first" {
		t.Fatalf("%v %v", vs, err)
	}
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFileEvaluators(t *testing.T) {
	groundedness, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "evaluators", "groundedness.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(groundedness), `"warn_at": 0.8,`, `"warn_at": 0.85,`, 1)
	if changed == string(groundedness) {
		t.Fatal("could not change the groundedness template")
	}
	dir := writeFiles(t, map[string]string{
		"tone.json":         tone(1, "it"),
		"tone-2.json":       tone(2, "the answer"),
		"broken.json":       `{"id":`,
		"dup.json":          tone(1, "again"),
		"groundedness.json": changed,
		"notes.txt":         "ignored",
	})
	r, err := New(context.Background(), Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := r.Evaluator("tone")
	if !ok || e.Origin != OriginFile || e.LatestVersion != 2 || len(e.Versions) != 2 || e.Latest().File != "tone-2.json" {
		t.Fatalf("tone %+v", e)
	}
	// Precedence: the file spec replaces the built-in one with the same ref.
	g, _ := r.Get("groundedness@1")
	if g.Outcomes.WarnAt != 0.85 {
		t.Errorf("groundedness warn_at %v: the file must win over the built-in", g.Outcomes.WarnAt)
	}
	if ge, _ := r.Evaluator("groundedness"); ge.Origin != OriginBuiltin || ge.Latest().Source != SourceFile {
		t.Errorf("groundedness origin %s source %s", ge.Origin, ge.Latest().Source)
	}
	warns := strings.Join(r.Warnings(), "\n")
	for _, want := range []string{"broken.json", "tone.json: tone@1 is already defined by dup.json", "groundedness@1: the file groundedness.json spec replaces a different built-in spec"} {
		if !strings.Contains(warns, want) {
			t.Errorf("warnings lack %q:\n%s", want, warns)
		}
	}
	// "dup.json" sorts before "tone.json", so it is the first definition and tone.json is skipped.
	if v, _ := r.Version("tone", 1); v.File != "dup.json" {
		t.Errorf("tone@1 from %s", v.File)
	}

	for _, d := range []string{filepath.Join(dir, "missing"), ""} {
		if r, err := New(context.Background(), Config{Dir: d}); err != nil || len(r.Refs()) != 11 || len(r.Warnings()) != 0 {
			t.Errorf("dir %q: %v %v", d, err, r.Warnings())
		}
	}
	notDir := filepath.Join(dir, "notes.txt")
	if r, err := New(context.Background(), Config{Dir: notDir}); err != nil || !strings.Contains(strings.Join(r.Warnings(), ""), "not a directory") {
		t.Errorf("file as dir: %v", err)
	}
}

func newPostgres(t *testing.T) (*store.PostgresStore, string) {
	t.Helper()
	schema := storetest.New(t)
	s, err := store.NewPostgresStore(context.Background(), schema.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, store.ScopedConfigChannel(schema.Name)
}

// TestDatabasePrecedenceAndIdempotence registers the built-ins and files in Postgres, then checks
// that a second start writes nothing, that a stored version wins over a changed file spec (with a
// warning), and that a dashboard-only version joins on Reload.
func TestDatabasePrecedenceAndIdempotence(t *testing.T) {
	pg, channel := newPostgres(t)
	ctx := context.Background()
	dir := writeFiles(t, map[string]string{"tone.json": tone(1, "it")})

	r, err := New(ctx, Config{Dir: dir, Store: pg, Channel: channel})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings()) != 0 {
		t.Fatalf("warnings %v", r.Warnings())
	}
	rows, _ := pg.ListEvaluatorVersions(ctx)
	if len(rows) != 12 {
		t.Fatalf("%d stored versions, want 11 built-ins + tone@1", len(rows))
	}
	fp, _ := pg.EvaluatorsFingerprint(ctx)
	tv, _ := r.Version("tone", 1)
	if tv.Source != SourceDatabase || tv.CreatedBy != "file:tone.json" || tv.CreatedAt.IsZero() || tv.Changelog != "first" {
		t.Errorf("tone@1 %+v", tv)
	}
	if e, _ := r.Evaluator("tone"); e.Origin != OriginFile || e.UpdatedAt.IsZero() {
		t.Errorf("tone %+v", e)
	}
	if e, _ := r.Evaluator("refusal"); e.Origin != OriginBuiltin || e.Latest().CreatedBy != "system" {
		t.Errorf("refusal %+v", e)
	}

	// Second start with the same inputs: nothing is written.
	if _, err := New(ctx, Config{Dir: dir, Store: pg, Channel: channel}); err != nil {
		t.Fatal(err)
	}
	if fp2, _ := pg.EvaluatorsFingerprint(ctx); fp2 != fp {
		t.Errorf("second start changed the tables: %s -> %s", fp, fp2)
	}

	// A changed file spec for a stored version: the stored spec wins, with one warning.
	dir2 := writeFiles(t, map[string]string{"tone.json": tone(1, "the reply")})
	r2, err := New(ctx, Config{Dir: dir2, Store: pg, Channel: channel})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := r2.Get("tone@1")
	body, _ := json.Marshal(spec)
	if !strings.Contains(string(body), "Is it rude?") {
		t.Errorf("tone@1 is %s: the stored spec must win", body)
	}
	if w := r2.Warnings(); len(w) != 1 || !strings.Contains(w[0], "tone@1: the stored version differs from the file tone.json spec") {
		t.Errorf("warnings %v", w)
	}

	// A dashboard version (stored by another writer) joins on Reload.
	doc := []byte(tone(2, "the text"))
	v, err := newVersion(doc, SourceDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.UpsertEvaluatorVersions(ctx, channel, []store.EvaluatorUpsert{{
		Evaluator: store.EvaluatorRecord{ID: "tone", Name: "Tone", Category: "quality", Origin: OriginDashboard},
		Version: store.EvaluatorVersionRecord{EvaluatorID: "tone", Version: 2, Spec: doc, SpecSHA256: v.Hashes.SpecSHA256,
			QuestionSHA256: v.Hashes.QuestionSHA256, CalibrationKey: v.Hashes.CalibrationKey, CreatedBy: "admin", Changelog: "v2"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("tone@2"); ok {
		t.Fatal("tone@2 visible before Reload")
	}
	changedNow, err := r.Reload(ctx)
	if err != nil || !changedNow {
		t.Fatalf("reload %v %v", changedNow, err)
	}
	if again, _ := r.Reload(ctx); again {
		t.Error("a second Reload without changes reported a change")
	}
	e, _ := r.Evaluator("tone")
	if _, ok := r.Get("tone@2"); !ok || e.LatestVersion != 2 || e.Latest().CreatedBy != "admin" {
		t.Errorf("tone after reload %+v", e)
	}
}

// fakeStore is an in-memory Store for failure paths.
type fakeStore struct {
	Store
	err error
}

func (f fakeStore) UpsertEvaluatorVersions(context.Context, string, []store.EvaluatorUpsert) ([]store.EvaluatorUpsertResult, error) {
	return nil, f.err
}

func TestDatabaseErrorsAreFatal(t *testing.T) {
	if _, err := New(context.Background(), Config{Store: fakeStore{err: os.ErrDeadlineExceeded}}); err == nil {
		t.Error("an unreachable database must fail New")
	}
}

// brokenTone breaks the hard rule unresolved_state_reference: `evidence` is not a state field.
func brokenTone(version int) string {
	return strings.Replace(tone(version, "it"), `"Is it rude?"`, "\"Is it rude about `evidence`?\"", 1)
}

func TestHardRuleViolatorsAreExcluded(t *testing.T) {
	if !strings.Contains(brokenTone(1), "`evidence`") {
		t.Fatal("brokenTone did not change the spec")
	}
	// An embedded built-in that breaks a hard rule fails the start, like a malformed one.
	if _, err := readDir(fstest.MapFS{"e/tone.v1.json": {Data: []byte(brokenTone(1))}}, "e", SourceBuiltin, true); err == nil ||
		!strings.Contains(err.Error(), "tone@1 breaks the hard rule unresolved_state_reference at question.instructions") {
		t.Errorf("embedded violator: %v", err)
	}
	// A file that breaks one is skipped with a warning; the other versions stay.
	dir := writeFiles(t, map[string]string{"tone-1.json": tone(1, "it"), "tone-2.json": brokenTone(2)})
	r, err := New(context.Background(), Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("tone@2"); ok {
		t.Error("tone@2 breaks a hard rule and must not resolve")
	}
	if e, ok := r.Evaluator("tone"); !ok || e.LatestVersion != 1 || len(e.Versions) != 1 {
		t.Errorf("tone %+v", e)
	}
	w := r.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "tone-2.json: tone@2 breaks the hard rule unresolved_state_reference") || !strings.HasSuffix(w[0], "(skipped)") {
		t.Errorf("warnings %v", w)
	}
	// Several broken rules: the first in full, then a count.
	twice := strings.Replace(brokenTone(1), `"outcomes":{"warn_at":0.7}`, `"outcomes":{"warn_at":0.7,"critical_at":0.5}`, 1)
	if _, err := newVersion([]byte(twice), SourceFile); err == nil || !strings.HasSuffix(err.Error(), "(and 1 more)") {
		t.Errorf("two violations: %v", err)
	}
}

// TestStoredHardRuleViolatorIsSkipped stores a version that breaks a hard rule behind the
// registry's back (as an older writer could have): Reload skips it with a warning, logged once
// however often the registry reloads afterwards.
func TestStoredHardRuleViolatorIsSkipped(t *testing.T) {
	pg, channel := newPostgres(t)
	ctx := context.Background()
	var logs bytes.Buffer
	r, err := New(ctx, Config{Store: pg, Channel: channel, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id, doc string) {
		t.Helper()
		if _, err := pg.UpsertEvaluatorVersions(ctx, channel, []store.EvaluatorUpsert{{
			Evaluator: store.EvaluatorRecord{ID: id, Name: "Tone", Category: "quality", Origin: OriginDashboard},
			Version: store.EvaluatorVersionRecord{EvaluatorID: id, Version: 1, Spec: []byte(doc), SpecSHA256: strings.Repeat("a", 64),
				QuestionSHA256: strings.Repeat("b", 64), CalibrationKey: strings.Repeat("c", 64), CreatedBy: "admin", Changelog: "v1"},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	insert("tone", brokenTone(1))
	if changed, err := r.Reload(ctx); err != nil || !changed {
		t.Fatalf("reload %v %v", changed, err)
	}
	if _, ok := r.Get("tone@1"); ok {
		t.Error("a stored version that breaks a hard rule must not resolve")
	}
	if _, ok := r.Evaluator("tone"); ok {
		t.Error("an evaluator without a usable version must not be listed")
	}
	const excluded = "stored tone@1: tone@1 breaks the hard rule unresolved_state_reference"
	if w := strings.Join(r.Warnings(), "\n"); !strings.Contains(w, excluded) {
		t.Errorf("warnings %q", w)
	}
	// Another stored evaluator changes the fingerprint: the reload picks it up, keeps the
	// warning and does not log it again.
	insert("tone_two", strings.Replace(tone(1, "it"), `"id":"tone"`, `"id":"tone_two"`, 1))
	if changed, err := r.Reload(ctx); err != nil || !changed {
		t.Fatalf("second reload %v %v", changed, err)
	}
	if _, ok := r.Get("tone_two@1"); !ok {
		t.Error("tone_two@1 must resolve after the reload")
	}
	if w := strings.Join(r.Warnings(), "\n"); !strings.Contains(w, excluded) {
		t.Errorf("warnings after the second reload %q", w)
	}
	if n := strings.Count(logs.String(), excluded); n != 1 {
		t.Errorf("the exclusion was logged %d times, want once:\n%s", n, logs.String())
	}
}

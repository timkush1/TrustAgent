package vectors

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCompare(t *testing.T) {
	actual := decode(t, `{"a": 1, "b": {"c": [1, {"d": null, "e": "x"}], "f": true}, "g": null, "h": 0.30000000000000004}`)
	matches := []string{
		`{}`,
		`{"a": 1.0000000001}`,
		`{"b": {"c": [1, {}]}}`,
		`{"b": {"c": [1, {"d": null}], "f": true}, "g": null, "h": 0.3}`,
	}
	for _, m := range matches {
		if diffs := Compare(decode(t, m), actual, 1e-9); len(diffs) > 0 {
			t.Errorf("%s: %v", m, diffs)
		}
	}
	mismatches := map[string]string{
		`{"a": 2}`:                      "$.a",
		`{"z": null}`:                   "$.z: missing",
		`{"b": {"c": [1]}}`:             "$.b.c: want 1 elements",
		`{"b": {"c": [1, {"e": "y"}]}}`: `$.b.c[1].e: want "y"`,
		`{"b": {"f": false}}`:           "$.b.f",
		`{"a": "1"}`:                    "$.a",
		`{"a": {}}`:                     "$.a: want an object",
		`{"a": []}`:                     "$.a: want an array",
		`{"g": 0}`:                      "$.g",
	}
	for m, where := range mismatches {
		diffs := Compare(decode(t, m), actual, 1e-9)
		if len(diffs) != 1 || !strings.Contains(diffs[0], where) {
			t.Errorf("%s: diffs %v, want one at %s", m, diffs, where)
		}
	}
}

func TestLoadAndCheck(t *testing.T) {
	fsys := fstest.MapFS{
		"vectors/README.md":            {Data: []byte("# vectors")},
		"vectors/tokens/ok.json":       {Data: []byte(`{"vector":"1","kind":"tokens","name":"ok","status":"frozen","input":{},"expected":{}}`)},
		"vectors/tokens/misnamed.json": {Data: []byte(`{"vector":"2","kind":"canonical","name":"other","status":"done","input":{},"expected":{}}`)},
	}
	vs, err := Load(fsys, "vectors")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].Name != "other" || vs[1].Name != "ok" || vs[1].Dir != "tokens" {
		t.Fatalf("Load = %+v", vs)
	}
	if err := vs[1].Check(); err != nil {
		t.Errorf("ok vector: %v", err)
	}
	err = vs[0].Check()
	for _, want := range []string{`vector is "2"`, `kind "canonical"`, `name "other"`, `status "done"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check() = %v, want it to mention %s", err, want)
		}
	}
	if vs[1].Tol() != DefaultTolerance {
		t.Errorf("Tol() = %v", vs[1].Tol())
	}
	bad := fstest.MapFS{"vectors/tokens/bad.json": {Data: []byte(`{`)}}
	if _, err := Load(bad, "vectors"); err == nil {
		t.Error("Load accepted invalid JSON")
	}
}

func TestRunKinds(t *testing.T) {
	if _, err := Run("nonsense", nil, nil); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("unknown kind: %v", err)
	}
	if _, err := Run("detectors", json.RawMessage(`{}`), nil); err == nil {
		t.Error("a kind without a Go runner must fail Run")
	}
	for kind, k := range Kinds {
		if (k.run == nil) != (k.Skip != "") {
			t.Errorf("kind %s: a runner and a skip reason are mutually exclusive, and one is required", kind)
		}
	}
	out, err := Run("tokens", json.RawMessage(`{"value": "abc", "cpt": 4}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if diffs := Compare(decode(t, `{"narrow": 5, "wide": 0, "est": 2}`), out, 0); len(diffs) > 0 {
		t.Error(diffs)
	}
	if _, err := Run("tokens", json.RawMessage(`{"value": "abc", "cpt": 4, "extra": 1}`), nil); err == nil {
		t.Error("unknown input fields must be rejected")
	}
	gen := GeneratedExpected("compile", decode(t, `{"requests": [{"model": "m", "state": {}, "questions": {"q": {}}}]}`))
	if diffs := Compare(decode(t, `{"requests": [{"model": "m", "state": {}}]}`), gen, 0); len(diffs) > 0 ||
		strings.Contains(show(gen), "questions") {
		t.Errorf("GeneratedExpected kept the questions: %s", show(gen))
	}
}

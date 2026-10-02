package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/truthtable/backend-go/internal/decision/vectors"
)

func TestFormat(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"b": [1, 2.50, "x"], "a": {"y": null, "x": true}}`), &v); err != nil {
		t.Fatal(err)
	}
	if got, want := format(v, 1), `{"a": {"x": true, "y": null}, "b": [1, 2.5, "x"]}`; got != want {
		t.Errorf("format = %s, want %s", got, want)
	}
	long := map[string]any{"ids": []any{}, "text": strings.Repeat("word ", 30)}
	ids := make([]any, 40)
	for i := range ids {
		ids[i] = "item-" + string(rune('a'+i%26))
	}
	long["ids"] = ids
	got := format(long, 1)
	for _, line := range strings.Split(got, "\n") {
		if len(line) > lineWidth && !strings.Contains(line, `"text"`) {
			t.Errorf("line longer than %d: %q", lineWidth, line)
		}
	}
	var back any
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("format produced invalid JSON: %v\n%s", err, got)
	}
	if ok, _ := sameJSON(json.RawMessage(got), long); !ok {
		t.Error("format changed the value")
	}
	if !strings.HasPrefix(got, "{\n    \"ids\": [\n      \"item-a\",") || !strings.HasSuffix(got, "\n  }") {
		t.Errorf("unexpected layout:\n%s", got)
	}
}

const draftVector = `{
  "vector": "1",
  "kind": "tokens",
  "name": "sample",
  "status": "draft",
  "description": "kept as is",
  "input": {"value": "abc",   "cpt": 4.0},
  "expected": null
}
`

func TestRewrite(t *testing.T) {
	var want any = map[string]any{"est": 2.0}
	gen := "cmd/vectorgen@abc"
	out, err := rewrite([]byte(draftVector), fileEdit{expected: &want, generatedBy: &gen})
	if err != nil {
		t.Fatal(err)
	}
	wantText := strings.Replace(strings.Replace(draftVector, `"expected": null`, `"expected": {"est": 2}`, 1),
		`"status": "draft",`, `"status": "draft",`+"\n"+`  "generated_by": "cmd/vectorgen@abc",`, 1)
	if string(out) != wantText {
		t.Errorf("rewrite =\n%s\nwant\n%s", out, wantText)
	}
	frozen := "frozen"
	gen2 := "cmd/vectorgen@def"
	out2, err := rewrite(out, fileEdit{status: &frozen, generatedBy: &gen2})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out2, []byte(`"status": "frozen",`+"\n"+`  "generated_by": "cmd/vectorgen@def",`)) ||
		!bytes.Contains(out2, []byte(`"input": {"value": "abc",   "cpt": 4.0}`)) {
		t.Errorf("second rewrite:\n%s", out2)
	}
	if _, err := rewrite([]byte(`[1]`), fileEdit{}); err == nil {
		t.Error("rewrite accepted a non-object")
	}
	if _, err := rewrite([]byte(`{"a": 1}`), fileEdit{status: &frozen}); err == nil {
		t.Error("rewrite accepted a file without status")
	}
}

// writeContracts builds a minimal contracts directory with one draft vector.
func writeContracts(t *testing.T, vector string) string {
	t.Helper()
	return writeVector(t, t.TempDir(), "tokens", vector)
}

// writeVector writes vector as vectors/<kind>/sample.json under dir and returns dir.
func writeVector(t *testing.T, dir, kind, vector string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "vectors", kind), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vectors", kind, "sample.json"), []byte(vector), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// semanticVector is a hand-written detectors vector: expected is a subset of the output (findings
// is left out) that the reference agrees with.
const semanticVector = `{
  "vector": "1",
  "kind": "detectors",
  "name": "sample",
  "status": "draft",
  "description": "kept as is",
  "input": {
    "detector": "numbers_not_in_evidence",
    "response": "Refunds within 60 days.",
    "evidence": [{"id": "p", "source": "request_system", "text": "Refunds within 30 days."}],
    "user_request": "",
    "system_instructions": "",
    "rules": {}
  },
  "expected": {"ran": true, "fired": true},
  "tolerance": 1e-9
}
`

func TestSemanticVerifyAndFreeze(t *testing.T) {
	dir := writeVector(t, t.TempDir(), "detectors", semanticVector)
	path := filepath.Join(dir, "vectors", "detectors", "sample.json")
	var stdout, stderr bytes.Buffer

	// Verified in every mode, but never written, and not frozen unless its kind is named.
	for _, args := range [][]string{{"-check"}, {"-rev", "test"}, {"-freeze", "-rev", "test"}, {"-kinds", "tokens", "-freeze"}} {
		stderr.Reset()
		if code := run(append([]string{"-contracts", dir}, args...), &stdout, &stderr); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, stderr.String())
		}
		if b, _ := os.ReadFile(path); string(b) != semanticVector {
			t.Fatalf("%v rewrote a semantic vector:\n%s", args, b)
		}
	}
	// Freezing rewrites the file in place and keeps its permissions (a repository source).
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-contracts", dir, "-kinds", "detectors", "-freeze", "-rev", "test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("freeze: exit %d: %s", code, stderr.String())
	}
	b, _ := os.ReadFile(path)
	if want := strings.Replace(semanticVector, `"status": "draft"`, `"status": "frozen"`, 1); string(b) != want {
		t.Fatalf("frozen file:\n%s\nwant only the status changed", b)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("frozen file mode %v, want -rw-------", got)
	}

	// A draft the reference disagrees with fails and is never frozen.
	wrong := strings.Replace(semanticVector, `"fired": true`, `"fired": false`, 1)
	dir = writeVector(t, t.TempDir(), "detectors", wrong)
	path = filepath.Join(dir, "vectors", "detectors", "sample.json")
	stderr.Reset()
	if code := run([]string{"-contracts", dir, "-kinds", "detectors", "-freeze", "-rev", "test"}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "$.fired: want false, got true") {
		t.Fatalf("disagreeing draft: exit %d: %s", code, stderr.String())
	}
	if b, _ := os.ReadFile(path); string(b) != wrong {
		t.Error("vectorgen froze a vector the reference disagrees with")
	}

	// Semantic vectors are written by hand: a null expected is an error, not something to fill in.
	dir = writeVector(t, t.TempDir(), "detectors", strings.Replace(semanticVector, `{"ran": true, "fired": true}`, "null", 1))
	stderr.Reset()
	if code := run([]string{"-contracts", dir, "-rev", "test"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "written by hand") {
		t.Fatalf("null expected: exit %d: %s", code, stderr.String())
	}
}

func TestGenerateCheckFreeze(t *testing.T) {
	dir := writeContracts(t, draftVector)
	path := filepath.Join(dir, "vectors", "tokens", "sample.json")
	var stdout, stderr bytes.Buffer

	if code := run([]string{"-contracts", dir, "-check"}, &stdout, &stderr); code != 1 {
		t.Fatalf("check on a stale vector: exit %d, want 1 (%s)", code, stderr.String())
	}
	if code := run([]string{"-contracts", dir, "-rev", "test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("generate: exit %d: %s", code, stderr.String())
	}
	b, _ := os.ReadFile(path)
	if !bytes.Contains(b, []byte(`"expected": {"est": 2, "narrow": 5, "wide": 0}`)) || !bytes.Contains(b, []byte(`"generated_by": "cmd/vectorgen@test"`)) {
		t.Fatalf("generated file:\n%s", b)
	}
	if code := run([]string{"-contracts", dir, "-check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("check after generate: exit %d: %s", code, stderr.String())
	}
	if code := run([]string{"-contracts", dir, "-kinds", "tokens", "-freeze", "-rev", "test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("freeze: exit %d: %s", code, stderr.String())
	}
	b, _ = os.ReadFile(path)
	if !bytes.Contains(b, []byte(`"status": "frozen"`)) {
		t.Fatalf("frozen file:\n%s", b)
	}

	// A frozen vector whose expected value no longer matches is an error and is left alone.
	tampered := bytes.Replace(b, []byte(`"est": 2`), []byte(`"est": 3`), 1)
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"-contracts", dir, "-rev", "test"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "frozen vector") {
		t.Fatalf("frozen mismatch: exit %d: %s", code, stderr.String())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, tampered) {
		t.Error("vectorgen rewrote a frozen vector")
	}
}

func TestRunUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// A kind without a Go runner (vectors.Kinds Skip) cannot be processed.
	usage := [][]string{{"-kinds", "nonsense"}, {"-check", "-freeze"}, {"extra"}, {"-nope"}}
	for k, info := range vectors.Kinds {
		if info.Skip != "" {
			usage = append(usage, []string{"-kinds", k})
		}
	}
	for _, args := range usage {
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	dir := writeContracts(t, `{"vector": "1", "kind": "tokens", "name": "sample", "status": "draft", "input": {"cpt": 0}, "expected": null}`)
	if code := run([]string{"-contracts", dir, "-rev", "x"}, &stdout, &stderr); code != 1 {
		t.Errorf("a vector the reference rejects: exit %d, want 1", code)
	}
	if code := run([]string{"-contracts", filepath.Join(dir, "missing")}, &stdout, &stderr); code != 1 {
		t.Errorf("missing contracts directory: exit %d, want 1", code)
	}
}

// cost is mechanical (D85): vectorgen writes the calculator's result as expected.
func TestRunCostVector(t *testing.T) {
	dir := writeVector(t, t.TempDir(), "cost", `{
  "vector": "1",
  "kind": "cost",
  "name": "sample",
  "status": "draft",
  "input": {
    "fn": "call_cost", "provider": "openrouter", "model": "typesafe/jev-1.13", "input_tokens": 566, "output_tokens": 0,
    "table": {"version": "v1", "currency": "USD", "entries": [{"match": "typesafe/jev-1.13*", "provider": "openrouter", "input_per_mtok": 0.042, "output_per_mtok": 0}]}
  },
  "expected": null
}
`)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-contracts", dir, "-kinds", "cost", "-rev", "test"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, "vectors", "cost", "sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `"expected": {"cost_source": "computed", "cost_usd": 0.000023772, "price_table_version": "v1"}`
	if !strings.Contains(string(b), want) {
		t.Errorf("generated vector:\n%s\nwant it to contain %s", b, want)
	}
}

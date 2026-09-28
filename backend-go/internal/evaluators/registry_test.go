package evaluators

import (
	"slices"
	"testing"
	"testing/fstest"
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
}

func TestRegistryRejectsBadFiles(t *testing.T) {
	good, err := fromFS(fstest.MapFS{}, ".")
	if err != nil || len(good.Refs()) != 0 {
		t.Fatalf("empty dir: %v %v", good, err)
	}
	spec := `{"spec_version":"evaluator/v1","id":"tone","version":1,"name":"Tone","category":"quality",
	  "state_fields":["assistant_response"],"question":{"type":"noul","instructions":"Is it rude?","criteria":{"true":"Rude","false":"Polite"}},
	  "evidence":{"requires":"none"},"signal":{"invert":false},"outcomes":{"warn_at":0.7}}`
	for name, fsys := range map[string]fstest.MapFS{
		"misnamed": {"e/tone.json": {Data: []byte(spec)}},
		"invalid":  {"e/tone.v1.json": {Data: []byte(`{"id":1}`)}},
	} {
		if _, err := fromFS(fsys, "e"); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	r, err := fromFS(fstest.MapFS{"e/tone.v1.json": {Data: []byte(spec)}, "e/README.md": {Data: []byte("x")}}, "e")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("tone@1"); !ok {
		t.Error("tone@1 not registered")
	}
}

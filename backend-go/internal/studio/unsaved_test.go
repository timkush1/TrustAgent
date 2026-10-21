package studio

import (
	"bytes"
	"strings"
	"testing"

	"github.com/truthtable/contracts"
)

// forkedSpec is the groundedness template renamed, as "Duplicate & edit" would start it.
func forkedSpec(t *testing.T) []byte {
	t.Helper()
	b, err := contracts.FS.ReadFile("evaluators/groundedness.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.Replace(b, []byte(`"id": "groundedness"`), []byte(`"id": "my_grounding"`), 1)
	return bytes.Replace(b, []byte(`"builtin": true`), []byte(`"builtin": false`), 1)
}

func TestCompileUnsavedUsesTheNextVersion(t *testing.T) {
	doc := forkedSpec(t)
	u, err := CompileUnsaved(doc, 3)
	if err != nil {
		t.Fatal(err)
	}
	if u.Spec.Ref() != "my_grounding@4" {
		t.Errorf("ref = %s", u.Spec.Ref())
	}
	h, _ := u.Spec.Hashes()
	if want := "my_grounding@draft:" + h.SpecSHA256[:12]; u.ResultRef != want {
		t.Errorf("result ref = %s, want %s", u.ResultRef, want)
	}
	fresh, err := CompileUnsaved(doc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Spec.Version != 1 || fresh.ResultRef == u.ResultRef {
		t.Errorf("a new id: version %d, ref %s", fresh.Spec.Version, fresh.ResultRef)
	}
	again, _ := CompileUnsaved(doc, 3)
	if again.ResultRef != u.ResultRef {
		t.Error("the same draft got a different result ref")
	}
	// The question is untouched: same question hash as the spec as written.
	orig, _ := CompileUnsaved(doc, 0)
	oh, _ := orig.Spec.Hashes()
	if oh.QuestionSHA256 != h.QuestionSHA256 {
		t.Error("the question changed with the version")
	}
}

func TestWithMemberKeepsOrderAndBytes(t *testing.T) {
	got, err := withMember([]byte(`{"b": {"z": 1, "a": [2, 1]}, "version": 7, "a": "x"}`), "version", []byte("9"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"b":{"z": 1, "a": [2, 1]},"version":9,"a":"x"}`; string(got) != want {
		t.Errorf("got %s, want %s", got, want)
	}
	got, _ = withMember([]byte(`{"id":"x"}`), "version", []byte("1"))
	if string(got) != `{"id":"x","version":1}` {
		t.Errorf("appended: %s", got)
	}
	got, _ = withMember([]byte(`{"version":1,"version":2}`), "version", []byte("5"))
	if string(got) != `{"version":5}` {
		t.Errorf("repeated: %s", got)
	}
	for _, bad := range []string{`[]`, `"x"`, `{"a":`, ``} {
		if _, err := withMember([]byte(bad), "version", []byte("1")); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if _, err := CompileUnsaved([]byte(`{"id":"x"}`), -1); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Errorf("negative latest: %v", err)
	}
}

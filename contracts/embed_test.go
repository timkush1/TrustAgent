package contracts

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// TestEmbeddedJSONParses checks that every embedded .json file is valid JSON, so a broken
// contract fails the Go build's tests as well as make contracts-check.
func TestEmbeddedJSONParses(t *testing.T) {
	n := 0
	err := fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".json" {
			return err
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return err
		}
		if !json.Valid(b) {
			t.Errorf("%s: invalid JSON", p)
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no JSON files embedded")
	}
}

// TestSchemaIDs checks that every schema's $id is SchemaBaseURI + its file name.
func TestSchemaIDs(t *testing.T) {
	entries, err := fs.ReadDir(FS, "schema")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".schema.json") {
			continue
		}
		b, err := FS.ReadFile("schema/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if want := SchemaBaseURI + e.Name(); s.ID != want {
			t.Errorf("%s: $id = %q, want %q", e.Name(), s.ID, want)
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("no schemas embedded")
	}
}

// TestKeyRegistry checks the registry shape the Go config loader relies on.
func TestKeyRegistry(t *testing.T) {
	b, err := FS.ReadFile("config/keys.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		Version string `json:"version"`
		Keys    []struct {
			Name     string  `json:"name"`
			Default  *string `json:"default"`
			Secret   bool    `json:"secret"`
			Advanced bool    `json:"advanced"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Version != "1" {
		t.Errorf("version = %q, want 1", reg.Version)
	}
	names := map[string]bool{}
	basic := 0
	for _, k := range reg.Keys {
		if names[k.Name] {
			t.Errorf("duplicate key %s", k.Name)
		}
		names[k.Name] = true
		if !k.Advanced {
			basic++
		}
		if k.Secret && k.Default != nil && *k.Default != "" {
			t.Errorf("secret key %s ships a default", k.Name)
		}
	}
	if basic > 15 {
		t.Errorf(".env.example would list %d keys, want at most 15", basic)
	}
}

// TestPatternsCompileInRE2 checks that the regular expressions the Go reference compiles at runtime are
// RE2 syntax (validate.mjs checks the same patterns in JavaScript): every {pattern, flags} rule in rules/,
// compiled as "(?flags)" + pattern, and every string "pattern" keyword in the JSON Schemas.
func TestPatternsCompileInRE2(t *testing.T) {
	for _, dir := range []string{"rules", "schema"} {
		entries, err := fs.ReadDir(FS, dir)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range entries {
			if path.Ext(e.Name()) != ".json" {
				continue
			}
			b, err := FS.ReadFile(dir + "/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			var doc any
			if err := json.Unmarshal(b, &doc); err != nil {
				t.Fatalf("%s/%s: %v", dir, e.Name(), err)
			}
			walkPatterns(doc, "", func(where, pattern, flags string) {
				n++
				src := pattern
				if flags != "" {
					src = "(?" + flags + ")" + pattern
				}
				if _, err := regexp.Compile(src); err != nil {
					t.Errorf("%s/%s %s: %v", dir, e.Name(), where, err)
				}
			})
		}
		if n == 0 {
			t.Errorf("%s: no patterns found", dir)
		}
	}
}

// walkPatterns calls fn for every object with a string "pattern" member, passing its string "flags"
// member (or "").
func walkPatterns(v any, where string, fn func(where, pattern, flags string)) {
	switch x := v.(type) {
	case map[string]any:
		if p, ok := x["pattern"].(string); ok {
			flags, _ := x["flags"].(string)
			fn(where+"/pattern", p, flags)
		}
		for k, c := range x {
			if k != "pattern" {
				walkPatterns(c, where+"/"+k, fn)
			}
		}
	case []any:
		for i, c := range x {
			walkPatterns(c, fmt.Sprintf("%s[%d]", where, i), fn)
		}
	}
}

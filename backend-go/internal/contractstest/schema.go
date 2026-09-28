// Package contractstest validates Go output against the contract JSON Schemas embedded from
// contracts/schema (SPEC §2.4: Go tests validate live responses against the same schemas the
// fixtures and the TypeScript types use). It is imported by tests only.
package contractstest

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/truthtable/contracts"
)

var (
	compileOnce sync.Once
	compiler    *jsonschema.Compiler
	compileErr  error
	mu          sync.Mutex
	compiled    = map[string]*jsonschema.Schema{}
)

func load() (*jsonschema.Compiler, error) {
	compileOnce.Do(func() {
		c := jsonschema.NewCompiler()
		entries, err := fs.ReadDir(contracts.FS, "schema")
		if err != nil {
			compileErr = err
			return
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".schema.json") {
				continue
			}
			b, err := contracts.FS.ReadFile("schema/" + e.Name())
			if err != nil {
				compileErr = err
				return
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
			if err != nil {
				compileErr = err
				return
			}
			if err := c.AddResource(contracts.SchemaBaseURI+e.Name(), doc); err != nil {
				compileErr = err
				return
			}
		}
		compiler = c
	})
	return compiler, compileErr
}

// Schema returns the compiled schema contracts/schema/<name> (for example
// "decision.v1.schema.json"), or a definition inside it ("api.v1.schema.json#/definitions/X").
func Schema(t testing.TB, name string) *jsonschema.Schema {
	t.Helper()
	c, err := load()
	if err != nil {
		t.Fatalf("contract schemas: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if s, ok := compiled[name]; ok {
		return s
	}
	s, err := c.Compile(contracts.SchemaBaseURI + name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	compiled[name] = s
	return s
}

// Validate marshals v to JSON and validates it against the named schema.
func Validate(t testing.TB, name string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal for %s: %v", name, err)
	}
	ValidateJSON(t, name, b)
}

// ValidateJSON validates a JSON document against the named schema.
func ValidateJSON(t testing.TB, name string, doc []byte) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("%s: invalid JSON: %v", name, err)
	}
	if err := Schema(t, name).Validate(inst); err != nil {
		t.Errorf("%s: %v\ndocument: %s", name, err, doc)
	}
}

// Package vectors runs the golden vectors of contracts/vectors against the Go reference in
// internal/decision (SPEC §7.6). The decision tests (TestVectors) and cmd/vectorgen share it: the
// tests compare each vector's expected value with the reference output, and vectorgen writes the
// reference output into the expected value of the mechanical kinds.
//
// The input and expected shape of every kind is documented in contracts/vectors/README.md.
package vectors

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Status values of a vector's lifecycle.
const (
	StatusDraft  = "draft"
	StatusFrozen = "frozen"
)

// DefaultTolerance is the number tolerance when a vector does not set one.
const DefaultTolerance = 1e-9

// Vector is one contracts/vectors/<kind>/<name>.json file (contracts/schema/vector.v1.schema.json).
type Vector struct {
	Vector      string          `json:"vector"`
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	Description string          `json:"description,omitempty"`
	GeneratedBy string          `json:"generated_by,omitempty"`
	Input       json.RawMessage `json:"input"`
	Expected    json.RawMessage `json:"expected"`
	Tolerance   *float64        `json:"tolerance,omitempty"`

	// Dir is the kind directory the file sits in and Path its path in the file system it was
	// loaded from.
	Dir  string `json:"-"`
	Path string `json:"-"`
}

// Tol returns the vector's number tolerance.
func (v *Vector) Tol() float64 {
	if v.Tolerance != nil {
		return *v.Tolerance
	}
	return DefaultTolerance
}

// Check reports file-level problems: the format version, the kind matching its directory, the
// name matching its file name, and the status.
func (v *Vector) Check() error {
	var problems []string
	if v.Vector != "1" {
		problems = append(problems, fmt.Sprintf("vector is %q, want \"1\"", v.Vector))
	}
	if v.Kind != v.Dir {
		problems = append(problems, fmt.Sprintf("kind %q does not match its directory %q", v.Kind, v.Dir))
	}
	if want := strings.TrimSuffix(path.Base(v.Path), ".json"); v.Name != want {
		problems = append(problems, fmt.Sprintf("name %q does not match the file name %q", v.Name, want))
	}
	if v.Status != StatusDraft && v.Status != StatusFrozen {
		problems = append(problems, fmt.Sprintf("status %q is neither draft nor frozen", v.Status))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Load reads every <kind>/<name>.json under root in fsys, sorted by path. Files outside a kind
// directory (README.md) are ignored.
func Load(fsys fs.FS, root string) ([]*Vector, error) {
	var out []*Vector
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".json" || path.Dir(path.Dir(p)) != path.Clean(root) {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		v := &Vector{Dir: path.Base(path.Dir(p)), Path: p}
		if err := json.Unmarshal(b, v); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, v)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

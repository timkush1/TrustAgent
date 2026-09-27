// Package evaluators is the registry of evaluator specs the gate can attach to a route (SPEC §3.1).
//
// S0 serves the built-in templates embedded from contracts/evaluators. Custom specs from
// TRUTHTABLE_EVALUATORS_DIR and dashboard-authored versions in the database join in P3/P4.
package evaluators

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/truthtable/contracts"

	"github.com/truthtable/backend-go/internal/decision"
)

// Registry resolves evaluator refs ("<id>@<version>") to specs. It is read-only after construction
// and safe for concurrent use.
type Registry struct {
	specs map[string]*decision.Spec
}

// Builtins loads every built-in template (contracts/evaluators/<id>.v<version>.json).
func Builtins() (*Registry, error) {
	return fromFS(contracts.FS, "evaluators")
}

func fromFS(fsys fs.FS, dir string) (*Registry, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	r := &Registry{specs: map[string]*decision.Spec{}}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		spec, err := decision.ParseSpec(b)
		if err != nil {
			return nil, fmt.Errorf("evaluator %s: %w", e.Name(), err)
		}
		if want := fmt.Sprintf("%s.v%d.json", spec.ID, spec.Version); e.Name() != want {
			return nil, fmt.Errorf("evaluator %s: file name must be %s", e.Name(), want)
		}
		if _, dup := r.specs[spec.Ref()]; dup {
			return nil, fmt.Errorf("evaluator %s is defined twice", spec.Ref())
		}
		r.specs[spec.Ref()] = spec
	}
	return r, nil
}

// Get returns the spec for ref.
func (r *Registry) Get(ref string) (*decision.Spec, bool) {
	s, ok := r.specs[ref]
	return s, ok
}

// Specs returns the specs keyed by ref, as compile() and decide() take them. The map is shared:
// callers must not modify it.
func (r *Registry) Specs() map[string]*decision.Spec { return r.specs }

// Refs lists the registered refs, sorted.
func (r *Registry) Refs() []string {
	out := make([]string, 0, len(r.specs))
	for ref := range r.specs {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

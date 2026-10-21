package vectors

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/truthtable/backend-go/internal/decision"
)

// lintKind runs the lint vectors through internal/decision/lint.go, the reference that
// frontend-react/src/lib/decision/lint.ts ports (D54).
var lintKind = Kind{run: runLint}

// lintResult is a lint vector's expected value: the sorted distinct codes of the hard-rule errors
// and of the lint warnings.
type lintResult struct {
	Errors []string `json:"errors"`
	Lints  []string `json:"lints"`
}

// runLint validates input.spec, like the TypeScript runner: without route context, or with
// input.attached, the evaluator ids attached to the route (LintOptions.Attached: the D98
// corroboration and implied-evaluator warnings). No registry or threshold overrides. A lint
// vector's spec must be schema-valid, so a schema finding is an error of the vector rather than an
// output.
func runLint(in json.RawMessage, _ fs.FS) (any, error) {
	var x struct {
		Spec     json.RawMessage `json:"spec"`
		Attached *[]string       `json:"attached"`
	}
	dec := json.NewDecoder(bytes.NewReader(in))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&x); err != nil {
		return nil, fmt.Errorf("lint input: %w", err)
	}
	if len(x.Spec) == 0 || string(x.Spec) == "null" {
		return nil, errors.New("lint input: spec is required")
	}
	var opts decision.LintOptions
	if x.Attached != nil {
		opts.Attached = decision.IDSet(*x.Attached...)
	}
	v := decision.ValidateSpec(x.Spec, opts)
	if len(v.Errors) > 0 {
		e := v.Errors[0]
		return nil, fmt.Errorf("lint input: the spec is not schema-valid (%d findings; first at %q: %s)", len(v.Errors), e.Path, e.Message)
	}
	var out lintResult
	out.Errors, out.Lints = decision.LintCodes(v.Lints)
	return out, nil
}

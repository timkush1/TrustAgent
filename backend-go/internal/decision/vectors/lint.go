package vectors

import (
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

// runLint validates input.spec without route context (no registry, attachments or threshold
// overrides), like the TypeScript runner. A lint vector's spec must be schema-valid, so a schema
// finding is an error of the vector rather than an output.
func runLint(in json.RawMessage, _ fs.FS) (any, error) {
	var x struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(in, &x); err != nil {
		return nil, fmt.Errorf("lint input: %w", err)
	}
	if len(x.Spec) == 0 || string(x.Spec) == "null" {
		return nil, errors.New("lint input: spec is required")
	}
	v := decision.ValidateSpec(x.Spec, decision.LintOptions{})
	if len(v.Errors) > 0 {
		e := v.Errors[0]
		return nil, fmt.Errorf("lint input: the spec is not schema-valid (%d findings; first at %q: %s)", len(v.Errors), e.Path, e.Message)
	}
	var out lintResult
	out.Errors, out.Lints = decision.LintCodes(v.Lints)
	return out, nil
}

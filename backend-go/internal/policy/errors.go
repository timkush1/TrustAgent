package policy

import (
	"errors"
	"fmt"
	"strings"
)

// FieldError is one validation finding (api.ts FieldError): a dotted path into the policy
// document ("routes[1].evaluators[0].thresholds.warn_at"), the 1-based line and column of the
// YAML node it refers to (0 when unknown), and a message.
type FieldError struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	Message string `json:"message"`
}

// String renders "path (line L, col C): message".
func (e FieldError) String() string {
	var b strings.Builder
	b.WriteString(e.Path)
	if e.Line > 0 {
		fmt.Fprintf(&b, " (line %d, col %d)", e.Line, e.Col)
	}
	if e.Path != "" || e.Line > 0 {
		b.WriteString(": ")
	}
	b.WriteString(e.Message)
	return b.String()
}

// ValidationError is the error of a policy document that does not validate. Warnings are the
// non-fatal findings of the same check.
type ValidationError struct {
	Errors   []FieldError
	Warnings []FieldError
}

// Error joins the errors: "policy: <path> (line L, col C): <message>; ...".
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Errors))
	for i, fe := range e.Errors {
		parts[i] = fe.String()
	}
	return "policy: " + strings.Join(parts, "; ")
}

// FieldErrors returns the field errors of err when it is (or wraps) a *ValidationError.
func FieldErrors(err error) []FieldError {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Errors
	}
	return nil
}

// Engine errors. The REST layer maps them to SPEC §2.4 codes.
var (
	// ErrFileManaged: without a database the policy file is the only source (409 file_managed).
	ErrFileManaged = errors.New("policy: file-managed (no database)")
	// ErrLocked: TRUTHTABLE_CONFIG_LOCK makes the file authoritative (423 locked).
	ErrLocked = errors.New("policy: locked (TRUTHTABLE_CONFIG_LOCK)")
	// ErrPreconditionRequired: a save without If-Match (428).
	ErrPreconditionRequired = errors.New("policy: If-Match is required")
	// ErrStaleVersion: If-Match names a version that is no longer the newest (412 stale_version).
	ErrStaleVersion = errors.New("policy: the policy changed since it was read")
	// ErrVersionNotFound: no such policy version (404).
	ErrVersionNotFound = errors.New("policy: no such version")
	// ErrFileMissing: adopt-file without a policy file (409 conflict).
	ErrFileMissing = errors.New("policy: the policy file does not exist")
	// ErrBadSource: a request gave neither or both of yaml and doc (400).
	ErrBadSource = errors.New("policy: give exactly one of yaml or doc")
)

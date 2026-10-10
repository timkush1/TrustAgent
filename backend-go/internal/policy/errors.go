package policy

import (
	"errors"
	"fmt"
	"strings"
)

// FieldError is one validation finding (api.ts FieldError): a dotted path into the policy
// document ("routes[1].evaluators[0].thresholds.warn_at"), the 1-based line and column of the
// YAML node it refers to (0 when unknown), and a message. A warning also carries its code (one of
// WarningCodes) and the values its words need (Details), so the dashboard words it by code and
// never parses the message.
type FieldError struct {
	Path    string         `json:"path"`
	Line    int            `json:"line,omitempty"`
	Col     int            `json:"col,omitempty"`
	Message string         `json:"message"`
	Code    string         `json:"code,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// Policy warning codes: every warning the policy check and the engine write carries one, and the
// list is closed (contracts/ui copy policies.warnings.<code> words each one). Details by code:
//
//	no_corroborator:           {route, ref, corroborators[]}
//	implies_unattached:        {route, ref, implies}
//	fail_closed_not_enforcing: {route, mode}
//	sample_rate_not_observe:   {route, mode}
//	trusts_user_context:       {route, mode}
//	client_rules_enforcing:    {route}
//	newest_version_invalid:    {bad_version, active_version}
//	file_diverged:             {path}
//
// route is the route's name, "default" for the defaults.
const (
	WarnNoCorroborator         = "no_corroborator"
	WarnImpliesUnattached      = "implies_unattached"
	WarnFailClosedNotEnforcing = "fail_closed_not_enforcing"
	WarnSampleRateNotObserve   = "sample_rate_not_observe"
	WarnTrustsUserContext      = "trusts_user_context"
	WarnClientRulesEnforcing   = "client_rules_enforcing"
	WarnNewestVersionInvalid   = "newest_version_invalid"
	WarnFileDiverged           = "file_diverged"
)

// ErrEscalateNotBelowWarn is the code of the error that refuses thresholds whose escalate_from is
// not below warn_at (SPEC §2.8 hard rule), for example a calibrated warn_at under the evaluator's
// escalate_from. Details: {route, ref, warn_at, escalate_from}.
const ErrEscalateNotBelowWarn = "escalate_not_below_warn"

// WarningCodes is the closed list of policy warning codes.
var WarningCodes = []string{
	WarnNoCorroborator, WarnImpliesUnattached, WarnFailClosedNotEnforcing, WarnSampleRateNotObserve,
	WarnTrustsUserContext, WarnClientRulesEnforcing, WarnNewestVersionInvalid, WarnFileDiverged,
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

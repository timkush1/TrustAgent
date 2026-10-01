package main

import (
	"context"
	"errors"
)

// errNotImplemented is what a Step-0 seam returns until its owner fills it in.
var errNotImplemented = errors.New("not implemented")

// CredentialWrite is one credential write (P4-PLAN D75): PUT /api/credentials/:name (#55), the
// verify-and-bind of POST /api/credentials/:name/verify (#57) and the key from Connect OpenRouter
// (#59) all go through writeCredential.
type CredentialWrite struct {
	// Name is credentials.name; Provider must match it (D29).
	Name     string
	Provider string
	// Secret is the key; empty for a verify of the stored key (#57).
	Secret string
	// BaseURL is accepted for compatible and upstream-* only (D29, D78).
	BaseURL string
	// Verify checks the key with its provider first (default true); Bind lists the targets to
	// bind when verification passes (decision_model, judge).
	Verify bool
	Bind   []string
	// Actor is the admin_audit_log actor; Action its action (credential.put, credential.verify,
	// credential.connect_openrouter, ...).
	Actor  string
	Action string
}

// CredentialWriteResult is api.ts CredentialWriteResult.
type CredentialWriteResult struct {
	Credential   any                           `json:"credential"`
	Stored       bool                          `json:"stored"`
	Bindings     CredentialWriteBindings       `json:"bindings"`
	Verification *CredentialVerificationResult `json:"verification"`
}

// CredentialWriteBindings is CredentialWriteResult.bindings.
type CredentialWriteBindings struct {
	Applied []string             `json:"applied"`
	Skipped []CredentialBindSkip `json:"skipped"`
}

// CredentialBindSkip is one skipped binding target with its reason (env_managed, backend_pinned,
// verification_failed).
type CredentialBindSkip struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// CredentialVerificationResult is api.ts CredentialVerification.
type CredentialVerificationResult struct {
	OK                bool     `json:"ok"`
	Status            string   `json:"status"`
	Hint              *string  `json:"hint"`
	LatencyMs         *int64   `json:"latency_ms"`
	CostUSD           *float64 `json:"cost_usd"`
	CostSource        *string  `json:"cost_source"`
	KeyLabel          *string  `json:"key_label,omitempty"`
	LimitUSD          *float64 `json:"limit_usd,omitempty"`
	LimitRemainingUSD *float64 `json:"limit_remaining_usd,omitempty"`
}

// writeCredential stores, verifies and binds one credential in one transaction (D75), then
// publishes credentials_changed (and settings_updated when a binding applied), writes the audit
// row and pushes to the engine when the judge is bound. Step-0 stub: errNotImplemented until
// GCONN fills it in.
func (s *server) writeCredential(ctx context.Context, w CredentialWrite) (CredentialWriteResult, error) {
	_, _ = ctx, w
	return CredentialWriteResult{}, errNotImplemented
}

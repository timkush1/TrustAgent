package upstream

import "github.com/truthtable/backend-go/internal/runtimecfg"

// FieldError is a runtime upstream setting that is refused (a 422 validation_failed field error),
// e.g. code fallback_key_required with details {host} (P4-PLAN D41).
type FieldError struct {
	Path    string
	Code    string
	Message string
	Details map[string]any
}

// Warning is a runtime upstream risk that does not refuse the write (D15 stays a warning).
type Warning struct {
	Code    string
	Message string
	Details map[string]any
}

// RuntimeRisks checks runtime upstream settings against the stored credentials before a PATCH
// of /api/settings is saved (D41, D23): the D23 condition is a field error, D15 a warning.
// Step-0 stub: it finds nothing until GCONN fills it in. upstream imports runtimecfg, never the
// other way round.
func RuntimeRisks(settings runtimecfg.RuntimeSettings, creds runtimecfg.CredentialSet) (fieldErrs []FieldError, warnings []Warning) {
	_, _ = settings, creds
	return nil, nil
}

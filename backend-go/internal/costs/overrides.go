// Package costs is the cost ledger, the spend caps, ROI and the effective price table (P4,
// GCOST). It uses the pure calculator in internal/decision and never imports internal/runtimecfg
// (P4-PLAN D90): the server adapts runtime settings into plain callbacks.
package costs

import "github.com/truthtable/backend-go/internal/decision"

// FieldError is one finding on the dashboard's pricing_overrides: Path is the settings path
// (pricing_overrides[2].input_per_mtok), Code a machine code the web renders words for. Warning
// marks a finding that does not refuse the write (a price far above the contracts price, D53).
type FieldError struct {
	Path    string
	Code    string
	Message string
	Warning bool
}

// ValidateOverrides checks pricing overrides before they are saved (P4-PLAN D53): it refuses $0
// for providers that are not local and warns when a price is more than 100 times the contracts
// price. Step-0 stub: it finds nothing until GCOST fills it in.
func ValidateOverrides(overrides []decision.PriceEntry) []FieldError {
	_ = overrides
	return nil
}

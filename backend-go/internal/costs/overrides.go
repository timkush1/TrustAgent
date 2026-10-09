// Package costs is the cost ledger, the spend caps, ROI and the effective price table (SPEC §7.4,
// GCOST). It uses the pure calculator in internal/decision and never imports internal/runtimecfg
// (D90): the server adapts runtime settings into plain callbacks.
package costs

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/truthtable/backend-go/internal/decision"
)

// FieldError is one finding on the dashboard's pricing_overrides: Path is the settings path
// (pricing_overrides[2].input_per_mtok), Code a machine code the web renders words for. Warning
// marks a finding that does not refuse the write (a price far above the contracts price, D53).
// Details carries the numbers the words need, when there are any.
type FieldError struct {
	Path    string
	Code    string
	Message string
	Warning bool
	Details map[string]any
}

// Codes of the ValidateOverrides findings.
const (
	// CodeInvalidPriceEntry: the entry breaks a pricing.v1 rule; Path names the field.
	CodeInvalidPriceEntry = "invalid_price_entry"
	// CodePriceZeroNotLocal: $0 in and out for a provider that is not local (D53). Details:
	// provider.
	CodePriceZeroNotLocal = "price_zero_not_local"
	// CodeDuplicateOverride: an earlier override has the same provider and match, so this one
	// could never apply. Details: index (0-based) of that earlier override.
	CodeDuplicateOverride = "duplicate_override"
	// CodePriceFarAboveContracts (a warning): more than FarAboveFactor times the contracts price
	// of the same model (D53). Details: contracts_per_mtok, ratio, and index (0-based) of the
	// override, so the Prices panel attaches the warning to its row.
	CodePriceFarAboveContracts = "price_far_above_contracts"
)

// FarAboveFactor is how many times the contracts price an override may be before it is flagged.
const FarAboveFactor = 100

// overridesPath is the settings path of the overrides list.
const overridesPath = "pricing_overrides"

// ValidateOverrides checks pricing overrides before they are saved (D53). Errors refuse
// the write: an entry that breaks a pricing.v1 rule (an override's label may only be override),
// $0 for a provider that is not local, and a repeat of an earlier override's provider and match.
// A warning does not: a price more than 100 times the contracts price of the same model. Findings
// are in entry order, errors before warnings within an entry.
func ValidateOverrides(overrides []decision.PriceEntry) []FieldError {
	var out []FieldError
	contracts, _ := decision.ContractsPrices() // a missing table leaves nothing to compare with
	seen := map[[2]string]int{}
	for i, e := range overrides {
		at := fmt.Sprintf("%s[%d]", overridesPath, i)
		if err, ok := overrideError(at, e, seen, i); ok {
			out = append(out, err)
			continue
		}
		out = append(out, farAboveWarnings(at, i, e, contracts)...)
	}
	return out
}

// overrideError is the first error that refuses override e at index i (seen collects the
// provider and match of the overrides before it).
func overrideError(at string, e decision.PriceEntry, seen map[[2]string]int, i int) (FieldError, bool) {
	if field, problem := decision.CheckPriceEntry(e); field != "" {
		return FieldError{Path: at + "." + field, Code: CodeInvalidPriceEntry, Message: field + " " + problem + "."}, true
	}
	if e.Label != "" && e.Label != decision.PriceLabelOverride {
		return FieldError{Path: at + ".label", Code: CodeInvalidPriceEntry, Message: "label must be override or empty: your prices are never vendor prices."}, true
	}
	if e.Free() && !decision.IsLocalProvider(e.Provider) {
		return FieldError{
			Path: at + ".input_per_mtok", Code: CodePriceZeroNotLocal,
			Message: fmt.Sprintf("A $0 price is only allowed for local models; %s calls are billed.", e.Provider),
			Details: map[string]any{"provider": e.Provider},
		}, true
	}
	key := [2]string{e.Provider, e.Match}
	if first, dup := seen[key]; dup {
		return FieldError{
			Path: at + ".match", Code: CodeDuplicateOverride,
			Message: fmt.Sprintf("Override %d already prices %s on %s, so this one would never apply.", first+1, e.Match, e.Provider),
			Details: map[string]any{"index": first},
		}, true
	}
	seen[key] = i
	return FieldError{}, false
}

// farAboveWarnings flags the prices of e that are more than FarAboveFactor times the contracts
// price of the same model: the contracts entry with the same provider and match, otherwise the
// one that would price a model the glob names. i is the override's index.
func farAboveWarnings(at string, i int, e decision.PriceEntry, contracts decision.PriceTable) []FieldError {
	ref, ok := contractsEntryFor(e, contracts)
	if !ok {
		return nil
	}
	var out []FieldError
	for _, p := range []struct {
		field         string
		price, listed float64
	}{
		{"input_per_mtok", e.InputPerMTok, ref.InputPerMTok},
		{"output_per_mtok", e.OutputPerMTok, ref.OutputPerMTok},
	} {
		if p.listed <= 0 || p.price <= FarAboveFactor*p.listed {
			continue
		}
		ratio := math.Round(p.price/p.listed*10) / 10
		out = append(out, FieldError{
			Path: at + "." + p.field, Code: CodePriceFarAboveContracts, Warning: true,
			Message: fmt.Sprintf("$%s per million tokens is %s× the built-in price of $%s; check the units (USD per million tokens).",
				formatUSD(p.price), formatUSD(ratio), formatUSD(p.listed)),
			Details: map[string]any{"contracts_per_mtok": p.listed, "ratio": ratio, "index": i},
		})
	}
	return out
}

func contractsEntryFor(e decision.PriceEntry, contracts decision.PriceTable) (decision.PriceEntry, bool) {
	for _, c := range contracts.Entries {
		if c.Provider == e.Provider && c.Match == e.Match {
			return c, true
		}
	}
	return decision.LookupPrice(contracts.Entries, e.Provider, strings.ReplaceAll(e.Match, "*", ""))
}

// usableOverrides are the overrides ValidateOverrides raises no error for, in order.
func usableOverrides(overrides []decision.PriceEntry) []decision.PriceEntry {
	out := make([]decision.PriceEntry, 0, len(overrides))
	seen := map[[2]string]int{}
	for i, e := range overrides {
		if _, bad := overrideError("", e, seen, i); !bad {
			out = append(out, e)
		}
	}
	return out
}

// formatUSD prints a number in plain decimal notation without trailing zeros.
func formatUSD(x float64) string {
	return strconv.FormatFloat(x, 'f', -1, 64)
}

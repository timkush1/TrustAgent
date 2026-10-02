package costs

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sync/atomic"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
)

// PriceBook is the effective price table (P4-PLAN D53): the dashboard's pricing overrides, then
// the TRUTHTABLE_PRICE_TABLE_FILE table when one is set, then the embedded contracts table, merged
// by decision.EffectivePrices. The server keeps the overrides current from the runtime settings
// (SetOverrides); every reader sees one immutable snapshot, swapped atomically, so a cost is never
// priced from half an update.
type PriceBook struct {
	contracts decision.PriceTable
	file      *decision.PriceTable
	cur       atomic.Pointer[priceSnapshot]
}

// priceSnapshot is one effective table and its JSON (what the engine is pushed).
type priceSnapshot struct {
	table decision.PriceTable
	json  []byte
}

// NewPriceBook is a book over the contracts table and an optional file table (nil for none), with
// no overrides yet.
func NewPriceBook(contracts decision.PriceTable, file *decision.PriceTable) *PriceBook {
	b := &PriceBook{contracts: contracts, file: file}
	b.SetOverrides(nil)
	return b
}

// LoadPriceBook builds the book from the embedded contracts table and, when path is not empty,
// the pricing.v1 file it names (TRUTHTABLE_PRICE_TABLE_FILE). A file that is missing or not a
// valid pricing.v1 table is an error: the operator asked for those prices.
func LoadPriceBook(path string) (*PriceBook, error) {
	contracts, err := decision.ContractsPrices()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return NewPriceBook(contracts, nil), nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the operator names the price table (TRUTHTABLE_PRICE_TABLE_FILE)
	if err != nil {
		return nil, fmt.Errorf("price table file: %w", err)
	}
	file, err := decision.ParsePriceTable(raw)
	if err != nil {
		return nil, fmt.Errorf("price table file %s: %w", path, err)
	}
	return NewPriceBook(contracts, &file), nil
}

// SetOverrides replaces the dashboard overrides. Overrides that ValidateOverrides would refuse (a
// settings row written before validation, or edited by hand) are left out rather than priced, and
// their number is returned so the caller can log it.
func (b *PriceBook) SetOverrides(overrides []decision.PriceEntry) (dropped int) {
	usable := usableOverrides(overrides)
	t := decision.EffectivePrices(b.contracts, b.file, usable)
	raw, _ := json.Marshal(t) // cannot fail: every entry has finite prices
	b.cur.Store(&priceSnapshot{table: t, json: raw})
	return len(overrides) - len(usable)
}

// Effective is the price table in force (GET /api/pricing): overrides first. The caller owns the
// returned copy.
func (b *PriceBook) Effective() decision.PriceTable {
	t := b.cur.Load().table
	t.Entries = slices.Clone(t.Entries)
	return t
}

// EffectiveJSON is the effective table with these overrides, as pricing.v1 JSON: what the engine
// is pushed (RuntimeConfig.price_table_json, P4-PLAN D34). It is a pure function of overrides and
// does not change the book.
func (b *PriceBook) EffectiveJSON(overrides []decision.PriceEntry) []byte {
	raw, _ := json.Marshal(decision.EffectivePrices(b.contracts, b.file, usableOverrides(overrides))) // cannot fail: finite prices
	return raw
}

// CurrentJSON is the JSON of the effective table in force.
func (b *PriceBook) CurrentJSON() []byte { return slices.Clone(b.cur.Load().json) }

// Version is the version of the effective table: the file's when one is set, else the contracts
// version.
func (b *PriceBook) Version() string { return b.cur.Load().table.Version }

// Contracts is the embedded contracts table (ValidateOverrides compares against it).
func (b *PriceBook) Contracts() decision.PriceTable {
	t := b.contracts
	t.Entries = slices.Clone(t.Entries)
	return t
}

// Lookup resolves the entry pricing model on provider (decision.LookupPrice over the effective
// table) and the price_table_version a cost priced by it records.
func (b *PriceBook) Lookup(provider, model string) (decision.PriceEntry, string, bool) {
	t := b.cur.Load().table
	e, ok := decision.LookupPrice(t.Entries, provider, model)
	if !ok {
		return decision.PriceEntry{}, t.Version, false
	}
	return e, t.VersionOf(e), true
}

// PriceCall prices one call with the effective table (decision.PriceCall); reported is the cost
// the provider reported, or nil.
func (b *PriceBook) PriceCall(provider, model string, inputTokens, outputTokens int64, reported *float64) decision.CallCost {
	return decision.PriceCall(decision.CallCostInput{
		Provider: provider, Model: model, InputTokens: inputTokens, OutputTokens: outputTokens,
		ReportedUSD: reported, Table: b.cur.Load().table,
	})
}

// JevPrices is the book as a jev.PriceFunc: the input price of a decision model, looked up under
// its backend's pricing provider (jev.PricingProvider).
func (b *PriceBook) JevPrices(backend, model string) (perMTok float64, version string, ok bool) {
	e, v, ok := b.Lookup(jev.PricingProvider(backend), model)
	if !ok {
		return 0, "", false
	}
	return e.InputPerMTok, v, true
}

var _ jev.PriceFunc = (*PriceBook)(nil).JevPrices

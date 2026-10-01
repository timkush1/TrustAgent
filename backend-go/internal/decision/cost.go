package decision

// PriceEntry is one price table entry: api.ts PriceEntry, the entries of contracts pricing.v1
// and of the dashboard's pricing_overrides (P4-PLAN D52, D53, D90). It lives in the
// contract-owned decision package so runtimecfg and costs share it without importing each
// other. The pure cost calculator joins it here in P4 (GCOST).
type PriceEntry struct {
	// Match is a model glob ("*" matches any run of characters).
	Match    string `json:"match"`
	Provider string `json:"provider"`
	// InputPerMTok and OutputPerMTok are USD per million tokens.
	InputPerMTok  float64 `json:"input_per_mtok"`
	OutputPerMTok float64 `json:"output_per_mtok"`
	Source        string  `json:"source,omitempty"`
	// Label is vendor_price for table entries and override for dashboard overrides.
	Label string `json:"label,omitempty"`
	AsOf  string `json:"as_of,omitempty"`
	Note  string `json:"note,omitempty"`
}

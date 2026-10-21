package decision

// The pure cost calculator (SPEC §7.4, D52, D53, D85): the price table types, the price
// lookup with its precedence, the cost of one call, and the cost of 1,000 answers at given
// settings. internal/costs (the ledger, caps and ROI) and internal/jev use it; the engine prices its
// LLM calls the same way (backend-python providers/pricing.py), and the cost vectors in
// contracts/vectors/cost hold both to it.
//
// Arithmetic is exact decimal: a price is the decimal its JSON spells (the shortest representation
// of the float64), costs are summed as exact rationals, and each result is the float64 nearest the
// exact decimal value. 566 tokens at $0.042/M is therefore 2.3772e-05, never
// 0.000023772000000000002. Ports reproduce it with decimal arithmetic on the shortest
// representation of each number (Python Decimal(repr(x))).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/truthtable/contracts"
)

// Cost sources: cost_events.cost_source, CostRecord.cost_source and api.ts CostSource.
const (
	// CostReported is a cost the provider reported (OpenRouter usage.cost).
	CostReported = "reported"
	// CostComputed is tokens × the price table.
	CostComputed = "computed"
	// CostFreeLocal is a local model (Ollama): its compute is not priced.
	CostFreeLocal = "free_local"
	// CostSimulated is a mock call: nothing was called, so nothing was spent.
	CostSimulated = "simulated"
	// CostUnknown is a call no price table entry covers.
	CostUnknown = "unknown"
)

// Price labels (PriceEntry.Label).
const (
	PriceLabelVendor   = "vendor_price"
	PriceLabelOverride = "override"
)

// PriceVersionOverride is the price_table_version of a cost priced by a dashboard override.
const PriceVersionOverride = "override"

// PriceEntry is one price table entry: api.ts PriceEntry, the entries of contracts pricing.v1
// and of the dashboard's pricing_overrides (D52, D53, D90). It lives in the
// contract-owned decision package so runtimecfg and costs share it without importing each
// other.
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

// Free reports whether the entry prices both input and output at $0.
func (e PriceEntry) Free() bool { return e.InputPerMTok == 0 && e.OutputPerMTok == 0 }

// PriceTable is a pricing.v1 document (api.ts PricingTable).
type PriceTable struct {
	Version  string       `json:"version"`
	Currency string       `json:"currency"`
	Entries  []PriceEntry `json:"entries"`
}

// VersionOf is the price_table_version of a cost priced by e from t: "override" for a dashboard
// override, otherwise the table's version.
func (t PriceTable) VersionOf(e PriceEntry) string {
	if e.Label == PriceLabelOverride {
		return PriceVersionOverride
	}
	return t.Version
}

// IsLocalProvider reports whether provider runs on the operator's own hardware (Ollama), whose
// compute is not priced.
func IsLocalProvider(provider string) bool { return provider == "ollama" }

// IsSimulatedProvider reports whether provider is a mock that calls nothing.
func IsSimulatedProvider(provider string) bool { return provider == "mock" }

// ------------------------------------------------------------------------------------- price table

// Limits and patterns of pricing.v1 (contracts/schema/pricing.v1.schema.json).
const (
	maxPriceVersion = 32
	maxPriceMatch   = 128
	maxPriceText    = 300
)

var (
	priceProviderRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	priceDateRE     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// ErrPriceTable marks a document that is not a valid pricing.v1 table.
var ErrPriceTable = errors.New("invalid price table")

// ParsePriceTable decodes a pricing.v1 document strictly: unknown fields are refused, and the
// version, the USD currency, at least one entry and every entry's schema rules are required.
func ParsePriceTable(b []byte) (PriceTable, error) {
	var t PriceTable
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&t); err != nil {
		return PriceTable{}, fmt.Errorf("%w: %v", ErrPriceTable, err)
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return PriceTable{}, fmt.Errorf("%w: data after the table", ErrPriceTable)
	}
	if err := t.Validate(); err != nil {
		return PriceTable{}, err
	}
	return t, nil
}

// Validate checks the pricing.v1 rules that decoding cannot: a version of 1-32 characters, the
// USD currency, at least one entry, and each entry's rules (CheckPriceEntry). Lengths count
// characters (Unicode code points), as JSON Schema's maxLength does.
func (t PriceTable) Validate() error {
	if t.Version == "" || utf8.RuneCountInString(t.Version) > maxPriceVersion {
		return fmt.Errorf("%w: version must be 1-%d characters", ErrPriceTable, maxPriceVersion)
	}
	if t.Currency != "USD" {
		return fmt.Errorf("%w: currency must be USD", ErrPriceTable)
	}
	if len(t.Entries) == 0 {
		return fmt.Errorf("%w: entries must not be empty", ErrPriceTable)
	}
	for i, e := range t.Entries {
		if field, msg := CheckPriceEntry(e); field != "" {
			return fmt.Errorf("%w: entries[%d].%s %s", ErrPriceTable, i, field, msg)
		}
	}
	return nil
}

// CheckPriceEntry checks one entry against the pricing.v1 schema. It returns the offending field
// and what is wrong with it, or "" when the entry is valid.
func CheckPriceEntry(e PriceEntry) (field, problem string) {
	switch {
	case e.Match == "" || utf8.RuneCountInString(e.Match) > maxPriceMatch:
		return "match", fmt.Sprintf("must be 1-%d characters", maxPriceMatch)
	case !priceProviderRE.MatchString(e.Provider):
		return "provider", "must be 1-32 lowercase letters, digits, - or _"
	case !validPrice(e.InputPerMTok):
		return "input_per_mtok", "must be a number of at least 0"
	case !validPrice(e.OutputPerMTok):
		return "output_per_mtok", "must be a number of at least 0"
	case utf8.RuneCountInString(e.Source) > maxPriceText:
		return "source", fmt.Sprintf("must be at most %d characters", maxPriceText)
	case e.Label != "" && e.Label != PriceLabelVendor && e.Label != PriceLabelOverride:
		return "label", "must be vendor_price or override"
	case e.AsOf != "" && !validDate(e.AsOf):
		return "as_of", "must be a date (YYYY-MM-DD)"
	case utf8.RuneCountInString(e.Note) > maxPriceText:
		return "note", fmt.Sprintf("must be at most %d characters", maxPriceText)
	}
	return "", ""
}

func validPrice(p float64) bool { return p >= 0 && !math.IsInf(p, 0) && !math.IsNaN(p) }

// validDate reports whether s is a calendar date in the YYYY-MM-DD form (JSON Schema format date).
func validDate(s string) bool {
	if !priceDateRE.MatchString(s) {
		return false
	}
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// ContractsPrices is the embedded contracts/pricing/prices.v1.json (parsed once). The caller owns
// the returned entries.
func ContractsPrices() (PriceTable, error) {
	t, err := contractsPrices()
	t.Entries = slices.Clone(t.Entries)
	return t, err
}

var contractsPrices = sync.OnceValues(func() (PriceTable, error) {
	b, err := contracts.FS.ReadFile("pricing/prices.v1.json")
	if err != nil {
		return PriceTable{}, err
	}
	t, err := ParsePriceTable(b)
	if err != nil {
		return PriceTable{}, fmt.Errorf("pricing/prices.v1.json: %w", err)
	}
	return t, nil
})

// EffectivePrices is the price table costs are computed with (D53). Its entries are, in
// order: the dashboard overrides (labelled override), the TRUTHTABLE_PRICE_TABLE_FILE table when
// one is set (file may be nil), then the contracts table. A lookup takes the first matching entry
// of the provider, so an override wins over the file and the file over contracts; an entry a
// higher layer repeats stays listed but never matches first. The version is the file's when a file
// is set, otherwise the contracts version. The inputs are not modified.
func EffectivePrices(base PriceTable, file *PriceTable, overrides []PriceEntry) PriceTable {
	out := PriceTable{Version: base.Version, Currency: "USD"}
	if file != nil {
		out.Version = file.Version
	}
	n := len(overrides) + len(base.Entries)
	if file != nil {
		n += len(file.Entries)
	}
	out.Entries = make([]PriceEntry, 0, n)
	for _, e := range overrides {
		e.Label = PriceLabelOverride
		out.Entries = append(out.Entries, e)
	}
	if file != nil {
		out.Entries = append(out.Entries, file.Entries...)
	}
	out.Entries = append(out.Entries, base.Entries...)
	return out
}

// GlobMatch matches s against pattern, in which "*" matches any run of characters (including
// none) and every other character is literal.
func GlobMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, last)
}

// LookupPrice resolves the entry that prices model on provider (D53): the first entry of
// that provider whose glob matches, so the provider match comes first; when the provider has none,
// the first matching entry of any other provider prices it, except that a local or mock provider
// never borrows a paid price and a local or mock entry (a free "*" catch-all) never prices another
// provider's call. An empty provider (a backend with no pricing provider) takes the second rule
// only.
func LookupPrice(entries []PriceEntry, provider, model string) (PriceEntry, bool) {
	if provider != "" {
		for _, e := range entries {
			if e.Provider == provider && GlobMatch(e.Match, model) {
				return e, true
			}
		}
		if IsLocalProvider(provider) || IsSimulatedProvider(provider) {
			return PriceEntry{}, false
		}
	}
	for _, e := range entries {
		if IsLocalProvider(e.Provider) || IsSimulatedProvider(e.Provider) {
			continue
		}
		if GlobMatch(e.Match, model) {
			return e, true
		}
	}
	return PriceEntry{}, false
}

// ------------------------------------------------------------------------------------ arithmetic

// TokenCostUSD is inputTokens × inputPerMTok + outputTokens × outputPerMTok, divided by one
// million, as the float64 nearest its exact decimal value. Negative token counts count as 0.
func TokenCostUSD(inputTokens, outputTokens int64, inputPerMTok, outputPerMTok float64) float64 {
	inputTokens, outputTokens = max(inputTokens, 0), max(outputTokens, 0)
	// Fast path: prices with at most six decimals are whole micro-dollars per million tokens, so
	// the sum is an exact integer number of pico-dollars and one division rounds it correctly.
	if mi, ok := microDollars(inputPerMTok); ok {
		if mo, ok := microDollars(outputPerMTok); ok {
			if pico, ok := mulAddExact(inputTokens, mi, outputTokens, mo); ok {
				return float64(pico) / 1e12
			}
		}
	}
	sum := new(big.Rat).Mul(new(big.Rat).SetInt64(inputTokens), decimalRat(inputPerMTok))
	sum.Add(sum, new(big.Rat).Mul(new(big.Rat).SetInt64(outputTokens), decimalRat(outputPerMTok)))
	return ratFloat(sum.Quo(sum, big.NewRat(1_000_000, 1)))
}

// exactLimit bounds the integers the fast path works with, so their float64 conversion is exact.
const exactLimit = 1 << 53

// microDollars is p × 1e6 when p (USD per million tokens) has at most six decimals and the
// product is below 2^53.
func microDollars(p float64) (int64, bool) {
	if !validPrice(p) {
		return 0, false
	}
	s := strconv.FormatFloat(p, 'f', -1, 64)
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 6 || len(whole) > 9 {
		return 0, false
	}
	n, err := strconv.ParseInt(whole+frac+strings.Repeat("0", 6-len(frac)), 10, 64)
	if err != nil || n >= exactLimit {
		return 0, false
	}
	return n, true
}

// mulAddExact is a×b + c×d when every step stays below 2^53.
func mulAddExact(a, b, c, d int64) (int64, bool) {
	ab, ok1 := mulBelow(a, b)
	cd, ok2 := mulBelow(c, d)
	if !ok1 || !ok2 || ab+cd >= exactLimit {
		return 0, false
	}
	return ab + cd, true
}

func mulBelow(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a >= exactLimit/b {
		return 0, false
	}
	return a * b, true
}

// decimalRat is the exact decimal x spells: the shortest representation that parses back to x.
func decimalRat(x float64) *big.Rat {
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(x, 'g', -1, 64))
	if !ok { // unreachable for finite x
		return new(big.Rat)
	}
	return r
}

// ratFloat is the float64 nearest r.
func ratFloat(r *big.Rat) float64 {
	f, _ := r.Float64()
	return f
}

// ---------------------------------------------------------------------------------- call_cost

// CallCostInput is one call to price: the cost vectors' call_cost input (fn aside).
type CallCostInput struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	// ReportedUSD is the cost the provider reported (OpenRouter usage.cost), when it did.
	ReportedUSD *float64 `json:"reported_cost_usd,omitempty"`
	// Table is the effective price table (EffectivePrices).
	Table PriceTable `json:"table"`
}

// CallCost is the priced cost of one call.
type CallCost struct {
	// USD is nil when the cost is unknown.
	USD    *float64 `json:"cost_usd"`
	Source string   `json:"cost_source"`
	// PriceTableVersion is "override" when a dashboard override priced the call, otherwise the
	// version of the table in force.
	PriceTableVersion string `json:"price_table_version"`
}

// PriceCall prices one call (SPEC §7.4). The first rule that applies wins:
//
//	a mock provider                           → $0, simulated
//	the provider reported the cost            → that cost, reported
//	a local provider with no or a free entry  → $0, free_local
//	no entry prices the model (LookupPrice)   → unknown (no cost)
//	otherwise                                 → tokens × the entry's prices, computed
//
// A reported cost below $0 (or not finite) is not a cost: it is ignored and the call is priced
// from the table, so a malformed report can never lower the day's spend.
func PriceCall(in CallCostInput) CallCost {
	out := CallCost{PriceTableVersion: in.Table.Version}
	switch {
	case IsSimulatedProvider(in.Provider):
		out.USD, out.Source = ptr(0.0), CostSimulated
		return out
	case in.ReportedUSD != nil && validPrice(*in.ReportedUSD):
		out.USD, out.Source = ptr(*in.ReportedUSD), CostReported
		return out
	}
	e, ok := LookupPrice(in.Table.Entries, in.Provider, in.Model)
	if ok {
		out.PriceTableVersion = in.Table.VersionOf(e)
	}
	switch {
	case IsLocalProvider(in.Provider) && (!ok || e.Free()):
		out.USD, out.Source = ptr(0.0), CostFreeLocal
	case !ok:
		out.Source = CostUnknown
	default:
		out.USD = ptr(TokenCostUSD(in.InputTokens, in.OutputTokens, e.InputPerMTok, e.OutputPerMTok))
		out.Source = CostComputed
	}
	return out
}

// -------------------------------------------------------------------------------- price_lookup

// PriceLookupInput is the cost vectors' price_lookup input (fn aside): the three D53 layers and
// the provider and model to resolve.
type PriceLookupInput struct {
	Contracts PriceTable   `json:"contracts"`
	File      *PriceTable  `json:"file,omitempty"`
	Overrides []PriceEntry `json:"overrides"`
	Provider  string       `json:"provider"`
	Model     string       `json:"model"`
}

// PriceLookup is the entry a provider and model resolve to (nil when none does) and the
// price_table_version a cost priced by it records.
type PriceLookup struct {
	Entry             *PriceEntry `json:"entry"`
	PriceTableVersion string      `json:"price_table_version"`
}

// LookupEffective resolves in.Provider and in.Model against EffectivePrices of the three layers.
func LookupEffective(in PriceLookupInput) PriceLookup {
	t := EffectivePrices(in.Contracts, in.File, in.Overrides)
	out := PriceLookup{PriceTableVersion: t.Version}
	if e, ok := LookupPrice(t.Entries, in.Provider, in.Model); ok {
		out.Entry, out.PriceTableVersion = &e, t.VersionOf(e)
	}
	return out
}

// --------------------------------------------------------------------------------------- per_1k

// ModelRef names a priced model: a pricing provider and a model id.
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Per1kSettings are the inputs of the cost of 1,000 answers, named as the inputs of
// contracts/pricing/assumptions.v1.json. Token counts are per call; rates are fractions of answers.
type Per1kSettings struct {
	// The fast check: one decision-model call per answer.
	FastCheckInputTokens  float64 `json:"fast_check_input_tokens"`
	FastCheckOutputTokens float64 `json:"fast_check_output_tokens"`
	// The deep audit of one answer: one decompose call, then one verify call per claim.
	ClaimsPerAnswer       float64 `json:"claims_per_answer"`
	DecomposeInputTokens  float64 `json:"decompose_input_tokens"`
	DecomposeOutputTokens float64 `json:"decompose_output_tokens"`
	EvidenceContextTokens float64 `json:"evidence_context_tokens"`
	VerifierPromptTokens  float64 `json:"verifier_prompt_tokens"`
	ClaimTokens           float64 `json:"claim_tokens"`
	VerifyOutputTokens    float64 `json:"verify_output_tokens"`
	// Which answers are deep-audited: every grounding-flagged answer, pass_sample_rate of the
	// others, and uniform_sample_rate of all answers.
	GroundingFlagRate float64 `json:"grounding_flag_rate"`
	PassSampleRate    float64 `json:"pass_sample_rate"`
	UniformSampleRate float64 `json:"uniform_sample_rate"`
}

// Per1kInput is the cost vectors' per_1k input (fn aside).
type Per1kInput struct {
	Table         PriceTable    `json:"table"`
	DecisionModel ModelRef      `json:"decision_model"`
	Judge         ModelRef      `json:"judge"`
	Settings      Per1kSettings `json:"settings"`
}

// Per1k is the estimated cost of answers at given settings. A cost is nil when its model's price
// is unknown, and the total is nil when either part is.
type Per1k struct {
	// FastCheckUSD is the fast check of one answer; DeepAuditUSD the deep audit of one audited
	// answer.
	FastCheckUSD *float64 `json:"fast_check_usd"`
	DeepAuditUSD *float64 `json:"deep_audit_usd"`
	// AuditRate is the share of answers deep-audited: flagged + (1 − flagged) × pass sample +
	// uniform sample, at most 1.
	AuditRate          float64  `json:"audit_rate"`
	FastCheckPer1k     *float64 `json:"fast_check_per_1k"`
	DeepAuditPer1k     *float64 `json:"deep_audit_per_1k"`
	TotalPer1k         *float64 `json:"total_per_1k"`
	DecisionCostSource string   `json:"decision_cost_source"`
	JudgeCostSource    string   `json:"judge_cost_source"`
}

// ErrPer1kSettings marks settings the per-1k calculator cannot use.
var ErrPer1kSettings = errors.New("invalid per-1k settings")

// Validate refuses negative or non-finite numbers and rates above 1.
func (s Per1kSettings) Validate() error {
	type field struct {
		name string
		v    float64
		rate bool
	}
	for _, f := range []field{
		{"fast_check_input_tokens", s.FastCheckInputTokens, false},
		{"fast_check_output_tokens", s.FastCheckOutputTokens, false},
		{"claims_per_answer", s.ClaimsPerAnswer, false},
		{"decompose_input_tokens", s.DecomposeInputTokens, false},
		{"decompose_output_tokens", s.DecomposeOutputTokens, false},
		{"evidence_context_tokens", s.EvidenceContextTokens, false},
		{"verifier_prompt_tokens", s.VerifierPromptTokens, false},
		{"claim_tokens", s.ClaimTokens, false},
		{"verify_output_tokens", s.VerifyOutputTokens, false},
		{"grounding_flag_rate", s.GroundingFlagRate, true},
		{"pass_sample_rate", s.PassSampleRate, true},
		{"uniform_sample_rate", s.UniformSampleRate, true},
	} {
		switch {
		case f.rate && (!validPrice(f.v) || f.v > 1):
			return fmt.Errorf("%w: %s must be between 0 and 1", ErrPer1kSettings, f.name)
		case !validPrice(f.v):
			return fmt.Errorf("%w: %s must be a number of at least 0", ErrPer1kSettings, f.name)
		}
	}
	return nil
}

// AuditTokens is the deep audit of one answer at s as one input and one output token count:
// decompose, plus claims × (evidence + verifier prompt + claim) in and claims × verify out.
func (s Per1kSettings) AuditTokens() (input, output *big.Rat) {
	claims := decimalRat(s.ClaimsPerAnswer)
	perClaimIn := sumRat(decimalRat(s.EvidenceContextTokens), decimalRat(s.VerifierPromptTokens), decimalRat(s.ClaimTokens))
	input = sumRat(decimalRat(s.DecomposeInputTokens), new(big.Rat).Mul(claims, perClaimIn))
	output = sumRat(decimalRat(s.DecomposeOutputTokens), new(big.Rat).Mul(claims, decimalRat(s.VerifyOutputTokens)))
	return input, output
}

// auditRate is flagged + (1 − flagged) × pass + uniform, capped at 1 (every answer).
func (s Per1kSettings) auditRate() *big.Rat {
	flagged := decimalRat(s.GroundingFlagRate)
	others := new(big.Rat).Sub(big.NewRat(1, 1), flagged)
	r := sumRat(flagged, new(big.Rat).Mul(others, decimalRat(s.PassSampleRate)), decimalRat(s.UniformSampleRate))
	if r.Cmp(big.NewRat(1, 1)) > 0 {
		return big.NewRat(1, 1)
	}
	return r
}

// CostPer1k estimates the cost of 1,000 answers (the SPEC §1 cost story, the landing calculator
// and the Budgets estimates): one fast check per answer, plus a deep audit of the audited share.
// With the shipped defaults of contracts/pricing/assumptions.v1.json it is about $3.0.
func CostPer1k(in Per1kInput) (Per1k, error) {
	if err := in.Settings.Validate(); err != nil {
		return Per1k{}, err
	}
	s := in.Settings
	rate := s.auditRate()
	out := Per1k{AuditRate: ratFloat(rate)}
	thousand := big.NewRat(1000, 1)

	fast, src := estimateRat(in.Table, in.DecisionModel, decimalRat(s.FastCheckInputTokens), decimalRat(s.FastCheckOutputTokens))
	out.DecisionCostSource = src
	if fast != nil {
		out.FastCheckUSD = ptr(ratFloat(fast))
		out.FastCheckPer1k = ptr(ratFloat(new(big.Rat).Mul(fast, thousand)))
	}
	auditIn, auditOut := s.AuditTokens()
	audit, src := estimateRat(in.Table, in.Judge, auditIn, auditOut)
	out.JudgeCostSource = src
	var auditPer1k *big.Rat
	if audit != nil {
		out.DeepAuditUSD = ptr(ratFloat(audit))
		auditPer1k = new(big.Rat).Mul(new(big.Rat).Mul(audit, rate), thousand)
		out.DeepAuditPer1k = ptr(ratFloat(auditPer1k))
	}
	if fast != nil && audit != nil {
		out.TotalPer1k = ptr(ratFloat(sumRat(new(big.Rat).Mul(fast, thousand), auditPer1k)))
	}
	return out, nil
}

// estimateRat is the exact cost of inputTokens and outputTokens on m priced from t, and its cost
// source; the cost is nil when it is unknown. Estimates have no reported cost.
func estimateRat(t PriceTable, m ModelRef, inputTokens, outputTokens *big.Rat) (*big.Rat, string) {
	if IsSimulatedProvider(m.Provider) {
		return new(big.Rat), CostSimulated
	}
	e, ok := LookupPrice(t.Entries, m.Provider, m.Model)
	switch {
	case IsLocalProvider(m.Provider) && (!ok || e.Free()):
		return new(big.Rat), CostFreeLocal
	case !ok:
		return nil, CostUnknown
	}
	usd := sumRat(new(big.Rat).Mul(inputTokens, decimalRat(e.InputPerMTok)), new(big.Rat).Mul(outputTokens, decimalRat(e.OutputPerMTok)))
	return usd.Quo(usd, big.NewRat(1_000_000, 1)), CostComputed
}

func sumRat(xs ...*big.Rat) *big.Rat {
	s := new(big.Rat)
	for _, x := range xs {
		s.Add(s, x)
	}
	return s
}

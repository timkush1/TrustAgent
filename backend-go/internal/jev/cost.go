package jev

import (
	"strings"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
)

// CostKindDecision is cost_events.kind for decision-model calls.
const CostKindDecision = "decision"

// Attempt statuses in cost events (cost_events.status).
const (
	AttemptOK        = "ok"
	AttemptHedgeLost = "hedge_lost"
	AttemptCancelled = "cancelled"
)

// CostEvent is one cost_events row: one per decision attempt, hedge losers included (SPEC §7.4).
type CostEvent struct {
	OccurredAt time.Time
	TraceID    string
	Route      string
	KeyLabel   string
	Component  string // gate, probe, studio_test, dry_run, playground
	Kind       string // decision
	Provider   string // the backend id
	Model      string // the snapshot when known, else the requested model
	Attempt    int    // 0-based attempt index within the call
	Hedge      bool
	// InputTokens is the reported count; for an attempt without usage that may have been billed
	// (a cancelled or timed-out request) it is the estimate and Estimated is set.
	InputTokens  *int
	OutputTokens *int
	CostUSD      float64
	CostSource   string // reported, computed, simulated
	Estimated    bool
	// PriceTableVersion is the contracts price table version, "override" or "default".
	PriceTableVersion string
	LatencyMs         int64
	// Status is ok, hedge_lost, cancelled or error:<kind>.
	Status    string
	Questions int
	// Late marks an attempt that finished after the caller stopped waiting.
	Late bool
}

// CostSink receives cost events. It must not block for long; the ledger (P4) buffers them.
type CostSink interface {
	RecordCost(CostEvent)
}

// CostFunc adapts a function to CostSink.
type CostFunc func(CostEvent)

// RecordCost calls f.
func (f CostFunc) RecordCost(e CostEvent) { f(e) }

type discardCosts struct{}

func (discardCosts) RecordCost(CostEvent) {}

// PriceFunc returns the input price per million tokens of model on backend and the version of
// the price table it came from ("override" for a dashboard override); ok is false when no entry
// matches, and the default list price applies (P4-PLAN D53).
type PriceFunc func(backend, model string) (perMTok float64, version string, ok bool)

// PricingProvider is the price table provider of a decision backend: openrouter for both
// OpenRouter endpoints, typesafe for TypeSafe direct, compatible for a compatible endpoint, mock
// for the mock. An entry of that provider prices the call first; when the provider has none, any
// paid provider's matching entry does (decision.LookupPrice), so a compatible endpoint serving a
// Jev model is priced like Jev while an override written for provider compatible wins over it.
func PricingProvider(backend string) string {
	switch backend {
	case BackendOpenRouterDecisions, BackendOpenRouterSystemOne:
		return "openrouter"
	case BackendTypeSafe:
		return "typesafe"
	case BackendCompatible:
		return "compatible"
	case BackendMock:
		return "mock"
	}
	return ""
}

// contractsTable is the embedded contracts price table, parsed and copied once: decision calls
// are priced from it on every call, and nothing modifies it.
var contractsTable = sync.OnceValues(decision.ContractsPrices)

// ContractsPrices prices decision calls from the embedded contracts table with the shared
// calculator (decision.LookupPrice). It is what a client without Config.Prices uses.
func ContractsPrices(backend, model string) (perMTok float64, version string, ok bool) {
	t, err := contractsTable()
	if err != nil {
		return 0, "", false
	}
	e, ok := decision.LookupPrice(t.Entries, PricingProvider(backend), model)
	if !ok {
		return 0, "", false
	}
	return e.InputPerMTok, t.VersionOf(e), true
}

// priceOf is the input price of model for this client's computed costs: the
// TRUTHTABLE_JEV_PRICE_PER_MTOK override, else Config.Prices (the effective price table), else the
// contracts table, else the $0.042/M list price. A leading "~" (an OpenRouter alias) is ignored.
func (c *httpClient) priceOf(model string) price {
	return lookupPrice(c.cfg.PricePerMTok, c.prices, c.info.Backend, model)
}

func lookupPrice(override float64, prices PriceFunc, backend, model string) price {
	if override > 0 {
		return price{perMTok: override, version: decision.PriceVersionOverride}
	}
	if prices == nil {
		prices = ContractsPrices
	}
	if p, v, ok := prices(backend, strings.TrimPrefix(model, "~")); ok {
		return price{perMTok: p, version: v}
	}
	return price{perMTok: DefaultPricePerMTok, version: "default"}
}

// EstimateUSD is the computed input cost of inputTokens on the backend and model of info, priced
// from the contracts table (ContractsPrices). ok is false for the mock and off backends, which make
// no paid call.
func EstimateUSD(info Info, inputTokens int) (usd float64, ok bool) {
	switch info.Backend {
	case BackendMock, BackendOff, "":
		return 0, false
	}
	return lookupPrice(0, nil, info.Backend, info.Model).usd(inputTokens), true
}

// price is the input price used for computed costs (decision models bill input tokens; Jev's
// output is free).
type price struct {
	perMTok float64
	version string
}

// usd is the cost of tokens input tokens, as the float64 nearest its exact decimal value
// (decision.TokenCostUSD), so it serializes as 2.3772e-05 rather than 0.000023772000000000002.
func (p price) usd(tokens int) float64 {
	return decision.TokenCostUSD(int64(tokens), 0, p.perMTok, 0)
}

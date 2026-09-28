package jev

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/truthtable/contracts"
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

// price is the input price used for computed costs.
type price struct {
	perMTok float64
	version string
}

func (p price) usd(tokens int) float64 { return float64(tokens) * p.perMTok / 1e6 }

// priceFor picks the input price of model on backend: the TRUTHTABLE_JEV_PRICE_PER_MTOK override,
// else the first matching entry of contracts/pricing/prices.v1.json, else the $0.042/M list price.
func priceFor(override float64, backend, model string) price {
	if override > 0 {
		return price{perMTok: override, version: "override"}
	}
	t := loadPriceTable()
	provider := ""
	switch backend {
	case BackendOpenRouterDecisions, BackendOpenRouterSystemOne:
		provider = "openrouter"
	case BackendTypeSafe:
		provider = "typesafe"
	}
	name := strings.TrimPrefix(model, "~")
	for _, pass := range []bool{true, false} { // provider match first, then any provider
		for _, e := range t.Entries {
			if pass && provider != "" && e.Provider != provider || e.Provider == "ollama" || e.InputPerMTok == nil {
				continue
			}
			if globMatch(e.Match, name) {
				return price{perMTok: *e.InputPerMTok, version: t.Version}
			}
		}
		if provider == "" {
			break
		}
	}
	return price{perMTok: DefaultPricePerMTok, version: "default"}
}

type priceTable struct {
	Version string `json:"version"`
	Entries []struct {
		Match        string   `json:"match"`
		Provider     string   `json:"provider"`
		InputPerMTok *float64 `json:"input_per_mtok"`
	} `json:"entries"`
}

var loadPriceTable = sync.OnceValue(func() priceTable {
	var t priceTable
	if b, err := contracts.FS.ReadFile("pricing/prices.v1.json"); err == nil {
		_ = json.Unmarshal(b, &t)
	}
	return t
})

// globMatch matches s against a pattern where * matches any run of characters.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

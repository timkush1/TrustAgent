package costs

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/contracts"
)

// Assumptions are the inputs of contracts/pricing/assumptions.v1.json the gateway uses: the
// settings of the cost of 1,000 answers and the reference models its estimates are priced at.
type Assumptions struct {
	Settings decision.Per1kSettings
	// DecisionModel and Judge are models the referenced price table entries match.
	DecisionModel decision.ModelRef
	Judge         decision.ModelRef
}

// assumptionsFile is the part of assumptions.v1.json Assumptions reads.
type assumptionsFile struct {
	Prices map[string]struct {
		Provider string `json:"provider"`
		Match    string `json:"match"`
	} `json:"prices"`
	Inputs map[string]struct {
		Value float64 `json:"value"`
	} `json:"inputs"`
}

// ContractsAssumptions reads the embedded assumptions.v1.json once.
var ContractsAssumptions = sync.OnceValues(func() (Assumptions, error) {
	raw, err := contracts.FS.ReadFile("pricing/assumptions.v1.json")
	if err != nil {
		return Assumptions{}, fmt.Errorf("assumptions: %w", err)
	}
	return parseAssumptions(raw)
})

func parseAssumptions(raw []byte) (Assumptions, error) {
	var f assumptionsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return Assumptions{}, fmt.Errorf("assumptions: %w", err)
	}
	inputs := make(map[string]float64, len(f.Inputs))
	for k, v := range f.Inputs {
		inputs[k] = v.Value
	}
	// Per1kSettings is named as the assumption inputs, so a JSON round trip maps them.
	b, _ := json.Marshal(inputs) // cannot fail: a map of finite numbers
	var a Assumptions
	if err := json.Unmarshal(b, &a.Settings); err != nil {
		return Assumptions{}, fmt.Errorf("assumptions: %w", err)
	}
	if err := a.Settings.Validate(); err != nil {
		return Assumptions{}, fmt.Errorf("assumptions: %w", err)
	}
	ref := func(alias string) (decision.ModelRef, error) {
		p, ok := f.Prices[alias]
		if !ok || p.Provider == "" || p.Match == "" {
			return decision.ModelRef{}, fmt.Errorf("assumptions: prices.%s is missing", alias)
		}
		// A model id the entry's glob matches.
		return decision.ModelRef{Provider: p.Provider, Model: strings.ReplaceAll(p.Match, "*", "")}, nil
	}
	var err error
	if a.DecisionModel, err = ref("decision_model"); err != nil {
		return Assumptions{}, err
	}
	if a.Judge, err = ref("judge"); err != nil {
		return Assumptions{}, err
	}
	return a, nil
}

// auditEWMAAlpha is the weight of the newest audit in the running estimate: about the last ten
// audits carry it.
const auditEWMAAlpha = 0.2

// referenceAuditUSD is the deep audit of one answer at the published assumptions (Claude Haiku
// 4.5, $0.0355): the estimate when neither the judge nor the reference judge has a price.
const referenceAuditUSD = 0.0355

// JudgeFunc reports the judge the engine uses now (EngineInfo.judge: the effective provider and
// the model); empty strings when it is not known yet.
type JudgeFunc func() (provider, model string)

// AuditEstimator is what one deep audit is expected to cost, the amount the audit pool holds
// against the LLM cap before it runs an audit (P4-PLAN D48). It is a running average (EWMA) of the
// real cost of the audits the engine answered, started from the published assumptions at the
// judge's price: $0 for a mock or local (Ollama) judge, and the Claude Haiku 4.5 list price for a
// judge the price table does not cover (≈ $0.0355). A change of judge starts it over.
type AuditEstimator struct {
	book  *PriceBook
	judge JudgeFunc
	// auditIn and auditOut are the tokens of one audit at the assumptions; ref the reference
	// judge.
	auditIn, auditOut *big.Rat
	ref               decision.ModelRef

	mu   sync.Mutex
	key  string // the judge the average belongs to
	ewma float64
	n    int
}

// NewAuditEstimator builds the estimator over the price book and the engine's judge (nil: not
// known, the reference judge's price applies).
func NewAuditEstimator(book *PriceBook, judge JudgeFunc) (*AuditEstimator, error) {
	a, err := ContractsAssumptions()
	if err != nil {
		return nil, err
	}
	in, out := a.Settings.AuditTokens()
	return &AuditEstimator{book: book, judge: judge, auditIn: in, auditOut: out, ref: a.Judge}, nil
}

// Estimate is the expected cost of the next audit.
func (e *AuditEstimator) Estimate() float64 {
	provider, model := e.currentJudge()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.switchLocked(provider, model)
	if e.n > 0 {
		return e.ewma
	}
	return e.seed(provider, model)
}

// Observe folds the real cost of an audit the engine answered into the estimate. Failed calls
// are not observations: their cost is not known.
func (e *AuditEstimator) Observe(actualUSD float64) {
	actual := nonNegative(actualUSD)
	provider, model := e.currentJudge()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.switchLocked(provider, model)
	prev := e.ewma
	if e.n == 0 {
		prev = e.seed(provider, model)
	}
	e.ewma = auditEWMAAlpha*actual + (1-auditEWMAAlpha)*prev
	e.n++
}

func (e *AuditEstimator) currentJudge() (string, string) {
	if e.judge == nil {
		return "", ""
	}
	return e.judge()
}

// switchLocked starts the average over when the judge changed. Caller holds mu.
func (e *AuditEstimator) switchLocked(provider, model string) {
	key := provider + "\x00" + model
	if key != e.key {
		e.key, e.ewma, e.n = key, 0, 0
	}
}

// seed is one audit at the assumptions, priced for the judge: $0 for a mock or local judge, the
// judge's own price when the book has one, else the reference judge's.
func (e *AuditEstimator) seed(provider, model string) float64 {
	if provider != "" {
		if decision.IsSimulatedProvider(provider) {
			return 0
		}
		entry, _, ok := e.book.Lookup(provider, model)
		switch {
		case decision.IsLocalProvider(provider) && (!ok || entry.Free()):
			return 0
		case ok:
			return e.price(entry)
		}
	}
	if entry, _, ok := e.book.Lookup(e.ref.Provider, e.ref.Model); ok {
		return e.price(entry)
	}
	return referenceAuditUSD
}

// price is one audit's tokens at entry's prices.
func (e *AuditEstimator) price(entry decision.PriceEntry) float64 {
	usd := new(big.Rat).Mul(e.auditIn, ratOf(entry.InputPerMTok))
	usd.Add(usd, new(big.Rat).Mul(e.auditOut, ratOf(entry.OutputPerMTok)))
	usd.Quo(usd, big.NewRat(1_000_000, 1))
	f, _ := usd.Float64()
	return f
}

// ratOf is the exact decimal a price's JSON spells (decision's exact arithmetic).
func ratOf(p float64) *big.Rat {
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(p, 'g', -1, 64))
	if !ok {
		return new(big.Rat)
	}
	return r
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/truthtable/backend-go/internal/costs"
	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/store"
	"github.com/truthtable/contracts"
)

// GET /api/roi/summary and /api/roi/timeseries (SPEC §2.4 rows 47 and 48, §7.4): what the checks
// cost, counted from the cost ledger (cost_events), next to what asking an LLM would have cost.
//
//   - Spend (D50): the fast check is the gate's decision-model calls (every attempt, hedge losers
//     and late attempts included; Studio, dry-run, Playground and probe calls are not the gate's);
//     the deep audit is the audits' LLM calls. Simulated (mock) rows count only with
//     include_mock and are labelled simulated (D46).
//   - llm_same_checks (the headline counterfactual): the token model at the effective judge's
//     price, or the Claude Haiku 4.5 list price when the judge has no real price (D47); its basis
//     says whether a bench-lite run validated the model (contracts/claims/measured.json).
//   - llm_full_claim_audit, tried in order: uniform_sample (n ≥ 30), fitted_from_audits (least
//     squares on answer length, bootstrap interval, n ≥ fitMinN), static_assumptions (not
//     measured).
//
// Every counterfactual is an estimate (simulated over simulated traffic), and a combined figure
// carries the weakest label of its parts.

// fitMinN is the fewest costed deep audits fitted_from_audits fits a line to.
const fitMinN = 10

// The bootstrap of fitted_from_audits: resamples and a fixed seed, so the same audits always give
// the same interval.
const (
	bootstrapResamples = 200
	bootstrapSeed      = 20260927
)

// Same-checks token model (SPEC §7.4): per gated answer, (input tokens + 400) at the input price
// and 40 output tokens per question at the output price.
const (
	sameChecksExtraInputTokens  = 400
	sameChecksOutputPerQuestion = 40
	sameChecksFormula           = "(fast-check input tokens + 400) × input price + 40 output tokens per question × output price"
)

// roiSummary is api.ts RoiSummary.
type roiSummary struct {
	Window         string           `json:"window"`
	GatedResponses int64            `json:"gated_responses"`
	Simulated      bool             `json:"simulated"`
	DecisionCost   roiDecisionCost  `json:"decision_cost"`
	LLMCost        roiLLMCost       `json:"llm_cost"`
	TotalPer1k     labelledValue    `json:"total_per_1k"`
	Counterfactual []counterfactual `json:"counterfactuals"`
	Savings        roiSavings       `json:"savings_vs_same_checks"`
	Outcomes       roiOutcomes      `json:"outcomes"`
	LatencyMs      roiLatency       `json:"latency_ms"`
	Sampling       roiSampling      `json:"sampling"`
	ThirdParty     []string         `json:"third_party"`
}

type roiDecisionCost struct {
	labelledValue
	USD   float64  `json:"usd"`
	Per1k *float64 `json:"per_1k"`
}

type roiLLMCost struct {
	labelledValue
	USD         float64          `json:"usd"`
	Audits      int64            `json:"audits"`
	BySelection map[string]int64 `json:"by_selection"`
}

type counterfactual struct {
	Kind   string   `json:"kind"`
	Method string   `json:"method"`
	USD    *float64 `json:"usd"`
	Low    *float64 `json:"low"`
	High   *float64 `json:"high"`
	Per1k  *float64 `json:"per_1k"`
	N      int64    `json:"n"`
	Label  string   `json:"label"`
	Basis  string   `json:"basis"`
}

type roiSavings struct {
	USDLow  *float64 `json:"usd_low"`
	USDHigh *float64 `json:"usd_high"`
	Label   string   `json:"label"`
}

type roiOutcomes struct {
	Flagged     int64  `json:"flagged"`
	Blocked     int64  `json:"blocked"`
	Fallback    int64  `json:"fallback"`
	NotAssessed int64  `json:"not_assessed"`
	Label       string `json:"label"`
}

type roiLatency struct {
	GateP50          *float64 `json:"gate_p50"`
	GateP95          *float64 `json:"gate_p95"`
	DeadlineMissRate *float64 `json:"deadline_miss_rate"`
	Label            string   `json:"label"`
}

type roiSampling struct {
	UniformRate      float64    `json:"uniform_rate"`
	UniformToday     int64      `json:"uniform_today"`
	UniformCap       int        `json:"uniform_cap"`
	UniformStoppedAt *time.Time `json:"uniform_stopped_at"`
	LLMCapHitAt      *time.Time `json:"llm_cap_hit_at"`
}

// roiTimeseries is api.ts RoiTimeseries.
type roiTimeseries struct {
	Window string     `json:"window"`
	Bucket string     `json:"bucket"`
	Points []roiPoint `json:"points"`
}

type roiPoint struct {
	T             time.Time `json:"t"`
	DecisionUSD   float64   `json:"decision_usd"`
	LLMUSD        float64   `json:"llm_usd"`
	SameChecksUSD *float64  `json:"same_checks_usd"`
	Gated         int64     `json:"gated"`
}

// costLabel labels a sum by where its rows' costs came from: simulated when any is simulated;
// estimate when any was computed from tokens and the price table, or has no price (it counts $0);
// else measured (reported by the provider per call, or a local model that costs nothing).
func costLabel(c store.CostCounts, what string) (string, string) {
	switch {
	case c.Simulated > 0:
		return labelSimulated, what + ": simulated by the mock backends, not a real spend"
	case c.Unknown > 0:
		return labelEstimate, what + ": " + strconv.FormatInt(c.Unknown, 10) + " call(s) have no price in the price table and count $0"
	case c.Computed+c.Other > 0:
		return labelEstimate, what + ": tokens × the price table where the provider did not report a cost"
	case c.Reported > 0 && c.FreeLocal > 0:
		return labelMeasured, what + ": cost reported by the provider per call; local models cost nothing"
	case c.Reported > 0:
		return labelMeasured, what + ": cost reported by the provider per call"
	case c.FreeLocal > 0:
		return labelMeasured, what + ": a local model, no charge"
	}
	return labelMeasured, what + ": no calls in this window"
}

// spendLabel is costLabel over a window that may include simulated traffic: then the spend is
// labelled simulated whatever its rows say, as the rows of simulated traces are (and rows still
// being written must not make a simulated window look measured).
func spendLabel(c store.CostCounts, what string, simulatedTraffic bool) (string, string) {
	label, basis := costLabel(c, what)
	if simulatedTraffic && label != labelSimulated {
		return labelSimulated, what + ": the window includes simulated traffic (mock backends or test requests), not a real spend"
	}
	return label, basis
}

func (s *server) handleRoiSummary(c *gin.Context) {
	if !s.statsReady(c) {
		return
	}
	q, ok := s.parseStatsQuery(c, "7d")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), statsTimeout)
	defer cancel()
	r, err := s.stats.Roi(ctx, q.filter)
	if err != nil {
		s.statsFailed(c, "costs", err)
		return
	}
	simulated := s.dataLabel(r.Mock) == labelSimulated || r.DecisionSources.Simulated > 0 || r.LLMSources.Simulated > 0
	dataLabel, estimate := labelMeasured, labelEstimate
	if simulated {
		dataLabel, estimate = labelSimulated, labelSimulated
	}
	n := r.Checked
	out := roiSummary{Window: q.window, GatedResponses: n, Simulated: simulated, ThirdParty: []string{}}

	dLabel, dBasis := spendLabel(r.DecisionSources, "fast-check calls of the gate, hedges and late answers included", simulated)
	out.DecisionCost = roiDecisionCost{labelledValue: labelledValue{Value: ptrF(roundUSD(r.DecisionUSD)), Label: dLabel, Basis: dBasis},
		USD: roundUSD(r.DecisionUSD), Per1k: per1k(r.DecisionUSD, n)}
	lLabel, lBasis := spendLabel(r.LLMSources, "deep-audit judge calls", simulated)
	if note := freeMockJudgeNote(r); note != "" {
		lBasis = note
	}
	by := map[string]int64{}
	for sel, cnt := range r.AuditsBySelection {
		if slices.Contains(auditSelections, sel) {
			by[sel] = cnt
		}
	}
	out.LLMCost = roiLLMCost{labelledValue: labelledValue{Value: ptrF(roundUSD(r.LLMUSD)), Label: lLabel, Basis: lBasis},
		USD: roundUSD(r.LLMUSD), Audits: r.Audits, BySelection: by}
	out.TotalPer1k = labelledValue{Value: per1k(r.DecisionUSD+r.LLMUSD, n), Label: weakest(dLabel, lLabel),
		Basis: "fast check plus deep audit, per 1,000 answers the fast check assessed"}

	price := s.sameChecksPrice()
	same := sameChecksCounterfactual(r.SameChecksCalls, r.SameChecksInputTokens, r.SameChecksQuestions, price, measuredTokenModel())
	same.Label = estimate
	out.Savings = roiSavings{Label: weakest(same.Label, dLabel, lLabel)}
	if same.USD != nil {
		// Savings: the fast check replaces the same checks (high); counting the deep audits'
		// spend against it gives the low end.
		usd := *same.USD
		out.Savings.USDLow, out.Savings.USDHigh = ptrF(roundUSD(usd-r.DecisionUSD-r.LLMUSD)), ptrF(roundUSD(usd-r.DecisionUSD))
	}
	full := fullAuditCounterfactual(r, n)
	if full.Method == "static_assumptions" {
		// The published assumption (value claim llm_full_audit_cost_per_1k) stays an estimate over
		// simulated traffic too: its figure is the published one, and only the answer count it is
		// applied to is simulated, which the basis says.
		if simulated {
			full.Basis += "; applied to this window's simulated answers"
		}
	} else {
		full.Label = estimate
	}
	out.Counterfactual = []counterfactual{same, full}
	if price.note != "" {
		out.ThirdParty = append(out.ThirdParty, price.note)
	}

	out.Outcomes = roiOutcomes{Flagged: r.Flagged, Blocked: r.Blocked, Fallback: r.Fallback, NotAssessed: r.NotAssessed, Label: dataLabel}
	out.LatencyMs = roiLatency{GateP50: r.GateLatency.P50, GateP95: r.GateLatency.P95, Label: dataLabel}
	if r.Decisions > 0 {
		out.LatencyMs.DeadlineMissRate = ptrF(float64(r.DeadlineMisses) / float64(r.Decisions))
	}

	out.Sampling = roiSampling{UniformCap: s.cfg.UniformAuditsPerDay}
	if pol := s.policies.Engine.Current(); pol != nil && pol.Default != nil {
		out.Sampling.UniformRate = pol.Default.DeepAudit.UniformSampleRate
	}
	if today, stopped, err := s.stats.UniformToday(ctx, time.Now(), s.cfg.UniformAuditsPerDay); err == nil {
		out.Sampling.UniformToday, out.Sampling.UniformStoppedAt = today, stopped
	} else {
		s.log.Warn("uniform sample count failed", "err", err)
	}
	if l := s.ledger(); l != nil {
		out.Sampling.LLMCapHitAt = l.CapHitAt(costs.KindLLM)
	}
	c.JSON(http.StatusOK, out)
}

// freeMockJudgeNote is the deep-audit basis when every judge call of the window came from the
// mock judge, which reports $0: the spend is not a real one, and a real judge would add about the
// published full-audit assumption per deep audit. "" otherwise.
func freeMockJudgeNote(r *store.RoiData) string {
	c := r.LLMSources
	if r.Audits == 0 || c.Simulated == 0 || c.Reported+c.Computed+c.FreeLocal+c.Unknown+c.Other > 0 || r.LLMUSD != 0 {
		return ""
	}
	note := "deep-audit judge calls: the mock judge is free in the demo, so this spend is simulated, not a real one"
	if claim := staticFullAudit(); claim.per1k > 0 {
		note += "; a real judge adds about $" + strconv.FormatFloat(claim.per1k/1000, 'f', -1, 64) +
			" per deep audit (estimate, not measured: " + claim.basis + ")"
	}
	return note
}

// sameChecksCounterfactual is llm_same_checks: the token model over calls decisions that called
// the decision model (inputTokens and questions in all), at the judge price. usd is null when no
// price is known.
func sameChecksCounterfactual(calls, inputTokens, questions int64, price judgePriceInfo, check tokenModelCheck) counterfactual {
	cf := counterfactual{Kind: "llm_same_checks", Method: "token_model", N: calls, Label: labelEstimate,
		Basis: "the same questions asked of " + price.describe() + ": " + sameChecksFormula + ". " + check.basis()}
	if !price.ok {
		return cf
	}
	usd := sameChecksUSD(calls, inputTokens, questions, price)
	cf.USD, cf.Per1k = ptrF(roundUSD(usd)), per1k(usd, calls)
	return cf
}

// sameChecksUSD is the token model's cost of calls decisions at price.
func sameChecksUSD(calls, inputTokens, questions int64, price judgePriceInfo) float64 {
	in := float64(inputTokens + sameChecksExtraInputTokens*calls)
	out := float64(sameChecksOutputPerQuestion * questions)
	return (in*price.in + out*price.out) / 1e6
}

// fullAuditCounterfactual is llm_full_claim_audit by the first method the data allows: the mean
// cost of the uniform sample × N once 30 uniform audits are costed (normal 95% interval), else a
// line fitted to the window's deep audits (bootstrap interval), else the static assumption of
// contracts/claims/value-claims.v1.json, which is not measured.
func fullAuditCounterfactual(r *store.RoiData, n int64) counterfactual {
	if r.UniformCosted >= minRateN {
		usd := r.UniformMeanUSD * float64(n)
		half := 1.96 * r.UniformStddevUSD / math.Sqrt(float64(r.UniformCosted)) * float64(n)
		return counterfactual{Kind: "llm_full_claim_audit", Method: "uniform_sample", USD: ptrF(roundUSD(usd)),
			Low: ptrF(roundUSD(math.Max(0, usd-half))), High: ptrF(roundUSD(usd + half)), Per1k: ptrF(roundUSD(r.UniformMeanUSD * 1000)),
			N: r.UniformCosted, Label: labelEstimate,
			Basis: "mean deep-audit cost of the " + strconv.FormatInt(r.UniformCosted, 10) + " uniform-sample audits × the answers checked; 95% interval"}
	}
	if len(r.FitSamples) >= fitMinN {
		fit := fitFullAudit(r.FitSamples, n, r.CheckedChars, r.CheckedWithChars)
		return counterfactual{Kind: "llm_full_claim_audit", Method: "fitted_from_audits", USD: ptrF(roundUSD(fit.usd)),
			Low: ptrF(roundUSD(fit.low)), High: ptrF(roundUSD(fit.high)), Per1k: per1k(fit.usd, n), N: int64(len(r.FitSamples)),
			Label: labelEstimate,
			Basis: fmt.Sprintf("deep-audit cost fitted to answer length (least squares) over %d deep audits in this window, "+
				"chosen by risk rather than a uniform sample, and applied to the answers checked; 95%% bootstrap interval "+
				"(%d resamples)", len(r.FitSamples), bootstrapResamples)}
	}
	claim := staticFullAudit()
	cf := counterfactual{Kind: "llm_full_claim_audit", Method: "static_assumptions", N: 0, Label: labelEstimate,
		Basis: "not measured: " + claim.basis}
	if claim.per1k > 0 {
		cf.Per1k = ptrF(claim.per1k)
		cf.USD = ptrF(roundUSD(claim.per1k * float64(n) / 1000))
	}
	return cf
}

// fullAuditFit is the fitted_from_audits estimate and its bootstrap interval.
type fullAuditFit struct{ usd, low, high float64 }

// fitFullAudit fits cost ≈ α + β·answer characters to samples (least squares) and applies it to
// the n answers checked, whose characters sum to sumChars over the withChars answers that recorded
// a length (the others count at the mean length). The interval is the 2.5% and 97.5% quantiles of
// the same estimate over bootstrapResamples resamples drawn with a fixed seed. A total below $0 (a
// negative intercept on short answers) is $0.
func fitFullAudit(samples []store.FitSample, n, sumChars, withChars int64) fullAuditFit {
	var meanChars float64
	switch {
	case withChars > 0:
		meanChars = float64(sumChars) / float64(withChars)
	case len(samples) > 0:
		for _, s := range samples {
			meanChars += s.ResponseChars
		}
		meanChars /= float64(len(samples))
	}
	totalChars := float64(sumChars) + float64(n-withChars)*meanChars
	predict := func(alpha, beta float64) float64 { return math.Max(0, alpha*float64(n)+beta*totalChars) }
	alpha, beta := leastSquares(samples, nil)
	out := fullAuditFit{usd: predict(alpha, beta)}

	rng := rand.New(rand.NewPCG(bootstrapSeed, bootstrapSeed)) // #nosec G404 -- a reproducible bootstrap, not a secret
	idx := make([]int, len(samples))
	totals := make([]float64, bootstrapResamples)
	for i := range totals {
		for j := range idx {
			idx[j] = rng.IntN(len(samples))
		}
		a, b := leastSquares(samples, idx)
		totals[i] = predict(a, b)
	}
	sort.Float64s(totals)
	out.low = totals[int(0.025*bootstrapResamples)]
	out.high = totals[int(0.975*bootstrapResamples)-1]
	return out
}

// leastSquares fits cost = α + β·chars over samples (the ones idx picks, with repeats, when idx
// is not nil). Without spread in the lengths the slope is 0 and α the mean cost.
func leastSquares(samples []store.FitSample, idx []int) (alpha, beta float64) {
	at := func(i int) store.FitSample {
		if idx != nil {
			return samples[idx[i]]
		}
		return samples[i]
	}
	k := len(samples)
	if idx != nil {
		k = len(idx)
	}
	if k == 0 {
		return 0, 0
	}
	var mx, my float64
	for i := range k {
		s := at(i)
		mx += s.ResponseChars
		my += s.CostUSD
	}
	mx /= float64(k)
	my /= float64(k)
	var sxx, sxy float64
	for i := range k {
		s := at(i)
		dx := s.ResponseChars - mx
		sxx += dx * dx
		sxy += dx * (s.CostUSD - my)
	}
	if sxx == 0 {
		return my, 0
	}
	beta = sxy / sxx
	return my - beta*mx, beta
}

// judgePriceInfo is the price the same-checks counterfactual asks the judge at (D47).
type judgePriceInfo struct {
	// in and out are per million tokens; ok is false when no price is known at all.
	in, out float64
	ok      bool
	// name is the model priced; why says why the reference judge stands in for the configured
	// one ("" when the configured judge is priced).
	name, why string
	// note is the price's source for third_party ("" when none is recorded).
	note string
}

// describe names the priced model, its price and, for the reference judge, why it is used.
func (p judgePriceInfo) describe() string {
	if !p.ok {
		return p.name + " (no price is known, so no figure is given)"
	}
	d := p.name + " at $" + formatPrice(p.in) + " / $" + formatPrice(p.out) + " per million tokens"
	if p.why != "" {
		d += " (" + p.why + ")"
	}
	return d
}

// referenceJudgeName is the judge D47 prices the same checks at when the configured one has no
// real price.
const referenceJudgeName = "Claude Haiku 4.5 at list price"

// sameChecksPrice is the judge price of the same checks (D47) for the effective judge the engine
// reports (EngineInfo provider and model), priced through the price book in force.
func (s *server) sameChecksPrice() judgePriceInfo {
	provider, model := "", ""
	if s.pinger != nil {
		j := s.pinger.Health().Info.GetJudge()
		provider, model = j.GetEffectiveProvider(), j.GetModel()
	}
	return judgePriceFor(s.priceBook(), provider, model)
}

// judgePriceFor prices the same checks for the judge provider and model through book: the
// judge's own price, or the Claude Haiku 4.5 list price when the judge is the mock, a local model
// at no charge, not in the price table, or not reported yet ("" provider); the description says
// which.
func judgePriceFor(book *costs.PriceBook, provider, model string) judgePriceInfo {
	why := ""
	switch entry, _, ok := book.Lookup(provider, model); {
	case provider == "":
		why = "no deep-audit judge has reported its model yet"
	case decision.IsSimulatedProvider(provider):
		why = "the configured judge is the mock judge, which has no real price"
	case decision.IsLocalProvider(provider) && (!ok || entry.Free()):
		why = "the configured judge, " + provider + " " + model + ", runs locally at no charge"
	case !ok:
		why = "the configured judge, " + provider + " " + model + ", is not in the price table"
	default:
		return judgePriceInfo{in: entry.InputPerMTok, out: entry.OutputPerMTok, ok: true,
			name: "the configured judge, " + provider + " " + model, note: priceNote(provider+" "+model, entry)}
	}
	ref := judgePriceInfo{name: referenceJudgeName, why: why}
	if a, err := costs.ContractsAssumptions(); err == nil {
		if entry, _, ok := book.Lookup(a.Judge.Provider, a.Judge.Model); ok {
			ref.in, ref.out, ref.ok = entry.InputPerMTok, entry.OutputPerMTok, true
			ref.note = priceNote("Claude Haiku 4.5 list price", entry)
		}
	}
	return ref
}

// priceNote is the third_party line of a price entry: its prices and where they come from.
func priceNote(what string, e decision.PriceEntry) string {
	note := what + " $" + formatPrice(e.InputPerMTok) + " / $" + formatPrice(e.OutputPerMTok) + " per million tokens"
	var src []string
	switch {
	case e.Label == "override":
		src = append(src, "your price override")
	case e.Provider == "anthropic":
		src = append(src, "Anthropic")
	case e.Source != "":
		src = append(src, e.Source)
	}
	if e.AsOf != "" {
		src = append(src, "as of "+e.AsOf)
	}
	if len(src) > 0 {
		note += " (" + strings.Join(src, ", ") + ")"
	}
	return note
}

func formatPrice(p float64) string { return strconv.FormatFloat(p, 'f', -1, 64) }

// priceBook is the effective price table: the cost ledger's, or the embedded contracts table
// when the server built no ledger.
func (s *server) priceBook() *costs.PriceBook {
	if l := s.ledger(); l != nil {
		return l.Prices()
	}
	return contractsPriceBook()
}

var contractsPriceBook = sync.OnceValue(func() *costs.PriceBook {
	b, err := costs.LoadPriceBook("")
	if err != nil {
		return costs.NewPriceBook(decision.PriceTable{}, nil)
	}
	return b
})

// tokenModelCheck is what contracts/claims/measured.json says about the same-checks token model:
// the cost ratio of the same checks to the fast check a published bench-lite run measured.
type tokenModelCheck struct {
	validated bool
	ratio     float64
	runID     string
	tier      string
}

// basis is the validation sentence of the same-checks basis.
func (c tokenModelCheck) basis() string {
	if !c.validated {
		return "The token model is not yet validated against a measured run."
	}
	s := "The token model is validated: a published " + c.tier + " run measured the same checks at " +
		strconv.FormatFloat(c.ratio, 'f', -1, 64) + "× the fast-check cost"
	if c.runID != "" {
		s += " (run " + c.runID + ")"
	}
	return s + "."
}

// parseTokenModelCheck reads measured.json: validated only when a real (non-mock) published run
// recorded same_checks_cost_ratio.
func parseTokenModelCheck(raw []byte) tokenModelCheck {
	var doc struct {
		Status string  `json:"status"`
		Tier   *string `json:"tier"`
		Mock   bool    `json:"mock"`
		Values map[string]struct {
			Value *float64 `json:"value"`
			RunID *string  `json:"run_id"`
		} `json:"values"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Mock || doc.Status == "not_measured" {
		return tokenModelCheck{}
	}
	v, ok := doc.Values["same_checks_cost_ratio"]
	if !ok || v.Value == nil || *v.Value <= 0 || math.IsInf(*v.Value, 0) || math.IsNaN(*v.Value) {
		return tokenModelCheck{}
	}
	c := tokenModelCheck{validated: true, ratio: *v.Value, tier: "benchmark"}
	if v.RunID != nil {
		c.runID = *v.RunID
	}
	if doc.Tier != nil && *doc.Tier != "" {
		c.tier = *doc.Tier
	}
	return c
}

var measuredTokenModel = sync.OnceValue(func() tokenModelCheck {
	raw, err := contracts.FS.ReadFile("claims/measured.json")
	if err != nil {
		return tokenModelCheck{}
	}
	return parseTokenModelCheck(raw)
})

type staticClaim struct {
	per1k float64
	basis string
}

var staticFullAudit = sync.OnceValue(func() staticClaim {
	c := staticClaim{basis: "a static assumption about a full claim-level LLM audit"}
	b, err := contracts.FS.ReadFile("claims/value-claims.v1.json")
	if err != nil {
		return c
	}
	var doc struct {
		Claims []struct {
			ID    string  `json:"id"`
			Value float64 `json:"value"`
			Basis string  `json:"basis"`
		} `json:"claims"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return c
	}
	for _, cl := range doc.Claims {
		if cl.ID == "llm_full_audit_cost_per_1k" {
			c.per1k, c.basis = cl.Value, strings.TrimSuffix(cl.Basis, ".")
		}
	}
	return c
})

// roiBuckets are the buckets GET /api/roi/timeseries allows per window, the default first: a
// bucket is never longer than its window, and a window never has more than 168 buckets.
var roiBuckets = map[string][]string{
	"1h":  {"1h"},
	"24h": {"1h"},
	"7d":  {"1d", "1h"},
	"30d": {"1d"},
}

var roiBucketWidth = map[string]time.Duration{"1h": time.Hour, "1d": 24 * time.Hour}

// handleRoiTimeseries serves GET /api/roi/timeseries (SPEC §2.4 row 48): per UTC hour or day of
// the window, the fast-check and deep-audit spend of the answers created in it, the answers the
// fast check assessed, and the same-checks counterfactual at the judge price (null when no price
// is known). Every bucket is present, zero-filled. A bucket the window does not allow is 422.
func (s *server) handleRoiTimeseries(c *gin.Context) {
	if !s.statsReady(c) {
		return
	}
	q, ok := s.parseStatsQuery(c, "7d")
	if !ok {
		return
	}
	bucket := c.Query("bucket")
	if bucket != "" && roiBucketWidth[bucket] == 0 {
		badParam(c, "bucket", "must be 1h or 1d")
		return
	}
	allowed := roiBuckets[q.window]
	if bucket == "" && len(allowed) > 0 {
		bucket = allowed[0]
	}
	if !slices.Contains(allowed, bucket) {
		msg := "a " + q.window + " window is shorter than one bucket: use window 1h or longer"
		if len(allowed) > 0 {
			msg = "a " + q.window + " window takes bucket " + strings.Join(allowed, " or ")
		}
		abortAPI(c, http.StatusUnprocessableEntity, codeValidationFailed, "This window and bucket do not go together.",
			fieldError{Path: "bucket", Message: msg})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), statsTimeout)
	defer cancel()
	points, err := s.stats.RoiSeries(ctx, q.filter, roiBucketWidth[bucket])
	if err != nil {
		s.statsFailed(c, "the cost series", err)
		return
	}
	price := s.sameChecksPrice()
	out := roiTimeseries{Window: q.window, Bucket: bucket, Points: make([]roiPoint, 0, len(points))}
	for _, p := range points {
		pt := roiPoint{T: p.T, DecisionUSD: roundUSD(p.DecisionUSD), LLMUSD: roundUSD(p.LLMUSD), Gated: p.Gated}
		if price.ok {
			pt.SameChecksUSD = ptrF(roundUSD(sameChecksUSD(p.SameChecksCalls, p.SameChecksInputTokens, p.SameChecksQuestions, price)))
		}
		out.Points = append(out.Points, pt)
	}
	c.JSON(http.StatusOK, out)
}

// weakest is the weakest of labels: simulated, then estimate, then measured.
func weakest(labels ...string) string {
	rank := map[string]int{labelSimulated: 0, labelEstimate: 1, labelMeasured: 2}
	out := labelMeasured
	for _, l := range labels {
		if rank[l] < rank[out] {
			out = l
		}
	}
	return out
}

func per1k(usd float64, n int64) *float64 {
	if n <= 0 {
		return nil
	}
	return ptrF(roundUSD(usd / float64(n) * 1000))
}

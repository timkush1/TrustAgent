// Package studio runs the gate pipeline of SPEC §3.1 on samples outside the live gateway: the
// detectors, compile(), the decision-model call and decide(). gate-eval runs it over a dataset,
// the Evaluator Studio over a test bench (POST /api/studio/test) and over past traffic (POST
// /api/studio/dry-run). Unlike the gateway it never samples, hedges, retries a too-large request
// or records anything: the caller decides what to keep.
package studio

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
)

// Sample is one exchange to evaluate.
type Sample struct {
	// ID names the sample in results.
	ID string
	// The texts of the exchange (SPEC §2.9 compile inputs).
	UserRequest, Response, SystemInstructions string
	// Evidence is priority-ordered and not yet packed.
	Evidence []decision.EvidenceItem
	// Rules are the sample's app rules; they act as the request's trustagent.rules and are always
	// honoured (client_rules on).
	Rules map[string]string
	// MaxMarker is the highest [Document N] marker of the evidence (citation detector).
	MaxMarker int
	// Scenario is the X-TrustAgent-Scenario id the mock backend answers by.
	Scenario string
}

// Trusted reports whether any evidence item is trusted (the gate's evidence_present).
func (s *Sample) Trusted() bool {
	return slices.ContainsFunc(s.Evidence, func(it decision.EvidenceItem) bool { return !it.Untrusted })
}

// Usage is decision/v1 DecisionModelUsage for a sample's calls.
type Usage struct {
	Backend        string   `json:"backend"`
	ModelRequested string   `json:"model_requested"`
	Snapshot       *string  `json:"snapshot"`
	Calls          int      `json:"calls"`
	Attempts       int      `json:"attempts"`
	Hedged         bool     `json:"hedged"`
	LatencyMs      *int64   `json:"latency_ms"`
	InputTokens    *int     `json:"input_tokens"`
	CostUSD        *float64 `json:"cost_usd"`
	CostSource     string   `json:"cost_source"`
}

// Outcome is what the pipeline produced for a sample. The fields are filled stage by stage, so an
// outcome that stopped at an error still carries what the earlier stages produced.
type Outcome struct {
	// Skipped is set when the Spend cap stopped the run before this sample; nothing else is set.
	Skipped bool
	// Err is set when a stage failed; its message starts with the stage, which Stage names
	// (StageDetectors, StageCompile or StageDecide).
	Err   error
	Stage string
	// Detectors and Excluded are never nil.
	Detectors []decision.DetectorResult
	Excluded  []decision.Exclusion
	// Compiled is the compile() output, nil when compile failed.
	Compiled *decision.Compiled
	// Usage describes the decision-model calls, nil when none was made.
	Usage *Usage
	// RequestStatus is decide()'s meta.request_status: per request index, ok, timeout or
	// error:<kind>.
	RequestStatus map[string]string
	// Decision is the decide() core, nil when a stage failed.
	Decision *decision.Core
	// CostUSD is the sample's decision-model cost (0 when nothing was sent).
	CostUSD float64
	// LatencyMs is the wall time of the sample's pipeline, calls included.
	LatencyMs int64
}

// Pipeline stages an Outcome.Stage names.
const (
	StageDetectors = "detectors"
	StageCompile   = "compile"
	StageDecide    = "decide"
)

// fail records a stage's error.
func (o *Outcome) fail(stage string, err error) *Outcome {
	o.Err, o.Stage = fmt.Errorf("%s: %w", stage, err), stage
	return o
}

// Failed reports whether a request of the sample failed with the error kind (jev.KindBudget, ...).
func (o *Outcome) Failed(kind string) bool {
	for _, st := range o.RequestStatus {
		if st == "error:"+kind {
			return true
		}
	}
	return false
}

// Runner evaluates samples against a route. Set the fields, then call Evaluate; it is safe for
// concurrent use.
type Runner struct {
	// Client is the decision model.
	Client jev.Client
	// Route supplies the evaluators (with their stages, params and threshold overrides), the
	// evidence settings, on_no_evidence and the detector policy.
	Route *policy.Route
	// Mode is the configured mode decide() runs with.
	Mode string
	// Model is the decision model requested.
	Model string
	// Specs resolves the route's evaluator refs.
	Specs map[string]*decision.Spec
	// Budget is the compile() budget (the route's response and evidence caps included).
	Budget decision.Budget
	// PricePerMTok prices the token estimates (USD per million input tokens): reservations, the
	// charge of a failed or unpriced call, and EstimateUSD.
	PricePerMTok float64
	// CallTimeout bounds the calls of one sample; 0 leaves the context's own deadline.
	CallTimeout time.Duration
	// Spend caps the run's spend; nil is unlimited.
	Spend *Spend
	// Call is the template of every call's options (class, component, enforcing, labels); the
	// sample's scenario is added.
	Call jev.CallOptions
	// Now returns the current time; nil uses time.Now.
	Now func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// Evaluate runs the pipeline on one sample. It never returns nil.
func (r *Runner) Evaluate(ctx context.Context, s *Sample) *Outcome {
	start := r.now()
	o := &Outcome{Detectors: []decision.DetectorResult{}, Excluded: []decision.Exclusion{}, RequestStatus: map[string]string{}}
	defer func() { o.LatencyMs = r.now().Sub(start).Milliseconds() }()
	if r.Spend.Stopped() {
		o.Skipped = true
		return o
	}

	detectors, err := decision.Detect(&decision.DetectorInput{
		Response: s.Response, Evidence: s.Evidence, UserRequest: s.UserRequest,
		SystemInstructions: s.SystemInstructions, Rules: r.ResolveRules(s.Rules),
		PreDedupItems: len(s.Evidence), MaxMarker: s.MaxMarker, MinInteger: r.Route.Detectors.MinInteger,
	})
	if err != nil {
		return o.fail(StageDetectors, err)
	}
	o.Detectors = detectors

	compiled, err := r.Compile(s)
	if err != nil {
		return o.fail(StageCompile, err)
	}
	evs, err := compiled.DecideEvaluators(r.Specs, r.Thresholds())
	if err != nil {
		return o.fail(StageCompile, err)
	}
	if compiled.Excluded != nil {
		o.Excluded = compiled.Excluded
	}
	o.Compiled = compiled

	in := &decision.Input{
		Policy: decision.Policy{
			ConfiguredMode: r.Mode, Fail: r.Route.Fail, RequireCalibration: r.Route.RequireCalibration,
			OnNoEvidence: r.Route.OnNoEvidence,
			Detectors:    decision.DetectorPolicy{FlagOn: r.Route.Detectors.FlagOn, BlockOn: r.Route.Detectors.BlockOn},
		},
		Evaluators: evs, QuestionMap: compiled.QuestionMap, Excluded: compiled.Excluded,
		Calibration: map[string]decision.CalibrationEntry{},
		Meta: decision.Meta{
			Backend: r.Client.Info().Backend, Stream: "none", EvidencePresent: s.Trusted(),
			EvidenceTruncated: compiled.Truncated, RequestStatus: map[string]string{}, QIDRequest: compiled.QIDRequest,
		},
		Detectors: decision.DetectorStates(detectors),
	}
	if len(compiled.Requests) > 0 {
		if r.Client.Info().Backend == jev.BackendOff {
			skip := "decision_model_off"
			in.Meta.SkipReason = &skip
		} else {
			reserved := 0.0
			for _, t := range compiled.EstTokens {
				reserved += r.EstimateUSD(t) * ReserveFactor
			}
			if !r.Spend.Reserve(reserved) {
				o.Skipped, o.Compiled = true, nil
				o.Detectors, o.Excluded = []decision.DetectorResult{}, []decision.Exclusion{}
				return o
			}
			var u *Usage
			u, in.Answers, in.Meta.RequestStatus = r.call(ctx, s, compiled)
			actual := *u.CostUSD
			r.Spend.Settle(reserved, actual)
			o.Usage, o.CostUSD, o.RequestStatus = u, actual, in.Meta.RequestStatus
		}
	}
	core, err := decision.Decide(in)
	if err != nil {
		return o.fail(StageDecide, err)
	}
	o.Decision = core
	return o
}

// Compile runs compile() on a sample with the runner's route and budget.
func (r *Runner) Compile(s *Sample) (*decision.Compiled, error) {
	return decision.Compile(r.CompileRoute(), r.Specs, &decision.CompileInputs{
		UserRequest: s.UserRequest, AssistantResponse: s.Response, SystemInstructions: s.SystemInstructions,
		Evidence: s.Evidence, Rules: s.Rules,
	}, r.Budget)
}

// EstimateTokensUSD prices the token estimates of a compile.
func (r *Runner) EstimateTokensUSD(c *decision.Compiled) float64 {
	usd := 0.0
	for _, t := range c.EstTokens {
		usd += r.EstimateUSD(t)
	}
	return usd
}

// call sends every compiled request concurrently within the sample's call timeout.
func (r *Runner) call(ctx context.Context, s *Sample, c *decision.Compiled) (*Usage, map[string]*decision.RawAnswer, map[string]string) {
	if r.CallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.CallTimeout)
		defer cancel()
	}
	type outcome struct {
		resp *jev.Response
		err  error
	}
	opts := r.Call
	opts.Scenario = s.Scenario
	outcomes := make([]outcome, len(c.Requests))
	var wg sync.WaitGroup
	for k := range c.Requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := &c.Requests[k]
			resp, err := r.Client.Decide(ctx, &jev.Request{Model: req.Model, State: req.State, Questions: req.Questions}, opts)
			outcomes[k] = outcome{resp, err}
		}()
	}
	wg.Wait()

	u := &Usage{Backend: r.Client.Info().Backend, ModelRequested: r.Model, Calls: len(c.Requests), CostSource: jev.CostUnknown}
	answers := map[string]*decision.RawAnswer{}
	status := map[string]string{}
	sources := make([]string, 0, len(outcomes))
	cost := 0.0
	for k, o := range outcomes {
		key := strconv.Itoa(k)
		if o.err != nil || o.resp.CostUSD == nil {
			// A failed or unpriced call is charged at its estimate, so the cap never loses track
			// of it.
			cost += r.EstimateUSD(c.EstTokens[k])
			sources = append(sources, jev.CostComputed)
		} else {
			cost += *o.resp.CostUSD
			sources = append(sources, o.resp.CostSource)
		}
		if o.err != nil {
			u.Attempts++
			if kind := jev.ErrorKind(o.err); kind == jev.KindTimeout {
				status[key] = decision.RequestTimeout
			} else {
				status[key] = "error:" + kind
			}
			continue
		}
		status[key] = decision.RequestOK
		for qid := range c.Requests[k].Questions {
			if a, ok := o.resp.Answers[qid]; ok {
				answers[qid] = a
			}
		}
		u.Attempts += max(o.resp.Attempts, 1)
		u.Hedged = u.Hedged || o.resp.Hedged
		if o.resp.Snapshot != "" {
			snap := o.resp.Snapshot
			u.Snapshot = &snap
		}
		if lat := o.resp.LatencyMs; u.LatencyMs == nil || lat > *u.LatencyMs {
			u.LatencyMs = &lat
		}
		if o.resp.InputTokens != nil {
			n := *o.resp.InputTokens
			if u.InputTokens != nil {
				n += *u.InputTokens
			}
			u.InputTokens = &n
		}
	}
	u.CostUSD = &cost
	if len(sources) > 0 && !slices.ContainsFunc(sources, func(s string) bool { return s != sources[0] }) {
		u.CostSource = sources[0]
	}
	return u, answers, status
}

// EstimateUSD prices an estimated token count.
func (r *Runner) EstimateUSD(tokens int) float64 {
	return float64(tokens) * r.PricePerMTok / 1e6
}

// CompileRoute is the part of the route compile() reads. Sample rules act as the request's
// trustagent.rules and are always honoured (client_rules on).
func (r *Runner) CompileRoute() *decision.CompileRoute {
	cr := &decision.CompileRoute{
		OnNoEvidence: r.Route.OnNoEvidence, EvidenceSources: r.Route.Evidence.Sources, ClientRules: true, Model: r.Model,
	}
	for _, ref := range r.Route.Evaluators {
		cr.Evaluators = append(cr.Evaluators, decision.RouteEvaluator{Ref: ref.Ref, Stage: ref.StageOrDefault(), Enabled: ref.Enabled, Params: ref.Params})
	}
	return cr
}

// ResolveRules resolves the rules params of the route's enabled evaluators for the detectors:
// the sample's rule, else the route param, else the spec default.
func (r *Runner) ResolveRules(sampleRules map[string]string) map[string]string {
	out := map[string]string{}
	for _, ref := range r.Route.Evaluators {
		spec := r.Specs[ref.Ref]
		if spec == nil || !ref.IsEnabled() {
			continue
		}
		for name, p := range spec.Params {
			v := p.Default
			if rv, ok := ref.Params[name]; ok {
				v = rv
			}
			if cv, ok := sampleRules[name]; ok {
				v = cv
			}
			out[name] = v
		}
	}
	return out
}

// Thresholds are the route's threshold overrides in decide() form, by ref.
func (r *Runner) Thresholds() map[string]decision.Thresholds {
	out := make(map[string]decision.Thresholds, len(r.Route.Evaluators))
	for _, ref := range r.Route.Evaluators {
		out[ref.Ref] = ref.Thresholds.Decision()
	}
	return out
}

// StateFields lists the state members of the first request, sorted (every request shares one
// state): the calibration fit_context composition.
func StateFields(c *decision.Compiled) []string {
	out := []string{}
	if len(c.Requests) == 0 {
		return out
	}
	for k := range c.Requests[0].State {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

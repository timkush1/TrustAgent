package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/truthtable/backend-go/internal/decision"
	"github.com/truthtable/backend-go/internal/jev"
	"github.com/truthtable/backend-go/internal/policy"
)

// reserveFactor scales a call's estimated cost into the reservation the cost cap checks: the
// token estimate (SPEC §2.9 est with cpt) can undercount, so the cap keeps a 2x margin.
const reserveFactor = 2.0

// Skip reasons of an output line.
const (
	skippedOverBudget = "over_budget"
)

// spend enforces --max-cost-usd: no decision-model call starts unless its reserved cost fits in
// what is left, and after the first refusal no further example is evaluated. It is safe for
// concurrent use.
type spend struct {
	mu        sync.Mutex
	cap       float64 // 0 = unlimited
	spent     float64
	reserved  float64
	exhausted bool
}

// reserve claims usd of the budget. It fails, and marks the budget exhausted, when the claim
// would take spent plus outstanding reservations above the cap.
func (s *spend) reserve(usd float64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exhausted {
		return false
	}
	if s.cap > 0 && s.spent+s.reserved+usd > s.cap {
		s.exhausted = true
		return false
	}
	s.reserved += usd
	return true
}

// settle replaces a reservation with the actual cost.
func (s *spend) settle(reserved, actual float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved -= reserved
	s.spent += actual
}

// stopped reports whether the cap has refused a call.
func (s *spend) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exhausted
}

// total returns the actual cost so far.
func (s *spend) total() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spent
}

// evaluator runs the gate pipeline of SPEC §3.1 on examples: detectors, compile, the
// decision-model call, and decide() (which normalizes the answers).
type evaluator struct {
	client jev.Client
	route  *policy.Route
	mode   string
	model  string
	specs  map[string]*decision.Spec
	budget decision.Budget
	// pricePerMTok prices the reservations (USD per million input tokens).
	pricePerMTok float64
	// callTimeout bounds the calls of one example.
	callTimeout time.Duration
	spend       *spend
	now         func() time.Time
}

// compileInfo is what compile() produced for an example.
type compileInfo struct {
	Requests          int      `json:"requests"`
	EstTokens         []int    `json:"est_tokens"`
	CPT               float64  `json:"cpt"`
	QuestionSetSHA256 string   `json:"question_set_sha256"`
	RequestSHA256     []string `json:"request_sha256"`
	// StateFields are the state members sent (the calibration fit_context composition).
	StateFields  []string `json:"state_fields"`
	PackedIDs    []string `json:"packed_ids"`
	TruncatedIDs []string `json:"truncated_ids"`
	Truncated    bool     `json:"truncated"`
}

// usage is decision/v1 DecisionModelUsage for an example's calls.
type usage struct {
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

// result is one output line.
type result struct {
	ID string `json:"id"`
	// Line is the 1-based input line.
	Line int `json:"line"`
	// Error is set when the example could not be evaluated (invalid input, compile failure).
	Error *string `json:"error"`
	// Skipped is over_budget when the cost cap stopped the run before this example.
	Skipped *string `json:"skipped"`
	// Simulated is true with the mock backend: the probabilities are not model output.
	Simulated     bool                      `json:"simulated"`
	Route         string                    `json:"route"`
	Mode          string                    `json:"mode"`
	Decision      *decision.Core            `json:"decision"`
	Detectors     []decision.DetectorResult `json:"detectors"`
	Excluded      []decision.Exclusion      `json:"excluded"`
	Compile       *compileInfo              `json:"compile"`
	DecisionModel *usage                    `json:"decision_model"`
	// CostUSD is the example's decision-model cost (0 when nothing was sent).
	CostUSD float64 `json:"cost_usd"`
	// LatencyMs is the wall time of the example's pipeline, calls included.
	LatencyMs int64 `json:"latency_ms"`
	// Passed through from the input.
	Labels            json.RawMessage `json:"labels,omitempty"`
	LabelHallucinated *bool           `json:"label_hallucinated,omitempty"`
	LabelSource       string          `json:"label_source,omitempty"`
	Domain            string          `json:"domain,omitempty"`
	Meta              json.RawMessage `json:"meta,omitempty"`
}

func (e *evaluator) newResult(line int, ex *example) *result {
	r := &result{
		Line: line, Route: e.route.Name, Mode: e.mode, Simulated: e.client.Info().Backend == jev.BackendMock,
		Detectors: []decision.DetectorResult{}, Excluded: []decision.Exclusion{},
	}
	if ex != nil {
		l := ex.line
		r.ID, r.Labels, r.LabelHallucinated, r.LabelSource, r.Domain, r.Meta = l.ID, l.Labels, l.LabelHallucinated, l.LabelSource, l.Domain, l.Meta
	}
	return r
}

func fail(r *result, format string, args ...any) *result {
	msg := fmt.Sprintf(format, args...)
	r.Error = &msg
	return r
}

// evaluate gates one example. It never returns nil; problems are recorded on the line.
func (e *evaluator) evaluate(ctx context.Context, line int, ex *example) *result {
	start := e.now()
	r := e.newResult(line, ex)
	defer func() { r.LatencyMs = e.now().Sub(start).Milliseconds() }()
	if e.spend.stopped() {
		s := skippedOverBudget
		r.Skipped = &s
		return r
	}

	rules := e.resolveRules(ex.line.Rules)
	detectors, err := decision.Detect(&decision.DetectorInput{
		Response: ex.Response, Evidence: ex.Evidence, UserRequest: ex.UserRequest,
		SystemInstructions: ex.SystemInstructions, Rules: rules,
		PreDedupItems: len(ex.Evidence), MaxMarker: ex.line.MaxMarker, MinInteger: e.route.Detectors.MinInteger,
	})
	if err != nil {
		return fail(r, "detectors: %v", err)
	}
	r.Detectors = detectors

	compiled, err := decision.Compile(e.compileRoute(), e.specs, &decision.CompileInputs{
		UserRequest: ex.UserRequest, AssistantResponse: ex.Response, SystemInstructions: ex.SystemInstructions,
		Evidence: ex.Evidence, Rules: ex.line.Rules,
	}, e.budget)
	if err != nil {
		return fail(r, "compile: %v", err)
	}
	evs, err := compiled.DecideEvaluators(e.specs, e.thresholds())
	if err != nil {
		return fail(r, "compile: %v", err)
	}
	r.Excluded = compiled.Excluded
	r.Compile = &compileInfo{
		Requests: len(compiled.Requests), EstTokens: nonNilInts(compiled.EstTokens), CPT: compiled.CPT,
		QuestionSetSHA256: compiled.QuestionSetSHA256, RequestSHA256: nonNilStrings(compiled.RequestSHA256),
		StateFields: stateFields(compiled), PackedIDs: nonNilStrings(compiled.PackedIDs),
		TruncatedIDs: nonNilStrings(compiled.TruncatedIDs), Truncated: compiled.Truncated,
	}

	in := &decision.Input{
		Policy: decision.Policy{
			ConfiguredMode: e.mode, Fail: e.route.Fail, RequireCalibration: e.route.RequireCalibration,
			OnNoEvidence: e.route.OnNoEvidence,
			Detectors:    decision.DetectorPolicy{FlagOn: e.route.Detectors.FlagOn, BlockOn: e.route.Detectors.BlockOn},
		},
		Evaluators: evs, QuestionMap: compiled.QuestionMap, Excluded: compiled.Excluded,
		Calibration: map[string]decision.CalibrationEntry{},
		Meta: decision.Meta{
			Backend: e.client.Info().Backend, Stream: "none", EvidencePresent: ex.trusted(),
			EvidenceTruncated: compiled.Truncated, RequestStatus: map[string]string{}, QIDRequest: compiled.QIDRequest,
		},
		Detectors: decision.DetectorStates(detectors),
	}
	if len(compiled.Requests) > 0 {
		if e.client.Info().Backend == jev.BackendOff {
			skip := "decision_model_off"
			in.Meta.SkipReason = &skip
		} else {
			reserved := 0.0
			for _, t := range compiled.EstTokens {
				reserved += e.estimateUSD(t) * reserveFactor
			}
			if !e.spend.reserve(reserved) {
				s := skippedOverBudget
				r.Skipped = &s
				r.Compile, r.Excluded, r.Detectors = nil, []decision.Exclusion{}, []decision.DetectorResult{}
				return r
			}
			var u *usage
			u, in.Answers, in.Meta.RequestStatus = e.call(ctx, ex, compiled)
			actual := *u.CostUSD
			e.spend.settle(reserved, actual)
			r.DecisionModel, r.CostUSD = u, actual
		}
	}
	core, err := decision.Decide(in)
	if err != nil {
		return fail(r, "decide: %v", err)
	}
	r.Decision = core
	return r
}

// call sends every compiled request concurrently within the example's call timeout.
func (e *evaluator) call(ctx context.Context, ex *example, c *decision.Compiled) (*usage, map[string]*decision.RawAnswer, map[string]string) {
	ctx, cancel := context.WithTimeout(ctx, e.callTimeout)
	defer cancel()
	type outcome struct {
		resp *jev.Response
		err  error
	}
	outcomes := make([]outcome, len(c.Requests))
	var wg sync.WaitGroup
	for k := range c.Requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := &c.Requests[k]
			resp, err := e.client.Decide(ctx, &jev.Request{Model: req.Model, State: req.State, Questions: req.Questions},
				jev.CallOptions{Class: jev.ClassAsync, Scenario: ex.line.Scenario})
			outcomes[k] = outcome{resp, err}
		}()
	}
	wg.Wait()

	u := &usage{Backend: e.client.Info().Backend, ModelRequested: e.model, Calls: len(c.Requests), CostSource: jev.CostUnknown}
	answers := map[string]*decision.RawAnswer{}
	status := map[string]string{}
	sources := make([]string, 0, len(outcomes))
	cost := 0.0
	for k, o := range outcomes {
		key := strconv.Itoa(k)
		if o.err != nil || o.resp.CostUSD == nil {
			// A failed or unpriced call is charged at its estimate, so the cap never loses track of it.
			cost += e.estimateUSD(c.EstTokens[k])
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

// estimateUSD prices an estimated token count.
func (e *evaluator) estimateUSD(tokens int) float64 {
	return float64(tokens) * e.pricePerMTok / 1e6
}

// compileRoute is the part of the route compile() reads. Example rules act as the request's
// trustagent.rules and are always honoured (client_rules on): they are the dataset's app rules.
func (e *evaluator) compileRoute() *decision.CompileRoute {
	cr := &decision.CompileRoute{
		OnNoEvidence: e.route.OnNoEvidence, EvidenceSources: e.route.Evidence.Sources, ClientRules: true, Model: e.model,
	}
	for _, ref := range e.route.Evaluators {
		cr.Evaluators = append(cr.Evaluators, decision.RouteEvaluator{Ref: ref.Ref, Stage: ref.StageOrDefault(), Enabled: ref.Enabled, Params: ref.Params})
	}
	return cr
}

// resolveRules resolves the rules params of the route's enabled evaluators for the detectors:
// the example's rule, else the route param, else the spec default.
func (e *evaluator) resolveRules(exampleRules map[string]string) map[string]string {
	out := map[string]string{}
	for _, ref := range e.route.Evaluators {
		spec := e.specs[ref.Ref]
		if spec == nil || !ref.IsEnabled() {
			continue
		}
		for name, p := range spec.Params {
			v := p.Default
			if rv, ok := ref.Params[name]; ok {
				v = rv
			}
			if cv, ok := exampleRules[name]; ok {
				v = cv
			}
			out[name] = v
		}
	}
	return out
}

func (e *evaluator) thresholds() map[string]decision.Thresholds {
	out := make(map[string]decision.Thresholds, len(e.route.Evaluators))
	for _, ref := range e.route.Evaluators {
		out[ref.Ref] = ref.Thresholds.Decision()
	}
	return out
}

// stateFields lists the state members of the first request, sorted (every request shares one
// state).
func stateFields(c *decision.Compiled) []string {
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

func nonNilInts(xs []int) []int {
	if xs == nil {
		return []int{}
	}
	return xs
}

func nonNilStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// errNoDecisionModel is returned when the resolved backend is off.
var errNoDecisionModel = errors.New("the decision model is off")
